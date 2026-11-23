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

	"github.com/sudosylabs/proctor/server/app/apperror"
	"github.com/sudosylabs/proctor/server/app/exam/manageraccess"
	"github.com/sudosylabs/proctor/server/app/invocation"
	"github.com/sudosylabs/proctor/server/model"
	"github.com/sudosylabs/proctor/server/store"
)

// DraftProblem is one reason the draft cannot be published now. Code is the
// error publishing would return for it, and Field names the part of the draft
// at fault.
type DraftProblem struct {
	Reason     store.ExamPublicationProblemReason
	Code       apperror.Code
	Field      string
	ResourceID model.ExamResourceID
}

// DraftCheck is every publication check the draft at DraftRevision fails.
type DraftCheck struct {
	DraftRevision int64
	Problems      []DraftProblem
}

// Publishable reports whether publishing the checked draft would succeed.
func (c DraftCheck) Publishable() bool { return len(c.Problems) == 0 }

// draftProblemFields maps a failed publication check to the field to edit.
var draftProblemFields = map[store.ExamPublicationProblemReason]string{
	store.ExamPublicationExamArchived:                "exam",
	store.ExamPublicationExecutionDisabled:           "policy.execution",
	store.ExamPublicationEnvironmentProfileUnusable:  "policy.execution.profile_id",
	store.ExamPublicationAssistantDisabled:           "policy.assistant",
	store.ExamPublicationAssistantProfileUnusable:    "policy.assistant.profile_id",
	store.ExamPublicationTooManyResources:            "resources",
	store.ExamPublicationResourceTooLarge:            "resources.size_bytes",
	store.ExamPublicationTooManyStarterEntries:       "starter_workspace",
	store.ExamPublicationStarterFileTooLarge:         "starter_workspace.size_bytes",
	store.ExamPublicationStarterWorkspaceTooLarge:    "starter_workspace.total_bytes",
	store.ExamPublicationBrowserPolicyOriginMismatch: "browser_policy",
	store.ExamPublicationNoChanges:                   "draft",
}

// CheckExamDraft runs the checks PublishExamRevision runs on the current
// draft and reports every one it fails, without publishing. It needs the
// same permission as publishing and records no audit, since it changes
// nothing.
func (p *Publication) CheckExamDraft(
	ctx context.Context,
	inv invocation.Invocation,
	examID model.ExamID,
) (_ DraftCheck, resultErr error) {
	defer func() { resultErr = publicFailures.Conceal(resultErr) }()
	principal := inv.Principal()
	if principal.Validate() != nil || !examID.IsValid() {
		return DraftCheck{}, apperror.Invalid("exam.invalid", "exam_id")
	}

	examAccess, err := p.exams.Access(ctx, examID, principal.UserID)
	if err != nil {
		return DraftCheck{}, examStoreErrors.Translate(err)
	}

	action, err := manageraccess.SelectAction(
		ctx,
		p.academicUnitMembers,
		principal.UserID,
		examAccess,
		model.TimeUTC(p.now()),
		model.ActionExamPublish,
		model.ActionExamPublishOverride,
	)
	if err != nil {
		return DraftCheck{}, apperror.New("exam.unavailable").Wrap(err)
	}

	if err = p.authorizer.Authorize(ctx, inv, action, model.Resource{Type: model.ResourceExam, ID: examID.String()}); err != nil {
		return DraftCheck{}, err
	}

	result, err := p.revisions.CheckPublication(ctx, &store.ExamPublicationCheck{
		InstitutionOrigin:  p.institutionOrigin,
		ExecutionEnabled:   p.executionEnabled,
		AssistantEnabled:   p.assistantEnabled,
		AssistantProviders: p.assistantProviders,
		ExamID:             examID,
		ActorUserID:        principal.UserID,
		ManagerOverride:    action == model.ActionExamPublishOverride,
	})
	if err != nil {
		return DraftCheck{}, examStoreErrors.Translate(err)
	}
	if result == nil || result.DraftRevision < 1 {
		return DraftCheck{}, apperror.New("exam.unavailable").Wrap(errors.New("Exam Revision store returned no publication check"))
	}

	check := DraftCheck{DraftRevision: result.DraftRevision, Problems: make([]DraftProblem, 0, len(result.Problems))}
	for _, problem := range result.Problems {
		field, known := draftProblemFields[problem.Reason]
		if !known || (problem.Reason == store.ExamPublicationResourceTooLarge) != problem.ResourceID.IsValid() {
			return DraftCheck{}, apperror.New("exam.unavailable").Wrap(errors.New("Exam Revision store returned an unknown publication problem"))
		}
		check.Problems = append(check.Problems, DraftProblem{
			Reason:     problem.Reason,
			Code:       apperror.Code(apperror.CodeOf(examStoreErrors.Translate(problem.PublicationError()), "exam.unavailable")),
			Field:      field,
			ResourceID: problem.ResourceID,
		})
	}

	return check, nil
}
