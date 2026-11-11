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

	"github.com/sudosylabs/proctor/server/app/access"
	"github.com/sudosylabs/proctor/server/app/apperror"
	"github.com/sudosylabs/proctor/server/app/exam/manageraccess"
	"github.com/sudosylabs/proctor/server/app/idempotency"
	"github.com/sudosylabs/proctor/server/app/invocation"
	"github.com/sudosylabs/proctor/server/model"
	"github.com/sudosylabs/proctor/server/store"
)

type CreateCommand struct {
	AcademicUnitID       model.AcademicUnitID
	Title                string
	InstructionsMarkdown string
	IdempotencyKey       string
}

// CreateExam idempotently creates an Exam with its initial Exam Draft in the
// given Academic Unit.
func (a *Authoring) CreateExam(ctx context.Context, invocation invocation.Invocation, command CreateCommand) (_ View, resultErr error) {
	defer func() { a.recordOperational("create", resultErr) }()
	defer func() { resultErr = publicFailures.Present(resultErr) }()
	principal := invocation.Principal()
	if principal.Validate() != nil {
		return View{}, apperror.Invalid("exam.invalid", "principal")
	}
	if !command.AcademicUnitID.IsValid() {
		return View{}, apperror.Invalid("exam.invalid", "academic_unit_id")
	}

	commandIdempotency, err := idempotency.Require(invocation, idempotencyOperationCreate, command.IdempotencyKey, struct {
		AcademicUnitID       string `json:"academic_unit_id"`
		Title                string `json:"title"`
		InstructionsMarkdown string `json:"instructions_markdown"`
	}{command.AcademicUnitID.String(), command.Title, command.InstructionsMarkdown})
	if err != nil {
		return View{}, err
	}

	at := model.TimeUTC(a.now())
	examID := a.newID()
	if !examID.IsValid() {
		return View{}, apperror.Invalid("exam.invalid", "exam_id")
	}

	exam, err := model.NewExam(examID, command.AcademicUnitID, principal.UserID, at)
	if err != nil {
		return View{}, apperror.Invalid("exam.invalid", "exam").Wrap(err)
	}

	draft, err := model.NewExamDraft(examID, command.Title, command.InstructionsMarkdown, model.DefaultExamPolicySet(), at)
	if err != nil {
		return View{}, apperror.Invalid("exam.invalid", "draft").Wrap(err)
	}

	manager, err := model.NewExamManager(examID, principal.UserID, principal.UserID, at)
	if err != nil {
		return View{}, apperror.Invalid("exam.invalid", "manager").Wrap(err)
	}

	ordinary, err := manageraccess.HasCurrentMembership(ctx, a.academicUnitMembers, principal.UserID, command.AcademicUnitID, at)
	if err != nil {
		return View{}, apperror.New("exam.unavailable").Wrap(err)
	}

	action := model.ActionExamCreateOverride
	if ordinary {
		action = model.ActionExamCreate
	}

	resource := model.Resource{Type: model.ResourceAcademicUnit, ID: command.AcademicUnitID.String()}
	if err = a.authorizer.Authorize(ctx, invocation, action, resource); err != nil {
		return View{}, err
	}

	auditID, err := a.auditor.Begin(ctx, access.Mutation{
		Invocation: invocation,
		Action:     action,
		Resource:   resource,
		ScopeType:  model.RoleScopeAcademicUnit,
		ScopeID:    command.AcademicUnitID.String(),
		Operation:  "create",
		Value: map[string]any{
			"exam_id":          examID.String(),
			"academic_unit_id": command.AcademicUnitID.String(),
			"creator_user_id":  principal.UserID.String(),
		},
	})
	if err != nil {
		return View{}, err
	}

	creation := &store.ExamAuthoringCreation{Exam: exam, Draft: draft, Manager: manager, AuditEventID: auditID, AuditAt: at}
	result, err := a.exams.Create(ctx, creation, commandIdempotency)
	if err != nil {
		mapped := examStoreErrors.Translate(err)
		if auditErr := a.auditor.Fail(ctx, auditID, apperror.CodeOf(mapped, "exam.unavailable")); auditErr != nil {
			return View{}, errors.Join(mapped, auditErr)
		}

		return View{}, mapped
	}

	if result == nil || result.Value == nil {
		return View{}, apperror.New("exam.unavailable").Wrap(errors.New("exam store returned no creation result"))
	}

	if !result.Replayed {
		if effectErr := a.publishExamCreated(ctx, result.Value.Exam.ID); effectErr != nil {
			a.publisher.Report(ctx, "exam_created", effectErr)
		}
	}

	return projectView(result.Value), nil
}
