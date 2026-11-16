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
	"fmt"
	"io"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/sudosylabs/proctor/server/app/apperror"
	"github.com/sudosylabs/proctor/server/model"
	"github.com/sudosylabs/proctor/server/store"
)

// batchContentFake supplies one body per file mutation, in order.
type batchContentFake struct {
	f      *fixture
	bodies []string
	calls  int
	err    error
	empty  bool
}

func (c *batchContentFake) Next(context.Context) (io.Reader, error) {
	c.f.order = append(c.f.order, "content.next")
	c.calls++
	if c.err != nil {
		return nil, c.err
	}
	if c.empty || c.calls > len(c.bodies) {
		return nil, nil
	}
	return strings.NewReader(c.bodies[c.calls-1]), nil
}

func batchContent(f *fixture, bodies ...string) *batchContentFake {
	return &batchContentFake{f: f, bodies: bodies}
}

func createDirectory(path string) WorkspaceBatchMutation {
	return WorkspaceBatchMutation{Operation: model.AttemptWorkspaceMutationCreateDirectory, DestinationPath: path}
}

func createFile(path, checksum string, size int64) WorkspaceBatchMutation {
	return WorkspaceBatchMutation{Operation: model.AttemptWorkspaceMutationCreateFile, DestinationPath: path,
		MediaType: "text/plain", SHA256: checksum, Size: size}
}

func applyBatch(f *fixture, content WorkspaceBatchContent, cursor *int64, mutations ...WorkspaceBatchMutation) (WorkspaceBatchResult, error) {
	return f.service.ApplyCandidateExamWorkspaceBatch(context.Background(), f.inv, ApplyWorkspaceBatchCommand{
		Access: validWorkspaceMutationAccess(f), Mutations: mutations, ExpectedWorkspaceCursor: cursor,
		Content: content, IdempotencyKey: "test-key"})
}

// requireBatchFault asserts the public failure and the batch item it names;
// item is "" when the failure belongs to the whole batch.
func requireBatchFault(t *testing.T, err error, code, field, item string) {
	t.Helper()
	var fault *apperror.Error
	require.ErrorAs(t, err, &fault)
	require.Equal(t, code, fault.Code(), "fields=%v error=%v", fault.Fields(), err)
	fields := fault.Fields()
	if field != "" {
		require.Equal(t, field, fields["field"], "fields=%v", fields)
	}
	itemValue, named := fields["item"]
	if item == "" {
		require.False(t, named, "batch failure named item %q", itemValue)
		return
	}
	require.Equal(t, item, itemValue, "fields=%v", fields)
}

