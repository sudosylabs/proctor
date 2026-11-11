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

	"github.com/sudosylabs/proctor/server/app/access"
	"github.com/sudosylabs/proctor/server/app/apperror"
	"github.com/sudosylabs/proctor/server/app/invocation"
	"github.com/sudosylabs/proctor/server/model"
	"github.com/sudosylabs/proctor/server/store"
)

type SaveDecisionCommand struct {
	SubmissionID             model.SubmissionID
	ReviewID                 model.SubmissionReviewID
	FlagID                   model.IntegrityFlagID
	ExpectedReviewRevision   int64
	ExpectedDecisionRevision int64
	Outcome                  model.IntegrityReviewOutcome
	PrivateRationale         string
	IdempotencyKey           string
}

type UpdateDraftCommand struct {
	SubmissionID           model.SubmissionID
	ReviewID               model.SubmissionReviewID
	ExpectedReviewRevision int64
	ManagerNotes           string
	StudentRemarksMarkdown string
	IdempotencyKey         string
}

// SaveExamIntegrityDecision idempotently records the reviewer's decision on
// one Integrity Flag, creating the Submission Review when
// ExpectedReviewRevision is zero. It is fenced by the expected Review and
// decision revisions.
func (s *Service) SaveExamIntegrityDecision(
	ctx context.Context,
	inv invocation.Invocation,
	command SaveDecisionCommand,
) (_ Result, resultErr error) {
	defer func() { resultErr = publicFailures.Conceal(resultErr) }()
	if !command.SubmissionID.IsValid() ||
		!command.FlagID.IsValid() ||
		command.ExpectedReviewRevision < 0 ||
		command.ExpectedDecisionRevision < 0 ||
		(command.ExpectedReviewRevision == 0 && !command.ReviewID.IsZero()) ||
		(command.ExpectedReviewRevision > 0 && !command.ReviewID.IsValid()) {
		return Result{}, apperror.Invalid("exam.integrity_review.invalid", "decision")
	}

	principal := inv.Principal()
	if principal.Validate() != nil {
		return Result{}, apperror.Invalid("exam.integrity_review.invalid", "principal")
	}

	commandIdempotency, err := prepareDecisionIdempotency(inv, command)
	if err != nil {
		return Result{}, err
	}

	reviewID := command.ReviewID
	if reviewID.IsZero() {
		reviewID = s.newReviewID()
	}

	decisionID := s.newDecisionID()
	if _, decisionErr := model.NewIntegrityReviewDecision(
		decisionID,
		reviewID,
		command.FlagID,
		command.Outcome,
		principal.UserID,
		command.PrivateRationale,
		s.now(),
	); decisionErr != nil {
		return Result{}, apperror.Invalid("exam.integrity_review.invalid", "decision").Wrap(decisionErr)
	}

	override, authorization, err := s.authorizeReviewMutation(ctx, inv, command.SubmissionID)
	if err != nil {
		return Result{}, err
	}

	at := model.TimeUTC(s.now())
	auditID, err := s.auditor.Begin(ctx, access.Mutation{
		Invocation: inv,
		Action:     model.ActionSubmissionReview,
		Resource:   model.Resource{Type: model.ResourceSubmission, ID: command.SubmissionID.String()},
		ScopeType:  model.RoleScopeAcademicUnit,
		ScopeID:    authorization.AcademicUnitID.String(),
		Operation:  store.ExamIntegrityReviewDecisionOperation,
		Value: map[string]any{
			"submission_id":              command.SubmissionID.String(),
			"integrity_flag_id":          command.FlagID.String(),
			"expected_review_revision":   command.ExpectedReviewRevision,
			"expected_decision_revision": command.ExpectedDecisionRevision,
			"outcome":                    string(command.Outcome),
		},
	})
	if err != nil {
		return Result{}, err
	}

	stored, err := s.reviews.SaveDecision(ctx, &store.ExamIntegrityReviewDecisionMutation{
		SubmissionID:           command.SubmissionID,
		ReviewID:               reviewID,
		DecisionID:             decisionID,
		FlagID:                 command.FlagID,
		ActorUserID:            principal.UserID,
		ManagerOverride:        override,
		ExpectedReviewRevision: command.ExpectedReviewRevision,
		ExpectedRevision:       command.ExpectedDecisionRevision,
		Outcome:                command.Outcome,
		PrivateRationale:       command.PrivateRationale,
		ChangedAt:              at,
		AuditEventID:           auditID,
		AuditAt:                at,
	}, commandIdempotency)
	if err != nil {
		return Result{}, s.failAudit(ctx, auditID, err)
	}

	result, err := projectMutation(stored, command.SubmissionID)
	if err != nil {
		return Result{}, err
	}
	if !result.Replayed && (result.Review.ID != reviewID || result.Decision == nil || result.Decision.ID != decisionID) {
		return Result{}, apperror.New("exam.integrity_review.unavailable").Wrap(errors.New("unexpected fresh Review decision identities"))
	}

	s.publishUnlessReplayed(ctx, "exam_integrity_review_changed", result, result.Replayed, s.publishReviewChanged)
	return result, nil
}

