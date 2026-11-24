// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package sqlstore

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"

	"github.com/sudosylabs/proctor/server/model"
	"github.com/sudosylabs/proctor/server/store"
)

// servingNodeLeaseFenceKey fences offline administrator recovery against
// every serving node. Recovery holds it exclusively; node lease writes hold it
// shared so they exclude recovery without serializing one another.
const servingNodeLeaseFenceKey = "proctor:serving-node-lease-fence"

// SQLServingNodeLeaseStore implements store.ServingNodeLeaseStore on
// serving_node_leases.
type SQLServingNodeLeaseStore struct{ *SQLStore }

type servingNodeLeaseRow struct {
	NodeID    string    `db:"node_id"`
	LeaseID   string    `db:"lease_id"`
	UpdatedAt time.Time `db:"updated_at"`
	ExpiresAt time.Time `db:"expires_at"`
}

func newSQLServingNodeLeaseStore(sqlStore *SQLStore) store.ServingNodeLeaseStore {
	return &SQLServingNodeLeaseStore{SQLStore: sqlStore}
}

func (s SQLServingNodeLeaseStore) Upsert(ctx context.Context, claim *store.ServingNodeLeaseClaim) (*store.ServingNodeLease, error) {
	if claim == nil {
		return nil, store.NewErrInvalidInput("serving_node_lease", "upsert", nil)
	}

	nodeID, leaseID, lifetime := strings.TrimSpace(claim.NodeID), strings.TrimSpace(claim.LeaseID), claim.Lifetime
	if !validServingNodeLeaseInput(nodeID, leaseID, lifetime) {
		return nil, store.NewErrInvalidInput("serving_node_lease", "upsert", nil)
	}

	// The lease runs on the control pool, so ordinary load never makes a
	// renewal wait for a connection. It is four round trips: BEGIN, the
	// locks, one statement that decides and writes, and COMMIT. The locks
	// stay a statement of their own because a statement reads with the
	// snapshot it started with: a decision made in the locking statement could
	// miss a renewal that committed while the lock was being waited for.
	return runSQLTransaction(ctx, s.GetControl().Begin, "upsert serving node lease", func(
		ctx context.Context,
		tx *sqlxTxWrapper,
	) (*store.ServingNodeLease, error) {
		if err := lockServingNodeLease(ctx, tx, nodeID); err != nil {
			return nil, err
		}

		var row servingNodeLeaseUpsertRow
		if err := tx.Get(ctx, &row, servingNodeLeaseUpsertSQL,
			leaseID, leaseID, nodeID, nodeID, leaseID, lifetime.Microseconds()); err != nil {
			return nil, fmt.Errorf("upsert serving node lease: %w", translateError("serving_node_lease", nodeID, err))
		}
		if row.Conflict != "" {
			return nil, store.NewErrConflict("serving_node_lease", row.Conflict, nil)
		}
		return servingNodeLeaseModel(servingNodeLeaseRow{
			NodeID: row.NodeID.String, LeaseID: row.LeaseID.String,
			UpdatedAt: row.UpdatedAt.Time, ExpiresAt: row.ExpiresAt.Time,
		})
	})
}

// servingNodeLeaseUpsertSQL decides and writes a renewal in one statement on
// the database clock. A lease that expired under its own ID is never revived
// (lease_expired), and another live lease for the node is never replaced
// (node_id_live); otherwise the lease is written with a fresh expiry.
const servingNodeLeaseUpsertSQL = `
WITH clock AS (
	SELECT clock_timestamp() AS at
), verdict AS (
	SELECT clock.at, CASE
		WHEN current.lease_id = ? AND current.expires_at <= clock.at THEN 'lease_expired'
		WHEN current.lease_id <> ? AND current.expires_at > clock.at THEN 'node_id_live'
		ELSE ''
	END AS conflict
	FROM clock
	LEFT JOIN serving_node_leases current ON current.node_id = ?
), written AS (
	INSERT INTO serving_node_leases (node_id, lease_id, updated_at, expires_at)
	SELECT ?, ?, verdict.at, verdict.at + ? * interval '1 microsecond'
	FROM verdict
	WHERE verdict.conflict = ''
	ON CONFLICT (node_id) DO UPDATE
	SET lease_id = EXCLUDED.lease_id, updated_at = EXCLUDED.updated_at, expires_at = EXCLUDED.expires_at
	RETURNING node_id, lease_id, updated_at, expires_at
)
SELECT verdict.conflict, written.node_id, written.lease_id, written.updated_at, written.expires_at
FROM verdict
LEFT JOIN written ON true`

