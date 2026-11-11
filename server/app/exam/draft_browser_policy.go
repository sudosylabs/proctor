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

type ConfigureDraftBrowserPolicyCommand struct {
	ExamID                model.ExamID
	ExpectedDraftRevision int64
	Policy                model.BrowserPolicy
	IdempotencyKey        string
}

// ConfigureExamDraftBrowserPolicy replaces the complete canonical enforcement
// value. Publication, rather than this mutable Draft, controls open Sittings.
func (a *Authoring) ConfigureExamDraftBrowserPolicy(
	ctx context.Context,
	invocation invocation.Invocation,
	command ConfigureDraftBrowserPolicyCommand,
) (_ View, resultErr error) {
	defer func() { resultErr = publicFailures.Conceal(resultErr) }()
	command.Policy = command.Policy.Clone()
	principal := invocation.Principal()
	if principal.Validate() != nil || !command.ExamID.IsValid() || command.ExpectedDraftRevision < 1 {
		return View{}, apperror.Invalid("exam.invalid", "draft_revision")
	}

	policy, err := model.NewBrowserPolicy(command.Policy.Enabled, command.Policy.StartRuleID, command.Policy.Rules)
	if err != nil {
		return View{}, apperror.Invalid("exam.invalid", "browser_policy").Wrap(err)
	}
	if err = policy.ValidateInstitutionOrigin(a.institutionOrigin); err != nil {
		return View{}, apperror.Invalid("exam.invalid", "browser_policy").Wrap(err)
	}

	canonical, err := model.EncodeBrowserPolicy(policy)
	if err != nil {
		return View{}, apperror.Invalid("exam.invalid", "browser_policy").Wrap(err)
	}

	commandIdempotency, err := idempotency.Require(invocation, idempotencyOperationConfigureBrowserPolicy, command.IdempotencyKey, struct {
		ExamID                string          `json:"exam_id"`
		ExpectedDraftRevision int64           `json:"expected_draft_revision"`
		Policy                json.RawMessage `json:"browser_policy"`
	}{command.ExamID.String(), command.ExpectedDraftRevision, canonical})
	if err != nil {
		return View{}, err
	}

	at := model.TimeUTC(a.now())
	examAccess, err := a.exams.Access(ctx, command.ExamID, principal.UserID)
	if err != nil {
		return View{}, examStoreErrors.Translate(err)
	}
	if !examAccess.Consistent() {
		return View{}, apperror.New("exam.unavailable").Wrap(errors.New("exam store returned no access projection"))
	}

	action, err := a.actionForAccess(ctx, principal.UserID, examAccess, at, model.ActionExamManage, model.ActionExamManageOverride)
	if err != nil {
		return View{}, err
	}

	resource := model.Resource{Type: model.ResourceExam, ID: command.ExamID.String()}
	if err = a.authorizer.Authorize(ctx, invocation, action, resource); err != nil {
		return View{}, err
	}

	snapshot, err := a.exams.Get(ctx, command.ExamID, principal.UserID)
	if err != nil {
		return View{}, examStoreErrors.Translate(err)
	}
	if !snapshot.Consistent() {
		return View{}, apperror.New("exam.unavailable").Wrap(errors.New("exam store returned an incomplete snapshot"))
	}

	candidate := *snapshot.Draft
	changed, err := candidate.ApplyBrowserPolicy(policy, at)
	if err != nil {
		return View{}, apperror.Invalid("exam.invalid", "browser_policy").Wrap(err)
	}
	if !changed && snapshot.Draft.Revision == command.ExpectedDraftRevision && !snapshot.Exam.IsArchived() {
		return View{}, apperror.New("exam.draft.no_changes")
	}

	auditID, err := a.auditor.Begin(ctx, access.Mutation{
		Invocation: invocation,
		Action:     action,
		Resource:   resource,
		ScopeType:  model.RoleScopeAcademicUnit,
		ScopeID:    examAccess.Exam.AcademicUnitID.String(),
		Operation:  "configure_draft_browser_policy",
		Value: map[string]any{
			"exam_id":                 command.ExamID.String(),
			"expected_draft_revision": command.ExpectedDraftRevision,
			"draft_revision":          command.ExpectedDraftRevision + 1,
			"browser_policy_enabled":  policy.Enabled,
		},
	})
	if err != nil {
		return View{}, err
	}

	result, err := a.exams.UpdateDraftBrowserPolicy(
		ctx,
		&store.ExamDraftBrowserPolicyUpdate{
			InstitutionOrigin: a.institutionOrigin,
			ExamID:            command.ExamID,
			ActorUserID:       principal.UserID,
			ManagerOverride:   action == model.ActionExamManageOverride,
			ExpectedRevision:  command.ExpectedDraftRevision,
			Policy:            policy,
			UpdatedAt:         candidate.UpdatedAt,
			AuditEventID:      auditID,
			AuditAt:           at,
		},
		commandIdempotency,
	)
	if err != nil {
		mapped := examStoreErrors.Translate(err)
		if auditErr := a.auditor.Fail(ctx, auditID, apperror.CodeOf(mapped, "exam.unavailable")); auditErr != nil {
			return View{}, errors.Join(mapped, auditErr)
		}

		return View{}, mapped
	}

	if !result.ConsistentWithDraft() {
		return View{}, apperror.New("exam.unavailable").Wrap(errors.New("exam store returned no Browser Policy update result"))
	}

	if !result.Replayed {
		if effectErr := a.publishDraftUpdated(ctx, result.Value.Exam.ID, result.Value.Draft.Revision); effectErr != nil {
			a.publisher.Report(ctx, "exam_draft_updated", effectErr)
		}
	}

	return projectView(result.Value), nil
}