func TestWorkspaceBatchRevalidatesMutationAccessAuditsAndPublishesSafeChange(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	credential := model.NewCredentialToken()
	participationID := model.NewAttemptParticipationID()
	workspaceID := workspaceTargetFixture(f)
	f.workspace.cursor = 8
	f.workspace.proposedEntryID = model.NewAttemptWorkspaceEntryID()
	mutations := []WorkspaceBatchMutation{createDirectory("src")}

	result, err := f.service.ApplyCandidateExamWorkspaceBatch(context.Background(), f.inv, ApplyWorkspaceBatchCommand{
		Access: WorkspaceMutationAccess{CandidateAccess: CandidateAccess{AttemptID: f.attemptID, ConnectionID: f.connectionID,
			ContinuityCredential: credential}, ParticipationID: participationID, Generation: 4},
		Mutations: mutations, IdempotencyKey: "test-key",
	})
	require.NoError(t, err)
	require.Len(t, f.workspace.reservations, 1)
	require.Empty(t, f.workspace.reservations[0].ObjectIDs, "a batch without files only resolves its target")
	principal := f.inv.Principal()
	for _, writer := range []store.ExamAttemptWorkspaceWriteAccess{f.workspace.reservations[0].Access, f.workspace.batch().Access} {
		workspaceAccess, editor := writer.(store.ExamAttemptWorkspaceEditorAccess)
		require.True(t, editor, "the editor writes through editor access")
		require.Equal(t, f.attemptID, workspaceAccess.AttemptID)
		require.Equal(t, participationID, workspaceAccess.ParticipationID)
		require.Equal(t, int64(4), workspaceAccess.Generation, "the write is fenced by Participation generation")
		require.Equal(t, principal.UserID, workspaceAccess.CandidateUserID)
		require.Equal(t, principal.SessionID, workspaceAccess.SessionID)
		require.Equal(t, f.connectionID, workspaceAccess.ConnectionID)
		require.Equal(t, model.HashToken(credential), workspaceAccess.ContinuityCredentialHash)
	}
	batch := f.workspace.batch()
	require.Len(t, batch.Mutations, 1)
	require.Equal(t, store.ExamAttemptWorkspaceMutation{Operation: model.AttemptWorkspaceMutationCreateDirectory,
		EntryID: f.workspace.proposedEntryID, DestinationPath: "src"}, batch.Mutations[0])
	require.Nil(t, batch.ExpectedWorkspaceCursor)
	require.True(t, batch.AuditEventID.IsValid())
	require.Equal(t, f.at, batch.AuditAt)
	require.Empty(t, f.workspace.readied, "a batch without files readies nothing")

	require.Equal(t, f.attemptID, result.AttemptID)
	require.Equal(t, workspaceID, result.WorkspaceID)
	require.False(t, result.Replayed)
	require.Len(t, result.Changes, 1)
	require.Equal(t, int64(9), result.Changes[0].Cursor)
	require.Equal(t, workspaceChangedCalls(t, result), f.publisher.Calls())
	requireEventDataOmits(t, f.publisher.Calls(), "src", f.workspace.proposedEntryID.String())

	require.Equal(t, store.ExamAttemptWorkspaceBatchOperation, f.audit().operation)
	require.Equal(t, model.ActionExamSittingParticipate, f.audit().action)
	require.Equal(t, model.Resource{Type: model.ResourceExamSitting, ID: f.sitting.ID.String()}, f.audit().resource)
	require.Equal(t, model.RoleScopeClass, f.audit().scopeType)
	require.Equal(t, f.sitting.ClassID.String(), f.audit().scopeID)
	require.Equal(t, map[string]any{"exam_attempt_id": f.attemptID.String(), "mutation_count": float64(1), "author_kind": "candidate",
		"author_ref": f.connectionID.String()}, f.audit().values)
	auditCapture := fmt.Sprintf("%#v", f.audit().values)
	require.NotContains(t, auditCapture, credential)
	require.NotContains(t, auditCapture, model.HashToken(credential))
	require.NotContains(t, auditCapture, participationID.String())
	// Contract (guardrails 8 and 14): the audit begins before the durable write,
	// and the write commits before its effect.
	require.Equal(t, []string{"workspace.reserve", "audit-begin:exam.sitting.participate", "workspace.apply", "effects"}, f.order)
	wantIdempotency, prepareErr := prepareWorkspaceBatchIdempotency(f.userID, model.AttemptWorkspaceAuthorCandidate, "test-key", f.attemptID, mutations, nil)
	require.NoError(t, prepareErr)
	require.Equal(t, wantIdempotency, f.workspace.idempotency)
	require.Equal(t, store.ExamAttemptWorkspaceBatchOperation, f.workspace.idempotency.Operation)
}

