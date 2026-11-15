// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package attempt

import (
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/sudosylabs/proctor/server/app/invocation"
	"github.com/sudosylabs/proctor/server/model"
	"github.com/sudosylabs/proctor/server/store"
)

// environmentCaller is the execution plugin's runtime task.
var environmentCaller = invocation.Task("plugin.execenv.harvest")

func environmentBindingFixture(f *fixture) EnvironmentBinding {
	return EnvironmentBinding{AttemptID: f.attemptID, BindingID: model.NewAttemptEnvironmentBindingID(),
		Identity: model.AttemptEnvironmentIdentity{GrantID: "grant-1", Epoch: "epoch-1", Incarnation: 2}, Generation: 3}
}

func TestEnvironmentBatchWritesAsTheCandidateThroughTheSameBatch(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	workspaceID := workspaceTargetFixture(f)
	f.workspace.cursor = 4
	f.workspace.proposedVersion = model.NewWorkspaceContentVersion()
	checksum := strings.Repeat("c", 64)
	f.content.attemptContent = &model.AttemptWorkspaceContent{MediaType: "text/plain", SizeBytes: 4, SHA256: checksum}
	binding := environmentBindingFixture(f)
	mutations := []WorkspaceBatchMutation{createFile("main.go", checksum, 4)}

	result, err := f.service.ApplyEnvironmentWorkspaceBatch(context.Background(), environmentCaller, EnvironmentWorkspaceBatchCommand{
		Binding: binding, Mutations: mutations, Content: batchContent(f, "main"), IdempotencyKey: "harvest-1"})
	require.NoError(t, err)

	want := store.ExamAttemptWorkspaceEnvironmentAccess{AttemptID: binding.AttemptID, BindingID: binding.BindingID,
		Identity: binding.Identity, Generation: binding.Generation}
	require.Len(t, f.workspace.reservations, 1)
	require.Equal(t, want, f.workspace.reservations[0].Access, "the Store proves the writer by its binding")
	require.Len(t, f.workspace.readied, 1)
	require.Equal(t, want, f.workspace.readied[0].Access)
	require.Equal(t, want, f.workspace.batch().Access)

	// The outcome and the audit belong to the candidate the Store resolved.
	wantIdempotency, err := prepareWorkspaceBatchIdempotency(f.userID, model.AttemptWorkspaceAuthorEnvironment, "harvest-1",
		f.attemptID, mutations, nil)
	require.NoError(t, err)
	require.Equal(t, wantIdempotency, f.workspace.idempotency)
	require.Equal(t, f.userID, f.workspace.idempotency.UserID)
	require.Equal(t, store.ExamAttemptWorkspaceBatchOperation, f.workspace.idempotency.Operation)
	require.Equal(t, f.userID, f.systemAudit.actor, "audit names the candidate as actor")
	require.Equal(t, model.ActionExamSittingParticipate, f.systemAudit.action)
	require.Equal(t, model.Resource{Type: model.ResourceExamSitting, ID: f.sitting.ID.String()}, f.systemAudit.resource)
	require.Equal(t, model.RoleScopeClass, f.systemAudit.scopeType)
	require.Equal(t, f.sitting.ClassID.String(), f.systemAudit.scopeID)
	require.Equal(t, store.ExamAttemptWorkspaceBatchOperation, f.systemAudit.operation)
	require.Equal(t, map[string]any{"exam_attempt_id": f.attemptID.String(), "mutation_count": 1, "author_kind": "environment",
		"author_ref":             model.NewEnvironmentWorkspaceAuthor(binding.Identity.Incarnation, "").Ref,
		"environment_binding_id": binding.BindingID.String(), "system_task": "plugin.execenv.harvest"}, f.systemAudit.values)
	require.Nil(t, f.audit.values, "no Principal-bearing audit is begun for an environment")
	require.Equal(t, []string{"workspace.reserve", "content.next", "content.stage", "workspace.ready",
		"system.audit.on_behalf", "workspace.apply", "effects"}, f.order)

	require.Equal(t, workspaceID, result.WorkspaceID)
	require.Equal(t, f.userID, result.CandidateUserID)
	require.Len(t, result.Changes, 1)
	require.Equal(t, model.NewEnvironmentWorkspaceAuthor(binding.Identity.Incarnation, ""), result.Changes[0].Author)
	require.Equal(t, int64(5), result.Changes[0].Cursor)
	require.Equal(t, workspaceChangedCalls(t, result), f.publisher.Calls(), "the candidate gets the usual refetch hint")
}

