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

// TerminalCommand names one Submission Review at an expected revision for a
// terminal step: finalization, or release of the student result.
type TerminalCommand struct {
	SubmissionID           model.SubmissionID
	ReviewID               model.SubmissionReviewID
	ExpectedReviewRevision int64
	IdempotencyKey         string
}

type terminalMutationKind uint8

const (
	terminalFinalize terminalMutationKind = iota + 1
	terminalRelease
)

// FinalizeExamIntegrityReview idempotently finalizes a Submission Review,
// fenced by ExpectedReviewRevision, for a caller authorized to review the
// Submission.
func (s *Service) FinalizeExamIntegrityReview(
	ctx context.Context,
	invocation invocation.Invocation,
	command TerminalCommand,
) (_ Result, resultErr error) {
	defer func() { resultErr = publicFailures.Conceal(resultErr) }()
	return s.terminalMutation(
		ctx,
		invocation,
		command.SubmissionID,
		command.ReviewID,
		command.ExpectedReviewRevision,
		command.IdempotencyKey,
		terminalFinalize,
	)
}

// ReleaseStudentExamResult idempotently and irreversibly releases a finalized
// Submission Review's student result to the candidate, fenced by
// ExpectedReviewRevision, for a caller authorized to release results.
func (s *Service) ReleaseStudentExamResult(
	ctx context.Context,
	invocation invocation.Invocation,
	command TerminalCommand,
) (_ Result, resultErr error) {
	defer func() { resultErr = publicFailures.Conceal(resultErr) }()
	return s.terminalMutation(
		ctx,
		invocation,
		command.SubmissionID,
		command.ReviewID,
		command.ExpectedReviewRevision,
		command.IdempotencyKey,
		terminalRelease,
	)
}