func TestWorkspaceBatchCreateFileStagesBeforeAtomicApplyAndSuppressesPrivateEffects(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	credential := model.NewCredentialToken()
	workspaceTargetFixture(f)
	f.workspace.proposedVersion = model.NewWorkspaceContentVersion()
	checksum := strings.Repeat("a", 64)
	f.content.attemptContent = &model.AttemptWorkspaceContent{MediaType: "text/plain", SizeBytes: 4, SHA256: checksum}
	mutations := []WorkspaceBatchMutation{createFile("main.go", checksum, 4)}

	result, err := f.service.ApplyCandidateExamWorkspaceBatch(context.Background(), f.inv, ApplyWorkspaceBatchCommand{
		Access: WorkspaceMutationAccess{CandidateAccess: CandidateAccess{AttemptID: f.attemptID, ConnectionID: f.connectionID,
			ContinuityCredential: credential}, ParticipationID: model.NewAttemptParticipationID(), Generation: 2},
		Mutations: mutations, Content: batchContent(f, "main"), IdempotencyKey: "test-key",
	})
	require.NoError(t, err)
	objectID := f.content.attemptObjectID
	require.True(t, objectID.IsValid())
	require.Equal(t, []model.AttemptWorkspaceObjectID{objectID}, f.workspace.reservedObjectIDs())
	require.Len(t, f.workspace.readied, 1)
	require.Equal(t, int64(2), f.workspace.readied[0].Access.(store.ExamAttemptWorkspaceEditorAccess).Generation)
	require.Equal(t, []store.ExamAttemptWorkspaceReadyObject{{ObjectID: objectID, ContentVersion: f.workspace.proposedVersion,
		Content: *f.content.attemptContent}}, f.workspace.readied[0].Objects)
	mutation := f.workspace.batch().Mutations[0]
	require.Equal(t, objectID, mutation.ObjectID)
	require.Equal(t, "main.go", mutation.DestinationPath)
	require.True(t, mutation.EntryID.IsValid(), "the server chooses a created entry's ID")
	require.Equal(t, f.workspace.proposedVersion, result.Changes[0].ContentVersion)
	require.Equal(t, workspaceChangedCalls(t, result), f.publisher.Calls())
	require.Empty(t, f.workspace.reclaimable, "a consumed object is not reclaimed")
	capture := fmt.Sprintf("%#v", f.audit().values)
	require.NotContains(t, capture, "main.go")
	require.NotContains(t, capture, checksum)
	require.NotContains(t, capture, credential)
	require.NotContains(t, capture, model.HashToken(credential))
	// Contract: bytes are staged only inside a reserved object, the audit begins
	// before the durable apply (guardrail 8), and the apply commits before its
	// effect (guardrail 14).
	require.Equal(t, []string{"workspace.reserve", "content.next", "content.stage", "workspace.ready", "audit-begin:exam.sitting.participate", "workspace.apply", "effects"}, f.order)
	want, prepareErr := prepareWorkspaceBatchIdempotency(f.userID, model.AttemptWorkspaceAuthorCandidate, "test-key", f.attemptID, mutations, nil)
	require.NoError(t, prepareErr)
	require.Equal(t, want, f.workspace.idempotency)
}

func TestWorkspaceBatchReplaceFilePassesOwnedIdempotencyToStore(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	workspaceTargetFixture(f)
	entryID, currentVersion := model.NewAttemptWorkspaceEntryID(), model.NewWorkspaceContentVersion()
	checksum := strings.Repeat("c", 64)
	f.content.attemptContent = &model.AttemptWorkspaceContent{MediaType: "text/plain", SizeBytes: 4, SHA256: checksum}
	mutations := []WorkspaceBatchMutation{{Operation: model.AttemptWorkspaceMutationReplaceFile, EntryID: entryID,
		ExpectedPath: "main.go", ExpectedContentVersion: currentVersion, MediaType: "text/plain", SHA256: checksum, Size: 4}}
	_, err := applyBatch(f, batchContent(f, "main"), nil, mutations...)
	require.NoError(t, err)
	mutation := f.workspace.batch().Mutations[0]
	require.Equal(t, entryID, mutation.EntryID, "a replace keeps the candidate's entry")
	require.Equal(t, "main.go", mutation.ExpectedPath)
	require.Equal(t, currentVersion, mutation.ExpectedContentVersion)
	require.Equal(t, f.content.attemptObjectID, mutation.ObjectID)
	want, err := prepareWorkspaceBatchIdempotency(f.userID, model.AttemptWorkspaceAuthorCandidate, "test-key", f.attemptID, mutations, nil)
	require.NoError(t, err)
	require.Equal(t, want, f.workspace.idempotency)
}

func TestWorkspaceBatchReplayReclaimsStagedObjectsAndPublishesNoEffect(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	workspaceTargetFixture(f)
	f.workspace.replayed = true
	f.workspace.proposedEntryID = model.NewAttemptWorkspaceEntryID()
	f.workspace.proposedVersion = model.NewWorkspaceContentVersion()
	checksum := strings.Repeat("b", 64)
	f.content.attemptContent = &model.AttemptWorkspaceContent{MediaType: "text/plain", SizeBytes: 1, SHA256: checksum}
	result, err := applyBatch(f, batchContent(f, "x", "y"), nil,
		createFile("retry.txt", checksum, 1), createDirectory("src"), createFile("src/again.txt", checksum, 1))
	require.NoError(t, err, "reclaimable=%v effects=%#v", f.workspace.reclaimable, f.publisher.Calls())
	require.True(t, result.Replayed)
	require.Len(t, f.workspace.reservedObjectIDs(), 2)
	require.ElementsMatch(t, f.workspace.reservedObjectIDs(), f.workspace.reclaimable, "every losing stage is reclaimed")
	require.Equal(t, 1, f.workspace.reclaimCalls, "the losing stages are reclaimed together")
	require.Empty(t, f.publisher.Calls(), "a replay publishes nothing")
	for index, change := range result.Changes {
		require.Equal(t, f.workspace.proposedEntryID, f.workspace.batch().Mutations[index].EntryID)
		require.NotEqual(t, f.workspace.proposedEntryID, change.EntryID, "the replay keeps the retained entry")
	}
}

