// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package app

import (
	"context"
	"errors"

	"github.com/sudosylabs/proctor/server/app/access"
	"github.com/sudosylabs/proctor/server/app/apperror"
	"github.com/sudosylabs/proctor/server/app/exam/attempt"
	"github.com/sudosylabs/proctor/server/app/exam/sitting"
	"github.com/sudosylabs/proctor/server/app/invocation"
	"github.com/sudosylabs/proctor/server/model"
	"github.com/sudosylabs/proctor/server/store"
)

// examAttemptSittingAuthorizer is the Sitting authorization the Attempt
// manager adapter composes; its tests substitute a fake.
type examAttemptSittingAuthorizer interface {
	AuthorizeSittingView(context.Context, invocation.Invocation, model.ExamSittingID) error
	AuthorizeSittingManage(context.Context, invocation.Invocation, model.ExamSittingID) (sitting.ManagerAuthorization, error)
	AuthorizeBrowserActivityView(context.Context, invocation.Invocation, model.ExamSittingID) (sitting.BrowserActivityAuthorization, error)
	AuthorizeSubmissionView(context.Context, invocation.Invocation, model.ExamID, model.SubmissionID) error
}

type examAttemptManagerAuthorizationAdapter struct {
	sittings    examAttemptSittingAuthorizer
	submissions store.ExamSubmissionStore
	audit       examAttemptAuthorizationDecisionAudit
}

type examAttemptAuthorizationDecisionAudit interface {
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

func (a examAttemptManagerAuthorizationAdapter) AuthorizeSittingView(
	ctx context.Context,
	invocation invocation.Invocation,
	sittingID model.ExamSittingID,
) error {
	return a.sittings.AuthorizeSittingView(ctx, invocation, sittingID)
}

func (a examAttemptManagerAuthorizationAdapter) AuthorizeSittingManage(
	ctx context.Context,
	invocation invocation.Invocation,
	sittingID model.ExamSittingID,
) (attempt.ManagerAuthorization, error) {
	management, err := a.sittings.AuthorizeSittingManage(ctx, invocation, sittingID)
	if err != nil {
		return attempt.ManagerAuthorization{}, err
	}

	return attempt.ManagerAuthorization{Override: management.Override}, nil
}

func (a examAttemptManagerAuthorizationAdapter) AuthorizeBrowserActivityView(
	ctx context.Context,
	invocation invocation.Invocation,
	sittingID model.ExamSittingID,
) (attempt.BrowserActivityAuthorization, error) {
	browserActivity, err := a.sittings.AuthorizeBrowserActivityView(ctx, invocation, sittingID)
	if err != nil {
		return attempt.BrowserActivityAuthorization{}, err
	}

	return attempt.BrowserActivityAuthorization{AcademicUnitID: browserActivity.AcademicUnitID}, nil
}

func (a examAttemptManagerAuthorizationAdapter) AuthorizeSubmissionView(
	ctx context.Context,
	invocation invocation.Invocation,
	submissionID model.SubmissionID,
) error {
	if a.submissions == nil || a.audit == nil || !submissionID.IsValid() {
		return apperror.New("exam.attempt.unavailable").Wrap(errors.New("Submission authorization dependencies are invalid"))
	}

	authorization, err := a.submissions.Resolve(ctx, submissionID)
	if err != nil {
		if store.IsNotFound(err) {
			return apperror.New("exam.attempt.not_found").Wrap(err)
		}
		return apperror.New("exam.attempt.unavailable").Wrap(err)
	}

	if !authorization.ConsistentWith(submissionID) {
		return apperror.New("exam.attempt.unavailable").Wrap(errors.New("Submission authorization projection is incomplete"))
	}

	if authorization.CandidateUserID == invocation.Principal().UserID {
		if err = a.audit.RecordAuthorizationDecision(
			ctx,
			invocation.Principal(),
			model.ActionSubmissionView,
			model.Resource{Type: model.ResourceSubmission, ID: authorization.SubmissionID.String()},
			model.RoleScopeAcademicUnit,
			authorization.AcademicUnitID.String(),
			invocation.RequestMetadata(),
			false,
		); err != nil {
			return err
		}

		return apperror.New("exam.attempt.not_found")
	}

	return a.sittings.AuthorizeSubmissionView(
		ctx,
		invocation,
		authorization.ExamID,
		submissionID,
	)
}

var _ attempt.ManagerAuthorizer = examAttemptManagerAuthorizationAdapter{}
var _ attempt.Auditor = access.MutationAuditAdapter{}

// SupportsDeliveryRecovery reports whether a failure can carry delivery repair context.
func SupportsDeliveryRecovery(code string) bool { return attempt.SupportsDeliveryRecovery(code) }
