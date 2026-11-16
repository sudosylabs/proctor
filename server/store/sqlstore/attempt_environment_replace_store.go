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

// attemptEnvironmentReplaceOutcome is the retained outcome of one
// replacement command.
type attemptEnvironmentReplaceOutcome struct {
	ExamID        string `json:"exam_id"`
	SittingID     string `json:"sitting_id"`
	AttemptID     string `json:"attempt_id"`
	ReplaceBefore int64  `json:"replace_before"`
}

func (attemptEnvironmentReplaceOutcome) outcomeDocument() {}

// Replace asks the owner of an Attempt's environment to replace the machine
// of the incarnation the manager saw.
func (s SQLAttemptEnvironmentStore) Replace(
	ctx context.Context,
	input *store.AttemptEnvironmentReplacement,
	command *store.CommandIdempotency,
) (*store.AttemptEnvironmentReplacementResult, error) {
	if input == nil || command == nil || command.Operation != store.AttemptEnvironmentReplaceOperation ||
		command.UserID != input.ActorUserID || !input.ExamID.IsValid() || !input.SittingID.IsValid() ||
		!input.AttemptID.IsValid() || !input.ActorUserID.IsValid() || input.ExpectedIncarnation < 0 ||
		!input.AuditEventID.IsValid() || input.AuditAt.IsZero() {
		return nil, store.NewErrInvalidInput("attempt_environment", "replacement", nil)
	}
	guard := &store.ExamAttemptReallow{ExamID: input.ExamID, SittingID: input.SittingID, AttemptID: input.AttemptID,
		ActorUserID: input.ActorUserID, ManagerOverride: input.ManagerOverride}
	result, err := runIdempotentMutation(ctx, s.SQLStore, "replace attempt environment", idempotentMutation[attemptEnvironmentReplaceOutcome]{
		command:      command,
		auditEventID: input.AuditEventID,
		execute: func(ctx context.Context, tx *sqlxTxWrapper) (attemptEnvironmentReplaceOutcome, error) {
			if err := guardExamAttemptReallowAuthority(ctx, tx, guard, true); err != nil {
				return attemptEnvironmentReplaceOutcome{}, err
			}
			outcome, err := replaceAttemptEnvironment(ctx, tx, input)
			if err != nil {
				return attemptEnvironmentReplaceOutcome{}, err
			}
			return outcome, completeAttemptEnvironmentReplaceAudit(ctx, tx, outcome, "", input)
		},
		encode: func(value attemptEnvironmentReplaceOutcome) ([]byte, error) { return encodeOutcome(value) },
		decode: func(encoded []byte) (attemptEnvironmentReplaceOutcome, error) {
			var value attemptEnvironmentReplaceOutcome
			return value, decodeOutcome(encoded, &value)
		},
		completeReplay: func(ctx context.Context, tx *sqlxTxWrapper, value attemptEnvironmentReplaceOutcome, originalAuditID string) error {
			if err := guardExamAttemptReallowAuthority(ctx, tx, guard, false); err != nil {
				return err
			}
			return completeAttemptEnvironmentReplaceAudit(ctx, tx, value, originalAuditID, input)
		},
	})
	if err != nil {
		return nil, err
	}
	environment, err := s.Get(ctx, input.AttemptID)
	if err != nil {
		return nil, err
	}
	return &store.AttemptEnvironmentReplacementResult{Environment: environment, ReplaceBefore: result.Value.ReplaceBefore,
		Replayed: result.Replayed}, nil
}

// replaceAttemptEnvironment records the replacement on the locked row. Asking
// again for the same incarnation changes nothing.
func replaceAttemptEnvironment(
	ctx context.Context,
	tx *sqlxTxWrapper,
	input *store.AttemptEnvironmentReplacement,
) (attemptEnvironmentReplaceOutcome, error) {
	var row attemptEnvironmentRow
	err := tx.Get(ctx, &row, `SELECT `+attemptEnvironmentColumns+`
		FROM exam_attempt_environments WHERE exam_attempt_id = ? AND exam_sitting_id = ? FOR UPDATE`,
		input.AttemptID.String(), input.SittingID.String())
	if errors.Is(err, sql.ErrNoRows) {
		return attemptEnvironmentReplaceOutcome{}, store.NewErrNotFound("attempt_environment", input.AttemptID.String())
	}
	if err != nil {
		return attemptEnvironmentReplaceOutcome{}, fmt.Errorf("lock attempt environment: %w", err)
	}
	environment, err := row.model()
	if err != nil {
		return attemptEnvironmentReplaceOutcome{}, err
	}
	switch {
	case environment.Desired == model.AttemptEnvironmentDesiredEnded:
		return attemptEnvironmentReplaceOutcome{}, store.NewErrConflict("attempt_environment", "attempt_environment_ended", nil)
	case environment.Incarnation != input.ExpectedIncarnation:
		return attemptEnvironmentReplaceOutcome{}, store.NewErrConflict("attempt_environment", "attempt_environment_incarnation", nil)
	}
	outcome := attemptEnvironmentReplaceOutcome{ExamID: input.ExamID.String(), SittingID: input.SittingID.String(),
		AttemptID: input.AttemptID.String(), ReplaceBefore: environment.Incarnation + 1}
	if environment.ReplaceBefore == outcome.ReplaceBefore {
		return outcome, nil
	}
	if _, err = tx.Exec(ctx, `UPDATE exam_attempt_environments
		SET replace_before = ?, revision = revision + 1, updated_at = GREATEST(updated_at, clock_timestamp())
		WHERE exam_attempt_id = ?`, outcome.ReplaceBefore, input.AttemptID.String()); err != nil {
		return attemptEnvironmentReplaceOutcome{}, fmt.Errorf("replace attempt environment: %w", err)
	}
	return outcome, nil
}

func completeAttemptEnvironmentReplaceAudit(
	ctx context.Context,
	tx *sqlxTxWrapper,
	value attemptEnvironmentReplaceOutcome,
	originalAuditID string,
	input *store.AttemptEnvironmentReplacement,
) error {
	data := map[string]any{"exam_id": value.ExamID, "exam_sitting_id": value.SittingID,
		"exam_attempt_id": value.AttemptID, "replace_before": value.ReplaceBefore, "replayed": originalAuditID != ""}
	if originalAuditID != "" {
		data["original_audit_event_id"] = originalAuditID
	}
	encoded, err := model.EncodeAuditData(data)
	if err != nil {
		return err
	}
	if _, err = completeAuditEvent(ctx, tx, input.AuditEventID, model.AuditStatusSuccess, "", encoded,
		input.AuditAt); err != nil {
		return fmt.Errorf("complete attempt environment replacement audit: %w", err)
	}
	return nil
}
