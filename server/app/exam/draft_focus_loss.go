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
	"time"

	"github.com/sudosylabs/proctor/server/app/access"
	"github.com/sudosylabs/proctor/server/app/apperror"
	"github.com/sudosylabs/proctor/server/app/idempotency"
	"github.com/sudosylabs/proctor/server/app/invocation"
	"github.com/sudosylabs/proctor/server/model"
	"github.com/sudosylabs/proctor/server/store"
)

type ConfigureDraftFocusLossCommand struct {
	ExamID                model.ExamID
	ExpectedDraftRevision int64
	Enabled               bool
	MinimumDuration       time.Duration
	IncidentCount         int
	Window                time.Duration
	Outcome               model.IntegrityThresholdOutcome
	IdempotencyKey        string
}

// ConfigureExamDraftFocusLoss replaces only the supported Focus Loss rule. The
// complete policy is reconstructed from authoritative Draft state so callers
// cannot supply or weaken Connection Loss.
func (a *Authoring) ConfigureExamDraftFocusLoss(
	ctx context.Context,
	inv invocation.Invocation,
	command ConfigureDraftFocusLossCommand,
) (_ View, resultErr error) {
	defer func() { resultErr = publicFailures.Conceal(resultErr) }()
	focusLoss := model.FocusLossPolicy{
		Enabled:         command.Enabled,
		MinimumDuration: command.MinimumDuration,
		IncidentCount:   command.IncidentCount,
		Window:          command.Window,
		Outcome:         command.Outcome,
	}
	principal := inv.Principal()
	if principal.Validate() != nil || !command.ExamID.IsValid() || command.ExpectedDraftRevision < 1 {
		return View{}, apperror.Invalid("exam.invalid", "draft_revision")
	}

	focusLoss.MinimumDuration = time.Duration(focusLoss.MinimumDuration.Milliseconds()) * time.Millisecond
	focusLoss.Window = time.Duration(focusLoss.Window.Milliseconds()) * time.Millisecond
	policy := model.DefaultExamPolicySet()
	policy.FocusLoss = focusLoss
	if err := policy.Validate(); err != nil {
		return View{}, apperror.Invalid("exam.invalid", "focus_loss").Wrap(err)
	}

	commandIdempotency, err := idempotency.Require(inv, idempotencyOperationConfigureDraftFocusLoss, command.IdempotencyKey, struct {
		ExamID                      string                          `json:"exam_id"`
		ExpectedDraftRevision       int64                           `json:"expected_draft_revision"`
		Enabled                     bool                            `json:"enabled"`
		MinimumDurationMilliseconds int64                           `json:"minimum_duration_milliseconds"`
		IncidentCount               int                             `json:"incident_count"`
		WindowMilliseconds          int64                           `json:"window_milliseconds"`
		Outcome                     model.IntegrityThresholdOutcome `json:"outcome"`
	}{
		ExamID:                      command.ExamID.String(),
		ExpectedDraftRevision:       command.ExpectedDraftRevision,
		Enabled:                     focusLoss.Enabled,
		MinimumDurationMilliseconds: focusLoss.MinimumDuration.Milliseconds(),
		IncidentCount:               focusLoss.IncidentCount,
		WindowMilliseconds:          focusLoss.Window.Milliseconds(),
		Outcome:                     focusLoss.Outcome,
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
	changed, err := candidate.ApplyFocusLossPolicy(focusLoss, at)
	if err != nil {
		return View{}, apperror.Invalid("exam.invalid", "focus_loss").Wrap(err)
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
		Operation:  "configure_draft_focus_loss",
		Value: map[string]any{
			"exam_id":                 command.ExamID.String(),
			"expected_draft_revision": command.ExpectedDraftRevision,
			"draft_revision":          command.ExpectedDraftRevision + 1,
		},
	})
	if err != nil {
		return View{}, err
	}

	result, err := a.exams.UpdateDraftFocusLoss(ctx, &store.ExamDraftFocusLossUpdate{
		ExamID:           command.ExamID,
		ActorUserID:      principal.UserID,
		ManagerOverride:  action == model.ActionExamManageOverride,
		ExpectedRevision: command.ExpectedDraftRevision,
		FocusLoss:        focusLoss,
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
