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

// An environment owner calls the use cases in this file as an
// invocation.System with no principal. It acts only under its current lease and incarnation and only until the
// attempt has a submission; what it writes is audited as the candidate's own
// work.

func validEnvironmentCaller(system invocation.System) error {
	if !system.Valid() {
		return apperror.Invalid("exam.attempt.invalid", "system")
	}
	return nil
}

// EnvironmentOwner is the lease fence and incarnation an environment acts
// through; the store requires both to be current. Tag, when set, names the
// started process whose changes a batch carries, and joins the journal author.
type EnvironmentOwner struct {
	AttemptID   model.ExamAttemptID
	Incarnation int64
	Fence       int64
	Tag         model.AttemptEnvironmentProcessTag
}

func (o EnvironmentOwner) access() (store.ExamAttemptWorkspaceEnvironmentAccess, error) {
	access := store.ExamAttemptWorkspaceEnvironmentAccess{AttemptID: o.AttemptID, Incarnation: o.Incarnation, Fence: o.Fence,
		Tag: o.Tag}
	if !access.Valid() {
		return store.ExamAttemptWorkspaceEnvironmentAccess{}, apperror.Invalid("exam.attempt.invalid", "environment_owner")
	}
	return access, nil
}

// EnvironmentWorkspaceBatchCommand is a batch harvested from the guest,
// written on the candidate's behalf.
type EnvironmentWorkspaceBatchCommand struct {
	Owner EnvironmentOwner
	// Resources size the write budget.
	Resources               model.EnvironmentResources
	Mutations               []WorkspaceBatchMutation
	ExpectedWorkspaceCursor *int64
	Content                 WorkspaceBatchContent
	IdempotencyKey          string
}

// ApplyEnvironmentWorkspaceBatch is the editor's batch with another writer:
// the store proves the writer by lease and incarnation instead of session and
// connection, and the audit names the candidate as actor.
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
	if !command.Resources.Valid() {
		return WorkspaceBatchResult{}, apperror.Invalid("exam.attempt.invalid", "environment_resources")
	}

	return s.applyWorkspaceWrite(ctx, workspaceWriter{
		access:    environmentAccess,
		resources: command.Resources,
		beginAudit: func(ctx context.Context, target *store.ExamAttemptWorkspaceMutationTarget, value map[string]any) (model.AuditEventID, error) {
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

// EnvironmentWorkspaceQuery selects one manifest page; ExpectedCursor -1
// starts at the current cursor.
type EnvironmentWorkspaceQuery struct {
	Owner          EnvironmentOwner
	ExpectedCursor int64
	AfterEntryID   model.AttemptWorkspaceEntryID
	Limit          int
}

// ListEnvironmentWorkspace returns one page of the workspace manifest to the
// environment's owner.
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

// EnvironmentWorkspaceJournalQuery selects journal entries after AfterCursor.
type EnvironmentWorkspaceJournalQuery struct {
	Owner       EnvironmentOwner
	AfterCursor int64
	Limit       int
}

// ListEnvironmentWorkspaceJournal includes each entry's author so the
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

// EnvironmentWorkspaceFileQuery names one workspace file.
type EnvironmentWorkspaceFileQuery struct {
	Owner   EnvironmentOwner
	EntryID model.AttemptWorkspaceEntryID
}

// OpenEnvironmentWorkspaceFile opens a file's current content for the
// environment's owner.
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
