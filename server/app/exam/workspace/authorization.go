// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package workspace

import (
	"context"
	"errors"
	"time"

	"github.com/sudosylabs/proctor/server/app/apperror"
	"github.com/sudosylabs/proctor/server/app/exam/manageraccess"
	"github.com/sudosylabs/proctor/server/app/invocation"
	"github.com/sudosylabs/proctor/server/model"
)

type authorizationDecision struct {
	action   model.Action
	unitID   model.AcademicUnitID
	override bool
}

func (s *Service) authorizeExam(
	ctx context.Context,
	invocation invocation.Invocation,
	examID model.ExamID,
	mutation bool,
) (authorizationDecision, error) {
	principal := invocation.Principal()
	if principal.Validate() != nil || !examID.IsValid() {
		return authorizationDecision{}, apperror.Invalid("exam.starter_workspace.invalid", "exam_id")
	}

	examAccess, err := s.exams.Access(ctx, examID, principal.UserID)
	if err != nil {
		return authorizationDecision{}, storeErrors.Translate(err)
	}
	if !examAccess.Consistent() {
		return authorizationDecision{}, apperror.New("exam.starter_workspace.unavailable").Wrap(errors.New("Exam access projection is incomplete"))
	}

	ordinaryAction, overrideAction := model.ActionExamView, model.ActionExamViewOverride
	if mutation {
		ordinaryAction, overrideAction = model.ActionExamManage, model.ActionExamManageOverride
	}

	var at time.Time
	if examAccess.ActorIsManager {
		at = model.TimeUTC(s.now())
	}

	action, err := manageraccess.SelectAction(ctx, s.academicUnitMembers, principal.UserID, examAccess, at, ordinaryAction, overrideAction)
	if err != nil {
		return authorizationDecision{}, apperror.New("exam.starter_workspace.unavailable").Wrap(err)
	}

	resource := model.Resource{Type: model.ResourceExam, ID: examID.String()}
	if err := s.authorizer.Authorize(ctx, invocation, action, resource); err != nil {
		return authorizationDecision{}, err
	}

	return authorizationDecision{action: action, unitID: examAccess.Exam.AcademicUnitID, override: action == overrideAction}, nil
}
