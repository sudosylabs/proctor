// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package exam

import (
	"context"

	"github.com/sudosylabs/proctor/server/app/apperror"
	"github.com/sudosylabs/proctor/server/app/invocation"
	"github.com/sudosylabs/proctor/server/model"
	"github.com/sudosylabs/proctor/server/store"
)

// View is an Exam with its Draft and the summary facts an author sees beside
// it: who owns it, how many managers and Resources it has, and whether a
// Starter Workspace exists.
type View struct {
	Exam                model.Exam
	Draft               model.ExamDraft
	OwnerUserID         model.UserID
	ManagerCount        int
	Capacity            model.ExamCapacityPolicy
	ResourceCount       int
	HasStarterWorkspace bool
}

// GetExam returns an Exam and its Exam Draft to a caller authorized to view
// it, directly or through an override.
func (a *Authoring) GetExam(ctx context.Context, inv invocation.Invocation, id model.ExamID) (_ View, resultErr error) {
	defer func() { resultErr = publicFailures.Conceal(resultErr) }()
	principal := inv.Principal()
	if principal.Validate() != nil || !id.IsValid() {
		return View{}, apperror.Invalid("exam.invalid", "exam_id")
	}

	examAccess, err := a.exams.Access(ctx, id, principal.UserID)
	if err != nil {
		return View{}, examStoreErrors.Translate(err)
	}

	action, err := a.actionForAccess(
		ctx,
		principal.UserID,
		examAccess,
		model.TimeUTC(a.now()),
		model.ActionExamView,
		model.ActionExamViewOverride,
	)
	if err != nil {
		return View{}, err
	}

	if err = a.authorizer.Authorize(
		ctx,
		inv,
		action,
		model.Resource{Type: model.ResourceExam, ID: id.String()},
	); err != nil {
		return View{}, err
	}

	snapshot, err := a.exams.Get(ctx, id, principal.UserID)
	if err != nil {
		return View{}, examStoreErrors.Translate(err)
	}

	return projectView(snapshot), nil
}

// AuthorizeExamView performs the same current relationship and role decision used
// by Get without loading authored Draft content. Realtime subscriptions use it
// so a scoped role alone cannot reveal Exam events to a non-manager.
func (a *Authoring) AuthorizeExamView(ctx context.Context, inv invocation.Invocation, examID model.ExamID) error {
	principal := inv.Principal()
	if principal.Validate() != nil || !examID.IsValid() {
		return apperror.Invalid("exam.invalid", "exam_id")
	}

	examAccess, err := a.exams.Access(ctx, examID, principal.UserID)
	if err != nil {
		return examStoreErrors.Translate(err)
	}

	action, err := a.actionForAccess(
		ctx,
		principal.UserID,
		examAccess,
		model.TimeUTC(a.now()),
		model.ActionExamView,
		model.ActionExamViewOverride,
	)
	if err != nil {
		return err
	}

	return a.authorizer.Authorize(ctx, inv, action, model.Resource{Type: model.ResourceExam, ID: examID.String()})
}

func projectView(snapshot *store.ExamAuthoringSnapshot) View {
	return View{
		Exam:                *snapshot.Exam,
		Draft:               *snapshot.Draft,
		OwnerUserID:         snapshot.OwnerUserID,
		ManagerCount:        snapshot.ManagerCount,
		Capacity:            snapshot.Capacity,
		ResourceCount:       snapshot.ResourceCount,
		HasStarterWorkspace: snapshot.HasStarterWorkspace,
	}
}
