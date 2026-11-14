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
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/lib/pq"

	"github.com/sudosylabs/proctor/server/model"
	"github.com/sudosylabs/proctor/server/store"
)

// SQLPluginLeaseStore implements store.PluginLeaseStore on plugin_leases.
// Fences come from the plugin_lease_fences sequence, so a released row can
// be deleted without a later claim reusing its fence.
type SQLPluginLeaseStore struct{ *SQLStore }

type pluginLeaseRow struct {
	PluginID   string    `db:"plugin_id"`
	Key        string    `db:"key"`
	NodeID     string    `db:"node_id"`
	Fence      int64     `db:"fence"`
	AcquiredAt time.Time `db:"acquired_at"`
	RenewedAt  time.Time `db:"renewed_at"`
	ExpiresAt  time.Time `db:"expires_at"`
}

const pluginLeaseColumns = `plugin_id, key, node_id, fence, acquired_at, renewed_at, expires_at`

// claimPluginLeasesQuery takes every listed key that has no live lease. The
// NOT EXISTS filter skips keys held in the statement's snapshot; the
// conflict clause rechecks expiry on the newest row version, so a claim
// that committed meanwhile is never replaced.
const claimPluginLeasesQuery = `
	WITH now AS (SELECT clock_timestamp() AS at),
	wanted AS (
		SELECT DISTINCT wanted.key FROM unnest(?::text[]) AS wanted(key), now
		WHERE NOT EXISTS (
			SELECT 1 FROM plugin_leases held
			WHERE held.plugin_id = ? AND held.key = wanted.key AND held.expires_at > now.at
		)
		ORDER BY wanted.key
		LIMIT ?
	)
	INSERT INTO plugin_leases AS lease (plugin_id, key, node_id, fence, acquired_at, renewed_at, expires_at)
	SELECT ?, wanted.key, ?, nextval('plugin_lease_fences'), now.at, now.at, now.at + ?::double precision * interval '1 millisecond'
	FROM wanted, now
	ORDER BY wanted.key
	ON CONFLICT (plugin_id, key) DO UPDATE
	SET node_id = EXCLUDED.node_id, fence = EXCLUDED.fence, acquired_at = EXCLUDED.acquired_at,
		renewed_at = EXCLUDED.renewed_at, expires_at = EXCLUDED.expires_at
	WHERE lease.expires_at <= EXCLUDED.acquired_at
	RETURNING ` + pluginLeaseColumns

func newSQLPluginLeaseStore(sqlStore *SQLStore) store.PluginLeaseStore {
	return &SQLPluginLeaseStore{SQLStore: sqlStore}
}

// Claim takes one free or expired key.
func (s SQLPluginLeaseStore) Claim(ctx context.Context, claim *store.PluginLeaseClaim) (*store.PluginLease, error) {
	if claim == nil || !store.ValidPluginRecordKey(claim.Key) {
		return nil, store.NewErrInvalidInput("plugin_lease", "claim", nil)
	}
	leases, err := s.ClaimAvailable(ctx, &store.PluginLeaseAvailableClaim{
		PluginID: claim.PluginID, Keys: []string{claim.Key}, NodeID: claim.NodeID, Lifetime: claim.Lifetime, Limit: 1,
	})
	if err != nil {
		return nil, err
	}
	if len(leases) == 0 {
		return nil, store.ErrPluginLeaseHeld
	}
	return leases[0], nil
}

