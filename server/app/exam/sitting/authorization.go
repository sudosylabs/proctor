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
	"github.com/sudosylabs/proctor/server/store"
)

// AuthorizeSittingView rechecks the caller's view permission without returning
// sitting state; realtime calls it before accepting or keeping a subscription.
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

	value := *snapshot
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

// AuthorizeBrowserActivityView requires current exam manager membership and
// the dedicated permission on every read, including later pages. Browser
// history deliberately has no administrator override. A refusal and a
// missing Sitting both answer resource.not_found, as the other Sitting
// reads do, so an outsider cannot probe which Sittings exist.
func (s *Service) AuthorizeBrowserActivityView(
	ctx context.Context,
	inv invocation.Invocation,
	sittingID model.ExamSittingID,
) (access.BrowserActivityAuthorization, error) {
	unitID, err := s.authorizeBrowserActivityView(ctx, inv, sittingID)
	return access.BrowserActivityAuthorization{AcademicUnitID: unitID}, publicFailures.Conceal(err)
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

	value := *snapshot
	if value.Sitting.ID != sittingID {
		return "", apperror.New("exam.sitting.unavailable").Wrap(errors.New("Exam Sitting Store returned a mismatched snapshot"))
	}

	examAccess, err := s.exams.Access(ctx, value.Sitting.ExamID, inv.Principal().UserID)
	if err != nil {
		return "", storeErrors.Translate(err)
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

		return "", access.DeniedError()
	}

	if err := s.authorizer.Authorize(ctx, inv, model.ActionExamAttemptBrowserActivityView, resource); err != nil {
		return "", err
	}

	return examAccess.Exam.AcademicUnitID, nil
}

// AuthorizeSubmissionView checks the caller may view one submission. examID
// must already be resolved as the submission's owner; the authorizer resolves
// the submission resource itself for the audit.
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

// AuthorizeSubmissionReview checks the caller may record decisions on and
// finalize one submission, and reports whether an override was used.
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

// AuthorizeSubmissionRelease checks result release, which is a permission
// separate from review and finalization.
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

// AuthorizeSittingManage rechecks the caller's manage permission and reports
// whether the override action was used. It changes nothing.
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
	decision, err := s.authorizeSittingAction(ctx, inv, sittingID, model.ActionExamSittingManage, model.ActionExamSittingManageOverride)
	return decision.override, err
}

// AuthorizeSittingSubmissionView requires the same manager access
// to submissions as the sitting's submission list.
func (s *Service) AuthorizeSittingSubmissionView(ctx context.Context, inv invocation.Invocation, sittingID model.ExamSittingID) error {
	_, err := s.authorizeSittingAction(ctx, inv, sittingID, model.ActionSubmissionView, model.ActionSubmissionViewOverride)
	return err
}

