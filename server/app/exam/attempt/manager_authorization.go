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

	"github.com/sudosylabs/proctor/server/app/access"
	"github.com/sudosylabs/proctor/server/app/apperror"
	"github.com/sudosylabs/proctor/server/app/invocation"
	"github.com/sudosylabs/proctor/server/model"
	"github.com/sudosylabs/proctor/server/store"
)

// SittingAuthorizer is the current Exam Manager decision the Sitting area
// makes for a Sitting or one of its Submissions. *sitting.Service satisfies
// it; this package cannot import the Sitting area, and tests substitute a
// fake.
type SittingAuthorizer interface {
	AuthorizeSittingView(context.Context, invocation.Invocation, model.ExamSittingID) error
	AuthorizeSittingManage(context.Context, invocation.Invocation, model.ExamSittingID) (access.ManagerAuthorization, error)
	AuthorizeBrowserActivityView(context.Context, invocation.Invocation, model.ExamSittingID) (access.BrowserActivityAuthorization, error)
	AuthorizeSubmissionView(context.Context, invocation.Invocation, model.ExamID, model.SubmissionID) error
	AuthorizeSubmissionReview(context.Context, invocation.Invocation, model.ExamID, model.SubmissionID) (access.ManagerAuthorization, error)
}

// DecisionRecorder durably records an authorization decision this package
// makes without asking Access Control. *access.Auditor satisfies it and tests
// substitute a recorder.
type DecisionRecorder interface {
	RecordAuthorizationDecision(
		context.Context,
		model.Principal,
		model.Action,
		model.Resource,
		model.RoleScopeType,
		string,
		model.RequestMetadata,
		bool,
	) error
}

// SittingManagerAuthorizer is the ManagerAuthorizer the application runs. A
// Sitting decision is the Sitting area's own. A Submission decision first
// resolves the canonical Submission, because a caller names a Submission
// without the Exam the Sitting area decides against.
type SittingManagerAuthorizer struct {
	Sittings    SittingAuthorizer
	Submissions store.ExamSubmissionStore
	Audit       DecisionRecorder
}

// AuthorizeSittingView authorizes a manager's read of a Sitting's Attempts.
func (a SittingManagerAuthorizer) AuthorizeSittingView(
	ctx context.Context,
	inv invocation.Invocation,
	sittingID model.ExamSittingID,
) error {
	return a.Sittings.AuthorizeSittingView(ctx, inv, sittingID)
}

// AuthorizeSittingManage authorizes a manager intervention in a Sitting's
// Attempts and reports whether it rested on the override Action.
func (a SittingManagerAuthorizer) AuthorizeSittingManage(
	ctx context.Context,
	inv invocation.Invocation,
	sittingID model.ExamSittingID,
) (access.ManagerAuthorization, error) {
	management, err := a.Sittings.AuthorizeSittingManage(ctx, inv, sittingID)
	if err != nil {
		return access.ManagerAuthorization{}, err
	}

	return management, nil
}

// AuthorizeBrowserActivityView authorizes reading the browser activity of a
// Sitting's Attempts and reports the Academic Unit it is audited at.
func (a SittingManagerAuthorizer) AuthorizeBrowserActivityView(
	ctx context.Context,
	inv invocation.Invocation,
	sittingID model.ExamSittingID,
) (access.BrowserActivityAuthorization, error) {
	browserActivity, err := a.Sittings.AuthorizeBrowserActivityView(ctx, inv, sittingID)
	if err != nil {
		return access.BrowserActivityAuthorization{}, err
	}

	return browserActivity, nil
}

