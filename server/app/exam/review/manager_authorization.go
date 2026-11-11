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

// SittingAuthorizer is the current Exam Manager decision the Sitting area
// makes for one Submission of an Exam. *sitting.Service satisfies it; this
// package cannot import the Sitting area.
type SittingAuthorizer interface {
	AuthorizeSubmissionView(context.Context, invocation.Invocation, model.ExamID, model.SubmissionID) error
	AuthorizeSubmissionReview(context.Context, invocation.Invocation, model.ExamID, model.SubmissionID) (access.ManagerAuthorization, error)
	AuthorizeSubmissionRelease(context.Context, invocation.Invocation, model.ExamID, model.SubmissionID) (access.ManagerAuthorization, error)
}

// SittingManagerAuthorizer is the Authorizer the application runs. Every
// decision first resolves the canonical Submission, because a caller names a
// Submission without the Exam the Sitting area decides against. The candidate
// who made the Submission never reviews it: that denial is recorded at the
// Submission's Academic Unit and concealed as not found, whatever Role the
// candidate holds, before the current Exam Manager decision is asked.
type SittingManagerAuthorizer struct {
	Reviews  store.ExamIntegrityReviewStore
	Sittings SittingAuthorizer
	Audit    access.DecisionAudit
}

// AuthorizeSubmissionView authorizes reading a Submission's flags, evidence
// and Review.
func (a SittingManagerAuthorizer) AuthorizeSubmissionView(
	ctx context.Context,
	inv invocation.Invocation,
	submissionID model.SubmissionID,
) error {
	authorization, err := a.resolve(ctx, submissionID)
	if err != nil {
		return err
	}
	if authorization.CandidateUserID == inv.Principal().UserID {
		return a.denySelf(ctx, inv, authorization, model.ActionSubmissionView)
	}

	return a.Sittings.AuthorizeSubmissionView(
		ctx,
		inv,
		authorization.ExamID,
		submissionID,
	)
}

// AuthorizeSubmissionReview authorizes draft decisions and finalization, and
// reports whether the decision rested on the override Action.
func (a SittingManagerAuthorizer) AuthorizeSubmissionReview(
	ctx context.Context,
	inv invocation.Invocation,
	submissionID model.SubmissionID,
) (access.ManagerAuthorization, error) {
	authorization, err := a.resolve(ctx, submissionID)
	if err != nil {
		return access.ManagerAuthorization{}, err
	}
	if authorization.CandidateUserID == inv.Principal().UserID {
		return access.ManagerAuthorization{}, a.denySelf(ctx, inv, authorization, model.ActionSubmissionReview)
	}

	management, err := a.Sittings.AuthorizeSubmissionReview(
		ctx,
		inv,
		authorization.ExamID,
		submissionID,
	)
	if err != nil {
		return access.ManagerAuthorization{}, err
	}

	return management, nil
}

// AuthorizeSubmissionRelease authorizes releasing a finalized result to the
// candidate, and reports whether the decision rested on the override Action.
func (a SittingManagerAuthorizer) AuthorizeSubmissionRelease(
	ctx context.Context,
	inv invocation.Invocation,
	submissionID model.SubmissionID,
) (access.ManagerAuthorization, error) {
	authorization, err := a.resolve(ctx, submissionID)
	if err != nil {
		return access.ManagerAuthorization{}, err
	}
	if authorization.CandidateUserID == inv.Principal().UserID {
		return access.ManagerAuthorization{}, a.denySelf(ctx, inv, authorization, model.ActionSubmissionRelease)
	}

	management, err := a.Sittings.AuthorizeSubmissionRelease(
		ctx,
		inv,
		authorization.ExamID,
		submissionID,
	)
	if err != nil {
		return access.ManagerAuthorization{}, err
	}

	return management, nil
}

func (a SittingManagerAuthorizer) resolve(
	ctx context.Context,
	submissionID model.SubmissionID,
) (*store.ExamIntegrityReviewAuthorization, error) {
	if a.Reviews == nil || a.Sittings == nil || a.Audit == nil || !submissionID.IsValid() {
		return nil, apperror.New("exam.integrity_review.unavailable").Wrap(errors.New("Review authorization dependencies are invalid"))
	}

	authorization, err := a.Reviews.Resolve(ctx, submissionID)
	if err != nil {
		if store.IsNotFound(err) {
			return nil, apperror.New("exam.integrity_review.not_found").Wrap(err)
		}
		return nil, apperror.New("exam.integrity_review.unavailable").Wrap(err)
	}

	if !authorization.ConsistentWith(submissionID) {
		return nil, apperror.New("exam.integrity_review.unavailable").Wrap(errors.New("Review authorization projection is incomplete"))
	}

	return authorization, nil
}

func (a SittingManagerAuthorizer) denySelf(
	ctx context.Context,
	inv invocation.Invocation,
	authorization *store.ExamIntegrityReviewAuthorization,
	action model.Action,
) error {
	err := a.Audit.RecordAuthorizationDecision(
		ctx,
		inv.Principal(),
		action,
		model.Resource{Type: model.ResourceSubmission, ID: authorization.SubmissionID.String()},
		model.RoleScopeAcademicUnit,
		authorization.AcademicUnitID.String(),
		inv.RequestMetadata(),
		false,
	)
	if err != nil {
		return err
	}

	return apperror.New("exam.integrity_review.not_found")
}