func TestWorkspaceBatchDirectoryReplayAcceptsRetainedEntryInsteadOfFreshProposal(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	workspaceTargetFixture(f)
	f.workspace.replayed = true
	f.workspace.proposedEntryID = model.NewAttemptWorkspaceEntryID()
	result, err := applyBatch(f, nil, nil, createDirectory("src"))
	require.NoError(t, err, "result=%#v batch=%#v", result, f.workspace.batch())
	require.Len(t, result.Changes, 1)
	require.NotEqual(t, f.workspace.proposedEntryID, result.Changes[0].EntryID)
	require.Equal(t, f.workspace.proposedEntryID, f.workspace.batch().Mutations[0].EntryID)
	require.Empty(t, f.publisher.Calls())
	require.Zero(t, f.workspace.reclaimCalls, "a batch without files has nothing to reclaim")
}

func TestWorkspaceBatchReclamationDistinguishesStableAndUnknownApplyOutcomes(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name        string
		err         error
		wantCode    string
		wantItem    string
		wantReclaim bool
	}{
		{name: "stable conflict", err: store.NewErrConflict("attempt_workspace", "attempt_workspace_path", errors.New("collision")),
			wantCode: "exam.attempt.workspace.path_conflict", wantReclaim: true},
		{name: "stable conflict of one item", err: &store.ErrAttemptWorkspaceBatchItem{Index: 1,
			Err: store.NewErrConflict("attempt_workspace", "attempt_workspace_path", errors.New("collision"))},
			wantCode: "exam.attempt.workspace.path_conflict", wantItem: "1", wantReclaim: true},
		{name: "outcome unknown", err: errors.New("database acknowledgement lost"),
			wantCode: "exam.attempt.unavailable"},
		{name: "outcome unknown for one item", err: &store.ErrAttemptWorkspaceBatchItem{Index: 1, Err: errors.New("database acknowledgement lost")},
			wantCode: "exam.attempt.unavailable", wantItem: "1"},
		{name: "invalid state", err: store.ErrInvalidState, wantCode: "exam.attempt.unavailable"},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			f := newFixture(t)
			workspaceTargetFixture(f)
			f.workspace.batchErr = test.err
			checksum := strings.Repeat("d", 64)
			f.content.attemptContent = &model.AttemptWorkspaceContent{MediaType: "text/plain", SizeBytes: 1, SHA256: checksum}
			_, err := applyBatch(f, batchContent(f, "x", "y"), nil, createFile("work.txt", checksum, 1), createFile("more.txt", checksum, 1))
			requireBatchFault(t, err, test.wantCode, "", test.wantItem)
			require.Equal(t, test.wantCode, f.audit().failedCode, "the begun audit records the failure")
			require.Empty(t, f.publisher.Calls())
			if test.wantReclaim {
				require.ElementsMatch(t, f.workspace.reservedObjectIDs(), f.workspace.reclaimable)
				return
			}
			require.Empty(t, f.workspace.reclaimable, "an unknown outcome keeps objects the Store may have consumed")
		})
	}
}