func TestEnvironmentBatchRefusesAnIncompleteBindingBeforeTheStore(t *testing.T) {
	t.Parallel()
	for name, change := range map[string]func(*EnvironmentBinding){
		"no binding":       func(b *EnvironmentBinding) { b.BindingID = "" },
		"no Attempt":       func(b *EnvironmentBinding) { b.AttemptID = "" },
		"no generation":    func(b *EnvironmentBinding) { b.Generation = 0 },
		"invalid grant":    func(b *EnvironmentBinding) { b.Identity.GrantID = "grant/1" },
		"invalid epoch":    func(b *EnvironmentBinding) { b.Identity.Epoch = "" },
		"negative counter": func(b *EnvironmentBinding) { b.Identity.Incarnation = -1 },
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			f := newFixture(t)
			workspaceTargetFixture(f)
			binding := environmentBindingFixture(f)
			change(&binding)
			_, err := f.service.ApplyEnvironmentWorkspaceBatch(context.Background(), environmentCaller, EnvironmentWorkspaceBatchCommand{
				Binding: binding, Mutations: []WorkspaceBatchMutation{createDirectory("src")}, IdempotencyKey: "harvest"})
			requireBatchFault(t, err, "exam.attempt.invalid", "environment_binding", "")
			require.Empty(t, f.order, "nothing reached the Store")
		})
	}
}

func TestEnvironmentUseCasesRequireASystemCaller(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	workspaceTargetFixture(f)
	binding := environmentBindingFixture(f)
	for name, call := range map[string]func() error{
		"bind": func() error {
			_, err := f.service.BindExecutionEnvironment(context.Background(), invocation.System{},
				BindExecutionEnvironmentCommand{AttemptID: f.attemptID, Generation: 1, Identity: binding.Identity})
			return err
		},
		"end": func() error {
			_, err := f.service.EndExecutionEnvironmentBinding(context.Background(), invocation.System{},
				EndExecutionEnvironmentBindingCommand{AttemptID: f.attemptID, BindingID: binding.BindingID})
			return err
		},
		"batch": func() error {
			_, err := f.service.ApplyEnvironmentWorkspaceBatch(context.Background(), invocation.System{},
				EnvironmentWorkspaceBatchCommand{Binding: binding, Mutations: []WorkspaceBatchMutation{createDirectory("src")}, IdempotencyKey: "k"})
			return err
		},
		"list": func() error {
			_, err := f.service.ListEnvironmentWorkspace(context.Background(), invocation.System{},
				EnvironmentWorkspaceQuery{Binding: binding, ExpectedCursor: -1, Limit: 1})
			return err
		},
		"journal": func() error {
			_, err := f.service.ListEnvironmentWorkspaceJournal(context.Background(), invocation.System{},
				EnvironmentWorkspaceJournalQuery{Binding: binding, Limit: 1})
			return err
		},
		"file": func() error {
			_, err := f.service.OpenEnvironmentWorkspaceFile(context.Background(), invocation.System{},
				EnvironmentWorkspaceFileQuery{Binding: binding, EntryID: model.NewAttemptWorkspaceEntryID()})
			return err
		},
	} {
		requireBatchFault(t, call(), "exam.attempt.invalid", "system", "")
		require.Empty(t, f.order, "%s reached the Store", name)
	}
}