func (s *Service) authorizeSittingAction(
	ctx context.Context,
	inv invocation.Invocation,
	sittingID model.ExamSittingID,
	action, override model.Action,
) (authorizationDecision, error) {
	if !sittingID.IsValid() {
		return authorizationDecision{}, apperror.Invalid("exam.sitting.invalid", "exam_sitting_id")
	}
	if inv.Principal().Validate() != nil {
		return authorizationDecision{}, apperror.Invalid("exam.sitting.invalid", "principal")
	}

	snapshot, err := s.sittings.Resolve(ctx, sittingID)
	if err != nil {
		return authorizationDecision{}, storeErrors.Translate(err)
	}
	if snapshot.Sitting.ID != sittingID {
		return authorizationDecision{}, apperror.New("exam.sitting.unavailable").Wrap(errors.New("Exam Sitting Store returned a mismatched snapshot"))
	}

	return s.authorizeManagerAction(
		ctx,
		inv,
		snapshot.Sitting.ExamID,
		model.Resource{Type: model.ResourceExamSitting, ID: sittingID.String()},
		model.TimeUTC(s.now()),
		action,
		override,
	)
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

// SittingSubmissionsAuthorization says what a caller may see of one
// sitting's submissions: always the list, and its reviews only with
// ReviewVisible.
type SittingSubmissionsAuthorization struct {
	AcademicUnitID model.AcademicUnitID
	ReviewVisible  bool
}

// AuthorizeSittingSubmissions authorizes listing one sitting's submissions
// with submission.view, then asks, under submission.review, whether reviews
// show. Both rest on the manager relationship, as each Submission's own reads
// do; a refused review decision only hides the reviews.
func (s *Service) AuthorizeSittingSubmissions(
	ctx context.Context,
	inv invocation.Invocation,
	examID model.ExamID,
	sittingID model.ExamSittingID,
) (_ SittingSubmissionsAuthorization, resultErr error) {
	defer func() { resultErr = publicFailures.Conceal(resultErr) }()
	resource, err := s.examSittingResource(ctx, inv, examID, sittingID)
	if err != nil {
		return SittingSubmissionsAuthorization{}, err
	}
	at := model.TimeUTC(s.now())
	decision, err := s.authorizeManagerAction(ctx, inv, examID, resource, at,
		model.ActionSubmissionView, model.ActionSubmissionViewOverride)
	if err != nil {
		return SittingSubmissionsAuthorization{}, err
	}
	authorization := SittingSubmissionsAuthorization{AcademicUnitID: decision.unitID}
	_, err = s.authorizeManagerAction(ctx, inv, examID, resource, at,
		model.ActionSubmissionReview, model.ActionSubmissionReviewOverride)
	switch {
	case err == nil:
		authorization.ReviewVisible = true
	case !apperror.Is(err, "authorization.denied"):
		return SittingSubmissionsAuthorization{}, err
	}
	return authorization, nil
}

// AuthorizeSittingRelease authorizes a bulk result release for one sitting.
// Each Submission it names is then authorized again, as a single release is.
func (s *Service) AuthorizeSittingRelease(
	ctx context.Context,
	inv invocation.Invocation,
	examID model.ExamID,
	sittingID model.ExamSittingID,
) (_ access.ManagerAuthorization, _ model.AcademicUnitID, resultErr error) {
	defer func() { resultErr = publicFailures.Conceal(resultErr) }()
	resource, err := s.examSittingResource(ctx, inv, examID, sittingID)
	if err != nil {
		return access.ManagerAuthorization{}, "", err
	}
	decision, err := s.authorizeManagerAction(ctx, inv, examID, resource, model.TimeUTC(s.now()),
		model.ActionSubmissionRelease, model.ActionSubmissionReleaseOverride)
	if err != nil {
		return access.ManagerAuthorization{}, "", err
	}
	return access.ManagerAuthorization{Override: decision.override}, decision.unitID, nil
}

// AuthorizeLiveEvidence authorizes a manager's live, read-only evidence read
// of one of the sitting's attempts. It needs exam.sitting.manage, as the live
// board's controls do, and submission.view, as the same evidence read through
// the Submission needs; both rest on the manager relationship and both
// decisions are recorded, as an evidence read's is.
func (s *Service) AuthorizeLiveEvidence(
	ctx context.Context,
	inv invocation.Invocation,
	examID model.ExamID,
	sittingID model.ExamSittingID,
) (_ model.AcademicUnitID, resultErr error) {
	defer func() { resultErr = publicFailures.Conceal(resultErr) }()
	resource, err := s.examSittingResource(ctx, inv, examID, sittingID)
	if err != nil {
		return "", err
	}
	at := model.TimeUTC(s.now())
	if _, err = s.authorizeManagerAction(ctx, inv, examID, resource, at,
		model.ActionExamSittingManage, model.ActionExamSittingManageOverride); err != nil {
		return "", err
	}
	decision, err := s.authorizeManagerAction(ctx, inv, examID, resource, at,
		model.ActionSubmissionView, model.ActionSubmissionViewOverride)
	if err != nil {
		return "", err
	}
	return decision.unitID, nil
}

// AuthorizeSubmissionList authorizes listing submissions across exams under
// submission.view, and reads, without a further decision, the
// submission.review scopes that decide which rows show their review.
func (s *Service) AuthorizeSubmissionList(
	ctx context.Context,
	inv invocation.Invocation,
) (view, review store.ExamListVisibility, err error) {
	if inv.Principal().Validate() != nil {
		return store.ExamListVisibility{}, store.ExamListVisibility{}, apperror.Invalid("exam.sitting.invalid", "principal")
	}
	view, err = s.authorizer.AuthorizeExamManagerList(ctx, inv, model.ActionSubmissionView, model.ActionSubmissionViewOverride)
	if err != nil {
		return store.ExamListVisibility{}, store.ExamListVisibility{}, err
	}
	review, err = s.authorizer.ExamManagerScopes(ctx, inv.Principal(), model.ActionSubmissionReview, model.ActionSubmissionReviewOverride)
	if err != nil {
		return store.ExamListVisibility{}, store.ExamListVisibility{}, err
	}
	return view, review, nil
}

// examSittingResource resolves a sitting named under an exam, so a sitting of
// another exam is not found rather than authorized.
func (s *Service) examSittingResource(
	ctx context.Context,
	inv invocation.Invocation,
	examID model.ExamID,
	sittingID model.ExamSittingID,
) (model.Resource, error) {
	if !examID.IsValid() || !sittingID.IsValid() || inv.Principal().Validate() != nil {
		return model.Resource{}, apperror.Invalid("exam.sitting.invalid", "exam_sitting_id")
	}
	snapshot, err := s.sittings.Resolve(ctx, sittingID)
	if err != nil {
		return model.Resource{}, storeErrors.Translate(err)
	}
	if snapshot.Sitting.ID != sittingID || snapshot.Sitting.ExamID != examID {
		return model.Resource{}, apperror.New("exam.sitting.not_found")
	}
	return model.Resource{Type: model.ResourceExamSitting, ID: sittingID.String()}, nil
}
