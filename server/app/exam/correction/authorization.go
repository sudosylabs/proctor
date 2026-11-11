// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package correction

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
	override       bool
	academicUnitID model.AcademicUnitID
	action         model.Action
}

func (s *Service) authorizeSitting(
	ctx context.Context,
	invocation invocation.Invocation,
	examID model.ExamID,
	sittingID model.ExamSittingID,
) (authorizationDecision, error) {
	p := invocation.Principal()
	if p.Validate() != nil {
		return authorizationDecision{}, apperror.Invalid("exam.sitting.correction.invalid", "principal")
	}

	examAccess, err := s.exams.Access(ctx, examID, p.UserID)
	if err != nil {
		return authorizationDecision{}, storeErrors.Translate(err)
	}
	if examAccess == nil || examAccess.Exam == nil || examAccess.Exam.ID != examID {
		return authorizationDecision{}, apperror.New("exam.sitting.correction.unavailable").Wrap(errors.New("missing access"))
	}

	var at time.Time
	if examAccess.ActorIsManager {
		at = s.now()
	}

	action, err := manageraccess.SelectAction(
		ctx,
		s.academicUnitMembers,
		p.UserID,
		examAccess,
		at,
		model.ActionExamSittingManage,
		model.ActionExamSittingManageOverride,
	)
	if err != nil {
		return authorizationDecision{}, apperror.New("exam.sitting.correction.unavailable").Wrap(err)
	}

	if err = s.authorizer.Authorize(
		ctx,
		invocation,
		action,
		model.Resource{Type: model.ResourceExamSitting, ID: sittingID.String()},
	); err != nil {
		return authorizationDecision{}, err
	}

	return authorizationDecision{action == model.ActionExamSittingManageOverride, examAccess.Exam.AcademicUnitID, action}, nil
}
