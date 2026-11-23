// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package attempt

import (
	"context"

	"github.com/sudosylabs/proctor/server/app/apperror"
	"github.com/sudosylabs/proctor/server/app/invocation"
	"github.com/sudosylabs/proctor/server/model"
	"github.com/sudosylabs/proctor/server/store"
)

// AssistantCandidate is the candidate's live editor proof. The assistant
// carries it for a whole turn and the store rechecks it on every step, so the
// assistant acts only while the candidate could act themselves.
type AssistantCandidate struct {
	ExamID    model.ExamID
	Editor    store.ExamAttemptWorkspaceEditorAccess
	SittingID model.ExamSittingID
	ClassID   model.ClassID
}

// VerifyAssistantCandidate proves the candidate's access as the editor does
// and binds the proof to the participation generation that holds now.
func (s *Service) VerifyAssistantCandidate(
	ctx context.Context,
	inv invocation.Invocation,
	candidateAccess CandidateAccess,
) (_ AssistantCandidate, resultErr error) {
	defer func() { resultErr = publicFailures.Conceal(resultErr) }()
	verified, err := s.VerifyCandidateAttemptAccess(ctx, inv, candidateAccess)
	if err != nil {
		return AssistantCandidate{}, err
	}
	editor, err := workspaceMutationSelector(inv, WorkspaceMutationAccess{
		CandidateAccess: candidateAccess, ParticipationID: verified.ParticipationID, Generation: verified.Generation,
	})
	if err != nil {
		return AssistantCandidate{}, err
	}
	return AssistantCandidate{ExamID: verified.ExamID, Editor: editor, SittingID: verified.SittingID, ClassID: verified.ClassID}, nil
}

// AssistantWorkspaceBatchCommand is an assistant edit. The candidate's live
// editor proof grants it; the conversation, turn and call only attribute it.
type AssistantWorkspaceBatchCommand struct {
	Candidate               AssistantCandidate
	ConversationID          model.AssistantConversationID
	TurnID                  model.AssistantTurnID
	CallID                  model.AssistantCallID
	Mutations               []WorkspaceBatchMutation
	ExpectedWorkspaceCursor *int64
	Content                 WorkspaceBatchContent
	IdempotencyKey          string
}

// ApplyAssistantWorkspaceBatch is the editor's batch written by the assistant
// on the candidate's behalf. It takes the one write path, with the assistant
// author and its own budget; the audit names the candidate as actor and the
// assistant call as the author, as an environment write does.
func (s *Service) ApplyAssistantWorkspaceBatch(
	ctx context.Context,
	system invocation.System,
	command AssistantWorkspaceBatchCommand,
) (_ WorkspaceBatchResult, resultErr error) {
	defer func() { resultErr = publicFailures.Conceal(resultErr) }()
	if err := validEnvironmentCaller(system); err != nil {
		return WorkspaceBatchResult{}, err
	}
	assistantAccess := store.ExamAttemptWorkspaceAssistantAccess{ExamAttemptWorkspaceEditorAccess: command.Candidate.Editor,
		ConversationID: command.ConversationID, TurnID: command.TurnID, CallID: command.CallID}
	if !assistantAccess.WorkspaceAuthor().IsValid() {
		return WorkspaceBatchResult{}, apperror.Invalid("exam.attempt.invalid", "assistant_author")
	}
	return s.applyWorkspaceWrite(ctx, workspaceWriter{
		access:    assistantAccess,
		candidate: command.Candidate.Editor.CandidateUserID,
		beginAudit: func(ctx context.Context, target *store.ExamAttemptWorkspaceMutationTarget, value map[string]any) (model.AuditEventID, error) {
			value["system_task"] = system.Task
			return s.systemAuditor.BeginOnBehalf(ctx, target.CandidateUserID, model.ActionExamSittingParticipate,
				model.Resource{Type: model.ResourceExamSitting, ID: target.SittingID.String()}, model.RoleScopeClass,
				target.ClassID.String(), store.ExamAttemptWorkspaceBatchOperation, value)
		},
	}, workspaceWrite{
		Mutations:               command.Mutations,
		ExpectedWorkspaceCursor: command.ExpectedWorkspaceCursor,
		Content:                 command.Content,
		IdempotencyKey:          command.IdempotencyKey,
	})
}

// AssistantStoreError presents a store refusal as the editor would see it,
// after the assistant's own rules.
func AssistantStoreError(err error, rules ...apperror.StoreRule) error {
	return storeError(storeErrors.With(rules...), err)
}
