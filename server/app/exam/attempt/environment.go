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

	"github.com/sudosylabs/proctor/server/app/apperror"
	"github.com/sudosylabs/proctor/server/app/invocation"
	"github.com/sudosylabs/proctor/server/model"
	"github.com/sudosylabs/proctor/server/store"
)

// The use cases in this file are the core side of the execution environment
// seam. The execution plugin calls them through a typed capability, as an
// invocation.System naming its runtime task or Job; there is no plugin
// principal and no HTTP request. An environment acts only through a live
// binding to the Attempt's current Participation generation, and what it
// writes is the candidate's own work: journal origin environment, outcome and
// audit under the candidate.

func validEnvironmentCaller(system invocation.System) error {
	if !system.Valid() {
		return apperror.Invalid("exam.attempt.invalid", "system")
	}
	return nil
}

// EnvironmentBinding names the binding an execution environment acts
// through and repeats the identity and generation it was made for. The Store
// requires all of them to match the live binding.
type EnvironmentBinding struct {
	AttemptID  model.ExamAttemptID
	BindingID  model.AttemptEnvironmentBindingID
	Identity   model.AttemptEnvironmentIdentity
	Generation int64
}

func (b EnvironmentBinding) access() (store.ExamAttemptWorkspaceEnvironmentAccess, error) {
	access := store.ExamAttemptWorkspaceEnvironmentAccess{
		AttemptID:  b.AttemptID,
		BindingID:  b.BindingID,
		Identity:   b.Identity,
		Generation: b.Generation,
	}
	if !access.Valid() {
		return store.ExamAttemptWorkspaceEnvironmentAccess{}, apperror.Invalid("exam.attempt.invalid", "environment_binding")
	}
	return access, nil
}

// BindExecutionEnvironmentCommand binds an environment identity to one
// Participation generation of the Attempt.
type BindExecutionEnvironmentCommand struct {
	AttemptID  model.ExamAttemptID
	Generation int64
	Identity   model.AttemptEnvironmentIdentity
}

// ExecutionEnvironmentBinding is the live binding, and the binding of an
// earlier incarnation it replaced, if any.
type ExecutionEnvironmentBinding struct {
	Binding  model.AttemptEnvironmentBinding
	Replaced *model.AttemptEnvironmentBinding
}

// BindExecutionEnvironment binds an environment identity to the Attempt's
// current Participation generation. It is allowed only while the Attempt is
// Active and that generation is its active Participation. Binding the same
// identity again returns the live binding; a newer incarnation for the same
// generation replaces the old binding atomically. The binding ends by itself
// when the generation ends.
func (s *Service) BindExecutionEnvironment(
	ctx context.Context,
	system invocation.System,
	command BindExecutionEnvironmentCommand,
) (_ ExecutionEnvironmentBinding, resultErr error) {
	defer func() { resultErr = publicFailures.Conceal(resultErr) }()
	if err := validEnvironmentCaller(system); err != nil {
		return ExecutionEnvironmentBinding{}, err
	}
	if !command.AttemptID.IsValid() || command.Generation < 1 || command.Identity.Validate() != nil {
		return ExecutionEnvironmentBinding{}, apperror.Invalid("exam.attempt.invalid", "environment_binding")
	}

	bindingID := s.newEnvironmentBindingID()
	result, err := s.workspaces.BindEnvironment(ctx, &store.ExamAttemptEnvironmentBind{
		BindingID:  bindingID,
		AttemptID:  command.AttemptID,
		Generation: command.Generation,
		Identity:   command.Identity,
	})
	if err != nil {
		return ExecutionEnvironmentBinding{}, storeError(storeErrors, err)
	}
	if result == nil ||
		result.Binding.Validate() != nil ||
		!result.Binding.Live() ||
		result.Binding.AttemptID != command.AttemptID ||
		result.Binding.Generation != command.Generation ||
		result.Binding.Identity != command.Identity ||
		(!result.Repeated && result.Binding.ID != bindingID) ||
		(result.Replaced != nil && (result.Replaced.Live() || result.Replaced.AttemptID != command.AttemptID)) {
		return ExecutionEnvironmentBinding{}, apperror.New("exam.attempt.unavailable").Wrap(errors.New("inconsistent execution environment binding"))
	}

	return ExecutionEnvironmentBinding{Binding: result.Binding, Replaced: result.Replaced}, nil
}