func TestWorkspaceBatchMetadataMutationsCarrySelectiveEntryFences(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name  string
		build func(model.AttemptWorkspaceEntryID, model.WorkspaceContentVersion) WorkspaceBatchMutation
		check func(*testing.T, store.ExamAttemptWorkspaceMutation)
	}{
		{name: "move", build: func(id model.AttemptWorkspaceEntryID, _ model.WorkspaceContentVersion) WorkspaceBatchMutation {
			return WorkspaceBatchMutation{Operation: model.AttemptWorkspaceMutationMoveEntry, EntryID: id, ExpectedPath: "old", DestinationPath: "new"}
		}, check: func(t *testing.T, mutation store.ExamAttemptWorkspaceMutation) {
			require.Equal(t, "old", mutation.ExpectedPath)
			require.Equal(t, "new", mutation.DestinationPath)
		}},
		{name: "delete_file", build: func(id model.AttemptWorkspaceEntryID, version model.WorkspaceContentVersion) WorkspaceBatchMutation {
			return WorkspaceBatchMutation{Operation: model.AttemptWorkspaceMutationDeleteEntry, EntryID: id, ExpectedPath: "old", ExpectedContentVersion: version}
		}, check: func(t *testing.T, mutation store.ExamAttemptWorkspaceMutation) {
			require.Equal(t, "old", mutation.ExpectedPath)
			require.False(t, mutation.ExpectedContentVersion.IsZero())
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			f := newFixture(t)
			workspaceTargetFixture(f)
			entryID, version := model.NewAttemptWorkspaceEntryID(), model.NewWorkspaceContentVersion()
			mutation := test.build(entryID, version)
			_, err := applyBatch(f, nil, nil, mutation)
			require.NoError(t, err)
			stored := f.workspace.batch().Mutations[0]
			require.Equal(t, entryID, stored.EntryID)
			require.True(t, stored.ObjectID.IsZero())
			test.check(t, stored)
			want, prepareErr := prepareWorkspaceBatchIdempotency(f.userID, model.AttemptWorkspaceAuthorCandidate, "test-key", f.attemptID, []WorkspaceBatchMutation{mutation}, nil)
			require.NoError(t, prepareErr)
			require.Equal(t, want, f.workspace.idempotency)
		})
	}
}

func TestWorkspaceBatchStreamsEveryFileInOrderThroughOneReservation(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	workspaceTargetFixture(f)
	f.workspace.cursor = 20
	checksum := strings.Repeat("e", 64)
	f.content.attemptContent = &model.AttemptWorkspaceContent{MediaType: "text/plain", SizeBytes: 3, SHA256: checksum}
	existingID, existingVersion := model.NewAttemptWorkspaceEntryID(), model.NewWorkspaceContentVersion()
	movedID := model.NewAttemptWorkspaceEntryID()
	content := batchContent(f, "one", "two", "six")
	result, err := applyBatch(f, content, nil,
		createDirectory("src"),
		createFile("src/a.txt", checksum, 3),
		WorkspaceBatchMutation{Operation: model.AttemptWorkspaceMutationReplaceFile, EntryID: existingID, ExpectedPath: "notes.txt",
			ExpectedContentVersion: existingVersion, MediaType: "text/plain", SHA256: checksum, Size: 3},
		createFile("src/b.txt", checksum, 3),
		WorkspaceBatchMutation{Operation: model.AttemptWorkspaceMutationMoveEntry, EntryID: movedID, ExpectedPath: "docs", DestinationPath: "src/docs"},
	)
	require.NoError(t, err)
	require.Equal(t, 3, content.calls, "one body per file mutation")
	require.Equal(t, []string{
		"workspace.reserve",
		"content.next", "content.stage",
		"content.next", "content.stage",
		"content.next", "content.stage",
		"workspace.ready", "audit-begin:exam.sitting.participate", "workspace.apply", "effects",
	}, f.order, "every body is staged in batch order between one reservation and one readiness")

	require.Len(t, f.workspace.reservations, 1, "every object is reserved in one call")
	reserved := f.workspace.reservations[0].ObjectIDs
	require.Len(t, reserved, 3)
	require.Len(t, f.workspace.readied, 1, "every object is readied in one call")
	ready := f.workspace.readied[0].Objects
	require.Len(t, ready, 3)
	batch := f.workspace.batch()
	require.Len(t, batch.Mutations, 5)
	for position, index := range []int{1, 2, 3} {
		require.True(t, reserved[position].IsValid())
		require.Equal(t, reserved[position], ready[position].ObjectID, "ready objects follow the reservation order")
		require.Equal(t, reserved[position], batch.Mutations[index].ObjectID, "mutation %d consumes its own object", index)
		require.Equal(t, *f.content.attemptContent, ready[position].Content)
		require.True(t, ready[position].ContentVersion.IsValid())
	}
	require.NotEqual(t, reserved[0], reserved[1])
	require.NotEqual(t, reserved[1], reserved[2])
	require.Equal(t, f.content.attemptObjectID, reserved[2], "the last body staged into the last object")
	for _, index := range []int{0, 4} {
		require.True(t, batch.Mutations[index].ObjectID.IsZero(), "mutation %d carries no body", index)
	}

	// Creates get server-chosen IDs; other mutations keep the candidate's.
	require.True(t, batch.Mutations[0].EntryID.IsValid())
	require.True(t, batch.Mutations[3].EntryID.IsValid())
	require.NotEqual(t, batch.Mutations[0].EntryID, batch.Mutations[3].EntryID)
	require.Equal(t, existingID, batch.Mutations[2].EntryID)
	require.Equal(t, movedID, batch.Mutations[4].EntryID)

	require.Len(t, result.Changes, 5)
	require.Equal(t, int64(21), result.Changes[0].Cursor)
	require.Equal(t, int64(25), result.Changes[4].Cursor)
	calls := f.publisher.Calls()
	require.Equal(t, workspaceChangedCalls(t, result), calls, "one event per batch")
	require.Len(t, calls, 1)
	require.Contains(t, string(calls[0].Event.Data), `"first_workspace_cursor":21`)
	require.Contains(t, string(calls[0].Event.Data), `"workspace_cursor":25`)
	requireEventDataOmits(t, calls, "src", "notes", "docs", checksum)
	require.EqualValues(t, 5, f.audit().values["mutation_count"])
}