type servingNodeLeaseUpsertRow struct {
	Conflict  string         `db:"conflict"`
	NodeID    sql.NullString `db:"node_id"`
	LeaseID   sql.NullString `db:"lease_id"`
	UpdatedAt sql.NullTime   `db:"updated_at"`
	ExpiresAt sql.NullTime   `db:"expires_at"`
}

// Delete releases the input node's serving lease when it still holds the input lease ID.
func (s SQLServingNodeLeaseStore) Delete(ctx context.Context, input *store.ServingNodeLeaseDeletion) error {
	nodeID := input.NodeID
	leaseID := input.LeaseID
	nodeID, leaseID = strings.TrimSpace(nodeID), strings.TrimSpace(leaseID)
	if !validServingNodeLeaseIdentity(nodeID, leaseID) {
		return store.NewErrInvalidInput("serving_node_lease", "node_id", nil)
	}
	_, err := runSQLTransaction(ctx, s.GetControl().Begin, "delete serving node lease", func(
		ctx context.Context,
		tx *sqlxTxWrapper,
	) (struct{}, error) {
		if err := lockServingNodeLease(ctx, tx, nodeID); err != nil {
			return struct{}{}, err
		}
		if _, err := tx.Exec(ctx, `DELETE FROM serving_node_leases WHERE node_id = ? AND lease_id = ?`, nodeID, leaseID); err != nil {
			return struct{}{}, fmt.Errorf("delete serving node lease: %w", err)
		}
		return struct{}{}, nil
	})
	return err
}

// lockServingNodeLease admits one node's lease write: it shares the recovery
// fence with other nodes and exclusively serializes only the same node ID.
func lockServingNodeLease(ctx context.Context, tx *sqlxTxWrapper, nodeID string) error {
	if err := lockAdvisoryShared(ctx, tx, servingNodeLeaseFenceKey); err != nil {
		return fmt.Errorf("lock serving node lease fence: %w", err)
	}
	if err := lockAdvisory(ctx, tx, advisoryLockKey("serving-node-lease", nodeID)); err != nil {
		return fmt.Errorf("lock serving node lease: %w", err)
	}
	return nil
}

// lockServingNodeLeaseFence exclusively fences every serving node lease write.
func lockServingNodeLeaseFence(ctx context.Context, tx *sqlxTxWrapper) error {
	if err := lockAdvisory(ctx, tx, servingNodeLeaseFenceKey); err != nil {
		return fmt.Errorf("lock serving node lease fence: %w", err)
	}
	return nil
}

func validServingNodeLeaseInput(nodeID, leaseID string, lifetime time.Duration) bool {
	return validServingNodeLeaseIdentity(nodeID, leaseID) &&
		lifetime >= store.ServingNodeLeaseMinimumLifetime &&
		lifetime <= store.ServingNodeLeaseMaximumLifetime &&
		lifetime%time.Millisecond == 0
}

func validServingNodeLeaseIdentity(nodeID, leaseID string) bool {
	return nodeID != "" && len(nodeID) <= store.ClusterDiscoveryNodeIDMaxBytes && model.IsValidOpaqueID(leaseID)
}

func servingNodeLeaseModel(row servingNodeLeaseRow) (*store.ServingNodeLease, error) {
	lease := &store.ServingNodeLease{
		NodeID:    row.NodeID,
		LeaseID:   row.LeaseID,
		UpdatedAt: model.TimeUTC(row.UpdatedAt),
		ExpiresAt: model.TimeUTC(row.ExpiresAt),
	}
	if err := lease.Validate(); err != nil {
		return nil, fmt.Errorf("serving node lease persisted state: %w", err)
	}
	return lease, nil
}

var _ store.ServingNodeLeaseStore = (*SQLServingNodeLeaseStore)(nil)
