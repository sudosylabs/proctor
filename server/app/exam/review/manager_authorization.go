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
	"github.com/sudosylabs/proctor/server/app/exam/sitting"
	"github.com/sudosylabs/proctor/server/app/invocation"
	"github.com/sudosylabs/proctor/server/model"
	"github.com/sudosylabs/proctor/server/store"
)

// SittingManagerAuthorizer resolves the submission first, because callers
// name a submission without the exam the manager check needs. The candidate
// who made the submission never reviews it, whatever role they hold: that
// denial is audited at the submission's academic unit and reported as not
// found, before any manager check runs.
type SittingManagerAuthorizer struct {
	Reviews  store.ExamIntegrityReviewStore
	Sittings *sitting.Service
	Audit    *access.Auditor
}

// AuthorizeSubmissionView authorizes reading a submission's flags, evidence
// and review.
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
// reports whether the override action was used.
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
// candidate, and reports whether the override action was used.
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

	// Callers deny the submission's candidate from this projection, so it must
	// be the requested submission's, whatever the store returned.
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