// AuthorizeSubmissionView authorizes a manager's read of one sealed
// Submission. The candidate who made the Submission is never its manager:
// that denial is recorded at the Submission's Academic Unit and concealed as
// not found, whatever Role the candidate holds, before the current Exam
// Manager decision is asked.
func (a SittingManagerAuthorizer) AuthorizeSubmissionView(
	ctx context.Context,
	inv invocation.Invocation,
	submissionID model.SubmissionID,
) error {
	if a.Submissions == nil || a.Audit == nil || !submissionID.IsValid() {
		return apperror.New("exam.attempt.unavailable").Wrap(errors.New("Submission authorization dependencies are invalid"))
	}

	authorization, err := a.Submissions.Resolve(ctx, submissionID)
	if err != nil {
		if store.IsNotFound(err) {
			return apperror.New("exam.attempt.not_found").Wrap(err)
		}
		return apperror.New("exam.attempt.unavailable").Wrap(err)
	}

	// The Submission's Candidate is denied below, so the projection must be
	// the requested Submission's, whatever the Store returned.
	if !authorization.ConsistentWith(submissionID) {
		return apperror.New("exam.attempt.unavailable").Wrap(errors.New("Submission authorization projection is incomplete"))
	}

	if authorization.CandidateUserID == inv.Principal().UserID {
		if err = a.Audit.RecordAuthorizationDecision(
			ctx,
			inv.Principal(),
			model.ActionSubmissionView,
			model.Resource{Type: model.ResourceSubmission, ID: authorization.SubmissionID.String()},
			model.RoleScopeAcademicUnit,
			authorization.AcademicUnitID.String(),
			inv.RequestMetadata(),
			false,
		); err != nil {
			return err
		}

		return apperror.New("exam.attempt.not_found")
	}

	return a.Sittings.AuthorizeSubmissionView(
		ctx,
		inv,
		authorization.ExamID,
		submissionID,
	)
}

// ScreenClipViewAuthorization is what allowed a caller to see an Attempt's
// screen clips, and so the audit a playback is recorded under. A Sitting
// management decision is scoped to the Sitting's Class, which the caller
// fills in once it has resolved the Sitting.
type ScreenClipViewAuthorization struct {
	Action    model.Action
	Resource  model.Resource
	ScopeType model.RoleScopeType
	ScopeID   string
}

// AuthorizeScreenClipView lets the Sitting's Exam Managers see an Attempt's
// clips, and once the Attempt is submitted, those allowed to review its
// Submission. The candidate never sees their own.
func (a SittingManagerAuthorizer) AuthorizeScreenClipView(
	ctx context.Context,
	inv invocation.Invocation,
	sittingID model.ExamSittingID,
	attemptID model.ExamAttemptID,
) (ScreenClipViewAuthorization, error) {
	management, err := a.Sittings.AuthorizeSittingManage(ctx, inv, sittingID)
	if err == nil {
		action := model.ActionExamSittingManage
		if management.Override {
			action = model.ActionExamSittingManageOverride
		}
		return ScreenClipViewAuthorization{Action: action,
			Resource:  model.Resource{Type: model.ResourceExamSitting, ID: sittingID.String()},
			ScopeType: model.RoleScopeClass}, nil
	}
	if a.Submissions == nil {
		return ScreenClipViewAuthorization{}, err
	}
	submission, resolveErr := a.Submissions.ResolveAttempt(ctx, attemptID)
	if resolveErr != nil || submission.AttemptID != attemptID || submission.SittingID != sittingID ||
		submission.CandidateUserID == inv.Principal().UserID {
		return ScreenClipViewAuthorization{}, err
	}
	review, reviewErr := a.Sittings.AuthorizeSubmissionReview(ctx, inv, submission.ExamID, submission.SubmissionID)
	if reviewErr != nil {
		return ScreenClipViewAuthorization{}, err
	}
	action := model.ActionSubmissionReview
	if review.Override {
		action = model.ActionSubmissionReviewOverride
	}
	return ScreenClipViewAuthorization{Action: action,
		Resource:  model.Resource{Type: model.ResourceSubmission, ID: submission.SubmissionID.String()},
		ScopeType: model.RoleScopeAcademicUnit, ScopeID: submission.AcademicUnitID.String()}, nil
}
