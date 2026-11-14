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
	"github.com/sudosylabs/proctor/server/app/invocation"
	"github.com/sudosylabs/proctor/server/model"
	"github.com/sudosylabs/proctor/server/store"
)

func workspaceMutationSelector(
	inv invocation.Invocation,
	mutationAccess WorkspaceMutationAccess,
) (store.ExamAttemptWorkspaceMutationAccess, error) {
	read, err := candidateSelector(inv, mutationAccess.CandidateAccess)
	if err != nil {
		return store.ExamAttemptWorkspaceMutationAccess{}, err
	}
	if !mutationAccess.ParticipationID.IsValid() || mutationAccess.Generation < 1 {
		return store.ExamAttemptWorkspaceMutationAccess{}, apperror.Invalid("exam.attempt.invalid", "workspace_access")
	}

	return store.ExamAttemptWorkspaceMutationAccess{
		AttemptID:                read.AttemptID,
		ParticipationID:          mutationAccess.ParticipationID,
		Generation:               mutationAccess.Generation,
		CandidateUserID:          read.CandidateUserID,
		SessionID:                read.SessionID,
		DesktopRegistrationID:    read.DesktopRegistrationID,
		DPoPKeyThumbprint:        read.DPoPKeyThumbprint,
		ConnectionID:             read.ConnectionID,
		ContinuityCredentialHash: read.ContinuityCredentialHash,
	}, nil
}

func validWorkspaceTarget(target *store.ExamAttemptWorkspaceMutationTarget, workspaceAccess store.ExamAttemptWorkspaceMutationAccess) bool {
	return target != nil &&
		target.ExamID.IsValid() &&
		target.SittingID.IsValid() &&
		target.ClassID.IsValid() &&
		target.CandidateUserID == workspaceAccess.CandidateUserID &&
		target.WorkspaceID.IsValid()
}

func (s *Service) applyWorkspaceBatch(
	ctx context.Context,
	inv invocation.Invocation,
	target *store.ExamAttemptWorkspaceMutationTarget,
	batch *store.ExamAttemptWorkspaceBatch,
	commandIdempotency *store.CommandIdempotency,
) (WorkspaceBatchResult, error) {
	auditID, err := s.auditor.Begin(ctx, access.Mutation{
		Invocation: inv,
		Action:     model.ActionExamSittingParticipate,
		Resource:   model.Resource{Type: model.ResourceExamSitting, ID: target.SittingID.String()},
		ScopeType:  model.RoleScopeClass,
		ScopeID:    target.ClassID.String(),
		Operation:  store.ExamAttemptWorkspaceBatchOperation,
		Value: map[string]any{
			"exam_attempt_id": batch.Access.AttemptID.String(),
			"mutation_count":  len(batch.Mutations),
		},
	})
	if err != nil {
		return WorkspaceBatchResult{}, err
	}

	batch.AuditEventID, batch.AuditAt = auditID, model.TimeUTC(s.now())
	stored, err := s.workspaces.ApplyBatch(ctx, batch, commandIdempotency)
	if err != nil {
		return WorkspaceBatchResult{}, s.failAudit(ctx, auditID, workspaceBatchStoreError(err))
	}
	if !stored.ConsistentWith(target, batch.Mutations) {
		return WorkspaceBatchResult{}, apperror.New("exam.attempt.unavailable").Wrap(errors.New("inconsistent Attempt Workspace batch result"))
	}

	result := WorkspaceBatchResult{
		AttemptID:       batch.Access.AttemptID,
		SittingID:       stored.SittingID,
		CandidateUserID: stored.CandidateUserID,
		WorkspaceID:     stored.WorkspaceID,
		Changes:         stored.Changes,
		Replayed:        stored.Replayed,
	}

	if !result.Replayed {
		if effectErr := s.publishWorkspaceChanged(ctx, result); effectErr != nil {
			s.publisher.Report(ctx, "exam_attempt_workspace_changed", effectErr)
		}
	}

	return result, nil
}

func validateWorkspaceEntryFence(entryID model.AttemptWorkspaceEntryID, expectedPath string) (string, error) {
	if !entryID.IsValid() {
		return "", apperror.Invalid("exam.attempt.invalid", "workspace_command")
	}
	path, err := validateWorkspacePath(expectedPath)
	if err != nil {
		return "", apperror.Invalid("exam.attempt.invalid", "workspace_path").Wrap(err)
	}
	return path, nil
}

func validateWorkspaceMove(entryID model.AttemptWorkspaceEntryID, expectedPath, destinationPath string) (string, string, error) {
	expected, err := validateWorkspaceEntryFence(entryID, expectedPath)
	if err != nil {
		return "", "", err
	}
	destination, err := validateWorkspacePath(destinationPath)
	if err != nil || destination == expected {
		return "", "", apperror.Invalid("exam.attempt.invalid", "workspace_destination_path").Wrap(err)
	}
	return expected, destination, nil
}

func validateWorkspacePath(value string) (string, error) {
	normalized, err := model.NormalizeAttemptWorkspacePath(value)
	if err != nil || normalized != value {
		return "", errors.New("Workspace path is not canonical")
	}
	return normalized, nil
}