func TestWorkspaceBatchWithoutFilesNeedsNoContentAndMarksNothingReady(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	workspaceTargetFixture(f)
	result, err := applyBatch(f, nil, nil,
		createDirectory("src"),
		WorkspaceBatchMutation{Operation: model.AttemptWorkspaceMutationMoveEntry, EntryID: model.NewAttemptWorkspaceEntryID(),
			ExpectedPath: "old", DestinationPath: "src/new"},
		WorkspaceBatchMutation{Operation: model.AttemptWorkspaceMutationDeleteEntry, EntryID: model.NewAttemptWorkspaceEntryID(),
			ExpectedPath: "gone"},
	)
	require.NoError(t, err)
	require.Len(t, result.Changes, 3)
	require.Len(t, f.workspace.reservations, 1)
	require.Empty(t, f.workspace.reservations[0].ObjectIDs)
	require.Empty(t, f.workspace.readied, "MarkObjectsReady is never called without files")
	require.Zero(t, f.workspace.reclaimCalls)
	require.Equal(t, []string{"workspace.reserve", "audit-begin:exam.sitting.participate", "workspace.apply", "effects"}, f.order)
}

func TestWorkspaceBatchDigestMismatchNamesItemAndReclaimsEveryReservedObject(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	workspaceTargetFixture(f)
	staged := strings.Repeat("a", 64)
	f.content.attemptContent = &model.AttemptWorkspaceContent{MediaType: "text/plain", SizeBytes: 1, SHA256: staged}
	_, err := applyBatch(f, batchContent(f, "x", "y", "z"), nil,
		createDirectory("src"),
		createFile("src/a.txt", staged, 1),
		createFile("src/b.txt", strings.Repeat("b", 64), 1),
		createFile("src/c.txt", staged, 1),
	)
	requireBatchFault(t, err, "exam.attempt.invalid", "workspace_sha256", "2")
	require.Len(t, f.workspace.reservedObjectIDs(), 3)
	require.ElementsMatch(t, f.workspace.reservedObjectIDs(), f.workspace.reclaimable, "every reserved object is reclaimed")
	require.Equal(t, 1, f.workspace.reclaimCalls)
	require.Empty(t, f.workspace.readied)
	require.Empty(t, f.workspace.batches)
	require.Empty(t, f.publisher.Calls())
	require.Equal(t, []string{"workspace.reserve", "content.next", "content.stage", "content.next", "content.stage", "workspace.reclaim"}, f.order,
		"staging stops at the first mismatch and no audit begins")
}

func TestWorkspaceBatchContentFailureNamesItemAndReclaims(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name    string
		content *batchContentFake
	}{
		{name: "next fails", content: &batchContentFake{err: errors.New("part missing")}},
		{name: "no body", content: &batchContentFake{empty: true}},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			f := newFixture(t)
			workspaceTargetFixture(f)
			test.content.f = f
			checksum := strings.Repeat("a", 64)
			f.content.attemptContent = &model.AttemptWorkspaceContent{MediaType: "text/plain", SizeBytes: 1, SHA256: checksum}
			_, err := applyBatch(f, test.content, nil, createDirectory("src"), createFile("src/a.txt", checksum, 1))
			requireBatchFault(t, err, "exam.attempt.invalid", "workspace_content", "1")
			require.ElementsMatch(t, f.workspace.reservedObjectIDs(), f.workspace.reclaimable)
			require.Empty(t, f.workspace.batches)
		})
	}
}

