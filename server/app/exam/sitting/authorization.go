// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package sitting

import (
	"context"
	"errors"
	"time"

	"github.com/sudosylabs/proctor/server/app/access"
	"github.com/sudosylabs/proctor/server/app/apperror"
	"github.com/sudosylabs/proctor/server/app/exam/manageraccess"
	"github.com/sudosylabs/proctor/server/app/invocation"
	"github.com/sudosylabs/proctor/server/model"
)

// AuthorizeSittingView rechecks the current relationship and permission used by an
// exact read without returning Sitting state. Realtime subscriptions use this
// gate before accepting or retaining a resource-scoped subscription.
func (s *Service) AuthorizeSittingView(ctx context.Context, inv invocation.Invocation, sittingID model.ExamSittingID) error {
	if !sittingID.IsValid() {
		return apperror.Invalid("exam.sitting.invalid", "exam_sitting_id")
	}
	if inv.Principal().Validate() != nil {
		return apperror.Invalid("exam.sitting.invalid", "principal")
	}

	snapshot, err := s.sittings.Resolve(ctx, sittingID)
	if err != nil {
		return storeErrors.Translate(err)
	}

	value, err := requireSnapshot(snapshot)
	if err != nil {
		return err
	}
	if value.Sitting.ID != sittingID {
		return apperror.New("exam.sitting.unavailable").Wrap(errors.New("Exam Sitting Store returned a mismatched snapshot"))
	}

	_, err = s.authorizeManagerAction(
		ctx,
		inv,
		value.Sitting.ExamID,
		model.Resource{Type: model.ResourceExamSitting, ID: sittingID.String()},
		model.TimeUTC(s.now()),
		model.ActionExamSittingView,
		model.ActionExamSittingViewOverride,
	)
	return err
}

// AuthorizeBrowserActivityView requires exact current Exam Manager membership
// plus the dedicated permission on every read, including later pages.
// Browser history has no administrator or general examination override.
func (s *Service) AuthorizeBrowserActivityView(
	ctx context.Context,
	inv invocation.Invocation,
	sittingID model.ExamSittingID,
) (access.BrowserActivityAuthorization, error) {
	unitID, err := s.authorizeBrowserActivityView(ctx, inv, sittingID)
	return access.BrowserActivityAuthorization{AcademicUnitID: unitID}, err
}

func (s *Service) authorizeBrowserActivityView(
	ctx context.Context,
	inv invocation.Invocation,
	sittingID model.ExamSittingID,
) (model.AcademicUnitID, error) {
	if !sittingID.IsValid() || inv.Principal().Validate() != nil {
		return "", apperror.Invalid("exam.sitting.invalid", "exam_sitting_id")
	}

	snapshot, err := s.sittings.Resolve(ctx, sittingID)
	if err != nil {
		return "", storeErrors.Translate(err)
	}

	value, err := requireSnapshot(snapshot)
	if err != nil {
		return "", err
	}
	if value.Sitting.ID != sittingID {
		return "", apperror.New("exam.sitting.unavailable").Wrap(errors.New("Exam Sitting Store returned a mismatched snapshot"))
	}

	examAccess, err := s.exams.Access(ctx, value.Sitting.ExamID, inv.Principal().UserID)
	if err != nil {
		return "", storeErrors.Translate(err)
	}
	if !examAccess.ConsistentAndValidWith(value.Sitting.ExamID) {
		return "", apperror.New("exam.sitting.unavailable").Wrap(errors.New("Exam access projection is incomplete"))
	}

	resource := model.Resource{Type: model.ResourceExamSitting, ID: sittingID.String()}
	if !examAccess.ActorIsManager {
		if err := s.authorizer.RecordDenial(
			ctx,
			inv,
			model.ActionExamAttemptBrowserActivityView,
			resource,
			examAccess.Exam.AcademicUnitID,
		); err != nil {
			return "", err
		}

		return "", apperror.New("exam.sitting.not_found")
	}

	if err := s.authorizer.Authorize(ctx, inv, model.ActionExamAttemptBrowserActivityView, resource); err != nil {
		return "", err
	}

	return examAccess.Exam.AcademicUnitID, nil
}

// AuthorizeSubmissionView applies the current Exam Manager relationship and
// exact-unit membership decision to one immutable Submission resource. The
// caller resolves the Submission's owning Exam through the bounded ownership
// projection before invoking this gate; authorization independently resolves
// the canonical Submission resource for its scoped, durable decision audit.
func (s *Service) AuthorizeSubmissionView(
	ctx context.Context,
	inv invocation.Invocation,
	examID model.ExamID,
	submissionID model.SubmissionID,
) error {
	_, err := s.authorizeSubmission(
		ctx,
		inv,
		examID,
		submissionID,
		model.ActionSubmissionView,
		model.ActionSubmissionViewOverride,
	)
	return err
}

// AuthorizeSubmissionReview applies the current Exam Manager relationship and
// exact-unit membership decision for draft decisions and finalization.
func (s *Service) AuthorizeSubmissionReview(
	ctx context.Context,
	inv invocation.Invocation,
	examID model.ExamID,
	submissionID model.SubmissionID,
) (access.ManagerAuthorization, error) {
	override, err := s.authorizeSubmission(
		ctx,
		inv,
		examID,
		submissionID,
		model.ActionSubmissionReview,
		model.ActionSubmissionReviewOverride,
	)
	return access.ManagerAuthorization{Override: override}, err
}

