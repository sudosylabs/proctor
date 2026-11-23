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
	"github.com/sudosylabs/proctor/server/app/exam/sitting"
	"github.com/sudosylabs/proctor/server/app/invocation"
	"github.com/sudosylabs/proctor/server/model"
	"github.com/sudosylabs/proctor/server/store"
)

// SittingManagerAuthorizer answers the attempt use cases' manager questions.
// A submission decision first resolves the canonical submission, because a
// caller names a submission without the exam the sitting area decides against.
type SittingManagerAuthorizer struct {
	Sittings    *sitting.Service
	Submissions store.ExamSubmissionStore
	Audit       *access.Auditor
}

// AuthorizeSittingView authorizes a manager's read of a sitting's attempts.
func (a SittingManagerAuthorizer) AuthorizeSittingView(
	ctx context.Context,
	inv invocation.Invocation,
	sittingID model.ExamSittingID,
) error {
	return a.Sittings.AuthorizeSittingView(ctx, inv, sittingID)
}

// AuthorizeSittingManage authorizes a manager intervention and reports
// whether it rested on the override action.
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

// AuthorizeBrowserActivityView reports the academic unit the read is audited
// at.
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

// AuthorizeSubmissionView denies the submission's own candidate, whatever
// role they hold, before any manager decision is asked; the denial is
// concealed as not found.
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

	// The candidate is denied below, so the projection must be the requested
	// submission's, whatever the store returned.
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

// ScreenClipViewAuthorization is what allowed a caller to see an attempt's
// clips, and so the audit a playback is recorded under. A sitting decision is
// scoped to the class, which the caller fills in once it resolves the sitting.
type ScreenClipViewAuthorization struct {
	Action    model.Action
	Resource  model.Resource
	ScopeType model.RoleScopeType
	ScopeID   string
}

// AuthorizeScreenClipView admits sitting managers with submission view and,
// once the attempt is submitted, its reviewers. The manager path's self
// fence runs against the attempt's identity before the clip store is read.
func (a SittingManagerAuthorizer) AuthorizeScreenClipView(
	ctx context.Context,
	inv invocation.Invocation,
	sittingID model.ExamSittingID,
	attemptID model.ExamAttemptID,
) (ScreenClipViewAuthorization, error) {
	management, err := a.Sittings.AuthorizeSittingManage(ctx, inv, sittingID)
	if err == nil {
		err = a.Sittings.AuthorizeSittingSubmissionView(ctx, inv, sittingID)
	}
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
	if resolveErr != nil || submission.SittingID != sittingID ||
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
