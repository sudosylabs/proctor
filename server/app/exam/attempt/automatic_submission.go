// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package attempt

import (
	"context"
	"errors"

	"github.com/sudosylabs/proctor/server/app/apperror"
	"github.com/sudosylabs/proctor/server/app/invocation"
	"github.com/sudosylabs/proctor/server/model"
	"github.com/sudosylabs/proctor/server/store"
)

// AutomaticSubmissionResult is the receipt of a submission sealed at sitting
// close; ConnectionClosed reports that sealing also closed the live connection.
type AutomaticSubmissionResult struct {
	SubmissionResult
	ConnectionClosed bool
}

// ListAutomaticSealTargets returns up to limit unsealed attempts ordered by ID
// strictly after the cursor (zero starts from the beginning), or an empty,
// non-nil slice when nothing remains. system names the sealing job.
func (s *Service) ListAutomaticSealTargets(
	ctx context.Context,
	system invocation.System,
	sittingID model.ExamSittingID,
	after model.ExamAttemptID,
	limit int,
) ([]store.ExamSubmissionAutomaticSealTarget, error) {
	if !system.Valid() || !sittingID.IsValid() || (!after.IsZero() && !after.IsValid()) || limit < 1 || limit > 200 {
		return nil, apperror.Invalid("exam.attempt.invalid", "automatic_seal_list")
	}

	items, err := s.submissions.ListAutomaticSealTargets(ctx, store.ExamSubmissionAutomaticSealListOptions{
		SittingID:      sittingID,
		AfterAttemptID: after,
		Limit:          limit,
	})
	if err != nil {
		return nil, storeError(storeErrors, err)
	}

	previous := after
	for _, item := range items {
		if !validAutomaticSealTarget(item, sittingID) || (!previous.IsZero() && item.AttemptID.String() <= previous.String()) {
			return nil, apperror.New("exam.attempt.unavailable").Wrap(errors.New("invalid automatic Submission target page"))
		}

		previous = item.AttemptID
	}

	if items == nil {
		items = []store.ExamSubmissionAutomaticSealTarget{}
	}

	return items, nil
}

func validAutomaticSealTarget(target store.ExamSubmissionAutomaticSealTarget, sittingID model.ExamSittingID) bool {
	return target.ExamID.IsValid() &&
		target.SittingID == sittingID &&
		target.ClassID.IsValid() &&
		target.AcademicUnitID.IsValid() &&
		target.CandidateUserID.IsValid() &&
		target.AttemptID.IsValid() &&
		target.WorkspaceID.IsValid() &&
		target.CurrentRevisionID.IsValid() &&
		target.ParticipationID.IsValid() &&
		target.Generation > 0 &&
		target.ConnectionID.IsValid()
}