// EndExecutionEnvironmentBindingCommand ends one binding of the Attempt.
type EndExecutionEnvironmentBindingCommand struct {
	AttemptID model.ExamAttemptID
	BindingID model.AttemptEnvironmentBindingID
}

// EndExecutionEnvironmentBinding ends a binding, as the owner does when it
// destroys the grant. Ending an ended binding returns it unchanged.
func (s *Service) EndExecutionEnvironmentBinding(
	ctx context.Context,
	system invocation.System,
	command EndExecutionEnvironmentBindingCommand,
) (_ model.AttemptEnvironmentBinding, resultErr error) {
	defer func() { resultErr = publicFailures.Conceal(resultErr) }()
	if err := validEnvironmentCaller(system); err != nil {
		return model.AttemptEnvironmentBinding{}, err
	}
	if !command.AttemptID.IsValid() || !command.BindingID.IsValid() {
		return model.AttemptEnvironmentBinding{}, apperror.Invalid("exam.attempt.invalid", "environment_binding")
	}

	result, err := s.workspaces.EndEnvironmentBinding(ctx, &store.ExamAttemptEnvironmentBindingEnd{
		AttemptID: command.AttemptID,
		BindingID: command.BindingID,
	})
	if err != nil {
		return model.AttemptEnvironmentBinding{}, storeError(storeErrors, err)
	}
	if result == nil ||
		result.Binding.Validate() != nil ||
		result.Binding.Live() ||
		result.Binding.ID != command.BindingID ||
		result.Binding.AttemptID != command.AttemptID {
		return model.AttemptEnvironmentBinding{}, apperror.New("exam.attempt.unavailable").Wrap(errors.New("inconsistent execution environment binding end"))
	}

	return result.Binding, nil
}

// EnvironmentWorkspaceBatchCommand is a batch an execution environment
// harvested from its guest, written on the candidate's behalf.
type EnvironmentWorkspaceBatchCommand struct {
	Binding                 EnvironmentBinding
	Mutations               []WorkspaceBatchMutation
	ExpectedWorkspaceCursor *int64
	Content                 WorkspaceBatchContent
	IdempotencyKey          string
}

// ApplyEnvironmentWorkspaceBatch writes harvested changes into the Attempt
// Workspace on the candidate's behalf. It is the editor's batch with another
// writer: the same validation, budget, staging, all-or-nothing apply and
// realtime hint. The Store proves the writer by its live binding instead of
// Session, key, Connection and credential, and records origin environment.
// The outcome is keyed under the candidate, and audit names the candidate as
// actor with the origin and binding.
func (s *Service) ApplyEnvironmentWorkspaceBatch(
	ctx context.Context,
	system invocation.System,
	command EnvironmentWorkspaceBatchCommand,
) (_ WorkspaceBatchResult, resultErr error) {
	defer func() { resultErr = publicFailures.Conceal(resultErr) }()
	if err := validEnvironmentCaller(system); err != nil {
		return WorkspaceBatchResult{}, err
	}
	environmentAccess, err := command.Binding.access()
	if err != nil {
		return WorkspaceBatchResult{}, err
	}

	return s.applyWorkspaceWrite(ctx, workspaceWriter{
		access: environmentAccess,
		beginAudit: func(ctx context.Context, target *store.ExamAttemptWorkspaceMutationTarget, value map[string]any) (model.AuditEventID, error) {
			// The audit leads back to the system work that wrote.
			if system.Task != "" {
				value["system_task"] = system.Task
			} else {
				value["job_id"], value["job_attempt_id"] = system.JobID.String(), system.AttemptID.String()
			}
			return s.systemAuditor.BeginOnBehalf(
				ctx,
				target.CandidateUserID,
				model.ActionExamSittingParticipate,
				model.Resource{Type: model.ResourceExamSitting, ID: target.SittingID.String()},
				model.RoleScopeClass,
				target.ClassID.String(),
				store.ExamAttemptWorkspaceBatchOperation,
				value,
			)
		},
	}, workspaceWrite{
		Mutations:               command.Mutations,
		ExpectedWorkspaceCursor: command.ExpectedWorkspaceCursor,
		Content:                 command.Content,
		IdempotencyKey:          command.IdempotencyKey,
	})
}

