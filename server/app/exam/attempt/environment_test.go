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

// environmentCaller is the environment owner's runtime task.
var environmentCaller = invocation.Task("execution")

func environmentOwnerFixture(f *fixture) EnvironmentOwner {
	return EnvironmentOwner{AttemptID: f.attemptID, Incarnation: 2, Fence: 7}
}

func TestEnvironmentBatchWritesAsTheCandidateThroughTheSameBatch(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	workspaceID := workspaceTargetFixture(f)
	f.workspace.cursor = 4
	f.workspace.proposedVersion = model.NewWorkspaceContentVersion()
	checksum := strings.Repeat("c", 64)
	f.content.attemptContent = &model.AttemptWorkspaceContent{MediaType: "text/plain", SizeBytes: 4, SHA256: checksum}
	owner := environmentOwnerFixture(f)
	// A change a terminal's program made is journaled under its tag.
	owner.Tag = "terminal:1"
	mutations := []WorkspaceBatchMutation{createFile("main.go", checksum, 4)}

	result, err := f.service.ApplyEnvironmentWorkspaceBatch(context.Background(), environmentCaller, EnvironmentWorkspaceBatchCommand{
		Owner: owner, Resources: environmentResourcesFixture, Mutations: mutations, Content: batchContent(f, "main"),
		IdempotencyKey: "harvest-1"})
	require.NoError(t, err)

	want := store.ExamAttemptWorkspaceEnvironmentAccess{AttemptID: owner.AttemptID, Incarnation: owner.Incarnation, Fence: owner.Fence,
		Tag: owner.Tag}
	require.Len(t, f.workspace.reservations, 1)
	require.Equal(t, want, f.workspace.reservations[0].Access, "the Store proves the writer by its owner")
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
	require.Equal(t, f.userID, f.systemAudit().actor, "audit names the candidate as actor")
	require.Equal(t, model.ActionExamSittingParticipate, f.systemAudit().action)
	require.Equal(t, model.Resource{Type: model.ResourceExamSitting, ID: f.sitting.ID.String()}, f.systemAudit().resource)
	require.Equal(t, model.RoleScopeClass, f.systemAudit().scopeType)
	require.Equal(t, f.sitting.ClassID.String(), f.systemAudit().scopeID)
	require.Equal(t, store.ExamAttemptWorkspaceBatchOperation, f.systemAudit().operation)
	require.Equal(t, map[string]any{"exam_attempt_id": f.attemptID.String(), "mutation_count": float64(1), "author_kind": "environment",
		"author_ref":              model.NewEnvironmentWorkspaceAuthor(owner.Incarnation, "terminal:1").Ref,
		"environment_lease_fence": float64(owner.Fence), "system_task": "execution"}, f.systemAudit().values)
	require.Nil(t, f.audit().values, "no Principal-bearing audit is begun for an environment")
	require.Equal(t, []string{"workspace.reserve", "content.next", "content.stage", "workspace.ready",
		"audit-begin:exam.sitting.participate", "workspace.apply", "effects"}, f.order)

	require.Equal(t, workspaceID, result.WorkspaceID)
	require.Equal(t, f.userID, result.CandidateUserID)
	require.Len(t, result.Changes, 1)
	require.Equal(t, model.NewEnvironmentWorkspaceAuthor(owner.Incarnation, "terminal:1"), result.Changes[0].Author)
	require.Equal(t, int64(5), result.Changes[0].Cursor)
	require.Equal(t, workspaceChangedCalls(t, result), f.publisher.Calls(), "the candidate gets the usual refetch hint")
}

func TestEnvironmentBatchRefusesAnIncompleteOwnerBeforeTheStore(t *testing.T) {
	t.Parallel()
	for name, change := range map[string]func(*EnvironmentOwner){
		"no Attempt":           func(o *EnvironmentOwner) { o.AttemptID = "" },
		"no fence":             func(o *EnvironmentOwner) { o.Fence = 0 },
		"negative incarnation": func(o *EnvironmentOwner) { o.Incarnation = -1 },
		"invalid process tag":  func(o *EnvironmentOwner) { o.Tag = "Terminal 1" },
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			f := newFixture(t)
			workspaceTargetFixture(f)
			owner := environmentOwnerFixture(f)
			change(&owner)
			_, err := f.service.ApplyEnvironmentWorkspaceBatch(context.Background(), environmentCaller, EnvironmentWorkspaceBatchCommand{
				Owner: owner, Mutations: []WorkspaceBatchMutation{createDirectory("src")}, IdempotencyKey: "harvest"})
			requireBatchFault(t, err, "exam.attempt.invalid", "environment_owner", "")
			require.Empty(t, f.order, "nothing reached the Store")
		})
	}
}

