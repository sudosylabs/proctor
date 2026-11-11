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
	"slices"

	"github.com/sudosylabs/proctor/server/app/access"
	"github.com/sudosylabs/proctor/server/app/apperror"
	"github.com/sudosylabs/proctor/server/app/idempotency"
	"github.com/sudosylabs/proctor/server/app/invocation"
	"github.com/sudosylabs/proctor/server/model"
	"github.com/sudosylabs/proctor/server/store"
)

// CreateExportCommand requests an Examination Export of the given retention
// categories for a Sitting or one of its Submissions.
type CreateExportCommand struct {
	Scope          model.RetentionHoldScope
	Categories     []model.RetentionCategory
	IdempotencyKey string
}

// CreateExamExport idempotently queues an Examination Export for the caller
// and its build Job, after authorizing records export for the scope and
// access to every selected source category.
func (s *Exports) CreateExamExport(
	ctx context.Context,
	invocation invocation.Invocation,
	command CreateExportCommand,
) (_ *model.ExamExport, resultErr error) {
	defer func() { resultErr = publicFailures.Conceal(resultErr) }()
	command.Categories = slices.Clone(command.Categories)
	if len(command.Categories) == 2 &&
		command.Categories[0] == model.RetentionCategoryIntegrity &&
		command.Categories[1] == model.RetentionCategoryWork {
		command.Categories[0], command.Categories[1] = command.Categories[1], command.Categories[0]
	}

	if !model.ValidExamExportCategories(command.Categories) {
		return nil, apperror.Invalid("exam.invalid", "export_categories")
	}

	key := command.IdempotencyKey
	command.IdempotencyKey = ""
	commandIdempotency, err := idempotency.Require(invocation, store.ExamExportCreateOperation, key, command)
	if err != nil {
		return nil, err
	}

	action, unit, err := s.authorizeExport(ctx, invocation, command.Scope)
	if err != nil {
		return nil, err
	}

	selected, err := s.exports.ListCreationScope(ctx, command.Scope, commandIdempotency)
	if err != nil {
		return nil, examExportStoreErrors.Translate(err)
	}
	if err = s.authorizeSources(ctx, invocation, command.Scope, action, unit, command.Categories, selected); err != nil {
		return nil, err
	}

	exportID, jobID := s.newID(), s.newJobID()
	if !exportID.IsValid() || !jobID.IsValid() {
		return nil, apperror.New("exam.export.unavailable").Wrap(errors.New("invalid export identities"))
	}

	auditID, err := s.auditor.Begin(ctx, access.Mutation{
		Invocation: invocation,
		Action:     action,
		Resource:   command.Scope.Resource(),
		ScopeType:  model.RoleScopeAcademicUnit,
		ScopeID:    unit.String(),
		Operation:  "create_exam_export",
		Value:      map[string]any{"exam_export_id": exportID.String(), "submission_count": len(selected), "category_count": len(command.Categories)},
	})
	if err != nil {
		return nil, err
	}

	input := &store.ExamExportCreation{
		ExamExportAccess: store.ExamExportAccess{
			ExamRecordsMutation: store.ExamRecordsMutation{
				Scope:        command.Scope,
				Principal:    invocation.Principal(),
				Action:       action,
				AuditEventID: auditID,
				AuditAt:      model.TimeUTC(s.now()),
			},
			ExportID:   exportID,
			Categories: command.Categories,
		},
		JobID: jobID,
	}
	for _, a := range selected {
		input.SubmissionIDs = append(input.SubmissionIDs, a.SubmissionID)
	}

	result, err := s.exports.Create(ctx, input, commandIdempotency)
	if err != nil {
		mapped := examExportStoreErrors.Translate(err)
		code := apperror.CodeOf(mapped, "exam.export.unavailable")
		if auditErr := s.auditor.Fail(ctx, auditID, code); auditErr != nil {
			return nil, errors.Join(mapped, auditErr)
		}

		return nil, mapped
	}

	if result.Validate() != nil ||
		result.Scope != command.Scope ||
		result.RequesterUserID != invocation.Principal().UserID ||
		!slices.Equal(result.Categories, command.Categories) {
		return nil, apperror.New("exam.export.unavailable").Wrap(errors.New("invalid export creation result"))
	}

	return result, nil
}