func TestEnvironmentBatchMapsAnEndedBindingToAClosedConnection(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	workspaceTargetFixture(f)
	f.workspace.reserveErr = store.NewErrConflict("attempt_environment_binding", "attempt_environment_binding_current", nil)
	_, err := f.service.ApplyEnvironmentWorkspaceBatch(context.Background(), environmentCaller, EnvironmentWorkspaceBatchCommand{
		Binding: environmentBindingFixture(f), Mutations: []WorkspaceBatchMutation{createDirectory("src")}, IdempotencyKey: "harvest"})
	requireBatchFault(t, err, "exam.attempt.connection_closed", "", "")
	require.Nil(t, f.systemAudit.values, "a refused writer begins no audit")
}

func TestBindExecutionEnvironmentProposesABindingAndChecksTheResult(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	command := BindExecutionEnvironmentCommand{AttemptID: f.attemptID, Generation: 2,
		Identity: model.AttemptEnvironmentIdentity{GrantID: "grant-1", Epoch: "epoch-1", Incarnation: 1}}
	bound, err := f.service.BindExecutionEnvironment(context.Background(), environmentCaller, command)
	require.NoError(t, err)
	require.NotNil(t, f.workspace.bind)
	require.True(t, f.workspace.bind.BindingID.IsValid())
	require.Equal(t, f.workspace.bind.BindingID, bound.Binding.ID)
	require.Equal(t, command.AttemptID, f.workspace.bind.AttemptID)
	require.Equal(t, command.Generation, f.workspace.bind.Generation)
	require.Equal(t, command.Identity, f.workspace.bind.Identity)
	require.True(t, bound.Binding.Live())

	// A result for another identity is never trusted.
	f.workspace.bindResult = &store.ExamAttemptEnvironmentBindResult{Binding: bound.Binding}
	f.workspace.bindResult.Binding.Identity.Incarnation = 9
	_, err = f.service.BindExecutionEnvironment(context.Background(), environmentCaller, command)
	requireBatchFault(t, err, "exam.attempt.unavailable", "", "")

	_, err = f.service.BindExecutionEnvironment(context.Background(), environmentCaller, BindExecutionEnvironmentCommand{AttemptID: f.attemptID,
		Generation: 2, Identity: model.AttemptEnvironmentIdentity{GrantID: "", Epoch: "e"}})
	requireBatchFault(t, err, "exam.attempt.invalid", "environment_binding", "")

	f.workspace.bindResult = nil
	f.workspace.bindErr = store.NewErrConflict("attempt_participation", "attempt_participation_generation", nil)
	_, err = f.service.BindExecutionEnvironment(context.Background(), environmentCaller, command)
	requireBatchFault(t, err, "exam.attempt.connection_closed", "", "")
}

func TestEnvironmentReadsUseTheBindingAsReadAccess(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	binding := environmentBindingFixture(f)
	want := store.ExamAttemptWorkspaceEnvironmentAccess{AttemptID: binding.AttemptID, BindingID: binding.BindingID,
		Identity: binding.Identity, Generation: binding.Generation}
	workspaceID := model.NewExamAttemptWorkspaceID()
	f.workspace.page = &store.CandidateAttemptWorkspacePage{WorkspaceID: workspaceID, Cursor: 3}
	_, err := f.service.ListEnvironmentWorkspace(context.Background(), environmentCaller, EnvironmentWorkspaceQuery{Binding: binding,
		ExpectedCursor: -1, Limit: 50})
	require.NoError(t, err)
	require.Equal(t, want, f.workspace.readAccess)

	f.workspace.readAccess = nil
	f.workspace.journalPage = &store.CandidateWorkspaceJournalPage{WorkspaceID: workspaceID, CurrentCursor: 3}
	_, err = f.service.ListEnvironmentWorkspaceJournal(context.Background(), environmentCaller, EnvironmentWorkspaceJournalQuery{Binding: binding,
		AfterCursor: 1, Limit: 50})
	require.NoError(t, err)
	require.Equal(t, want, f.workspace.readAccess)

	_, err = f.service.OpenEnvironmentWorkspaceFile(context.Background(), environmentCaller, EnvironmentWorkspaceFileQuery{Binding: binding})
	requireBatchFault(t, err, "exam.attempt.invalid", "attempt_workspace_entry_id", "")
}