func TestWorkspaceBatchStoreFailureNamesTheRefusingMutation(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name     string
		err      error
		wantCode string
		wantItem string
	}{
		{name: "entry conflict at item 3", err: &store.ErrAttemptWorkspaceBatchItem{Index: 3,
			Err: store.NewErrConflict("attempt_workspace", "attempt_workspace_entry", errors.New("moved"))},
			wantCode: "exam.attempt.workspace.entry_conflict", wantItem: "3"},
		{name: "directory not empty at item 0", err: &store.ErrAttemptWorkspaceBatchItem{Index: 0,
			Err: store.NewErrConflict("attempt_workspace", "attempt_workspace_not_empty", errors.New("children"))},
			wantCode: "exam.attempt.workspace.directory_not_empty", wantItem: "0"},
		{name: "capacity belongs to the batch", err: store.NewErrConflict("attempt_workspace", "attempt_workspace_size_limit", errors.New("full")),
			wantCode: "exam.attempt.workspace.size_limit"},
		{name: "cursor fence belongs to the batch", err: store.NewErrConflict("attempt_workspace", "attempt_workspace_cursor", errors.New("moved on")),
			wantCode: "exam.attempt.workspace.cursor_conflict"},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			f := newFixture(t)
			workspaceTargetFixture(f)
			f.workspace.batchErr = test.err
			_, err := applyBatch(f, nil, nil,
				createDirectory("a"), createDirectory("b"), createDirectory("c"),
				WorkspaceBatchMutation{Operation: model.AttemptWorkspaceMutationMoveEntry, EntryID: model.NewAttemptWorkspaceEntryID(),
					ExpectedPath: "old", DestinationPath: "a/new"})
			requireBatchFault(t, err, test.wantCode, "", test.wantItem)
			require.Equal(t, test.wantCode, f.audit().failedCode)
			require.Empty(t, f.publisher.Calls())
			require.Equal(t, []string{"workspace.reserve", "audit-begin:exam.sitting.participate", "workspace.apply", "audit-fail"}, f.order)
		})
	}
}

func TestWorkspaceBatchReadinessFailureNamesItemAndReclaims(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	workspaceTargetFixture(f)
	checksum := strings.Repeat("a", 64)
	f.content.attemptContent = &model.AttemptWorkspaceContent{MediaType: "text/plain", SizeBytes: 1, SHA256: checksum}
	f.workspace.readyErr = &store.ErrAttemptWorkspaceBatchItem{Index: 1,
		Err: store.NewErrConflict("attempt_workspace", "attempt_workspace_size_limit", errors.New("file too large"))}
	_, err := applyBatch(f, batchContent(f, "x", "y"), nil, createFile("a.txt", checksum, 1), createFile("b.txt", checksum, 1))
	requireBatchFault(t, err, "exam.attempt.workspace.size_limit", "", "1")
	require.ElementsMatch(t, f.workspace.reservedObjectIDs(), f.workspace.reclaimable)
	require.Empty(t, f.workspace.batches)
}