// EnvironmentWorkspaceQuery selects one manifest page for an environment
// projecting the Attempt Workspace into its guest. ExpectedCursor -1 starts
// at the current cursor.
type EnvironmentWorkspaceQuery struct {
	Binding        EnvironmentBinding
	ExpectedCursor int64
	AfterEntryID   model.AttemptWorkspaceEntryID
	Limit          int
}

// ListEnvironmentWorkspace returns one page of the Attempt Workspace
// manifest to the bound environment.
func (s *Service) ListEnvironmentWorkspace(
	ctx context.Context,
	system invocation.System,
	query EnvironmentWorkspaceQuery,
) (_ WorkspacePage, resultErr error) {
	defer func() { resultErr = publicFailures.Conceal(resultErr) }()
	if err := validEnvironmentCaller(system); err != nil {
		return WorkspacePage{}, err
	}
	environmentAccess, err := query.Binding.access()
	if err != nil {
		return WorkspacePage{}, err
	}
	return s.listWorkspace(ctx, environmentAccess, query.ExpectedCursor, query.AfterEntryID, query.Limit)
}

// EnvironmentWorkspaceJournalQuery selects journal entries after a cursor
// for the bound environment.
type EnvironmentWorkspaceJournalQuery struct {
	Binding     EnvironmentBinding
	AfterCursor int64
	Limit       int
}

// ListEnvironmentWorkspaceJournal returns the journal entries after
// AfterCursor to the bound environment, each with its origin, so the
// environment can tell the editor's changes from its own.
func (s *Service) ListEnvironmentWorkspaceJournal(
	ctx context.Context,
	system invocation.System,
	query EnvironmentWorkspaceJournalQuery,
) (_ WorkspaceJournalPage, resultErr error) {
	defer func() { resultErr = publicFailures.Conceal(resultErr) }()
	if err := validEnvironmentCaller(system); err != nil {
		return WorkspaceJournalPage{}, err
	}
	environmentAccess, err := query.Binding.access()
	if err != nil {
		return WorkspaceJournalPage{}, err
	}
	return s.listWorkspaceJournal(ctx, environmentAccess, query.AfterCursor, query.Limit)
}

// EnvironmentWorkspaceFileQuery names one Workspace file for the bound
// environment.
type EnvironmentWorkspaceFileQuery struct {
	Binding EnvironmentBinding
	EntryID model.AttemptWorkspaceEntryID
}

// OpenEnvironmentWorkspaceFile opens the current content of one Workspace
// file for the bound environment.
func (s *Service) OpenEnvironmentWorkspaceFile(
	ctx context.Context,
	system invocation.System,
	query EnvironmentWorkspaceFileQuery,
) (_ OpenedContent, resultErr error) {
	defer func() { resultErr = publicFailures.Conceal(resultErr) }()
	if err := validEnvironmentCaller(system); err != nil {
		return OpenedContent{}, err
	}
	environmentAccess, err := query.Binding.access()
	if err != nil {
		return OpenedContent{}, err
	}
	return s.openWorkspaceFile(ctx, environmentAccess, query.EntryID)
}
