// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package workspace

import (
	"context"

	"github.com/sudosylabs/proctor/server/app/apperror"
	"github.com/sudosylabs/proctor/server/app/idempotency"
	"github.com/sudosylabs/proctor/server/app/invocation"
	"github.com/sudosylabs/proctor/server/model"
	"github.com/sudosylabs/proctor/server/store"
)

type CreateDirectoryCommand struct {
	ExamID                model.ExamID
	ExpectedDraftRevision int64
	Path                  string
	IdempotencyKey        string
}

type MoveEntryCommand struct {
	ExamID                model.ExamID
	EntryID               model.StarterWorkspaceEntryID
	ExpectedDraftRevision int64
	Path                  string
	IdempotencyKey        string
}

type RemoveEntryCommand struct {
	ExamID                model.ExamID
	EntryID               model.StarterWorkspaceEntryID
	ExpectedDraftRevision int64
	Recursive             bool
	IdempotencyKey        string
}

// CreateExamStarterWorkspaceDirectory idempotently creates a directory in the
// Exam Draft's Starter Workspace, fenced by ExpectedDraftRevision.
func (s *Service) CreateExamStarterWorkspaceDirectory(
	ctx context.Context,
	invocation invocation.Invocation,
	command CreateDirectoryCommand,
) (_ Result, resultErr error) {
	defer func() { resultErr = publicFailures.Conceal(resultErr) }()
	path, err := validateMutation(command.ExamID, "", command.ExpectedDraftRevision, command.Path)
	if err != nil {
		return Result{}, err
	}

	commandIdempotency, err := prepareWorkspaceIdempotency(
		invocation,
		idempotencyOperationCreateDirectory,
		command.IdempotencyKey,
		command.ExamID,
		command.ExpectedDraftRevision,
		"",
		"",
		path,
		"",
		0,
		"",
	)
	if err != nil {
		return Result{}, err
	}

	authorization, err := s.authorizeExam(ctx, invocation, command.ExamID, true)
	if err != nil {
		return Result{}, err
	}

	at := model.TimeUTC(s.now())
	entryID := s.newEntryID()
	if !entryID.IsValid() {
		return Result{}, apperror.Invalid("exam.starter_workspace.invalid", "entry_id")
	}

	mutation := &store.ExamStarterWorkspaceMutation{
		ExamID:                command.ExamID,
		ActorUserID:           invocation.Principal().UserID,
		ManagerOverride:       authorization.override,
		ExpectedDraftRevision: command.ExpectedDraftRevision,
		ChangedAt:             at,
		EntryID:               entryID,
		Path:                  path,
	}
	return s.runMutation(
		ctx,
		invocation,
		authorization,
		"directory_create",
		ChangeDirectoryCreated,
		mutation,
		commandIdempotency,
		s.workspaces.CreateDirectory,
	)
}

// MoveExamStarterWorkspaceEntry idempotently moves an entry of the Exam
// Draft's Starter Workspace to a new path, fenced by ExpectedDraftRevision.
func (s *Service) MoveExamStarterWorkspaceEntry(
	ctx context.Context,
	invocation invocation.Invocation,
	command MoveEntryCommand,
) (_ Result, resultErr error) {
	defer func() { resultErr = publicFailures.Conceal(resultErr) }()
	path, err := validateMutation(command.ExamID, command.EntryID, command.ExpectedDraftRevision, command.Path)
	if err != nil {
		return Result{}, err
	}

	commandIdempotency, err := prepareWorkspaceIdempotency(
		invocation,
		idempotencyOperationMoveEntry,
		command.IdempotencyKey,
		command.ExamID,
		command.ExpectedDraftRevision,
		"",
		command.EntryID.String(),
		path,
		"",
		0,
		"",
	)
	if err != nil {
		return Result{}, err
	}

	authorization, err := s.authorizeExam(ctx, invocation, command.ExamID, true)
	if err != nil {
		return Result{}, err
	}

	at := model.TimeUTC(s.now())
	mutation := &store.ExamStarterWorkspaceMutation{
		ExamID:                command.ExamID,
		ActorUserID:           invocation.Principal().UserID,
		ManagerOverride:       authorization.override,
		ExpectedDraftRevision: command.ExpectedDraftRevision,
		ChangedAt:             at,
		EntryID:               command.EntryID,
		Path:                  path,
	}
	return s.runMutation(ctx, invocation, authorization, "entry_move", ChangeEntryMoved, mutation, commandIdempotency, s.workspaces.MoveEntry)
}

// RemoveExamStarterWorkspaceEntry idempotently removes an entry, recursively
// when requested, from the Exam Draft's Starter Workspace, fenced by
// ExpectedDraftRevision.
func (s *Service) RemoveExamStarterWorkspaceEntry(
	ctx context.Context,
	invocation invocation.Invocation,
	command RemoveEntryCommand,
) (_ Result, resultErr error) {
	defer func() { resultErr = publicFailures.Conceal(resultErr) }()
	if err := validateMutationBase(command.ExamID, command.EntryID, command.ExpectedDraftRevision); err != nil {
		return Result{}, err
	}

	commandIdempotency, err := idempotency.Require(invocation, idempotencyOperationRemoveEntry, command.IdempotencyKey, struct {
		ExamID                string `json:"exam_id"`
		ExpectedDraftRevision int64  `json:"expected_draft_revision"`
		EntryID               string `json:"entry_id,omitempty"`
		Recursive             bool   `json:"recursive,omitempty"`
	}{command.ExamID.String(), command.ExpectedDraftRevision, command.EntryID.String(), command.Recursive})
	if err != nil {
		return Result{}, err
	}

	authorization, err := s.authorizeExam(ctx, invocation, command.ExamID, true)
	if err != nil {
		return Result{}, err
	}

	at := model.TimeUTC(s.now())
	mutation := &store.ExamStarterWorkspaceMutation{
		ExamID:                command.ExamID,
		ActorUserID:           invocation.Principal().UserID,
		ManagerOverride:       authorization.override,
		ExpectedDraftRevision: command.ExpectedDraftRevision,
		ChangedAt:             at,
		EntryID:               command.EntryID,
		Recursive:             command.Recursive,
	}
	return s.runMutation(
		ctx,
		invocation,
		authorization,
		"entry_remove",
		ChangeEntryRemoved,
		mutation,
		commandIdempotency,
		s.workspaces.RemoveEntry,
	)
}
