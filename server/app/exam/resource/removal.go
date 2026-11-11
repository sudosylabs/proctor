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

type RemoveCommand struct {
	ExamID                model.ExamID
	ResourceID            model.ExamResourceID
	ExpectedDraftRevision int64
	IdempotencyKey        string
}

// RemoveExamResource idempotently removes an Exam Resource from the Exam
// Draft, fenced by ExpectedDraftRevision.
func (s *Service) RemoveExamResource(
	ctx context.Context,
	inv invocation.Invocation,
	c RemoveCommand,
) (_ store.ExamResourceRecord, resultErr error) {
	defer func() { resultErr = publicFailures.Conceal(resultErr) }()
	if !c.ExamID.IsValid() || !c.ResourceID.IsValid() || c.ExpectedDraftRevision < 1 {
		return store.ExamResourceRecord{}, apperror.Invalid("exam.resource.invalid", "identity")
	}

	commandIdempotency, err := prepareResourceIdempotency(
		inv,
		idempotencyOperationRemoveResource,
		c.IdempotencyKey,
		c.ExamID,
		c.ExpectedDraftRevision,
		c.ResourceID.String(),
		"",
		"",
		"",
		0,
		"",
		nil,
	)
	if err != nil {
		return store.ExamResourceRecord{}, err
	}

	authorization, err := s.authorizeExam(ctx, inv, c.ExamID, true)
	if err != nil {
		return store.ExamResourceRecord{}, err
	}

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
		Operation:  "remove_resource",
		Value:      map[string]any{"exam_id": c.ExamID.String(), "exam_resource_id": c.ResourceID.String()},
	})
	if err != nil {
		return store.ExamResourceRecord{}, err
	}

	result, err := s.resources.Remove(
		ctx,
		&store.ExamResourceRemoval{
			ExamID:                c.ExamID,
			ActorUserID:           inv.Principal().UserID,
			ManagerOverride:       authorization.override,
			ExpectedDraftRevision: c.ExpectedDraftRevision,
			ResourceID:            c.ResourceID,
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
		return store.ExamResourceRecord{}, apperror.New("exam.resource.unavailable").Wrap(errors.New("missing removal outcome"))
	}

	s.publishChanged(ctx, result)
	return *result.Value, nil
}
