// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package exam

import (
	"context"
	"errors"

	"github.com/sudosylabs/proctor/server/app/apperror"
	"github.com/sudosylabs/proctor/server/app/idempotency"
	"github.com/sudosylabs/proctor/server/app/invocation"
	"github.com/sudosylabs/proctor/server/model"
	"github.com/sudosylabs/proctor/server/store"
)

type CompleteRecordsCommand struct {
	Scope                        model.RetentionHoldScope
	ExpectedRevision             int64
	AcknowledgedEvidenceRevision int64
	IdempotencyKey               string
}

// GetExamSittingRecords returns a Sitting's Sitting Records Completion state
// and pending Submission Review counts to a caller authorized to complete
// records.
func (r *Records) GetExamSittingRecords(
	ctx context.Context,
	invocation invocation.Invocation,
	examID model.ExamID,
	sittingID model.ExamSittingID,
) (_ *store.ExamRecordsCompletionSnapshot, resultErr error) {
	defer func() { resultErr = publicFailures.Conceal(resultErr) }()
	scope := model.RetentionHoldScope{ExamID: examID, SittingID: sittingID}
	if !sittingID.IsValid() {
		return nil, apperror.Invalid("exam.invalid", "exam_sitting_id")
	}

	if _, _, err := r.authorizeScope(
		ctx,
		invocation,
		scope,
		model.ActionExamRecordsComplete,
		model.ActionExamRecordsCompleteOverride,
		false,
	); err != nil {
		return nil, err
	}

	value, err := r.records.GetCompletion(ctx, examID, sittingID)
	if err != nil {
		return nil, examRecordsStoreErrors.Translate(err)
	}

	if !value.ConsistentWith(sittingID) {
		return nil, apperror.New("exam.unavailable").Wrap(errors.New("Exam records completion projection is incomplete"))
	}

	return value, nil
}

// CompleteExamSittingRecords idempotently records Sitting Records Completion
// for a Sitting, fenced by ExpectedRevision and the acknowledged evidence
// revision.
func (r *Records) CompleteExamSittingRecords(
	ctx context.Context,
	invocation invocation.Invocation,
	command CompleteRecordsCommand,
) (_ *model.ExamSittingRecordsCompletion, resultErr error) {
	defer func() { resultErr = publicFailures.Conceal(resultErr) }()
	if !command.Scope.SittingID.IsValid() ||
		!command.Scope.SubmissionID.IsZero() ||
		command.ExpectedRevision < 1 ||
		command.AcknowledgedEvidenceRevision < 0 {
		return nil, apperror.Invalid("exam.invalid", "records_completion")
	}

	key := command.IdempotencyKey
	command.IdempotencyKey = ""
	commandIdempotency, err := idempotency.Require(invocation, store.ExamRecordsCompleteOperation, key, command)
	if err != nil {
		return nil, err
	}

	scope, action, err := r.authorizeScope(
		ctx,
		invocation,
		command.Scope,
		model.ActionExamRecordsComplete,
		model.ActionExamRecordsCompleteOverride,
		false,
	)
	if err != nil {
		return nil, err
	}

	mutation, err := r.beginMutation(ctx, invocation, scope, action, "complete_records", map[string]any{"expected_revision": command.ExpectedRevision,
		"acknowledged_evidence_revision": command.AcknowledgedEvidenceRevision})
	if err != nil {
		return nil, err
	}

	result, err := r.records.CompleteRecords(ctx, &store.ExamRecordsCompletion{
		ExamRecordsMutation:          mutation,
		ExpectedRevision:             command.ExpectedRevision,
		AcknowledgedEvidenceRevision: command.AcknowledgedEvidenceRevision,
	}, commandIdempotency)
	if err != nil {
		return nil, r.failMutation(ctx, mutation, err)
	}
	if result == nil || result.Completion.Validate() != nil || result.Completion.SittingID != command.Scope.SittingID {
		return nil, apperror.New("exam.unavailable").Wrap(errors.New("Exam records completion result is incomplete"))
	}

	return result.Completion, nil
}
