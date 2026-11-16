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
	"time"

	"github.com/sudosylabs/proctor/server/model"
	"github.com/sudosylabs/proctor/server/store"
)

// SQLAttemptRuntimeLeaseStore implements store.AttemptRuntimeLeaseStore on
// attempt_runtime_leases. Fences come from the attempt_runtime_fences
// sequence, so a released row can be deleted without a later claim reusing
// its fence.
type SQLAttemptRuntimeLeaseStore struct{ *SQLStore }

func newSQLAttemptRuntimeLeaseStore(sqlStore *SQLStore) store.AttemptRuntimeLeaseStore {
	return &SQLAttemptRuntimeLeaseStore{SQLStore: sqlStore}
}

type attemptRuntimeLeaseRow struct {
	AttemptID string    `db:"exam_attempt_id"`
	NodeID    string    `db:"node_id"`
	Fence     int64     `db:"fence"`
	ExpiresAt time.Time `db:"expires_at"`
}

func (row attemptRuntimeLeaseRow) model() (store.AttemptRuntimeLease, error) {
	lease := store.AttemptRuntimeLease{AttemptID: model.ExamAttemptID(row.AttemptID), NodeID: row.NodeID, Fence: row.Fence,
		ExpiresAt: model.TimeUTC(row.ExpiresAt)}
	if !lease.AttemptID.IsValid() || lease.NodeID == "" || lease.Fence < 1 {
		return store.AttemptRuntimeLease{}, invalidPersistedState("attempt_runtime_lease", "value", errors.New("invalid lease"))
	}
	return lease, nil
}

// claimAttemptRuntimeLeasesQuery takes the wanted Attempts that have no live
// lease. The NOT EXISTS filter skips Attempts held in the statement's
// snapshot; the conflict clause rechecks expiry on the newest row version, so
// a claim that committed meanwhile is never replaced.
const claimAttemptRuntimeLeasesQuery = `
	INSERT INTO attempt_runtime_leases AS lease (exam_attempt_id, node_id, fence, acquired_at, renewed_at, expires_at)
	SELECT wanted.exam_attempt_id, ?, nextval('attempt_runtime_fences'), now.at, now.at,
		now.at + ?::double precision * interval '1 millisecond'
	FROM wanted, now
	ORDER BY wanted.exam_attempt_id
	ON CONFLICT (exam_attempt_id) DO UPDATE
	SET node_id = EXCLUDED.node_id, fence = EXCLUDED.fence, acquired_at = EXCLUDED.acquired_at,
		renewed_at = EXCLUDED.renewed_at, expires_at = EXCLUDED.expires_at
	WHERE lease.expires_at <= EXCLUDED.acquired_at
	RETURNING lease.exam_attempt_id, lease.node_id, lease.fence, lease.expires_at`

func validLeaseClaim(nodeID string, lifetime time.Duration) bool {
	return nodeID != "" && len(nodeID) <= 128 && lifetime >= time.Second && lifetime <= 10*time.Minute
}

// Claim takes one attempt's lease when it is free or expired.
func (s SQLAttemptRuntimeLeaseStore) Claim(
	ctx context.Context,
	claim *store.AttemptRuntimeLeaseClaim,
) (*store.AttemptRuntimeLease, error) {
	if claim == nil || !claim.AttemptID.IsValid() || !validLeaseClaim(claim.NodeID, claim.Lifetime) {
		return nil, store.NewErrInvalidInput("attempt_runtime_lease", "claim", nil)
	}
	var rows []attemptRuntimeLeaseRow
	if err := s.GetMaster().Select(ctx, &rows, `
		WITH now AS (SELECT clock_timestamp() AS at),
		wanted AS (
			SELECT ?::varchar AS exam_attempt_id FROM now
			WHERE NOT EXISTS (
				SELECT 1 FROM attempt_runtime_leases held
				WHERE held.exam_attempt_id = ? AND held.expires_at > now.at
			)
		)`+claimAttemptRuntimeLeasesQuery,
		claim.AttemptID.String(), claim.AttemptID.String(), claim.NodeID, claim.Lifetime.Milliseconds()); err != nil {
		return nil, fmt.Errorf("claim attempt runtime lease: %w",
			translateError("attempt_runtime_lease", claim.AttemptID.String(), err))
	}
	if len(rows) == 0 {
		return nil, nil
	}
	lease, err := rows[0].model()
	if err != nil {
		return nil, err
	}
	return &lease, nil
}

