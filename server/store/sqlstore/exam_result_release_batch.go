// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package sqlstore

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/sudosylabs/proctor/server/model"
	"github.com/sudosylabs/proctor/server/store"
)

type resultReleaseBatchDocument struct {
	ClaimToken    string                      `json:"claim_token,omitempty"`
	ClaimUntil    *time.Time                  `json:"claim_until,omitempty"`
	SubmissionIDs []model.SubmissionID        `json:"submission_ids"`
	Items         []resultReleaseBatchOutcome `json:"items"`
}

func (resultReleaseBatchDocument) outcomeDocument() {}

type resultReleaseBatchOutcome struct {
	SubmissionID model.SubmissionID             `json:"submission_id"`
	Outcome      store.ExamResultReleaseOutcome `json:"outcome"`
	ErrorCode    string                         `json:"error_code,omitempty"`
}

func (s *SQLExamIntegrityReviewStore) ClaimResultReleaseBatch(
	ctx context.Context,
	start *store.ExamResultReleaseBatchStart,
	command *store.CommandIdempotency,
) (*store.ExamResultReleaseBatchClaim, error) {
	if !validReleaseBatchCommand(command) || start == nil || !start.ExamID.IsValid() || !start.SittingID.IsValid() ||
		start.Audit == nil || start.Audit.ActorID != command.UserID || start.Audit.Status != model.AuditStatusAttempt ||
		(start.AllFinalized && len(start.SubmissionIDs) != 0) ||
		(!start.AllFinalized && (len(start.SubmissionIDs) == 0 || len(start.SubmissionIDs) > store.SubmissionListMaximum)) {
		return nil, store.NewErrInvalidInput("result_release_batch", "start", nil)
	}
	return runSQLTransaction(ctx, s.GetMaster().Begin, "claim result release batch", func(ctx context.Context, tx *sqlxTxWrapper) (*store.ExamResultReleaseBatchClaim, error) {
		row, document, err := lockResultReleaseBatch(ctx, tx, command)
		found := err == nil
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return nil, err
		}
		if found && document.ClaimToken == "" {
			items := make([]store.ExamResultReleaseBatchItem, 0, len(document.Items))
			for _, item := range document.Items {
				items = append(items, store.ExamResultReleaseBatchItem(item))
			}
			return &store.ExamResultReleaseBatchClaim{Items: items}, nil
		}

		at, err := jobDatabaseNow(ctx, tx)
		if err != nil {
			return nil, err
		}
		if found && document.ClaimUntil.After(at) {
			return nil, &store.ErrIdempotencyInProgress{}
		}
		if !found {
			document.SubmissionIDs, err = releaseBatchSelection(ctx, tx, start, command.UserID)
			if err != nil {
				return nil, err
			}
		}

		auditID, err := retainReleaseBatchAudit(ctx, tx, row.OriginalAuditID, start.Audit, at)
		if err != nil {
			return nil, err
		}
		document.ClaimToken = model.NewID()
		document.ClaimUntil = new(at.Add(store.ExamResultReleaseBatchLease))
		encoded, err := encodeOutcome(document)
		if err != nil {
			return nil, err
		}
		if found {
			err = updateResultReleaseBatch(ctx, tx, command, encoded, auditID, at)
		} else {
			_, err = tx.Exec(ctx, `INSERT INTO command_outcomes
				(user_id, operation, key_digest, fingerprint, outcome, original_audit_event_id, created_at, expires_at)
				VALUES (?, ?, ?, ?, ?::jsonb, ?, ?, ?)`, command.UserID.String(), command.Operation, command.KeyDigest[:],
				command.Fingerprint[:], string(encoded), auditID.String(), at, at.Add(command.Retention))
		}
		if err != nil {
			return nil, fmt.Errorf("retain result release batch claim: %w", err)
		}
		return &store.ExamResultReleaseBatchClaim{Token: document.ClaimToken, SubmissionIDs: document.SubmissionIDs}, nil
	})
}

func releaseBatchSelection(
	ctx context.Context, tx *sqlxTxWrapper, start *store.ExamResultReleaseBatchStart, viewer model.UserID,
) ([]model.SubmissionID, error) {
	if !start.AllFinalized {
		return slices.Clone(start.SubmissionIDs), nil
	}
	page, err := listSubmissions(ctx, tx, store.SubmissionListOptions{
		ExamID: start.ExamID, SittingID: start.SittingID, ReviewVisible: true, ExcludeCandidateUserID: viewer,
		Sort: store.SubmissionListBySubmitted, Limit: store.SubmissionListMaximum,
		Filter: store.SubmissionListFilter{
			ReviewStates:  []store.SubmissionListReviewState{store.SubmissionListReviewFinalized},
			ReleaseStates: []model.SubmissionReviewReleaseState{model.SubmissionReviewWithheld},
		},
	})
	if err != nil {
		return nil, err
	}
	ids := make([]model.SubmissionID, 0, len(page.Items))
	for _, item := range page.Items {
		ids = append(ids, item.SubmissionID)
	}
	return ids, nil
}

