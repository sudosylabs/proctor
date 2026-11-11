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

	"github.com/sudosylabs/proctor/server/app/access"
	"github.com/sudosylabs/proctor/server/app/apperror"
	"github.com/sudosylabs/proctor/server/app/invocation"
	"github.com/sudosylabs/proctor/server/model"
	"github.com/sudosylabs/proctor/server/store"
)

type ReorderCommand struct {
	ExamID                model.ExamID
	ExpectedDraftRevision int64
	ResourceIDs           []model.ExamResourceID
	IdempotencyKey        string
}

// ReorderExamResources idempotently sets the order of the Exam Draft's Exam
// Resources, fenced by ExpectedDraftRevision. ResourceIDs must not repeat.
func (s *Service) ReorderExamResources(
	ctx context.Context,
	invocation invocation.Invocation,
	c ReorderCommand,
) (_ []store.ExamResourceRecord, resultErr error) {
	defer func() { resultErr = publicFailures.Conceal(resultErr) }()
	c.ResourceIDs = append([]model.ExamResourceID(nil), c.ResourceIDs...)
	if !c.ExamID.IsValid() || c.ExpectedDraftRevision < 1 {
		return nil, apperror.Invalid("exam.resource.invalid", "identity")
	}
	if len(c.ResourceIDs) > model.ExamResourceMaximumCount {
		return nil, apperror.Invalid("exam.resource.invalid", "resource_ids")
	}

	seen := make(map[model.ExamResourceID]struct{}, len(c.ResourceIDs))
	for _, id := range c.ResourceIDs {
		if !id.IsValid() {
			return nil, apperror.Invalid("exam.resource.invalid", "resource_ids")
		}
		if _, exists := seen[id]; exists {
			return nil, apperror.Invalid("exam.resource.invalid", "resource_ids")
		}

		seen[id] = struct{}{}
	}

	c.ResourceIDs = append([]model.ExamResourceID(nil), c.ResourceIDs...)
	ids := make([]string, len(c.ResourceIDs))
	for index, id := range c.ResourceIDs {
		ids[index] = id.String()
	}

	commandIdempotency, err := prepareResourceIdempotency(
		invocation,
		idempotencyOperationReorderResources,
		c.IdempotencyKey,
		c.ExamID,
		c.ExpectedDraftRevision,
		"",
		"",
		"",
		"",
		0,
		"",
		ids,
	)
	if err != nil {
		return nil, err
	}

	authorization, err := s.authorizeExam(ctx, invocation, c.ExamID, true)
	if err != nil {
		return nil, err
	}

	current, err := s.resources.List(ctx, c.ExamID)
	if err != nil {
		return nil, storeErrors.Translate(err)
	}

	if len(current) == len(c.ResourceIDs) {
		same := true
		for index := range current {
			if current[index].Resource == nil || current[index].Resource.ID != c.ResourceIDs[index] {
				same = false
				break
			}
		}

		if same && s.currentActiveDraft(ctx, invocation, c.ExamID, c.ExpectedDraftRevision) {
			return nil, apperror.New("exam.resource.no_changes")
		}
	}

	at := model.TimeUTC(s.now())
	action := model.ActionExamManage
	if authorization.override {
		action = model.ActionExamManageOverride
	}

	auditID, err := s.auditor.Begin(ctx, access.Mutation{
		Invocation: invocation,
		Action:     action,
		Resource:   model.Resource{Type: model.ResourceExam, ID: c.ExamID.String()},
		ScopeType:  model.RoleScopeAcademicUnit,
		ScopeID:    authorization.academicUnitID.String(),
		Operation:  "reorder_resources",
		Value:      map[string]any{"exam_id": c.ExamID.String(), "resource_count": len(c.ResourceIDs)},
	})
	if err != nil {
		return nil, err
	}

	result, err := s.resources.Reorder(
		ctx,
		&store.ExamResourceReorder{
			ExamID:                c.ExamID,
			ActorUserID:           invocation.Principal().UserID,
			ManagerOverride:       authorization.override,
			ExpectedDraftRevision: c.ExpectedDraftRevision,
			ResourceIDs:           append([]model.ExamResourceID(nil), c.ResourceIDs...),
			ChangedAt:             at,
			AuditEventID:          auditID,
			AuditAt:               at,
		},
		commandIdempotency,
	)
	if err != nil {
		return nil, s.failAudit(ctx, auditID, err)
	}
	if result == nil {
		return nil, apperror.New("exam.resource.unavailable").Wrap(errors.New("missing reorder outcome"))
	}

	if !result.Replayed {
		if result.DraftRevision < 1 {
			s.publisher.Report(ctx, "exam_resource_changed", errors.New("missing committed Draft revision"))
		} else if effectErr := s.publishDraftUpdated(ctx, c.ExamID, result.DraftRevision); effectErr != nil {
			s.publisher.Report(ctx, "exam_resource_changed", effectErr)
		}
	}

	return result.Items, nil
}
