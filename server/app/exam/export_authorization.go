// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package exam

import (
	"context"
	"errors"
	"slices"

	"github.com/sudosylabs/proctor/server/app/apperror"
	"github.com/sudosylabs/proctor/server/app/exam/manageraccess"
	"github.com/sudosylabs/proctor/server/app/invocation"
	"github.com/sudosylabs/proctor/server/model"
	"github.com/sudosylabs/proctor/server/store"
)

func (s *Exports) authorizeExport(
	ctx context.Context,
	invocation invocation.Invocation,
	scope model.RetentionHoldScope,
) (model.Action, model.AcademicUnitID, error) {
	if scope.Validate() != nil ||
		!scope.SittingID.IsValid() ||
		invocation.Principal().Validate() != nil ||
		invocation.Principal().CredentialType != model.CredentialSessionAccess {
		return "", "", apperror.Invalid("exam.invalid", "export_scope")
	}

	examAccess, err := s.exams.Access(ctx, scope.ExamID, invocation.Principal().UserID)
	if err != nil {
		return "", "", examExportStoreErrors.Translate(err)
	}
	if !examAccess.ConsistentWithOwningUnit(scope.ExamID) {
		return "", "", apperror.New("exam.export.unavailable").Wrap(err)
	}

	action, err := manageraccess.SelectAction(
		ctx,
		s.academicUnitMembers,
		invocation.Principal().UserID,
		examAccess,
		model.TimeUTC(s.now()),
		model.ActionExamRecordsExport,
		model.ActionExamRecordsExportOverride,
	)
	if err != nil {
		return "", "", apperror.New("exam.export.unavailable").Wrap(err)
	}
	if err = s.authorizer.Authorize(ctx, invocation, action, scope.Resource()); err != nil {
		return "", "", err
	}

	return action, examAccess.Exam.AcademicUnitID, nil
}

func (s *Exports) authorizeSources(
	ctx context.Context,
	invocation invocation.Invocation,
	scope model.RetentionHoldScope,
	action model.Action,
	unit model.AcademicUnitID,
	categories []model.RetentionCategory,
	selected []store.ExamSubmissionAuthorization,
) error {
	if len(selected) < 1 || len(selected) > model.ExamExportMaximumSubmissions {
		return apperror.New("exam.export.unavailable").Wrap(errors.New("invalid export source scope"))
	}

	view, sittingView := model.ActionSubmissionViewOverride, model.ActionExamSittingViewOverride
	if action == model.ActionExamRecordsExport {
		view, sittingView = model.ActionSubmissionView, model.ActionExamSittingView
	}

	if scope.SubmissionID.IsZero() {
		if err := s.authorizer.Authorize(ctx, invocation, sittingView, scope.Resource()); err != nil {
			return err
		}
	}

	if slices.Contains(categories, model.RetentionCategoryBrowserActivity) {
		examAccess, err := s.exams.Access(ctx, scope.ExamID, invocation.Principal().UserID)
		if err != nil {
			return examExportStoreErrors.Translate(err)
		}
		if !examAccess.ConsistentWith(scope.ExamID, unit) {
			return apperror.New("exam.export.unavailable").Wrap(errors.New("invalid browser history access projection"))
		}

		if !examAccess.ActorIsManager {
			if err := s.authorizer.RecordDenial(
				ctx,
				invocation,
				model.ActionExamAttemptBrowserActivityView,
				model.Resource{Type: model.ResourceExamSitting, ID: scope.SittingID.String()},
				unit,
			); err != nil {
				return err
			}

			return apperror.New("exam.not_found")
		}

		if err := s.authorizer.Authorize(
			ctx,
			invocation,
			model.ActionExamAttemptBrowserActivityView,
			model.Resource{Type: model.ResourceExamSitting, ID: scope.SittingID.String()},
		); err != nil {
			return err
		}
	}

	var previous model.SubmissionID
	for _, a := range selected {
		if !a.SubmissionID.IsValid() ||
			a.SubmissionID <= previous ||
			a.ExamID != scope.ExamID ||
			a.SittingID != scope.SittingID ||
			a.AcademicUnitID != unit ||
			!a.CandidateUserID.IsValid() ||
			!a.AttemptID.IsValid() ||
			scope.SubmissionID.IsValid() && a.SubmissionID != scope.SubmissionID {
			return apperror.New("exam.export.unavailable").Wrap(errors.New("invalid export source scope"))
		}

		resource := model.Resource{Type: model.ResourceSubmission, ID: a.SubmissionID.String()}
		if a.CandidateUserID == invocation.Principal().UserID {
			if err := s.authorizer.RecordDenial(ctx, invocation, view, resource, unit); err != nil {
				return err
			}
			return apperror.New("exam.not_found")
		}

		if err := s.authorizer.Authorize(ctx, invocation, view, resource); err != nil {
			return err
		}

		previous = a.SubmissionID
	}

	return nil
}