func TestWorkspaceBatchRejectsInvalidCommandsBeforeAnyWrite(t *testing.T) {
	t.Parallel()
	checksum := strings.Repeat("a", 64)
	entryID, version := model.NewAttemptWorkspaceEntryID(), model.NewWorkspaceContentVersion()
	tooMany := make([]WorkspaceBatchMutation, model.AttemptWorkspaceBatchMaximumMutations+1)
	for index := range tooMany {
		tooMany[index] = createDirectory(fmt.Sprintf("d%d", index))
	}
	move := func(destination string) WorkspaceBatchMutation {
		return WorkspaceBatchMutation{Operation: model.AttemptWorkspaceMutationMoveEntry, EntryID: entryID, ExpectedPath: "old", DestinationPath: destination}
	}
	withEntryID := createDirectory("src")
	withEntryID.EntryID = entryID
	fileWithEntryID := createFile("main.go", checksum, 1)
	fileWithEntryID.EntryID = entryID
	directoryWithMedia := createDirectory("src")
	directoryWithMedia.MediaType = "text/plain"
	moveWithDigest := move("new")
	moveWithDigest.SHA256 = checksum
	deleteWithSize := WorkspaceBatchMutation{Operation: model.AttemptWorkspaceMutationDeleteEntry, EntryID: entryID, ExpectedPath: "old", Size: 1}
	replaceWithoutVersion := WorkspaceBatchMutation{Operation: model.AttemptWorkspaceMutationReplaceFile, EntryID: entryID,
		ExpectedPath: "main.go", MediaType: "text/plain", SHA256: checksum, Size: 1}
	replaceWithDestination := WorkspaceBatchMutation{Operation: model.AttemptWorkspaceMutationReplaceFile, EntryID: entryID,
		ExpectedPath: "main.go", DestinationPath: "other.go", ExpectedContentVersion: version, MediaType: "text/plain", SHA256: checksum, Size: 1}
	badDigest := createFile("main.go", strings.ToUpper(checksum), 1)
	noMedia := createFile("main.go", checksum, 1)
	noMedia.MediaType = ""
	tooLarge := createFile("main.go", checksum, model.AttemptWorkspaceMaximumFileBytes+1)

	for _, test := range []struct {
		name      string
		mutations []WorkspaceBatchMutation
		content   bool
		access    func(*WorkspaceMutationAccess)
		field     string
		item      string
	}{
		{name: "no mutations", field: "workspace_batch"},
		{name: "129 mutations", mutations: tooMany, field: "workspace_batch"},
		{name: "create directory with entry id", mutations: []WorkspaceBatchMutation{withEntryID}, field: "workspace_command", item: "0"},
		{name: "create file with entry id", mutations: []WorkspaceBatchMutation{createDirectory("src"), fileWithEntryID}, content: true,
			field: "workspace_command", item: "1"},
		{name: "directory with media type", mutations: []WorkspaceBatchMutation{directoryWithMedia}, field: "workspace_command", item: "0"},
		{name: "move with digest", mutations: []WorkspaceBatchMutation{createDirectory("src"), moveWithDigest}, field: "workspace_command", item: "1"},
		{name: "delete with size", mutations: []WorkspaceBatchMutation{deleteWithSize}, field: "workspace_command", item: "0"},
		{name: "replace without version", mutations: []WorkspaceBatchMutation{replaceWithoutVersion}, content: true, field: "workspace_command", item: "0"},
		{name: "replace with destination", mutations: []WorkspaceBatchMutation{replaceWithDestination}, content: true, field: "workspace_command", item: "0"},
		{name: "unknown operation", mutations: []WorkspaceBatchMutation{createDirectory("src"), {Operation: "copy_entry"}}, field: "workspace_operation", item: "1"},
		{name: "non-canonical path", mutations: []WorkspaceBatchMutation{createDirectory("src/../etc")}, field: "workspace_path", item: "0"},
		{name: "move onto itself", mutations: []WorkspaceBatchMutation{move("old")}, field: "workspace_destination_path", item: "0"},
		{name: "uppercase digest", mutations: []WorkspaceBatchMutation{badDigest}, content: true, field: "workspace_file", item: "0"},
		{name: "missing media type", mutations: []WorkspaceBatchMutation{noMedia}, content: true, field: "workspace_file", item: "0"},
		{name: "file too large", mutations: []WorkspaceBatchMutation{tooLarge}, content: true, field: "workspace_file", item: "0"},
		{name: "file without content", mutations: []WorkspaceBatchMutation{createFile("main.go", checksum, 1)}, field: "workspace_content"},
		{name: "missing participation", mutations: []WorkspaceBatchMutation{createDirectory("src")},
			access: func(a *WorkspaceMutationAccess) { a.ParticipationID = "" }, field: "workspace_access"},
		{name: "generation zero", mutations: []WorkspaceBatchMutation{createDirectory("src")},
			access: func(a *WorkspaceMutationAccess) { a.Generation = 0 }, field: "workspace_access"},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			f := newFixture(t)
			workspaceTargetFixture(f)
			command := ApplyWorkspaceBatchCommand{Access: validWorkspaceMutationAccess(f), Mutations: test.mutations, IdempotencyKey: "test-key"}
			if test.content {
				command.Content = batchContent(f, "x", "y")
			}
			if test.access != nil {
				test.access(&command.Access)
			}
			_, err := f.service.ApplyCandidateExamWorkspaceBatch(context.Background(), f.inv, command)
			requireBatchFault(t, err, "exam.attempt.invalid", test.field, test.item)
			require.Empty(t, f.workspace.reservations)
			require.Empty(t, f.workspace.batches)
			require.Empty(t, f.order)
		})
	}
}