func TestEnvironmentUseCasesRequireASystemCaller(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	workspaceTargetFixture(f)
	owner := environmentOwnerFixture(f)
	for name, call := range map[string]func() error{
		"batch": func() error {
			_, err := f.service.ApplyEnvironmentWorkspaceBatch(context.Background(), invocation.System{},
				EnvironmentWorkspaceBatchCommand{Owner: owner, Mutations: []WorkspaceBatchMutation{createDirectory("src")}, IdempotencyKey: "k"})
			return err
		},
		"list": func() error {
			_, err := f.service.ListEnvironmentWorkspace(context.Background(), invocation.System{},
				EnvironmentWorkspaceQuery{Owner: owner, ExpectedCursor: -1, Limit: 1})
			return err
		},
		"journal": func() error {
			_, err := f.service.ListEnvironmentWorkspaceJournal(context.Background(), invocation.System{},
				EnvironmentWorkspaceJournalQuery{Owner: owner, Limit: 1})
			return err
		},
		"file": func() error {
			_, err := f.service.OpenEnvironmentWorkspaceFile(context.Background(), invocation.System{},
				EnvironmentWorkspaceFileQuery{Owner: owner, EntryID: model.NewAttemptWorkspaceEntryID()})
			return err
		},
	} {
		requireBatchFault(t, call(), "exam.attempt.invalid", "system", "")
		require.Empty(t, f.order, "%s reached the Store", name)
	}
}

func TestEnvironmentBatchMapsAStaleOwnerToAClosedConnection(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	workspaceTargetFixture(f)
	f.workspace.reserveErr = store.NewErrConflict("attempt_environment", "attempt_environment_owner", nil)
	_, err := f.service.ApplyEnvironmentWorkspaceBatch(context.Background(), environmentCaller, EnvironmentWorkspaceBatchCommand{
		Owner: environmentOwnerFixture(f), Resources: environmentResourcesFixture, Mutations: []WorkspaceBatchMutation{createDirectory("src")},
		IdempotencyKey: "harvest"})
	requireBatchFault(t, err, "exam.attempt.connection_closed", "", "")
	require.Nil(t, f.systemAudit().values, "a refused writer begins no audit")
}

func TestEnvironmentReadsUseTheOwnerAsReadAccess(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	owner := environmentOwnerFixture(f)
	want := store.ExamAttemptWorkspaceEnvironmentAccess{AttemptID: owner.AttemptID, Incarnation: owner.Incarnation, Fence: owner.Fence}
	workspaceID := model.NewExamAttemptWorkspaceID()
	f.workspace.page = &store.CandidateAttemptWorkspacePage{WorkspaceID: workspaceID, Cursor: 3}
	_, err := f.service.ListEnvironmentWorkspace(context.Background(), environmentCaller, EnvironmentWorkspaceQuery{Owner: owner,
		ExpectedCursor: -1, Limit: 50})
	require.NoError(t, err)
	require.Equal(t, want, f.workspace.readAccess)

	f.workspace.readAccess = nil
	f.workspace.journalPage = &store.CandidateWorkspaceJournalPage{WorkspaceID: workspaceID, CurrentCursor: 3}
	_, err = f.service.ListEnvironmentWorkspaceJournal(context.Background(), environmentCaller, EnvironmentWorkspaceJournalQuery{Owner: owner,
		AfterCursor: 1, Limit: 50})
	require.NoError(t, err)
	require.Equal(t, want, f.workspace.readAccess)

	_, err = f.service.OpenEnvironmentWorkspaceFile(context.Background(), environmentCaller, EnvironmentWorkspaceFileQuery{Owner: owner})
	requireBatchFault(t, err, "exam.attempt.invalid", "attempt_workspace_entry_id", "")
}

// environmentResourcesFixture is the profile an environment batch carries.
var environmentResourcesFixture = model.EnvironmentResources{CPUMillis: 1000, MemoryBytes: 512 << 20, DiskBytes: 1 << 30}
