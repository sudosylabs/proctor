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
	"strings"

	"github.com/sudosylabs/proctor/server/app/access"
	"github.com/sudosylabs/proctor/server/app/apperror"
	"github.com/sudosylabs/proctor/server/app/invocation"
	"github.com/sudosylabs/proctor/server/model"
	"github.com/sudosylabs/proctor/server/store"
)

type EditMetadataCommand struct {
	ExamID                model.ExamID
	ResourceID            model.ExamResourceID
	ExpectedDraftRevision int64
	DisplayName           *string
	DescriptionMarkdown   *string
	IdempotencyKey        string
}

// EditExamResourceMetadata idempotently changes an Exam Resource's display
// name, description, or both, fenced by ExpectedDraftRevision. A nil field is
// left unchanged, but at least one must be set.
func (s *Service) EditExamResourceMetadata(
	ctx context.Context,
	inv invocation.Invocation,
	command EditMetadataCommand,
) (_ store.ExamResourceRecord, resultErr error) {
	defer func() { resultErr = publicFailures.Conceal(resultErr) }()
	if !command.ExamID.IsValid() ||
		!command.ResourceID.IsValid() ||
		command.ExpectedDraftRevision < 1 ||
		command.DisplayName == nil && command.DescriptionMarkdown == nil {
		return store.ExamResourceRecord{}, apperror.Invalid("exam.resource.invalid", "identity")
	}

	command.DisplayName = cloneStringPointer(command.DisplayName)
	if command.DisplayName != nil {
		*command.DisplayName = strings.TrimSpace(*command.DisplayName)
	}

	command.DescriptionMarkdown = cloneStringPointer(command.DescriptionMarkdown)
	commandIdempotency, err := prepareMetadataIdempotency(inv, command)
	if err != nil {
		return store.ExamResourceRecord{}, err
	}

	authorization, err := s.authorizeExam(ctx, inv, command.ExamID, true)
	if err != nil {
		return store.ExamResourceRecord{}, err
	}

	current, err := s.resources.Get(ctx, command.ExamID, command.ResourceID)
	if err != nil {
		return store.ExamResourceRecord{}, storeErrors.Translate(err)
	}
	if current == nil || current.Resource == nil {
		return store.ExamResourceRecord{}, apperror.New("exam.resource.unavailable").Wrap(errors.New("incomplete resource record"))
	}

	candidate := *current.Resource
	displayName, descriptionMarkdown := candidate.DisplayName, candidate.DescriptionMarkdown
	if command.DisplayName != nil {
		displayName = *command.DisplayName
	}
	if command.DescriptionMarkdown != nil {
		descriptionMarkdown = *command.DescriptionMarkdown
	}

	changed, validationErr := candidate.ApplyMetadata(displayName, descriptionMarkdown, model.TimeUTC(s.now()))
	if validationErr != nil {
		return store.ExamResourceRecord{}, apperror.Invalid("exam.resource.invalid", "metadata").Wrap(validationErr)
	}
	if !changed && s.currentActiveDraft(ctx, inv, command.ExamID, command.ExpectedDraftRevision) {
		return store.ExamResourceRecord{}, apperror.New("exam.resource.no_changes")
	}

	return s.updateMetadata(ctx, inv, authorization, command, commandIdempotency)
}

func (s *Service) updateMetadata(
	ctx context.Context,
	inv invocation.Invocation,
	authorization authorizationDecision,
	c EditMetadataCommand,
	commandIdempotency *store.CommandIdempotency,
) (store.ExamResourceRecord, error) {
	at := model.TimeUTC(s.now())
	action := model.ActionExamManage
	if authorization.override {
		action = model.ActionExamManageOverride
	}

	auditID, err := s.auditor.Begin(ctx, access.Mutation{
		Invocation: inv,
		Action:     action,
		Resource:   model.Resource{Type: model.ResourceExam, ID: c.ExamID.String()},
		ScopeType:  model.RoleScopeAcademicUnit,
		ScopeID:    authorization.academicUnitID.String(),
		Operation:  "edit_resource_metadata",
		Value:      map[string]any{"exam_id": c.ExamID.String(), "exam_resource_id": c.ResourceID.String()},
	})
	if err != nil {
		return store.ExamResourceRecord{}, err
	}

	displayName, descriptionMarkdown := "", ""
	current, err := s.resources.Get(ctx, c.ExamID, c.ResourceID)
	if err != nil {
		return store.ExamResourceRecord{}, s.failAudit(ctx, auditID, err)
	}
	if current == nil || current.Resource == nil {
		return store.ExamResourceRecord{}, s.failAudit(ctx, auditID, errors.New("missing resource record"))
	}

	displayName, descriptionMarkdown = current.Resource.DisplayName, current.Resource.DescriptionMarkdown
	if c.DisplayName != nil {
		displayName = *c.DisplayName
	}
	if c.DescriptionMarkdown != nil {
		descriptionMarkdown = *c.DescriptionMarkdown
	}

	result, err := s.resources.UpdateMetadata(
		ctx,
		&store.ExamResourceMetadataUpdate{
			ExamID:                c.ExamID,
			ActorUserID:           inv.Principal().UserID,
			ManagerOverride:       authorization.override,
			ExpectedDraftRevision: c.ExpectedDraftRevision,
			ResourceID:            c.ResourceID,
			DisplayName:           displayName,
			DescriptionMarkdown:   descriptionMarkdown,
			ChangedAt:             at,
			AuditEventID:          auditID,
			AuditAt:               at,
		},
		commandIdempotency,
	)
	if err != nil {
		return store.ExamResourceRecord{}, s.failAudit(ctx, auditID, err)
	}
	if result == nil || result.Value == nil {
		return store.ExamResourceRecord{}, apperror.New("exam.resource.unavailable").Wrap(errors.New("missing metadata outcome"))
	}

	s.publishChanged(ctx, result)
	return *result.Value, nil
}

func cloneStringPointer(value *string) *string {
	if value == nil {
		return nil
	}
	cloned := *value
	return &cloned
}