// A recovered batch keeps its audit attempt if the abandonment sweep has not
// terminalized it. The row lock orders a new claim against that sweep.
func retainReleaseBatchAudit(ctx context.Context, tx *sqlxTxWrapper, prior sql.NullString, prepared *model.AuditEvent, at time.Time) (model.AuditEventID, error) {
	if prior.Valid {
		var status model.AuditStatus
		if err := tx.Get(ctx, &status, `SELECT status FROM audit_events WHERE id = ? FOR UPDATE`, prior.String); err != nil {
			return "", fmt.Errorf("load result release batch audit: %w", err)
		}
		if status == model.AuditStatusAttempt {
			return model.AuditEventID(prior.String), nil
		}
	}
	audit, err := insertAuditEventAt(ctx, tx, prepared, at)
	if err != nil {
		return "", fmt.Errorf("begin result release batch audit: %w", err)
	}
	return audit.ID, nil
}

func (s *SQLExamIntegrityReviewStore) CompleteResultReleaseBatch(
	ctx context.Context, completion *store.ExamResultReleaseBatchCompletion, command *store.CommandIdempotency,
) error {
	if !validReleaseBatchCommand(command) || completion == nil || !model.IsValidOpaqueID(completion.Token) ||
		len(completion.Items) > store.SubmissionListMaximum {
		return store.NewErrInvalidInput("result_release_batch", "completion", nil)
	}
	_, err := runSQLTransaction(ctx, s.GetMaster().Begin, "complete result release batch", func(ctx context.Context, tx *sqlxTxWrapper) (struct{}, error) {
		return struct{}{}, completeResultReleaseBatch(ctx, tx, completion, command)
	})
	return err
}

func completeResultReleaseBatch(ctx context.Context, tx *sqlxTxWrapper, completion *store.ExamResultReleaseBatchCompletion, command *store.CommandIdempotency) error {
	row, document, err := lockResultReleaseBatch(ctx, tx, command)
	if err != nil {
		return err
	}
	at, err := jobDatabaseNow(ctx, tx)
	if err != nil {
		return err
	}
	if document.ClaimToken != completion.Token || document.ClaimUntil == nil || !document.ClaimUntil.After(at) {
		return &store.ErrIdempotencyInProgress{}
	}
	if len(completion.Items) != len(document.SubmissionIDs) {
		return store.NewErrInvalidInput("result_release_batch", "items", nil)
	}

	document.Items = make([]resultReleaseBatchOutcome, 0, len(completion.Items))
	counts := map[store.ExamResultReleaseOutcome]int{
		store.ExamResultReleased: 0, store.ExamResultAlreadyReleased: 0, store.ExamResultNotFinalized: 0,
		store.ExamResultReleaseDenied: 0, store.ExamResultReleaseFailed: 0,
	}
	for index, item := range completion.Items {
		if _, valid := counts[item.Outcome]; !valid || item.SubmissionID != document.SubmissionIDs[index] {
			return store.NewErrInvalidInput("result_release_batch", "item", nil)
		}
		counts[item.Outcome]++
		document.Items = append(document.Items, resultReleaseBatchOutcome(item))
	}
	result, err := model.EncodeAuditData(counts)
	if err != nil {
		return err
	}
	auditID := model.AuditEventID(row.OriginalAuditID.String)
	if _, err = completeAuditEvent(ctx, tx, auditID, model.AuditStatusSuccess, "", result, at); err != nil {
		return fmt.Errorf("complete result release batch audit: %w", err)
	}

	document.ClaimToken, document.ClaimUntil = "", nil
	encoded, err := encodeOutcome(document)
	if err != nil {
		return err
	}
	return updateResultReleaseBatch(ctx, tx, command, encoded, auditID, at)
}

func validReleaseBatchCommand(command *store.CommandIdempotency) bool {
	return command != nil && command.Operation == store.ExamResultReleaseBatchOperation &&
		command.UserID.IsValid() && command.Wait > 0 && command.Retention > 0
}

func lockResultReleaseBatch(ctx context.Context, tx *sqlxTxWrapper, command *store.CommandIdempotency) (commandOutcomeRow, resultReleaseBatchDocument, error) {
	var row commandOutcomeRow
	var document resultReleaseBatchDocument
	if err := lockCommandIdempotency(ctx, tx, command); err != nil {
		return row, document, err
	}
	if err := tx.Get(ctx, &row, `SELECT fingerprint, outcome, original_audit_event_id
		FROM command_outcomes WHERE user_id = ? AND operation = ? AND key_digest = ? FOR UPDATE`,
		command.UserID.String(), command.Operation, command.KeyDigest[:]); err != nil {
		return row, document, err
	}
	if !bytes.Equal(row.Fingerprint, command.Fingerprint[:]) {
		return row, document, &store.ErrIdempotencyConflict{}
	}
	if err := decodeOutcome([]byte(row.Outcome), &document); err != nil {
		return row, document, invalidPersistedState("command_outcome", "result_release_batch", err)
	}
	if (document.ClaimToken != "") != (document.ClaimUntil != nil) {
		return row, document, invalidPersistedState("command_outcome", "result_release_batch", errors.New("inconsistent claim"))
	}
	return row, document, nil
}

func updateResultReleaseBatch(
	ctx context.Context, tx *sqlxTxWrapper, command *store.CommandIdempotency, outcome []byte, auditID model.AuditEventID, at time.Time,
) error {
	_, err := tx.Exec(ctx, `UPDATE command_outcomes SET outcome = ?::jsonb, original_audit_event_id = ?, expires_at = ?
		WHERE user_id = ? AND operation = ? AND key_digest = ?`, string(outcome), auditID.String(), at.Add(command.Retention),
		command.UserID.String(), command.Operation, command.KeyDigest[:])
	return err
}
