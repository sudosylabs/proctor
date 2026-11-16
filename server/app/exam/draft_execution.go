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
	"github.com/sudosylabs/proctor/server/app/idempotency"
	"github.com/sudosylabs/proctor/server/app/invocation"
	"github.com/sudosylabs/proctor/server/model"
	"github.com/sudosylabs/proctor/server/store"
)

// ConfigureDraftExecutionCommand is the input of ConfigureExamDraftExecution.
type ConfigureDraftExecutionCommand struct {
	ExamID                model.ExamID
	ExpectedDraftRevision int64
	Execution             model.ExecutionPolicy
	IdempotencyKey        string
}

// ConfigureExamDraftExecution replaces only the draft's execution policy.
// An enabled policy must name an existing, unretired profile that allows its
// terminals; the store checks that under the lock that holds off retirement.
// Publication checks it again, with whether the installation allows execution.
func (a *Authoring) ConfigureExamDraftExecution(
	ctx context.Context,
	inv invocation.Invocation,
	command ConfigureDraftExecutionCommand,
) (_ View, resultErr error) {
	defer func() { resultErr = publicFailures.Conceal(resultErr) }()
	principal := inv.Principal()
	if principal.Validate() != nil || !command.ExamID.IsValid() || command.ExpectedDraftRevision < 1 {
		return View{}, apperror.Invalid("exam.invalid", "draft_revision")
	}
	if err := command.Execution.Validate(); err != nil {
		return View{}, apperror.Invalid("exam.invalid", "execution").Wrap(err)
	}

	commandIdempotency, err := idempotency.Require(inv, idempotencyOperationConfigureDraftExecution, command.IdempotencyKey, struct {
		ExamID                string `json:"exam_id"`
		ExpectedDraftRevision int64  `json:"expected_draft_revision"`
		Enabled               bool   `json:"enabled"`
		ProfileID             string `json:"profile_id"`
		RecordInput           bool   `json:"record_input"`
		Terminals             int    `json:"terminals"`
	}{
		ExamID:                command.ExamID.String(),
		ExpectedDraftRevision: command.ExpectedDraftRevision,
		Enabled:               command.Execution.Enabled,
		ProfileID:             command.Execution.Profile.String(),
		RecordInput:           command.Execution.RecordInput,
		Terminals:             command.Execution.Terminals,
	})
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
	changed, err := candidate.ApplyExecutionPolicy(command.Execution, at)
	if err != nil {
		return View{}, apperror.Invalid("exam.invalid", "execution").Wrap(err)
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
		Operation:  "configure_draft_execution",
		Value: map[string]any{
			"exam_id":                 command.ExamID.String(),
			"expected_draft_revision": command.ExpectedDraftRevision,
			"draft_revision":          command.ExpectedDraftRevision + 1,
			"execution_enabled":       command.Execution.Enabled,
		},
	})
	if err != nil {
		return View{}, err
	}

	result, err := a.exams.UpdateDraftExecution(ctx, &store.ExamDraftExecutionUpdate{
		ExamID:           command.ExamID,
		ActorUserID:      principal.UserID,
		ManagerOverride:  action == model.ActionExamManageOverride,
		ExpectedRevision: command.ExpectedDraftRevision,
		Execution:        command.Execution,
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
