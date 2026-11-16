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

	"github.com/sudosylabs/proctor/server/model"
	"github.com/sudosylabs/proctor/server/store"
)

// lockAttemptEnvironmentReadGuard checks an environment owner's access for
// a read or an evidence append: the access's lease is the attempt's current
// unexpired one, its incarnation the environment's current one, and the
// attempt has no submission. It share-locks the attempt, the lease and the
// environment, so a seal, a new claim or a new incarnation waits for the
// caller or is seen by it.
func lockAttemptEnvironmentReadGuard(
	ctx context.Context,
	tx *sqlxTxWrapper,
	access store.ExamAttemptWorkspaceEnvironmentAccess,
) error {
	if !access.Valid() {
		return store.NewErrInvalidInput("attempt_workspace", "access", nil)
	}
	var state string
	if err := tx.Get(ctx, &state, `SELECT state FROM exam_attempts WHERE id = ? FOR SHARE`, access.AttemptID.String()); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return store.NewErrNotFound("exam_attempt_access", access.AttemptID.String())
		}
		return fmt.Errorf("lock execution environment Attempt: %w", err)
	}
	return checkAttemptEnvironmentOwner(ctx, tx, access, state)
}

// checkAttemptEnvironmentOwner requires the access's lease and incarnation to
// be current and the attempt, already locked in state attemptState, to have
// no submission. The lease and environment rows are share-locked.
func checkAttemptEnvironmentOwner(
	ctx context.Context,
	tx *sqlxTxWrapper,
	access store.ExamAttemptWorkspaceEnvironmentAccess,
	attemptState string,
) error {
	if attemptState == string(model.ExamAttemptSubmitted) {
		return store.NewErrConflict("exam_attempt", "exam_attempt_state", nil)
	}
	var current []int64
	if err := tx.Select(ctx, &current, `SELECT lease.fence
		FROM attempt_runtime_leases lease
		JOIN exam_attempt_environments environment ON environment.exam_attempt_id = lease.exam_attempt_id
		WHERE lease.exam_attempt_id = ?
			AND lease.fence = ?
			AND lease.expires_at > statement_timestamp()
			AND environment.incarnation = ?
			AND environment.state <> 'ended'
		FOR SHARE OF lease, environment`, access.AttemptID.String(), access.Fence, access.Incarnation); err != nil {
		return fmt.Errorf("check execution environment owner: %w", err)
	}
	if len(current) != 1 {
		return store.NewErrConflict("attempt_environment", "attempt_environment_owner", nil)
	}
	return nil
}
