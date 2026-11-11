// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package resource

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
}

func (s *Service) authorizeExam(
	ctx context.Context,
	invocation invocation.Invocation,
	examID model.ExamID,
	manage bool,
) (authorizationDecision, error) {
	principal := invocation.Principal()
	if principal.Validate() != nil {
		return authorizationDecision{}, apperror.Invalid("exam.resource.invalid", "principal")
	}

	examAccess, err := s.exams.Access(ctx, examID, principal.UserID)
	if err != nil {
		return authorizationDecision{}, storeErrors.Translate(err)
	}
	if !examAccess.Consistent() {
		return authorizationDecision{}, apperror.New("exam.resource.unavailable").Wrap(errors.New("missing access"))
	}

	var at time.Time
	if examAccess.ActorIsManager {
		at = s.now()
	}

	ordinaryAction, overrideAction := model.ActionExamView, model.ActionExamViewOverride
	if manage {
		ordinaryAction, overrideAction = model.ActionExamManage, model.ActionExamManageOverride
	}

	action, err := manageraccess.SelectAction(ctx, s.academicUnitMembers, principal.UserID, examAccess, at, ordinaryAction, overrideAction)
	if err != nil {
		return authorizationDecision{}, apperror.New("exam.resource.unavailable").Wrap(err)
	}
	if err = s.authorizer.Authorize(ctx, invocation, action, model.Resource{Type: model.ResourceExam, ID: examID.String()}); err != nil {
		return authorizationDecision{}, err
	}

	return authorizationDecision{override: action == overrideAction, academicUnitID: examAccess.Exam.AcademicUnitID}, nil
}

// currentActiveDraft permits an unaudited no-op only after a bounded
// authoritative read confirms the exact active Draft generation. Apparent
// no-ops against archived or stale state must reach the audited named Store
// command so its locked guards remain authoritative.
func (s *Service) currentActiveDraft(
	ctx context.Context,
	invocation invocation.Invocation,
	examID model.ExamID,
	expectedRevision int64,
) bool {
	snapshot, err := s.exams.Get(ctx, examID, invocation.Principal().UserID)
	return err == nil &&
		snapshot != nil &&
		snapshot.Exam != nil &&
		snapshot.Draft != nil &&
		!snapshot.Exam.IsArchived() &&
		snapshot.Draft.Revision == expectedRevision
}
