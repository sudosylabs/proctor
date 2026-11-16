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

// AppendTerminalInput stores one terminal input record from the node that
// serves the terminal. That node holds no lease, so the record is checked
// against the environment's current incarnation instead: the Attempt and the
// environment are share-locked, so a seal or a new incarnation waits for the
// append or is seen by it.
func (s *SQLExamAttemptWorkspaceStore) AppendTerminalInput(
	ctx context.Context,
	input *store.ExamAttemptTerminalInputAppend,
) (*store.ExamAttemptEvidenceAppendResult, error) {
	if !input.Valid() {
		return nil, store.NewErrInvalidInput("attempt_evidence", "terminal_input", nil)
	}

	return runSQLTransaction(ctx, s.GetMaster().Begin, "append terminal input", func(
		ctx context.Context,
		tx *sqlxTxWrapper,
	) (*store.ExamAttemptEvidenceAppendResult, error) {
		var attemptState string
		if err := tx.Get(ctx, &attemptState, `SELECT state FROM exam_attempts WHERE id = ? FOR SHARE`,
			input.AttemptID.String()); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return nil, store.NewErrNotFound("exam_attempt_access", input.AttemptID.String())
			}
			return nil, fmt.Errorf("lock terminal input Attempt: %w", err)
		}
		if attemptState == string(model.ExamAttemptSubmitted) {
			return nil, store.NewErrConflict("exam_attempt", "exam_attempt_state", nil)
		}
		var environment struct {
			Incarnation int64  `db:"incarnation"`
			State       string `db:"state"`
		}
		if err := tx.Get(ctx, &environment, `SELECT incarnation, state
			FROM exam_attempt_environments
			WHERE exam_attempt_id = ?
			FOR SHARE`, input.AttemptID.String()); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return nil, store.NewErrNotFound("attempt_environment", input.AttemptID.String())
			}
			return nil, fmt.Errorf("lock terminal input environment: %w", err)
		}
		if environment.Incarnation != input.Incarnation || environment.State == string(model.AttemptEnvironmentEnded) {
			return nil, store.NewErrConflict("attempt_environment", "attempt_environment_incarnation", nil)
		}

		return appendAttemptEvidence(ctx, tx, input.AttemptID, input.EvidenceID, model.AttemptEvidenceInput,
			input.Author(), input.Sequence, input.Detail, input.ObservedAt)
	})
}
