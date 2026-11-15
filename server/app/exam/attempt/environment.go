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

// The use cases in this file serve the owner of an Attempt's execution
// environment, which calls them as an invocation.System naming its runtime
// task; there is no principal and no HTTP request. The owner acts only under
// its current runtime lease and environment incarnation, until the Attempt
// has a Submission, and what it writes is the candidate's own work: journal
// author environment, outcome and audit under the candidate.

func validEnvironmentCaller(system invocation.System) error {
	if !system.Valid() {
		return apperror.Invalid("exam.attempt.invalid", "system")
	}
	return nil
}

// EnvironmentOwner names the owner an environment acts through: the
// runtime lease fence it holds and the environment incarnation it acts for.
// The Store requires both to be current. Tag, when set, names the started
// process whose changes a batch carries: the journal's author is then the
// incarnation and that tag.
type EnvironmentOwner struct {
	AttemptID   model.ExamAttemptID
	Incarnation int64
	Fence       int64
	Tag         string
}

func (o EnvironmentOwner) access() (store.ExamAttemptWorkspaceEnvironmentAccess, error) {
	access := store.ExamAttemptWorkspaceEnvironmentAccess{AttemptID: o.AttemptID, Incarnation: o.Incarnation, Fence: o.Fence,
		Tag: o.Tag}
	if !access.Valid() {
		return store.ExamAttemptWorkspaceEnvironmentAccess{}, apperror.Invalid("exam.attempt.invalid", "environment_owner")
	}
	return access, nil
}

// EnvironmentWorkspaceBatchCommand is a batch an execution environment
// harvested from its guest, written on the candidate's behalf.
type EnvironmentWorkspaceBatchCommand struct {
	Owner                   EnvironmentOwner
	Mutations               []WorkspaceBatchMutation
	ExpectedWorkspaceCursor *int64
	Content                 WorkspaceBatchContent
	IdempotencyKey          string
}

// ApplyEnvironmentWorkspaceBatch writes harvested changes into the Attempt
// Workspace on the candidate's behalf. It is the editor's batch with another
// writer: the same validation, budget, staging, all-or-nothing apply and
// realtime hint. The Store proves the writer by its owner's lease and
// incarnation instead of Session, key, Connection and credential, and
// records author environment. The outcome is keyed under the candidate, and
// audit names the candidate as actor with the author and lease fence.
func (s *Service) ApplyEnvironmentWorkspaceBatch(
	ctx context.Context,
	system invocation.System,
	command EnvironmentWorkspaceBatchCommand,
) (_ WorkspaceBatchResult, resultErr error) {
	defer func() { resultErr = publicFailures.Conceal(resultErr) }()
	if err := validEnvironmentCaller(system); err != nil {
		return WorkspaceBatchResult{}, err
	}
	environmentAccess, err := command.Owner.access()
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
	Owner          EnvironmentOwner
	ExpectedCursor int64
	AfterEntryID   model.AttemptWorkspaceEntryID
	Limit          int
}

// ListEnvironmentWorkspace returns one page of the Attempt Workspace
// manifest to the environment's owner.
func (s *Service) ListEnvironmentWorkspace(
	ctx context.Context,
	system invocation.System,
	query EnvironmentWorkspaceQuery,
) (_ WorkspacePage, resultErr error) {
	defer func() { resultErr = publicFailures.Conceal(resultErr) }()
	if err := validEnvironmentCaller(system); err != nil {
		return WorkspacePage{}, err
	}
	environmentAccess, err := query.Owner.access()
	if err != nil {
		return WorkspacePage{}, err
	}
	return s.listWorkspace(ctx, environmentAccess, query.ExpectedCursor, query.AfterEntryID, query.Limit)
}

// EnvironmentWorkspaceJournalQuery selects journal entries after a cursor
// for the environment's owner.
type EnvironmentWorkspaceJournalQuery struct {
	Owner       EnvironmentOwner
	AfterCursor int64
	Limit       int
}

// ListEnvironmentWorkspaceJournal returns the journal entries after
// AfterCursor to the environment's owner, each with its author, so the
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
	environmentAccess, err := query.Owner.access()
	if err != nil {
		return WorkspaceJournalPage{}, err
	}
	return s.listWorkspaceJournal(ctx, environmentAccess, query.AfterCursor, query.Limit)
}

// EnvironmentWorkspaceFileQuery names one Workspace file for the
// environment's owner.
type EnvironmentWorkspaceFileQuery struct {
	Owner   EnvironmentOwner
	EntryID model.AttemptWorkspaceEntryID
}

// OpenEnvironmentWorkspaceFile opens the current content of one Workspace
// file for the environment's owner.
func (s *Service) OpenEnvironmentWorkspaceFile(
	ctx context.Context,
	system invocation.System,
	query EnvironmentWorkspaceFileQuery,
) (_ OpenedContent, resultErr error) {
	defer func() { resultErr = publicFailures.Conceal(resultErr) }()
	if err := validEnvironmentCaller(system); err != nil {
		return OpenedContent{}, err
	}
	environmentAccess, err := query.Owner.access()
	if err != nil {
		return OpenedContent{}, err
	}
	return s.openWorkspaceFile(ctx, environmentAccess, query.EntryID)
}