// AuthorizeSubmissionRelease keeps result release as a distinct, explicit
// permission from evidence review and finalization.
func (s *Service) AuthorizeSubmissionRelease(
	ctx context.Context,
	inv invocation.Invocation,
	examID model.ExamID,
	submissionID model.SubmissionID,
) (access.ManagerAuthorization, error) {
	override, err := s.authorizeSubmission(
		ctx,
		inv,
		examID,
		submissionID,
		model.ActionSubmissionRelease,
		model.ActionSubmissionReleaseOverride,
	)
	return access.ManagerAuthorization{Override: override}, err
}

func (s *Service) authorizeSubmission(
	ctx context.Context,
	inv invocation.Invocation,
	examID model.ExamID,
	submissionID model.SubmissionID,
	action, overrideAction model.Action,
) (bool, error) {
	if !examID.IsValid() || !submissionID.IsValid() {
		return false, apperror.Invalid("exam.sitting.invalid", "submission_id")
	}
	if inv.Principal().Validate() != nil {
		return false, apperror.Invalid("exam.sitting.invalid", "principal")
	}

	decision, err := s.authorizeManagerAction(
		ctx,
		inv,
		examID,
		model.Resource{Type: model.ResourceSubmission, ID: submissionID.String()},
		model.TimeUTC(s.now()),
		action,
		overrideAction,
	)
	if err != nil {
		return false, err
	}

	return decision.override, nil
}

// AuthorizeSittingManage rechecks the current manager relationship for a Sitting and
// reports whether the caller used the explicit management override action.
// It returns no mutable Sitting state and performs no mutation.
func (s *Service) AuthorizeSittingManage(
	ctx context.Context,
	inv invocation.Invocation,
	sittingID model.ExamSittingID,
) (access.ManagerAuthorization, error) {
	override, err := s.authorizeSittingManage(ctx, inv, sittingID)
	return access.ManagerAuthorization{Override: override}, err
}

func (s *Service) authorizeSittingManage(
	ctx context.Context,
	inv invocation.Invocation,
	sittingID model.ExamSittingID,
) (bool, error) {
	if !sittingID.IsValid() {
		return false, apperror.Invalid("exam.sitting.invalid", "exam_sitting_id")
	}
	if inv.Principal().Validate() != nil {
		return false, apperror.Invalid("exam.sitting.invalid", "principal")
	}

	snapshot, err := s.sittings.Resolve(ctx, sittingID)
	if err != nil {
		return false, storeErrors.Translate(err)
	}

	value, err := requireSnapshot(snapshot)
	if err != nil {
		return false, err
	}
	if value.Sitting.ID != sittingID {
		return false, apperror.New("exam.sitting.unavailable").Wrap(errors.New("Exam Sitting Store returned a mismatched snapshot"))
	}

	decision, err := s.authorizeManagerAction(
		ctx,
		inv,
		value.Sitting.ExamID,
		model.Resource{Type: model.ResourceExamSitting, ID: sittingID.String()},
		model.TimeUTC(s.now()),
		model.ActionExamSittingManage,
		model.ActionExamSittingManageOverride,
	)
	if err != nil {
		return false, err
	}

	return decision.override, nil
}

type authorizationDecision struct {
	action       model.Action
	unitID       model.AcademicUnitID
	override     bool
	examArchived bool
}

func (s *Service) authorizeExam(
	ctx context.Context,
	inv invocation.Invocation,
	examID model.ExamID,
	at time.Time,
	ordinaryAction, overrideAction model.Action,
) (authorizationDecision, error) {
	return s.authorizeManagerAction(
		ctx,
		inv,
		examID,
		model.Resource{Type: model.ResourceExam, ID: examID.String()},
		at,
		ordinaryAction,
		overrideAction,
	)
}

func (s *Service) authorizeManagerAction(
	ctx context.Context,
	inv invocation.Invocation,
	examID model.ExamID,
	resource model.Resource,
	at time.Time,
	ordinaryAction, overrideAction model.Action,
) (authorizationDecision, error) {
	principal := inv.Principal()
	if principal.Validate() != nil || !examID.IsValid() {
		return authorizationDecision{}, apperror.Invalid("exam.sitting.invalid", "exam_id")
	}

	examAccess, err := s.exams.Access(ctx, examID, principal.UserID)
	if err != nil {
		return authorizationDecision{}, storeErrors.Translate(err)
	}
	if !examAccess.ConsistentAndValidWith(examID) {
		return authorizationDecision{}, apperror.New("exam.sitting.unavailable").Wrap(errors.New("Exam access projection is incomplete"))
	}

	action, err := manageraccess.SelectAction(ctx, s.academicUnitMembers, principal.UserID, examAccess, at, ordinaryAction, overrideAction)
	if err != nil {
		return authorizationDecision{}, apperror.New("exam.sitting.unavailable").Wrap(err)
	}
	if err = s.authorizer.Authorize(ctx, inv, action, resource); err != nil {
		return authorizationDecision{}, err
	}

	return authorizationDecision{
		action:       action,
		unitID:       examAccess.Exam.AcademicUnitID,
		override:     action == overrideAction,
		examArchived: examAccess.Exam.IsArchived(),
	}, nil
}
