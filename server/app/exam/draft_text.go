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
	"strings"

	"github.com/sudosylabs/proctor/server/app/access"
	"github.com/sudosylabs/proctor/server/app/apperror"
	"github.com/sudosylabs/proctor/server/app/idempotency"
	"github.com/sudosylabs/proctor/server/app/invocation"
	"github.com/sudosylabs/proctor/server/model"
	"github.com/sudosylabs/proctor/server/store"
)

type EditDraftTextCommand struct {
	ExamID                model.ExamID
	ExpectedDraftRevision int64
	Title                 *string
	InstructionsMarkdown  *string
	IdempotencyKey        string
}

// EditExamDraftText idempotently changes the Exam Draft's title, Exam
// Instructions, or both, fenced by ExpectedDraftRevision. A nil field is left
// unchanged, but at least one must be set.
func (a *Authoring) EditExamDraftText(
	ctx context.Context,
	inv invocation.Invocation,
	command EditDraftTextCommand,
) (_ View, resultErr error) {
	defer func() { resultErr = publicFailures.Conceal(resultErr) }()
	principal := inv.Principal()
	if principal.Validate() != nil || !command.ExamID.IsValid() || command.ExpectedDraftRevision < 1 {
		return View{}, apperror.Invalid("exam.invalid", "draft_revision")
	}
	if command.Title == nil && command.InstructionsMarkdown == nil {
		return View{}, apperror.Invalid("exam.invalid", "fields")
	}

	title := cloneStringPointer(command.Title)
	if title != nil {
		*title = strings.TrimSpace(*title)
	}

	instructions := cloneStringPointer(command.InstructionsMarkdown)
	commandIdempotency, err := idempotency.Require(inv, idempotencyOperationEditDraftText, command.IdempotencyKey, struct {
		ExamID                string  `json:"exam_id"`
		ExpectedDraftRevision int64   `json:"expected_draft_revision"`
		Title                 *string `json:"title"`
		InstructionsMarkdown  *string `json:"instructions_markdown"`
	}{command.ExamID.String(), command.ExpectedDraftRevision, title, instructions})
	if err != nil {
		return View{}, err
	}

	at := model.TimeUTC(a.now())

	examAccess, err := a.exams.Access(ctx, command.ExamID, principal.UserID)
	if err != nil {
		return View{}, examStoreErrors.Translate(err)
	}

	action, err := a.actionForAccess(ctx, principal.UserID, examAccess, at, model.ActionExamManage, model.ActionExamManageOverride)
	if err != nil {
		return View{}, err
	}

	resource := model.Resource{Type: model.ResourceExam, ID: command.ExamID.String()}
	if err = a.authorizer.Authorize(ctx, inv, action, resource); err != nil {
		return View{}, err
	}

	snapshot, err := a.exams.Get(ctx, command.ExamID, principal.UserID)
	if err != nil {
		return View{}, examStoreErrors.Translate(err)
	}

	candidate := *snapshot.Draft
	changed, err := candidate.ApplyTextPatch(title, instructions, at)
	if err != nil {
		return View{}, apperror.Invalid("exam.invalid", "draft").Wrap(err)
	}
	if !changed && snapshot.Draft.Revision == command.ExpectedDraftRevision && !snapshot.Exam.IsArchived() {
		return View{}, apperror.New("exam.draft.no_changes")
	}
	if title != nil {
		title = &candidate.Title
	}
	if instructions != nil {
		instructions = &candidate.InstructionsMarkdown
	}

	auditID, err := a.auditor.Begin(ctx, access.Mutation{
		Invocation: inv,
		Action:     action,
		Resource:   resource,
		ScopeType:  model.RoleScopeAcademicUnit,
		ScopeID:    examAccess.Exam.AcademicUnitID.String(),
		Operation:  "edit_draft_text",
		Value: map[string]any{
			"exam_id":                 command.ExamID.String(),
			"expected_draft_revision": command.ExpectedDraftRevision,
			"draft_revision":          command.ExpectedDraftRevision + 1,
		},
	})
	if err != nil {
		return View{}, err
	}

	result, err := a.exams.UpdateDraftText(ctx, &store.ExamDraftTextUpdate{
		ExamID:               command.ExamID,
		ActorUserID:          principal.UserID,
		ManagerOverride:      action == model.ActionExamManageOverride,
		ExpectedRevision:     command.ExpectedDraftRevision,
		Title:                title,
		InstructionsMarkdown: instructions,
		UpdatedAt:            candidate.UpdatedAt,
		AuditEventID:         auditID,
		AuditAt:              at,
	}, commandIdempotency)
	if err != nil {
		mapped := examStoreErrors.Translate(err)
		if auditErr := a.auditor.Fail(ctx, auditID, apperror.CodeOf(mapped, "exam.unavailable")); auditErr != nil {
			return View{}, errors.Join(mapped, auditErr)
		}

		return View{}, mapped
	}

	if !result.Replayed {
		if effectErr := a.publishDraftUpdated(ctx, result.Value.Exam.ID, result.Value.Draft.Revision); effectErr != nil {
			a.publisher.Report(ctx, "exam_draft_updated", effectErr)
		}
	}

	return projectView(result.Value), nil
}

func cloneStringPointer(value *string) *string {
	if value == nil {
		return nil
	}
	cloned := *value
	return &cloned
}