// ClaimAvailable takes up to the limit of Attempts whose environment is not done
// and whose lease is free, oldest environment first.
func (s SQLAttemptRuntimeLeaseStore) ClaimAvailable(
	ctx context.Context,
	claim *store.AttemptRuntimeLeaseAvailableClaim,
) ([]store.AttemptRuntimeLease, error) {
	if claim == nil || !validLeaseClaim(claim.NodeID, claim.Lifetime) || claim.Limit < 1 || claim.Limit > 1000 {
		return nil, store.NewErrInvalidInput("attempt_runtime_lease", "claim", nil)
	}
	var rows []attemptRuntimeLeaseRow
	if err := s.GetMaster().Select(ctx, &rows, `
		WITH now AS (SELECT clock_timestamp() AS at),
		wanted AS (
			SELECT environment.exam_attempt_id FROM exam_attempt_environments environment, now
			WHERE NOT (environment.desired = 'ended' AND environment.state = 'ended')
				AND NOT EXISTS (
					SELECT 1 FROM attempt_runtime_leases held
					WHERE held.exam_attempt_id = environment.exam_attempt_id AND held.expires_at > now.at
				)
			ORDER BY environment.created_at, environment.exam_attempt_id
			LIMIT ?
		)`+claimAttemptRuntimeLeasesQuery,
		claim.Limit, claim.NodeID, claim.Lifetime.Milliseconds()); err != nil {
		return nil, fmt.Errorf("claim available attempt runtime leases: %w", err)
	}
	leases := make([]store.AttemptRuntimeLease, 0, len(rows))
	for _, row := range rows {
		lease, err := row.model()
		if err != nil {
			return nil, err
		}
		leases = append(leases, lease)
	}
	return leases, nil
}

// Renew extends the holder's unexpired lease from the database's now.
func (s SQLAttemptRuntimeLeaseStore) Renew(
	ctx context.Context,
	renewal *store.AttemptRuntimeLeaseRenewal,
) (*store.AttemptRuntimeLease, error) {
	if renewal == nil {
		return nil, store.NewErrInvalidInput("attempt_runtime_lease", "renew", nil)
	}
	lease, lifetime := renewal.Lease, renewal.Lifetime
	if !lease.AttemptID.IsValid() || lease.Fence < 1 || !validLeaseClaim(lease.NodeID, lifetime) {
		return nil, store.NewErrInvalidInput("attempt_runtime_lease", "renew", nil)
	}
	var row attemptRuntimeLeaseRow
	err := s.GetMaster().Get(ctx, &row, `
		WITH now AS (SELECT clock_timestamp() AS at)
		UPDATE attempt_runtime_leases AS lease
		SET renewed_at = GREATEST(lease.renewed_at, now.at),
			expires_at = GREATEST(lease.renewed_at, now.at) + ?::double precision * interval '1 millisecond'
		FROM now
		WHERE lease.exam_attempt_id = ? AND lease.node_id = ? AND lease.fence = ? AND lease.expires_at > now.at
		RETURNING lease.exam_attempt_id, lease.node_id, lease.fence, lease.expires_at`,
		lifetime.Milliseconds(), lease.AttemptID.String(), lease.NodeID, lease.Fence)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, store.ErrAttemptRuntimeLeaseLost
	}
	if err != nil {
		return nil, fmt.Errorf("renew attempt runtime lease: %w", err)
	}
	renewed, err := row.model()
	if err != nil {
		return nil, err
	}
	return &renewed, nil
}

// Release deletes the holder's lease, expired or not.
func (s SQLAttemptRuntimeLeaseStore) Release(ctx context.Context, release *store.AttemptRuntimeLeaseRelease) error {
	if release == nil {
		return store.NewErrInvalidInput("attempt_runtime_lease", "release", nil)
	}
	lease := release.Lease
	if !lease.AttemptID.IsValid() || lease.Fence < 1 || lease.NodeID == "" {
		return store.NewErrInvalidInput("attempt_runtime_lease", "release", nil)
	}
	if _, err := s.GetMaster().Exec(ctx, `DELETE FROM attempt_runtime_leases
		WHERE exam_attempt_id = ? AND node_id = ? AND fence = ?`, lease.AttemptID.String(), lease.NodeID, lease.Fence); err != nil {
		return fmt.Errorf("release attempt runtime lease: %w", err)
	}
	return nil
}

var _ store.AttemptRuntimeLeaseStore = (*SQLAttemptRuntimeLeaseStore)(nil)
