// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package attempt

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

// ReplaceEnvironmentCommand asks for a new machine for a candidate's
// execution environment, built from the Attempt Workspace.
// ExpectedIncarnation is the incarnation the manager saw on the board.
type ReplaceEnvironmentCommand struct {
	ExamID              model.ExamID
	SittingID           model.ExamSittingID
	AttemptID           model.ExamAttemptID
	ExpectedIncarnation int64
	IdempotencyKey      string
}

// ReplaceEnvironmentResult is the environment after the request. The owner
// replaces the machine shortly after; Incarnation then reaches
// ReplaceBefore, and the restart is recorded as an execution gap.
type ReplaceEnvironmentResult struct {
	Environment   model.AttemptEnvironment
	ReplaceBefore int64
	Replayed      bool
}

// ReplaceExamAttemptEnvironment asks the owner of a candidate's environment
// to replace its machine, for a manager of the Sitting, as a Kick is. The
// Workspace is the authority, so nothing the candidate saved is lost; what
// only the old machine held (processes, terminals, ignored files) is.
func (s *Service) ReplaceExamAttemptEnvironment(
	ctx context.Context,
	inv invocation.Invocation,
	command ReplaceEnvironmentCommand,
) (_ ReplaceEnvironmentResult, resultErr error) {
	defer func() { resultErr = publicFailures.Conceal(resultErr) }()
	principal := inv.Principal()
	if principal.Validate() != nil {
		return ReplaceEnvironmentResult{}, apperror.New("authentication.token_invalid")
	}
	if !command.ExamID.IsValid() || !command.SittingID.IsValid() || !command.AttemptID.IsValid() ||
		command.ExpectedIncarnation < 0 {
		return ReplaceEnvironmentResult{}, apperror.Invalid("exam.attempt.invalid", "environment_replacement")
	}
	if s.environmentStore == nil {
		return ReplaceEnvironmentResult{}, apperror.New("exam.attempt.not_found")
	}
	commandIdempotency, err := idempotency.Require(inv, store.AttemptEnvironmentReplaceOperation, command.IdempotencyKey, struct {
		ExamID              string `json:"exam_id"`
		SittingID           string `json:"exam_sitting_id"`
		AttemptID           string `json:"exam_attempt_id"`
		ExpectedIncarnation int64  `json:"expected_incarnation"`
	}{command.ExamID.String(), command.SittingID.String(), command.AttemptID.String(), command.ExpectedIncarnation})
	if err != nil {
		return ReplaceEnvironmentResult{}, err
	}

	management, err := s.managers.AuthorizeSittingManage(ctx, inv, command.SittingID)
	if err != nil {
		return ReplaceEnvironmentResult{}, err
	}
	sitting, err := s.sittings.Resolve(ctx, command.SittingID)
	if err != nil {
		return ReplaceEnvironmentResult{}, storeError(storeErrors, err)
	}
	action := model.ActionExamSittingManage
	if management.Override {
		action = model.ActionExamSittingManageOverride
	}
	auditID, err := s.auditor.Begin(ctx, access.Mutation{
		Invocation: inv,
		Action:     action,
		Resource:   model.Resource{Type: model.ResourceExamSitting, ID: command.SittingID.String()},
		ScopeType:  model.RoleScopeClass,
		ScopeID:    sitting.Sitting.ClassID.String(),
		Operation:  store.AttemptEnvironmentReplaceOperation,
		Value: map[string]any{
			"exam_id":              command.ExamID.String(),
			"exam_sitting_id":      command.SittingID.String(),
			"exam_attempt_id":      command.AttemptID.String(),
			"expected_incarnation": command.ExpectedIncarnation,
		},
	})
	if err != nil {
		return ReplaceEnvironmentResult{}, err
	}
	if sitting.Sitting.ExamID != command.ExamID {
		return ReplaceEnvironmentResult{}, s.failAudit(ctx, auditID, apperror.New("exam.attempt.not_found"))
	}

	stored, err := s.environmentStore.Replace(ctx, &store.AttemptEnvironmentReplacement{
		ExamID: command.ExamID, SittingID: command.SittingID, AttemptID: command.AttemptID,
		ActorUserID: principal.UserID, ManagerOverride: management.Override,
		ExpectedIncarnation: command.ExpectedIncarnation, AuditEventID: auditID, AuditAt: s.now(),
	}, commandIdempotency)
	if err != nil {
		return ReplaceEnvironmentResult{}, s.failAudit(ctx, auditID, storeError(storeErrors, err))
	}
	if stored == nil || stored.Environment == nil || stored.Environment.AttemptID != command.AttemptID ||
		stored.ReplaceBefore != command.ExpectedIncarnation+1 {
		return ReplaceEnvironmentResult{}, apperror.New("exam.attempt.unavailable").Wrap(errors.New("inconsistent environment replacement"))
	}
	if !stored.Replayed && s.environments != nil {
		s.environments.Wake(command.AttemptID)
	}
	return ReplaceEnvironmentResult{Environment: *stored.Environment, ReplaceBefore: stored.ReplaceBefore,
		Replayed: stored.Replayed}, nil
}