// UpdateExamIntegrityReview idempotently saves the draft remarks of a
// Submission Review, creating the Review when ExpectedReviewRevision is zero.
func (s *Service) UpdateExamIntegrityReview(
	ctx context.Context,
	inv invocation.Invocation,
	command UpdateDraftCommand,
) (_ Result, resultErr error) {
	defer func() { resultErr = publicFailures.Conceal(resultErr) }()
	if !command.SubmissionID.IsValid() ||
		command.ExpectedReviewRevision < 0 ||
		(command.ExpectedReviewRevision == 0 && !command.ReviewID.IsZero()) ||
		(command.ExpectedReviewRevision > 0 && !command.ReviewID.IsValid()) {
		return Result{}, apperror.Invalid("exam.integrity_review.invalid", "draft")
	}

	principal := inv.Principal()
	if principal.Validate() != nil {
		return Result{}, apperror.Invalid("exam.integrity_review.invalid", "principal")
	}

	commandIdempotency, err := prepareDraftIdempotency(inv, command)
	if err != nil {
		return Result{}, err
	}

	reviewID := command.ReviewID
	if reviewID.IsZero() {
		reviewID = s.newReviewID()
	}

	probe, err := model.NewSubmissionReview(reviewID, command.SubmissionID, principal.UserID, s.now())
	if err != nil {
		return Result{}, apperror.Invalid("exam.integrity_review.invalid", "draft").Wrap(err)
	}
	if err = probe.UpdateDraft(1, command.ManagerNotes, command.StudentRemarksMarkdown, s.now()); err != nil {
		return Result{}, apperror.Invalid("exam.integrity_review.invalid", "draft").Wrap(err)
	}

	override, authorization, err := s.authorizeReviewMutation(ctx, inv, command.SubmissionID)
	if err != nil {
		return Result{}, err
	}

	at := model.TimeUTC(s.now())
	auditID, err := s.auditor.Begin(ctx, access.Mutation{
		Invocation: inv,
		Action:     model.ActionSubmissionReview,
		Resource:   model.Resource{Type: model.ResourceSubmission, ID: command.SubmissionID.String()},
		ScopeType:  model.RoleScopeAcademicUnit,
		ScopeID:    authorization.AcademicUnitID.String(),
		Operation:  store.ExamIntegrityReviewDraftOperation,
		Value:      map[string]any{"submission_id": command.SubmissionID.String(), "expected_review_revision": command.ExpectedReviewRevision},
	})
	if err != nil {
		return Result{}, err
	}

	stored, err := s.reviews.UpdateDraft(ctx, &store.ExamIntegrityReviewDraftMutation{
		SubmissionID:           command.SubmissionID,
		ReviewID:               reviewID,
		ActorUserID:            principal.UserID,
		ManagerOverride:        override,
		ExpectedRevision:       command.ExpectedReviewRevision,
		ManagerNotes:           command.ManagerNotes,
		StudentRemarksMarkdown: command.StudentRemarksMarkdown,
		ChangedAt:              at,
		AuditEventID:           auditID,
		AuditAt:                at,
	}, commandIdempotency)
	if err != nil {
		return Result{}, s.failAudit(ctx, auditID, err)
	}

	result, err := projectMutation(stored, command.SubmissionID)
	if err != nil {
		return Result{}, err
	}
	if !result.Replayed && result.Review.ID != reviewID {
		return Result{}, apperror.New("exam.integrity_review.unavailable").Wrap(errors.New("unexpected fresh Review identity"))
	}

	s.publishUnlessReplayed(ctx, "exam_integrity_review_changed", result, result.Replayed, s.publishReviewChanged)
	return result, nil
}