func (s *Service) terminalMutation(
	ctx context.Context,
	invocation invocation.Invocation,
	submissionID model.SubmissionID,
	reviewID model.SubmissionReviewID,
	expectedRevision int64,
	idempotencyKey string,
	kind terminalMutationKind,
) (Result, error) {
	if !submissionID.IsValid() ||
		!reviewID.IsValid() ||
		expectedRevision < 1 ||
		(kind != terminalFinalize && kind != terminalRelease) {
		return Result{}, apperror.Invalid("exam.integrity_review.invalid", "review")
	}

	principal := invocation.Principal()
	if principal.Validate() != nil {
		return Result{}, apperror.Invalid("exam.integrity_review.invalid", "principal")
	}

	operation := store.ExamIntegrityReviewFinalizeOperation
	if kind == terminalRelease {
		operation = store.ExamIntegrityReviewReleaseOperation
	}

	commandIdempotency, err := prepareTerminalIdempotency(invocation, operation, idempotencyKey, submissionID, reviewID, expectedRevision)
	if err != nil {
		return Result{}, err
	}

	var override bool
	var authorization *store.ExamIntegrityReviewAuthorization
	if kind == terminalRelease {
		override, authorization, err = s.authorizeReleaseMutation(ctx, invocation, submissionID)
	} else {
		override, authorization, err = s.authorizeReviewMutation(ctx, invocation, submissionID)
	}

	if err != nil {
		return Result{}, err
	}

	action := model.ActionSubmissionReview
	if kind == terminalRelease {
		action = model.ActionSubmissionRelease
	}

	auditID, err := s.auditor.Begin(ctx, access.Mutation{
		Invocation: invocation,
		Action:     action,
		Resource:   model.Resource{Type: model.ResourceSubmission, ID: submissionID.String()},
		ScopeType:  model.RoleScopeAcademicUnit,
		ScopeID:    authorization.AcademicUnitID.String(),
		Operation:  operation,
		Value: map[string]any{
			"submission_id":            submissionID.String(),
			"submission_review_id":     reviewID.String(),
			"expected_review_revision": expectedRevision,
		},
	})
	if err != nil {
		return Result{}, err
	}

	at := model.TimeUTC(s.now())
	var releasePreparation *store.ExamIntegrityReviewReleasePreparation
	if kind == terminalRelease {
		// The release-time reservation is durable, so it follows the audit
		// attempt like every other examination write.
		releasePreparation, err = s.reviews.PrepareRelease(ctx, &store.ExamIntegrityReviewReleasePrepare{
			SubmissionID:     submissionID,
			ReviewID:         reviewID,
			ExpectedRevision: expectedRevision,
		})
		if err != nil {
			return Result{}, s.failAudit(ctx, auditID, err)
		}

		if releasePreparation == nil || releasePreparation.ReleaseAt.IsZero() {
			return Result{}, s.failAudit(
				ctx,
				auditID,
				apperror.New("exam.integrity_review.unavailable").Wrap(errors.New("invalid result release preparation")),
			)
		}

		at = model.TimeUTC(releasePreparation.ReleaseAt)
	}

	var stored *store.ExamIntegrityReviewMutationResult
	if kind == terminalRelease {
		var notice *store.PreparedMail
		var expectedRecipientRevision int64
		if !releasePreparation.Replayed {
			prepared, prepareErr := s.mail.PrepareResultRelease(ctx, ResultReleaseMailPreparation{
				CandidateUserID:        authorization.CandidateUserID,
				ExamID:                 authorization.ExamID,
				SittingID:              authorization.SittingID,
				ReviewID:               reviewID,
				ExpectedReviewRevision: expectedRevision,
				ReleasedAt:             at,
			})
			if prepareErr != nil || prepared == nil || prepared.Notice == nil || prepared.ExpectedRecipientRevision < 1 {
				if prepareErr == nil {
					prepareErr = errors.New("invalid result release mail preparation")
				}
				return Result{}, s.failAudit(ctx, auditID, apperror.New("exam.integrity_review.unavailable").Wrap(prepareErr))
			}

			notice, expectedRecipientRevision = prepared.Notice, prepared.ExpectedRecipientRevision
		}

		stored, err = s.reviews.Release(ctx, &store.ExamIntegrityReviewRelease{
			SubmissionID:              submissionID,
			ReviewID:                  reviewID,
			ActorUserID:               principal.UserID,
			ManagerOverride:           override,
			ExpectedRevision:          expectedRevision,
			ChangedAt:                 at,
			AuditEventID:              auditID,
			AuditAt:                   at,
			CandidateUserID:           authorization.CandidateUserID,
			Notice:                    notice,
			ExpectedRecipientRevision: expectedRecipientRevision,
		}, commandIdempotency)
	} else {
		stored, err = s.reviews.Finalize(ctx, &store.ExamIntegrityReviewFinalize{
			SubmissionID:     submissionID,
			ReviewID:         reviewID,
			ActorUserID:      principal.UserID,
			ManagerOverride:  override,
			ExpectedRevision: expectedRevision,
			ChangedAt:        at,
			AuditEventID:     auditID,
			AuditAt:          at,
		}, commandIdempotency)
	}

	if err != nil {
		return Result{}, s.failAudit(ctx, auditID, err)
	}

	result, err := projectMutation(stored, submissionID)
	if err != nil {
		return Result{}, err
	}

	if result.Review.ID != reviewID ||
		(kind == terminalRelease && result.Review.ReleaseState != model.SubmissionReviewReleased) ||
		(kind == terminalFinalize && result.Review.State != model.SubmissionReviewFinalized) {
		return Result{}, apperror.New("exam.integrity_review.unavailable").Wrap(errors.New("inconsistent terminal Review result"))
	}

	if kind == terminalRelease {
		s.publishUnlessReplayed(ctx, "exam_integrity_result_released", result, result.Replayed, s.publishResultReleased)
	} else {
		s.publishUnlessReplayed(ctx, "exam_integrity_review_finalized", result, result.Replayed, s.publishReviewFinalized)
	}

	return result, nil
}
