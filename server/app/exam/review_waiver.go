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

type WaiveReviewCommand struct {
	ExpectedDeliveryInventoryRevision int64
	Scope                             model.RetentionHoldScope
	ExpectedRevision                  int64
	ExpectedReviewRevision            int64
	ExpectedDiscrepancyCount          int64
	ReasonCode                        string
	PrivateReason                     string
	IdempotencyKey                    string
}

// GetSubmissionReviewWaiver returns the Submission Review Waiver for one
// Submission, or nil when none exists. The Submission's own candidate is
// denied.
func (r *Records) GetSubmissionReviewWaiver(
	ctx context.Context,
	invocation invocation.Invocation,
	scope model.RetentionHoldScope,
) (_ *model.SubmissionReviewWaiver, resultErr error) {
	defer func() { resultErr = publicFailures.Conceal(resultErr) }()
	if !scope.SubmissionID.IsValid() {
		return nil, apperror.Invalid("exam.invalid", "submission_id")
	}

	if _, _, err := r.authorizeScope(
		ctx,
		invocation,
		scope,
		model.ActionExamRecordsComplete,
		model.ActionExamRecordsCompleteOverride,
		true,
	); err != nil {
		return nil, err
	}

	value, found, err := r.records.FindReviewWaiver(ctx, scope)
	if err != nil {
		return nil, examRecordsStoreErrors.Translate(err)
	}
	if !found && value == nil {
		return nil, nil
	}
	if !found || value.Validate() != nil || value.SubmissionID != scope.SubmissionID {
		return nil, apperror.New("exam.unavailable").Wrap(errors.New("Submission Review waiver projection is incomplete"))
	}

	return value, nil
}

// WaiveSubmissionReview idempotently records a Submission Review Waiver,
// fenced by the expected waiver, Review, and delivery inventory revisions and
// the expected discrepancy count. The Submission's own candidate is denied.
func (r *Records) WaiveSubmissionReview(
	ctx context.Context,
	invocation invocation.Invocation,
	command WaiveReviewCommand,
) (_ *model.SubmissionReviewWaiver, resultErr error) {
	defer func() { resultErr = publicFailures.Conceal(resultErr) }()
	if !command.Scope.SubmissionID.IsValid() ||
		command.ExpectedRevision < 0 ||
		command.ExpectedReviewRevision < 0 ||
		command.ExpectedDeliveryInventoryRevision < 0 ||
		command.ExpectedDiscrepancyCount < 0 ||
		model.ValidateRecordsReason(command.ReasonCode, command.PrivateReason) != nil {
		return nil, apperror.Invalid("exam.invalid", "review_waiver")
	}

	key := command.IdempotencyKey
	command.IdempotencyKey = ""
	commandIdempotency, err := idempotency.Require(invocation, store.ExamRecordsWaiveReviewOperation, key, command)
	if err != nil {
		return nil, err
	}

	scope, action, err := r.authorizeScope(
		ctx,
		invocation,
		command.Scope,
		model.ActionExamRecordsComplete,
		model.ActionExamRecordsCompleteOverride,
		true,
	)
	if err != nil {
		return nil, err
	}

	mutation, err := r.beginMutation(
		ctx,
		invocation,
		scope,
		action,
		"waive_review",
		map[string]any{"expected_revision": command.ExpectedRevision, "reason_code": command.ReasonCode},
	)
	if err != nil {
		return nil, err
	}

	result, err := r.records.WaiveReview(
		ctx,
		&store.ExamRecordsReviewWaiver{
			ExamRecordsMutation:               mutation,
			ExpectedRevision:                  command.ExpectedRevision,
			ExpectedReviewRevision:            command.ExpectedReviewRevision,
			ExpectedDeliveryInventoryRevision: command.ExpectedDeliveryInventoryRevision,
			ExpectedDiscrepancyCount:          command.ExpectedDiscrepancyCount,
			ReasonCode:                        command.ReasonCode,
			PrivateReason:                     command.PrivateReason,
		},
		commandIdempotency,
	)
	if err != nil {
		return nil, r.failMutation(ctx, mutation, err)
	}
	if result == nil || result.Waiver.Validate() != nil || result.Waiver.SubmissionID != command.Scope.SubmissionID {
		return nil, apperror.New("exam.unavailable").Wrap(errors.New("Submission Review waiver result is incomplete"))
	}

	return result.Waiver, nil
}
