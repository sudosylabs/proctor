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

// UpdateDraftExecution replaces the draft's execution policy behind the
// manager, archive and revision fences.
func (s SQLExamAuthoringStore) UpdateDraftExecution(
	ctx context.Context,
	input *store.ExamDraftExecutionUpdate,
	command *store.CommandIdempotency,
) (*store.ExamAuthoringCommandResult, error) {
	if input == nil || !input.ExamID.IsValid() || !input.ActorUserID.IsValid() || input.ExpectedRevision < 1 ||
		input.UpdatedAt.IsZero() || !input.AuditEventID.IsValid() || input.AuditAt.IsZero() {
		return nil, store.NewErrInvalidInput("exam_draft", "execution_update", nil)
	}
	if err := input.Execution.Validate(); err != nil {
		return nil, store.NewErrInvalidInput("exam_draft", "execution", nil).Wrap(err)
	}
	if command == nil {
		return nil, store.NewErrInvalidInput("exam_draft", "idempotency", nil)
	}

	result, err := runIdempotentMutation(ctx, s.SQLStore, "exam Draft execution update", idempotentMutation[*store.ExamAuthoringSnapshot]{
		command:      command,
		auditEventID: input.AuditEventID,
		execute: func(ctx context.Context, tx *sqlxTxWrapper) (*store.ExamAuthoringSnapshot, error) {
			return updateExamDraftExecution(ctx, tx, input)
		},
		encode: encodeExamAuthoringOutcome,
		decode: func(data []byte) (*store.ExamAuthoringSnapshot, error) {
			var outcome examAuthoringOutcome
			if decodeErr := decodeOutcome(data, &outcome); decodeErr != nil {
				return nil, decodeErr
			}
			return outcome.model()
		},
		completeReplay: func(ctx context.Context, tx *sqlxTxWrapper, snapshot *store.ExamAuthoringSnapshot, originalAuditID string) error {
			encoded, encodeErr := model.EncodeAuditData(map[string]any{
				"exam_id":                 snapshot.Exam.ID.String(),
				"draft_revision":          snapshot.Draft.Revision,
				"idempotency_replayed":    true,
				"original_audit_event_id": originalAuditID,
			})
			if encodeErr != nil {
				return encodeErr
			}
			_, completeErr := completeAuditEvent(ctx, tx, input.AuditEventID, model.AuditStatusSuccess, "", encoded, input.AuditAt)
			return completeErr
		},
	})
	if err != nil {
		return nil, err
	}
	return &store.ExamAuthoringCommandResult{Value: result.Value, Replayed: result.Replayed}, nil
}

func updateExamDraftExecution(
	ctx context.Context,
	tx *sqlxTxWrapper,
	input *store.ExamDraftExecutionUpdate,
) (*store.ExamAuthoringSnapshot, error) {
	var row examAuthoringRow
	query := examAuthoringSelect + ` WHERE e.id = ? FOR UPDATE OF e, d`
	if err := tx.Get(ctx, &row, query, input.ActorUserID.String(), input.ExamID.String()); err != nil {
		return nil, translateError("exam", input.ExamID.String(), err)
	}
	snapshot, err := row.model()
	if err != nil {
		return nil, err
	}
	if !snapshot.ActorIsManager && !input.ManagerOverride {
		return nil, store.NewErrNotFound("exam_manager", input.ActorUserID.String())
	}
	if snapshot.Exam.IsArchived() {
		return nil, store.NewErrConflict("exam", "exam_archived", nil)
	}
	if snapshot.Draft.Revision != input.ExpectedRevision {
		return nil, store.NewErrConflict("exam_draft", "exam_draft_revision", nil)
	}
	if err = requireUsableEnvironmentProfile(ctx, tx, input.Execution); err != nil {
		return nil, err
	}

	candidate := *snapshot.Draft
	changed, err := candidate.ApplyExecutionPolicy(input.Execution, model.TimeUTC(input.UpdatedAt))
	if err != nil {
		return nil, store.NewErrInvalidInput("exam_draft", "execution", nil).Wrap(err)
	}
	if !changed {
		return nil, store.NewErrConflict("exam_draft", "exam_draft_no_changes", nil)
	}
	policy, err := model.EncodeExamPolicySet(candidate.Policy)
	if err != nil {
		return nil, err
	}
	result, err := tx.Exec(ctx, `UPDATE exam_drafts
		SET policy = ?::jsonb, updated_at = ?, updated_by_user_id = ?, revision = ?
		WHERE exam_id = ?
			AND revision = ?`,
		string(policy), candidate.UpdatedAt, input.ActorUserID.String(), candidate.Revision, input.ExamID.String(), input.ExpectedRevision)
	if err != nil {
		return nil, fmt.Errorf("update exam Draft execution: %w", translateError("exam_draft", input.ExamID.String(), err))
	}
	if affected, rowsErr := result.RowsAffected(); rowsErr != nil {
		return nil, fmt.Errorf("inspect exam Draft execution update: %w", rowsErr)
	} else if affected != 1 {
		return nil, store.NewErrConflict("exam_draft", "exam_draft_revision", nil)
	}

	row.Policy = jsonValue(policy)
	row.DraftUpdatedAt = candidate.UpdatedAt
	row.DraftUpdatedBy = input.ActorUserID.String()
	row.DraftRevision = candidate.Revision
	auditData, err := model.EncodeAuditData(map[string]any{"exam_id": input.ExamID.String(), "draft_revision": candidate.Revision})
	if err != nil {
		return nil, err
	}
	if _, err = completeAuditEvent(ctx, tx, input.AuditEventID, model.AuditStatusSuccess, "", auditData, input.AuditAt); err != nil {
		return nil, fmt.Errorf("complete exam Draft execution audit: %w", err)
	}
	return row.model()
}

// requireUsableEnvironmentProfile checks, under a share lock that holds off
// a concurrent retirement, that an enabled execution policy names a profile
// that exists, is not retired and allows its terminals. A disabled policy
// names none.
func requireUsableEnvironmentProfile(ctx context.Context, tx *sqlxTxWrapper, policy model.ExecutionPolicy) error {
	if !policy.Enabled {
		return nil
	}
	var profile struct {
		MaxTerminals int          `db:"max_terminals"`
		RetiredAt    sql.NullTime `db:"retired_at"`
	}
	err := tx.Get(ctx, &profile, `SELECT max_terminals, retired_at FROM environment_profiles WHERE id = ? FOR SHARE`,
		policy.Profile.String())
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return store.NewErrConflict("exam_draft", "environment_profile_unusable", nil)
	case err != nil:
		return fmt.Errorf("read the execution policy's environment profile: %w", err)
	case profile.RetiredAt.Valid || policy.Terminals > profile.MaxTerminals:
		return store.NewErrConflict("exam_draft", "environment_profile_unusable", nil)
	}
	return nil
}
