// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package review

import (
	"context"
	"errors"
	"strings"
	"unicode/utf8"

	"github.com/sudosylabs/proctor/server/app/access"
	"github.com/sudosylabs/proctor/server/app/apperror"
	"github.com/sudosylabs/proctor/server/app/idempotency"
	"github.com/sudosylabs/proctor/server/app/invocation"
	"github.com/sudosylabs/proctor/server/model"
	"github.com/sudosylabs/proctor/server/store"
)

// ReopenCommand returns one finalized review to draft. Reason is the
// manager's private explanation; the candidate never sees it.
type ReopenCommand struct {
	SubmissionID           model.SubmissionID
	ReviewID               model.SubmissionReviewID
	ExpectedReviewRevision int64
	Reason                 string
	IdempotencyKey         string
}

// ReopenExamIntegrityReview idempotently returns a finalized review to draft
// so its decisions can be revised, keeping them. A review whose result is
// released can't be reopened, because release is final.
func (s *Service) ReopenExamIntegrityReview(
	ctx context.Context,
	inv invocation.Invocation,
	command ReopenCommand,
) (_ Result, resultErr error) {
	defer func() { resultErr = publicFailures.Conceal(resultErr) }()
	command.Reason = strings.TrimSpace(command.Reason)
	if !command.SubmissionID.IsValid() || !command.ReviewID.IsValid() || command.ExpectedReviewRevision < 1 {
		return Result{}, apperror.Invalid("exam.integrity_review.invalid", "review")
	}
	if command.Reason == "" || utf8.RuneCountInString(command.Reason) > model.SubmissionReviewReopenReasonMaxRunes {
		return Result{}, apperror.Invalid("exam.integrity_review.invalid", "reason")
	}

	principal := inv.Principal()
	if principal.Validate() != nil {
		return Result{}, apperror.Invalid("exam.integrity_review.invalid", "principal")
	}

	commandIdempotency, err := idempotency.Require(inv, store.ExamIntegrityReviewReopenOperation, command.IdempotencyKey, struct {
		SubmissionID     string `json:"submission_id"`
		ReviewID         string `json:"submission_review_id"`
		ExpectedRevision int64  `json:"expected_review_revision"`
		Reason           string `json:"reason"`
	}{command.SubmissionID.String(), command.ReviewID.String(), command.ExpectedReviewRevision, command.Reason})
	if err != nil {
		return Result{}, err
	}

	override, authorization, err := s.authorizeReviewMutation(ctx, inv, command.SubmissionID)
	if err != nil {
		return Result{}, err
	}

	// The reason is unbounded manager prose about a candidate, so the audit
	// records only that one was given.
	auditID, err := s.auditor.Begin(ctx, access.Mutation{
		Invocation: inv,
		Action:     model.ActionSubmissionReview,
		Resource:   model.Resource{Type: model.ResourceSubmission, ID: command.SubmissionID.String()},
		ScopeType:  model.RoleScopeAcademicUnit,
		ScopeID:    authorization.AcademicUnitID.String(),
		Operation:  store.ExamIntegrityReviewReopenOperation,
		Value: map[string]any{
			"submission_id":            command.SubmissionID.String(),
			"submission_review_id":     command.ReviewID.String(),
			"expected_review_revision": command.ExpectedReviewRevision,
		},
	})
	if err != nil {
		return Result{}, err
	}

	at := model.TimeUTC(s.now())
	stored, err := s.reviews.Reopen(ctx, &store.ExamIntegrityReviewReopen{
		SubmissionID:     command.SubmissionID,
		ReviewID:         command.ReviewID,
		ActorUserID:      principal.UserID,
		ManagerOverride:  override,
		ExpectedRevision: command.ExpectedReviewRevision,
		Reason:           command.Reason,
		ChangedAt:        at,
		AuditEventID:     auditID,
		AuditAt:          at,
	}, commandIdempotency)
	if err != nil {
		return Result{}, s.failAudit(ctx, auditID, err)
	}

	result, err := projectMutation(stored)
	if err != nil {
		return Result{}, err
	}
	if result.Review.State != model.SubmissionReviewDraft || result.Review.ReleaseState != model.SubmissionReviewWithheld {
		return Result{}, apperror.New("exam.integrity_review.unavailable").Wrap(errors.New("inconsistent reopened Review result"))
	}

	s.publishUnlessReplayed(ctx, "exam_integrity_review_changed", result, result.Replayed, s.publishReviewChanged)
	return result, nil
}