// SealForSittingClose seals one target under a system audit tied to the
// invoking job. A replay returns the stored receipt and skips the mail and
// post-commit effects.
func (s *Service) SealForSittingClose(
	ctx context.Context,
	system invocation.System,
	target store.ExamSubmissionAutomaticSealTarget,
) (AutomaticSubmissionResult, error) {
	if !system.Valid() || !validAutomaticSealTarget(target, target.SittingID) {
		return AutomaticSubmissionResult{}, apperror.Invalid("exam.attempt.invalid", "automatic_seal")
	}

	preparation, err := s.submissions.PrepareAutomaticSeal(ctx, target)
	if err != nil {
		return AutomaticSubmissionResult{}, storeError(storeErrors, err)
	}
	if preparation == nil || preparation.SealAt.IsZero() {
		return AutomaticSubmissionResult{}, apperror.New("exam.attempt.unavailable").Wrap(errors.New("invalid automatic Submission preparation"))
	}
	// The closing sitting already asked every environment to settle and
	// waited the budget. Asking for no new settle here means a sitting of
	// unreachable hosts seals without spending a budget per attempt.
	if !preparation.Replayed && s.settler != nil {
		s.settler.AwaitPendingSettle(ctx, target.AttemptID)
	}

	auditID, err := s.systemAuditor.Begin(
		ctx,
		model.ActionExamSittingManage,
		model.Resource{Type: model.ResourceExamSitting, ID: target.SittingID.String()},
		model.RoleScopeClass,
		target.ClassID.String(),
		store.ExamSubmissionAutomaticSealOperation,
		map[string]any{
			"exam_id":         target.ExamID.String(),
			"exam_sitting_id": target.SittingID.String(),
			"exam_attempt_id": target.AttemptID.String(),
			"job_id":          system.JobID.String(),
			"job_attempt_id":  system.AttemptID.String(),
		},
	)
	if err != nil {
		return AutomaticSubmissionResult{}, err
	}

	at := preparation.SealAt
	proposed, receiptCode := s.newSubmissionID(), s.newReceiptCode()
	if !proposed.IsValid() {
		return AutomaticSubmissionResult{}, s.failSystemAudit(ctx, auditID, apperror.Invalid("exam.attempt.invalid", "submission_id"))
	}
	if !receiptCode.IsValid() {
		return AutomaticSubmissionResult{}, s.failSystemAudit(ctx, auditID, apperror.Invalid("exam.attempt.invalid", "receipt_code"))
	}

	eventID, eventJobID := s.newDomainEventID(), s.newJobID()
	if !eventID.IsValid() || !eventJobID.IsValid() {
		return AutomaticSubmissionResult{}, s.failSystemAudit(ctx, auditID, apperror.Invalid("exam.attempt.invalid", "domain_event_id"))
	}

	var notice *store.PreparedMail
	var recipient *model.User
	if !preparation.Replayed {
		preparedMail, prepareErr := s.mail.PrepareSubmissionReceipt(ctx, SubmissionMailPreparation{
			CandidateUserID: target.CandidateUserID,
			ExamID:          target.ExamID,
			SittingID:       target.SittingID,
			SubmissionID:    proposed,
			ReceiptCode:     receiptCode,
			SealedAt:        at,
			Provenance:      model.ExamSubmissionSittingClosed,
		})
		if prepareErr != nil || preparedMail == nil || preparedMail.Notice == nil || preparedMail.Recipient == nil {
			if prepareErr == nil {
				prepareErr = errors.New("invalid automatic Submission receipt mail preparation")
			}
			return AutomaticSubmissionResult{}, s.failSystemAudit(ctx, auditID, apperror.New("exam.attempt.unavailable").Wrap(prepareErr))
		}

		notice, recipient = preparedMail.Notice, preparedMail.Recipient
	}

	stored, err := s.submissions.SealForSittingClose(ctx, &store.ExamSubmissionAutomaticSeal{
		Target:           target,
		SubmissionID:     proposed,
		ReceiptCode:      receiptCode,
		AuditEventID:     auditID,
		AuditAt:          at,
		Notice:           notice,
		Recipient:        recipient,
		DomainEventID:    eventID,
		DomainEventJobID: eventJobID,
	})
	if err != nil {
		return AutomaticSubmissionResult{}, s.failSystemAudit(ctx, auditID, storeError(storeErrors, err))
	}

	result, err := projectAutomaticSubmissionResult(stored)
	if err != nil {
		return AutomaticSubmissionResult{}, err
	}

	if !result.Replayed {
		if effectErr := s.publishAttemptSealedForSittingClose(ctx, result); effectErr != nil {
			s.publisher.Report(ctx, "exam_attempt_sealed_for_sitting_close", effectErr)
		}
	}

	return result, nil
}

func projectAutomaticSubmissionResult(stored *store.ExamSubmissionAutomaticSealResult) (AutomaticSubmissionResult, error) {
	if !stored.Sealed() {
		return AutomaticSubmissionResult{}, apperror.New("exam.attempt.unavailable").Wrap(errors.New("inconsistent automatic Submission result"))
	}

	return AutomaticSubmissionResult{SubmissionResult: SubmissionResult{
		Receipt:         stored.Receipt,
		ExamID:          stored.ExamID,
		Provenance:      model.ExamSubmissionSittingClosed,
		SittingID:       stored.SittingID,
		ClassID:         stored.ClassID,
		CandidateUserID: stored.CandidateUserID,
		ParticipationID: stored.ParticipationID,
		Generation:      stored.Generation,
		ConnectionID:    stored.ConnectionID,
		Replayed:        stored.Replayed,
	}, ConnectionClosed: stored.ConnectionClosed}, nil
}

func (s *Service) failSystemAudit(ctx context.Context, auditID model.AuditEventID, err error) error {
	code := apperror.CodeOf(err, "exam.attempt.unavailable")
	if auditErr := s.systemAuditor.Fail(ctx, auditID, code); auditErr != nil {
		return errors.Join(err, auditErr)
	}
	return err
}
