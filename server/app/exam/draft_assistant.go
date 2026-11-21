// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package exam

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/sudosylabs/proctor/server/app/access"
	"github.com/sudosylabs/proctor/server/app/apperror"
	"github.com/sudosylabs/proctor/server/app/idempotency"
	"github.com/sudosylabs/proctor/server/app/invocation"
	"github.com/sudosylabs/proctor/server/model"
	"github.com/sudosylabs/proctor/server/store"
)

// ConfigureDraftAssistantCommand is the input of ConfigureExamDraftAssistant.
type ConfigureDraftAssistantCommand struct {
	ExamID                model.ExamID
	ExpectedDraftRevision int64
	Assistant             model.AssistantPolicy
	IdempotencyKey        string
}

// ConfigureExamDraftAssistant replaces only the draft's assistant policy.
// An enabled policy must name an existing, unretired profile that allows its
// level and budgets; the store checks that under the lock that holds off retirement.
// Publication checks it again, with whether the installation allows the assistant.
func (a *Authoring) ConfigureExamDraftAssistant(
	ctx context.Context,
	inv invocation.Invocation,
	command ConfigureDraftAssistantCommand,
) (_ View, resultErr error) {
	defer func() { resultErr = publicFailures.Conceal(resultErr) }()
	principal := inv.Principal()
	if principal.Validate() != nil || !command.ExamID.IsValid() || command.ExpectedDraftRevision < 1 {
		return View{}, apperror.Invalid("exam.invalid", "draft_revision")
	}
	if err := command.Assistant.Validate(); err != nil {
		return View{}, apperror.Invalid("exam.invalid", "assistant").Wrap(err)
	}

	canonical, err := model.EncodeAssistantPolicy(command.Assistant)
	if err != nil {
		return View{}, apperror.Invalid("exam.invalid", "assistant").Wrap(err)
	}
	commandIdempotency, err := idempotency.Require(inv, idempotencyOperationConfigureDraftAssistant, command.IdempotencyKey, struct {
		ExamID                string          `json:"exam_id"`
		ExpectedDraftRevision int64           `json:"expected_draft_revision"`
		Assistant             json.RawMessage `json:"assistant"`
	}{command.ExamID.String(), command.ExpectedDraftRevision, canonical})
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
	changed, err := candidate.ApplyAssistantPolicy(command.Assistant, at)
	if err != nil {
		return View{}, apperror.Invalid("exam.invalid", "assistant").Wrap(err)
	}
	if !changed && snapshot.Draft.Revision == command.ExpectedDraftRevision && !snapshot.Exam.IsArchived() {
		return View{}, apperror.New("exam.draft.no_changes")
	}

	auditID, err := a.auditor.Begin(ctx, access.Mutation{
		Invocation: inv,
		Action:     action,
		Resource:   resource,
		ScopeType:  model.RoleScopeAcademicUnit,
		ScopeID:    examAccess.Exam.AcademicUnitID.String(),
		Operation:  "configure_draft_assistant",
		Value: map[string]any{
			"exam_id":                 command.ExamID.String(),
			"expected_draft_revision": command.ExpectedDraftRevision,
			"draft_revision":          command.ExpectedDraftRevision + 1,
			"assistant_enabled":       command.Assistant.Enabled,
		},
	})
	if err != nil {
		return View{}, err
	}

	result, err := a.exams.UpdateDraftAssistant(ctx, &store.ExamDraftAssistantUpdate{
		ExamID:           command.ExamID,
		ActorUserID:      principal.UserID,
		ManagerOverride:  action == model.ActionExamManageOverride,
		ExpectedRevision: command.ExpectedDraftRevision,
		Assistant:        command.Assistant,
		UpdatedAt:        candidate.UpdatedAt,
		AuditEventID:     auditID,
		AuditAt:          at,
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