// ClaimAvailable takes up to the limit of the listed keys that are free or
// expired, in one statement.
func (s SQLPluginLeaseStore) ClaimAvailable(ctx context.Context, claim *store.PluginLeaseAvailableClaim) ([]*store.PluginLease, error) {
	if claim == nil || !claim.PluginID.IsValid() || !store.ValidPluginLeaseNodeID(claim.NodeID) ||
		!store.ValidPluginLeaseLifetime(claim.Lifetime) || len(claim.Keys) == 0 ||
		len(claim.Keys) > store.PluginLeaseClaimMaxKeys || claim.Limit < 1 || claim.Limit > store.PluginLeaseClaimMaxKeys {
		return nil, store.NewErrInvalidInput("plugin_lease", "claim", nil)
	}
	for _, key := range claim.Keys {
		if !store.ValidPluginRecordKey(key) {
			return nil, store.NewErrInvalidInput("plugin_lease", "key", nil)
		}
	}
	var rows []pluginLeaseRow
	if err := s.GetMaster().Select(ctx, &rows, claimPluginLeasesQuery,
		pq.Array(claim.Keys), claim.PluginID.String(), claim.Limit,
		claim.PluginID.String(), claim.NodeID, claim.Lifetime.Milliseconds()); err != nil {
		return nil, fmt.Errorf("claim plugin leases: %w", translateError("plugin_lease", claim.PluginID.String(), err))
	}
	leases := make([]*store.PluginLease, 0, len(rows))
	for _, row := range rows {
		lease, err := row.model()
		if err != nil {
			return nil, err
		}
		leases = append(leases, lease)
	}
	slices.SortFunc(leases, func(a, b *store.PluginLease) int {
		switch {
		case a.Key < b.Key:
			return -1
		case a.Key > b.Key:
			return 1
		}
		return 0
	})
	return leases, nil
}

// Renew extends the holder's unexpired lease from the database's now.
func (s SQLPluginLeaseStore) Renew(ctx context.Context, renewal *store.PluginLeaseRenewal) (*store.PluginLease, error) {
	if renewal == nil || !renewal.PluginID.IsValid() || !store.ValidPluginRecordKey(renewal.Key) ||
		!store.ValidPluginLeaseNodeID(renewal.NodeID) || renewal.Fence < 1 || !store.ValidPluginLeaseLifetime(renewal.Lifetime) {
		return nil, store.NewErrInvalidInput("plugin_lease", "renew", nil)
	}
	var row pluginLeaseRow
	err := s.GetMaster().Get(ctx, &row, `
		WITH now AS (SELECT clock_timestamp() AS at)
		UPDATE plugin_leases AS lease
		SET renewed_at = GREATEST(lease.renewed_at, now.at),
			expires_at = GREATEST(lease.renewed_at, now.at) + ?::double precision * interval '1 millisecond'
		FROM now
		WHERE lease.plugin_id = ? AND lease.key = ? AND lease.node_id = ? AND lease.fence = ?
			AND lease.expires_at > now.at
		RETURNING `+qualifiedPluginLeaseColumns,
		renewal.Lifetime.Milliseconds(), renewal.PluginID.String(), renewal.Key, renewal.NodeID, renewal.Fence)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, store.ErrPluginLeaseLost
	}
	if err != nil {
		return nil, fmt.Errorf("renew plugin lease: %w", err)
	}
	return row.model()
}

// qualifiedPluginLeaseColumns names the columns of the updated row, which
// a join with now would otherwise make ambiguous.
const qualifiedPluginLeaseColumns = `lease.plugin_id, lease.key, lease.node_id, lease.fence, lease.acquired_at, lease.renewed_at, lease.expires_at`

// Release deletes the holder's lease, expired or not.
func (s SQLPluginLeaseStore) Release(ctx context.Context, release *store.PluginLeaseRelease) error {
	if release == nil || !release.PluginID.IsValid() || !store.ValidPluginRecordKey(release.Key) ||
		!store.ValidPluginLeaseNodeID(release.NodeID) || release.Fence < 1 {
		return store.NewErrInvalidInput("plugin_lease", "release", nil)
	}
	if _, err := s.GetMaster().Exec(ctx, `DELETE FROM plugin_leases
		WHERE plugin_id = ? AND key = ? AND node_id = ? AND fence = ?`,
		release.PluginID.String(), release.Key, release.NodeID, release.Fence); err != nil {
		return fmt.Errorf("release plugin lease: %w", err)
	}
	return nil
}

func (row pluginLeaseRow) model() (*store.PluginLease, error) {
	lease := &store.PluginLease{
		PluginID:   model.PluginID(row.PluginID),
		Key:        row.Key,
		NodeID:     row.NodeID,
		Fence:      row.Fence,
		AcquiredAt: model.TimeUTC(row.AcquiredAt),
		RenewedAt:  model.TimeUTC(row.RenewedAt),
		ExpiresAt:  model.TimeUTC(row.ExpiresAt),
	}
	if err := lease.Validate(); err != nil {
		return nil, fmt.Errorf("plugin lease persisted state: %w", err)
	}
	return lease, nil
}

var _ store.PluginLeaseStore = (*SQLPluginLeaseStore)(nil)
