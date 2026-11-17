// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package storetest

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/sudosylabs/proctor/server/model"
	"github.com/sudosylabs/proctor/server/store"
)

// ExamAttemptWorkspaceSQLProbe exposes only fixture setup which public store
// operations deliberately cannot perform, plus an independently constructed
// adapter for multi-node concurrency characterization.
type ExamAttemptWorkspaceSQLProbe struct {
	MakeObjectCleanupDue         func(*testing.T, context.Context, model.AttemptWorkspaceObjectID)
	MakeCleanupClaimStale        func(*testing.T, context.Context, model.AttemptWorkspaceObjectID)
	FillWorkspaceEntryQuota      func(*testing.T, context.Context, model.ExamAttemptWorkspaceID)
	MakeJournalGap               func(*testing.T, context.Context, model.ExamAttemptWorkspaceID)
	SetParticipationLeaseExpired func(*testing.T, context.Context, model.AttemptParticipationID)
	PassSittingScheduledEnd      func(*testing.T, context.Context, model.ExamSittingID)
	ConcurrentPeer               store.ExamAttemptWorkspaceStore
}

// TestExamAttemptWorkspaceStore verifies the public acknowledged workspace
// contract against a concrete adapter and, when supplied, an independent peer.
func TestExamAttemptWorkspaceStore(t *testing.T, ss store.Store, workspace store.ExamAttemptWorkspaceStore, probes ...ExamAttemptWorkspaceSQLProbe) {
	t.Helper()
	ctx := context.Background()
	admittedCapacity := model.DefaultExamCapacityPolicy()
	admittedCapacity.WorkspaceMaximumEntries = 420
	admittedCapacity.WorkspaceMaximumFileBytes = 8 << 20
	admittedCapacity.WorkspaceMaximumTotalBytes = 5 * admittedCapacity.WorkspaceMaximumFileBytes
	fixture := newExamAttemptFixtureWithCapacity(t, ctx, ss, nil, nil, admittedCapacity)
	// Admission and later workspace growth use the policy frozen into the
	// published revision, not a subsequently lowered institution policy.
	institution, err := ss.Institution().GetSingleton(ctx)
	require.NoError(t, err)
	lowered := model.DefaultExamCapacityPolicy()
	lowered.WorkspaceMaximumEntries = 1
	lowered.WorkspaceMaximumFileBytes = 1
	lowered.WorkspaceMaximumTotalBytes = 1
	institution.ExamCapacity = lowered
	_, err = UpdateInstitution(ctx, ss, institution)
	require.NoError(t, err)
	credentialHash := model.HashToken(model.NewCredentialToken())
	connect := &store.ExamAttemptConnect{SittingID: fixture.sitting.ID, CandidateUserID: fixture.candidate.ID,
		SessionID: fixture.session.ID, DesktopRegistrationID: fixture.session.DesktopRegistrationID,
		DPoPKeyThumbprint: fixture.session.DPoPKeyThumbprint, AttemptID: model.NewExamAttemptID(), WorkspaceID: model.NewExamAttemptWorkspaceID(),
		ParticipationID: model.NewAttemptParticipationID(), ConnectionID: model.NewAttemptConnectionID(),
		ContinuityCredentialHash: credentialHash,
		AuditEventID:             saveExamAttemptAudit(t, ctx, ss, fixture).ID, AuditAt: model.NowUTC()}
	prepareExamAttemptConnect(t, ctx, ss, connect)
	connected, err := ss.ExamAttempt().Connect(ctx, connect,
		examCommand(fixture.candidate.ID, store.ExamAttemptConnectOperation, "workspace-connect", "workspace-connect"))
	require.NoError(t, err)

	access := store.ExamAttemptWorkspaceEditorAccess{AttemptID: connected.Attempt.ID,
		ParticipationID: connected.Participation.ID, Generation: connected.Participation.Generation,
		CandidateUserID: fixture.candidate.ID, SessionID: fixture.session.ID,
		DesktopRegistrationID: fixture.session.DesktopRegistrationID, DPoPKeyThumbprint: fixture.session.DPoPKeyThumbprint,
		ConnectionID: connected.Connection.ID, ContinuityCredentialHash: credentialHash}
	// This scenario outlasts one initial participation lease on a loaded
	// database, so the fixture renews it between phases as a live client would.
	renewal := &store.ExamAttemptParticipationRenewal{DesktopCompatibilityPolicyRevision: 1, AttemptID: connected.Attempt.ID,
		ParticipationID: connected.Participation.ID, ConnectionID: connected.Connection.ID, CandidateUserID: fixture.candidate.ID,
		SessionID: fixture.session.ID, Generation: connected.Participation.Generation, DesktopRegistrationID: fixture.session.DesktopRegistrationID,
		DPoPKeyThumbprint: fixture.session.DPoPKeyThumbprint, ContinuityCredentialHash: credentialHash}
	prepareParticipationSecurityControl(t, ctx, ss, renewal)
	renew := func() {
		t.Helper()
		renewal.ConnectionID = access.ConnectionID
		renewal.Sequence++
		_, renewErr := ss.ExamAttempt().RenewParticipation(ctx, renewal)
		require.NoError(t, renewErr)
	}
	presentation, err := ss.ExamAttempt().GetCandidatePresentation(ctx, store.CandidateAttemptAccess{
		AttemptID: access.AttemptID, CandidateUserID: access.CandidateUserID, SessionID: access.SessionID,
		DesktopRegistrationID: access.DesktopRegistrationID, DPoPKeyThumbprint: access.DPoPKeyThumbprint,
		ConnectionID: access.ConnectionID, ContinuityCredentialHash: access.ContinuityCredentialHash,
	})
	require.NoError(t, err)
	require.Equal(t, admittedCapacity, presentation.Capacity)
	require.NotEqual(t, lowered, presentation.Capacity)
	objectID := model.NewAttemptWorkspaceObjectID()
	reservation := &store.ExamAttemptWorkspaceObjectReservation{Access: access, ObjectIDs: []model.AttemptWorkspaceObjectID{objectID}}
	target, err := workspace.ReserveObjects(ctx, reservation)
	require.NoError(t, err)
	require.NotNil(t, target)
	require.Equal(t, fixture.examID, target.ExamID)
	require.Equal(t, fixture.sitting.ID, target.SittingID)
	require.Equal(t, fixture.class.ID, target.ClassID)
	require.Equal(t, fixture.candidate.ID, target.CandidateUserID)
	require.Equal(t, connected.Workspace.ID, target.WorkspaceID)
	// An exact repeat keeps the same unused reservation.
	repeatedTarget, err := workspace.ReserveObjects(ctx, reservation)
	require.NoError(t, err, "ReserveObjects(exact repeat)")
	require.Equal(t, target, repeatedTarget, "ReserveObjects(exact repeat)")
	// With no objects, a reservation only resolves the target.
	resolvedTarget, err := workspace.ReserveObjects(ctx, &store.ExamAttemptWorkspaceObjectReservation{Access: access})
	require.NoError(t, err, "ReserveObjects(no objects)")
	require.Equal(t, target, resolvedTarget, "ReserveObjects(no objects)")
	duplicateID := model.NewAttemptWorkspaceObjectID()
	_, err = workspace.ReserveObjects(ctx, &store.ExamAttemptWorkspaceObjectReservation{Access: access,
		ObjectIDs: []model.AttemptWorkspaceObjectID{duplicateID, duplicateID}})
	requireInvalidInput(t, err, "ReserveObjects(duplicate object)")
	_, err = workspace.ReserveObjects(ctx, &store.ExamAttemptWorkspaceObjectReservation{Access: access,
		ObjectIDs: newAttemptWorkspaceObjectIDs(model.AttemptWorkspaceBatchMaximumMutations + 1)})
	requireInvalidInput(t, err, "ReserveObjects(more objects than a batch)")
	readyInput := &store.ExamAttemptWorkspaceObjectsReady{Access: access, Objects: []store.ExamAttemptWorkspaceReadyObject{{
		ObjectID: objectID, ContentVersion: model.NewWorkspaceContentVersion(),
		Content: model.AttemptWorkspaceContent{MediaType: "text/x-go", SizeBytes: 12, SHA256: strings.Repeat("a", 64)}}}}
	require.NoError(t, workspace.MarkObjectsReady(ctx, readyInput))
	require.NoError(t, workspace.MarkObjectsReady(ctx, readyInput), "MarkObjectsReady(exact repeat)")
	ready := readyInput.Objects[0]
	// Different content for a ready object conflicts, and refuses the whole
	// call: the object readied beside it keeps no content.
	sibling := stageAttemptWorkspaceReservations(t, ctx, workspace, access, 1)[0]
	siblingReady := store.ExamAttemptWorkspaceReadyObject{ObjectID: sibling, ContentVersion: model.NewWorkspaceContentVersion(),
		Content: model.AttemptWorkspaceContent{MediaType: "text/plain", SizeBytes: 3, SHA256: strings.Repeat("e", 64)}}
	changedReady := ready
	changedReady.Content.SHA256 = strings.Repeat("b", 64)
	err = workspace.MarkObjectsReady(ctx, &store.ExamAttemptWorkspaceObjectsReady{Access: access,
		Objects: []store.ExamAttemptWorkspaceReadyObject{siblingReady, changedReady}})
	requireConflictConstraint(t, err, "attempt_workspace_object_state")
	siblingReady.Content.SHA256 = strings.Repeat("f", 64)
	require.NoError(t, workspace.MarkObjectsReady(ctx, &store.ExamAttemptWorkspaceObjectsReady{Access: access,
		Objects: []store.ExamAttemptWorkspaceReadyObject{siblingReady}}), "MarkObjectsReady(after a refused call)")
	err = workspace.MarkObjectsReady(ctx, &store.ExamAttemptWorkspaceObjectsReady{Access: access})
	requireInvalidInput(t, err, "MarkObjectsReady(no objects)")
	err = workspace.MarkObjectsReady(ctx, &store.ExamAttemptWorkspaceObjectsReady{Access: access,
		Objects: []store.ExamAttemptWorkspaceReadyObject{ready, ready}})
	requireInvalidInput(t, err, "MarkObjectsReady(duplicate object)")
	oversized := stageAttemptWorkspaceReservations(t, ctx, workspace, access, 1)[0]
	err = workspace.MarkObjectsReady(ctx, &store.ExamAttemptWorkspaceObjectsReady{Access: access,
		Objects: []store.ExamAttemptWorkspaceReadyObject{{ObjectID: oversized, ContentVersion: model.NewWorkspaceContentVersion(),
			Content: model.AttemptWorkspaceContent{MediaType: "application/octet-stream",
				SizeBytes: admittedCapacity.WorkspaceMaximumFileBytes + 1, SHA256: strings.Repeat("a", 64)}}}})
	requireConflictConstraint(t, err, "attempt_workspace_size_limit")

	entryID := model.NewAttemptWorkspaceEntryID()
	createFile := store.ExamAttemptWorkspaceMutation{Operation: model.AttemptWorkspaceMutationCreateFile,
		EntryID: entryID, DestinationPath: "notes.go", ObjectID: ready.ObjectID}
	batch := newAttemptWorkspaceBatch(t, ctx, ss, fixture, access, createFile)
	command := attemptWorkspaceBatchCommand(fixture.candidate.ID, "workspace-create-file")
	created, err := workspace.ApplyBatch(ctx, batch, command)
	require.NoError(t, err)
	requireWorkspaceBatchEcho(t, created, target, batch.Mutations, workspaceEcho{model.StarterWorkspaceEntryFile, ready.ContentVersion})
	require.False(t, created.Replayed)
	require.Equal(t, fixture.sitting.ID, created.SittingID)
	require.Equal(t, fixture.class.ID, created.ClassID)
	require.Equal(t, fixture.candidate.ID, created.CandidateUserID)
	require.Equal(t, connected.Workspace.ID, created.WorkspaceID)
	require.Equal(t, int64(1), created.Changes[0].Cursor)
	require.Equal(t, entryID, created.Changes[0].EntryID)
	createdFile, err := workspace.ResolveFile(ctx, journalAccess(access), entryID)
	require.NoError(t, err)
	require.Equal(t, createFile.DestinationPath, createdFile.Entry.Path)
	require.Equal(t, ready.ContentVersion, createdFile.Entry.ContentVersion)
	require.Equal(t, ready.ObjectID, createdFile.AttemptObjectID)
	replayBatch := *batch
	replayBatch.AuditEventID = saveExamAttemptAudit(t, ctx, ss, fixture).ID
	replayedCreate, err := workspace.ApplyBatch(ctx, &replayBatch, command)
	require.NoError(t, err)
	requireWorkspaceBatchEcho(t, replayedCreate, target, replayBatch.Mutations, workspaceEcho{model.StarterWorkspaceEntryFile, ""})
	require.True(t, replayedCreate.Replayed, "ApplyBatch(replay) = %#v, first = %#v", replayedCreate, created)
	require.Equal(t, created.Changes, replayedCreate.Changes, "ApplyBatch(replay) = %#v, first = %#v", replayedCreate, created)
	requireSuccessfulAudit(t, ctx, ss, batch.AuditEventID)
	requireSuccessfulAudit(t, ctx, ss, replayBatch.AuditEventID)
	closed, err := ss.ExamAttempt().CloseConnection(ctx, &store.ExamAttemptConnectionClose{
		ConnectionID: access.ConnectionID, CandidateUserID: fixture.candidate.ID, SessionID: fixture.session.ID,
		Reason: model.AttemptConnectionCloseTransport, AuditEventID: saveExamAttemptAudit(t, ctx, ss, fixture).ID, AuditAt: model.NowUTC(),
	})
	require.NoError(t, err)
	require.True(t, closed.ConsistentWith(access.AttemptID, fixture.sitting.ID, access.ConnectionID), "CloseConnection returned a result inconsistent with its request")
	require.NotNil(t, closed)
	require.True(t, closed.Changed)
	_, err = workspace.List(ctx, store.CandidateWorkspaceListOptions{Access: journalAccess(access), ExpectedCursor: -1, Limit: 200})
	require.True(t, store.IsNotFound(err), "List(after Connection close) error")
	_, err = workspace.ResolveFile(ctx, journalAccess(access), entryID)
	require.True(t, store.IsNotFound(err), "ResolveFile(after Connection close) error")
	_, err = workspace.ListJournal(ctx, store.CandidateWorkspaceJournalOptions{Access: journalAccess(access), Limit: 200})
	require.True(t, store.IsNotFound(err), "ListJournal(after Connection close) error")
	reconnect := &store.ExamAttemptConnect{SittingID: fixture.sitting.ID, CandidateUserID: fixture.candidate.ID,
		SessionID: fixture.session.ID, DesktopRegistrationID: fixture.session.DesktopRegistrationID,
		DPoPKeyThumbprint: fixture.session.DPoPKeyThumbprint, AttemptID: model.NewExamAttemptID(), WorkspaceID: model.NewExamAttemptWorkspaceID(),
		ParticipationID: model.NewAttemptParticipationID(), ConnectionID: model.NewAttemptConnectionID(),
		ContinuityCredentialHash: credentialHash, AuditEventID: saveExamAttemptAudit(t, ctx, ss, fixture).ID, AuditAt: model.NowUTC()}
	prepareExamAttemptConnect(t, ctx, ss, reconnect)
	reconnected, err := ss.ExamAttempt().Connect(ctx, reconnect,
		examCommand(fixture.candidate.ID, store.ExamAttemptConnectOperation, "workspace-reconnect", "workspace-reconnect"))
	require.NoError(t, err)
	require.True(t, reconnected.ConsistentWith(fixture.class.ID, fixture.candidate.ID, fixture.session.ID, fixture.sitting.ID), "Connect returned a result inconsistent with its request")
	require.NotNil(t, reconnected)
	require.Equal(t, connected.Attempt.ID, reconnected.Attempt.ID)
	require.Equal(t, connected.Workspace.ID, reconnected.Workspace.ID)
	require.Equal(t, connected.Participation.ID, reconnected.Participation.ID)
	require.Equal(t, connected.Participation.Generation, reconnected.Participation.Generation)
	require.Equal(t, reconnect.ConnectionID, reconnected.Connection.ID)
	access.ConnectionID = reconnected.Connection.ID
	replayAfterReconnect := *batch
	replayAfterReconnect.Access = access
	replayAfterReconnect.AuditEventID = saveExamAttemptAudit(t, ctx, ss, fixture).ID
	replayedAfterReconnect, err := workspace.ApplyBatch(ctx, &replayAfterReconnect, command)
	require.NoError(t, err)
	requireWorkspaceBatchEcho(t, replayedAfterReconnect, target, replayAfterReconnect.Mutations, workspaceEcho{model.StarterWorkspaceEntryFile, ""})
	require.True(t, replayedAfterReconnect.Replayed)
	require.Equal(t, created.Changes, replayedAfterReconnect.Changes)
	requireSuccessfulAudit(t, ctx, ss, replayAfterReconnect.AuditEventID)
	journal, err := workspace.ListJournal(ctx, store.CandidateWorkspaceJournalOptions{
		Access: store.CandidateAttemptAccess{AttemptID: access.AttemptID, CandidateUserID: access.CandidateUserID,
			SessionID: access.SessionID, DesktopRegistrationID: fixture.session.DesktopRegistrationID,
			DPoPKeyThumbprint: fixture.session.DPoPKeyThumbprint, ConnectionID: access.ConnectionID, ContinuityCredentialHash: access.ContinuityCredentialHash},
		AfterCursor: 0, Limit: model.AttemptWorkspaceJournalReadMaximum})
	require.NoError(t, err)
	require.True(t, journal.ConsistentWith(0), "ListJournal returned a result inconsistent with its request")
	require.NotNil(t, journal)
	require.Equal(t, connected.Workspace.ID, journal.WorkspaceID)
	require.Equal(t, int64(1), journal.CurrentCursor)
	require.False(t, journal.HasMore)
	require.False(t, journal.RefreshRequired)
	require.Len(t, journal.Entries, 1)
	require.Equal(t, int64(1), journal.Entries[0].Cursor)
	require.Equal(t, entryID, journal.Entries[0].EntryID)
	require.Equal(t, createFile.DestinationPath, journal.Entries[0].NewPath)
	require.Equal(t, created.Changes[0], journal.Entries[0])
	manifest, err := workspace.List(ctx, store.CandidateWorkspaceListOptions{Access: store.CandidateAttemptAccess{
		AttemptID: access.AttemptID, CandidateUserID: access.CandidateUserID, SessionID: access.SessionID,
		DesktopRegistrationID: fixture.session.DesktopRegistrationID, DPoPKeyThumbprint: fixture.session.DPoPKeyThumbprint,
		ConnectionID: access.ConnectionID, ContinuityCredentialHash: access.ContinuityCredentialHash}, ExpectedCursor: -1, Limit: 200})
	require.NoError(t, err)
	require.True(t, manifest.Consistent(), "List returned a result inconsistent with its request")
	require.NotNil(t, manifest)
	require.Equal(t, connected.Workspace.ID, manifest.WorkspaceID)
	require.Equal(t, int64(1), manifest.Cursor)
	require.False(t, manifest.RefreshRequired)
	require.Len(t, manifest.Items, 3)
	refresh, err := workspace.List(ctx, store.CandidateWorkspaceListOptions{Access: journalAccess(access), ExpectedCursor: 0,
		AfterEntryID: manifest.Items[0].EntryID, Limit: 200})
	require.NoError(t, err)
	require.NotNil(t, refresh)
	require.True(t, refresh.RefreshRequired)
	require.Empty(t, refresh.Items)
	require.Equal(t, int64(1), refresh.Cursor)

	const concurrentCreates = 12
	raceEntries := make([]model.AttemptWorkspaceEntryID, concurrentCreates)
	raceBatches := make([]*store.ExamAttemptWorkspaceBatch, concurrentCreates)
	raceCommands := make([]*store.CommandIdempotency, concurrentCreates)
	for i := range raceBatches {
		raceEntries[i] = model.NewAttemptWorkspaceEntryID()
		raceBatches[i] = newAttemptWorkspaceBatch(t, ctx, ss, fixture, access, store.ExamAttemptWorkspaceMutation{
			Operation: model.AttemptWorkspaceMutationCreateDirectory, EntryID: raceEntries[i],
			DestinationPath: "race-" + string(rune('a'+i))})
		raceCommands[i] = attemptWorkspaceBatchCommand(fixture.candidate.ID, "workspace-race-"+string(rune('a'+i)))
	}
	writeDone := make(chan error, 1)
	go func() {
		for i := range raceBatches {
			if _, writeErr := workspace.ApplyBatch(ctx, raceBatches[i], raceCommands[i]); writeErr != nil {
				writeDone <- writeErr
				return
			}
		}
		writeDone <- nil
	}()
	for {
		page, listErr := workspace.List(ctx, store.CandidateWorkspaceListOptions{Access: journalAccess(access), ExpectedCursor: -1, Limit: 200})
		require.NoError(t, listErr)
		require.False(t, page.RefreshRequired, "List(concurrent snapshot) cursor=%d items=%d page=%#v", page.Cursor, len(page.Items), page)
		require.False(t, page.HasMore, "List(concurrent snapshot) cursor=%d items=%d page=%#v", page.Cursor, len(page.Items), page)
		require.Len(t, page.Items, int(page.Cursor)+2, "List(concurrent snapshot) cursor=%d items=%d page=%#v", page.Cursor, len(page.Items), page)
		select {
		case writeErr := <-writeDone:
			require.NoError(t, writeErr)
			goto writesComplete
		default:
			time.Sleep(time.Millisecond)
		}
	}

writesComplete:
	journal, err = workspace.ListJournal(ctx, store.CandidateWorkspaceJournalOptions{Access: journalAccess(access), AfterCursor: 0, Limit: 1})
	require.NoError(t, err)
	require.True(t, journal.ConsistentWith(0), "ListJournal returned a result inconsistent with its request")
	require.NotNil(t, journal)
	require.True(t, journal.HasMore)
	require.Len(t, journal.Entries, 1)
	require.Equal(t, int64(concurrentCreates+1), journal.CurrentCursor)
	textContent := func(label string) model.AttemptWorkspaceContent {
		return model.AttemptWorkspaceContent{MediaType: "text/plain", SizeBytes: int64(len(label)), SHA256: strings.Repeat("c", 64)}
	}
	stage := func(contents ...model.AttemptWorkspaceContent) []store.ExamAttemptWorkspaceReadyObject {
		t.Helper()
		return stageAttemptWorkspaceObjects(t, ctx, workspace, access, contents...)
	}
	apply := func(label string, mutations ...store.ExamAttemptWorkspaceMutation) (*store.ExamAttemptWorkspaceBatchResult, error) {
		t.Helper()
		return workspace.ApplyBatch(ctx, newAttemptWorkspaceBatch(t, ctx, ss, fixture, access, mutations...),
			attemptWorkspaceBatchCommand(fixture.candidate.ID, "workspace-"+label))
	}
	state := func() attemptWorkspaceState {
		t.Helper()
		return readAttemptWorkspaceState(t, ctx, workspace, journalAccess(access))
	}

	renew()
	replacement := stage(textContent("replacement"))[0]
	replaceMutation := store.ExamAttemptWorkspaceMutation{Operation: model.AttemptWorkspaceMutationReplaceFile, EntryID: entryID,
		ExpectedPath: createFile.DestinationPath, ExpectedContentVersion: ready.ContentVersion, ObjectID: replacement.ObjectID}
	replaced, err := apply("replace", replaceMutation)
	require.NoError(t, err)
	requireWorkspaceBatchEcho(t, replaced, target, []store.ExamAttemptWorkspaceMutation{replaceMutation},
		workspaceEcho{model.StarterWorkspaceEntryFile, replacement.ContentVersion})
	require.Equal(t, int64(concurrentCreates+2), replaced.Changes[0].Cursor)
	resolved, err := workspace.ResolveFile(ctx, journalAccess(access), entryID)
	require.NoError(t, err)
	require.NotNil(t, resolved)
	require.Equal(t, replacement.ContentVersion, resolved.Entry.ContentVersion)
	require.Equal(t, replacement.ObjectID, resolved.AttemptObjectID)
	moveFile := store.ExamAttemptWorkspaceMutation{Operation: model.AttemptWorkspaceMutationMoveEntry, EntryID: entryID,
		ExpectedPath: createFile.DestinationPath, DestinationPath: "renamed.go"}
	movedFile, err := apply("move-file", moveFile)
	require.NoError(t, err)
	requireWorkspaceBatchEcho(t, movedFile, target, []store.ExamAttemptWorkspaceMutation{moveFile},
		workspaceEcho{model.StarterWorkspaceEntryFile, replacement.ContentVersion})
	resolvedAfterMove, err := workspace.ResolveFile(ctx, journalAccess(access), entryID)
	require.NoError(t, err)
	require.Equal(t, "renamed.go", resolvedAfterMove.Entry.Path, "file move: moved=%#v before=%#v after=%#v", movedFile, resolved, resolvedAfterMove)
	require.Equal(t, resolved.AttemptObjectID, resolvedAfterMove.AttemptObjectID, "file move changed content object: moved=%#v before=%#v after=%#v", movedFile, resolved, resolvedAfterMove)
	require.Equal(t, resolved.ContentVersion, resolvedAfterMove.ContentVersion, "file move changed content object: moved=%#v before=%#v after=%#v", movedFile, resolved, resolvedAfterMove)
	var starterFile store.CandidateAttemptWorkspaceItem
	for _, item := range manifest.Items {
		if item.Kind == model.StarterWorkspaceEntryFile && item.EntryID != entryID {
			starterFile = item
			break
		}
	}
	require.True(t, starterFile.EntryID.IsValid(), "fixture has no Starter-origin file")
	starterResolved, err := workspace.ResolveFile(ctx, journalAccess(access), starterFile.EntryID)
	require.NoError(t, err)
	require.Equal(t, model.AttemptWorkspaceStorageStarter, starterResolved.StorageOrigin)
	cowObject := stage(textContent("copy-on-write"))[0]
	cowMutation := store.ExamAttemptWorkspaceMutation{Operation: model.AttemptWorkspaceMutationReplaceFile, EntryID: starterFile.EntryID,
		ExpectedPath: starterFile.Path, ExpectedContentVersion: starterFile.ContentVersion, ObjectID: cowObject.ObjectID}
	cow, err := apply("replace-starter", cowMutation)
	require.NoError(t, err)
	requireWorkspaceBatchEcho(t, cow, target, []store.ExamAttemptWorkspaceMutation{cowMutation},
		workspaceEcho{model.StarterWorkspaceEntryFile, cowObject.ContentVersion})
	cowResolved, err := workspace.ResolveFile(ctx, journalAccess(access), starterFile.EntryID)
	require.NoError(t, err)
	require.Equal(t, model.AttemptWorkspaceStorageAttempt, cowResolved.StorageOrigin)
	require.Equal(t, cowObject.ObjectID, cowResolved.AttemptObjectID)
	require.Equal(t, starterFile.EntryID, cowResolved.Entry.EntryID)
	originals, err := ss.ExamAttempt().ListCandidateStarterWorkspace(ctx, journalAccess(access), 128)
	require.NoError(t, err)
	require.Contains(t, originals, store.CandidateStarterWorkspaceFile{ObjectID: starterResolved.StarterObjectID}, "candidate replacement keeps original admitted starter context")
	outsider := journalAccess(access)
	outsider.CandidateUserID = model.NewUserID()
	_, err = ss.ExamAttempt().ListCandidateStarterWorkspace(ctx, outsider, 128)
	require.True(t, store.IsNotFound(err), "another candidate cannot read original starter context")

	childObject := stage(textContent("child"))[0]
	childID := model.NewAttemptWorkspaceEntryID()
	child, err := apply("create-child", store.ExamAttemptWorkspaceMutation{Operation: model.AttemptWorkspaceMutationCreateFile,
		EntryID: childID, DestinationPath: "race-a/child.txt", ObjectID: childObject.ObjectID})
	require.NoError(t, err)
	require.Equal(t, "race-a/child.txt", child.Changes[0].NewPath)
	raceARoot := raceEntries[0]
	moved, err := apply("move-tree", store.ExamAttemptWorkspaceMutation{Operation: model.AttemptWorkspaceMutationMoveEntry,
		EntryID: raceARoot, ExpectedPath: "race-a", DestinationPath: "moved"})
	require.NoError(t, err)
	require.Equal(t, "moved", moved.Changes[0].NewPath)
	require.Equal(t, model.StarterWorkspaceEntryDirectory, moved.Changes[0].EntryKind)
	_, err = apply("delete-nonempty", store.ExamAttemptWorkspaceMutation{Operation: model.AttemptWorkspaceMutationDeleteEntry,
		EntryID: raceARoot, ExpectedPath: "moved"})
	requireAttemptWorkspaceBatchItem(t, err, 0, "attempt_workspace_not_empty")
	deletedChild, err := apply("delete-child", store.ExamAttemptWorkspaceMutation{Operation: model.AttemptWorkspaceMutationDeleteEntry,
		EntryID: childID, ExpectedPath: "moved/child.txt", ExpectedContentVersion: childObject.ContentVersion})
	require.NoError(t, err)
	require.Equal(t, "moved/child.txt", deletedChild.Changes[0].OldPath)
	require.Empty(t, deletedChild.Changes[0].NewPath)
	deletedDirectory, err := apply("delete-directory", store.ExamAttemptWorkspaceMutation{Operation: model.AttemptWorkspaceMutationDeleteEntry,
		EntryID: raceARoot, ExpectedPath: "moved"})
	require.NoError(t, err)
	require.Equal(t, model.StarterWorkspaceEntryDirectory, deletedDirectory.Changes[0].EntryKind)
	_, err = apply("path-collision", store.ExamAttemptWorkspaceMutation{Operation: model.AttemptWorkspaceMutationMoveEntry,
		EntryID: raceEntries[1], ExpectedPath: "race-b", DestinationPath: "race-c"})
	requireAttemptWorkspaceBatchItem(t, err, 0, "attempt_workspace_path")
	_, err = apply("missing-parent", store.ExamAttemptWorkspaceMutation{Operation: model.AttemptWorkspaceMutationCreateDirectory,
		EntryID: model.NewAttemptWorkspaceEntryID(), DestinationPath: "missing/child"})
	requireAttemptWorkspaceBatchItem(t, err, 0, "attempt_workspace_path")

	type moveRaceResult struct {
		input   *store.ExamAttemptWorkspaceBatch
		command *store.CommandIdempotency
		result  *store.ExamAttemptWorkspaceBatchResult
		err     error
	}
	moveRace := make(chan moveRaceResult, 2)
	for i, destination := range []string{"race-winner-a", "race-winner-b"} {
		input := newAttemptWorkspaceBatch(t, ctx, ss, fixture, access, store.ExamAttemptWorkspaceMutation{
			Operation: model.AttemptWorkspaceMutationMoveEntry, EntryID: raceEntries[3], ExpectedPath: "race-d", DestinationPath: destination})
		command := attemptWorkspaceBatchCommand(fixture.candidate.ID, "workspace-move-race-"+string(rune('a'+i)))
		go func() {
			result, raceErr := workspace.ApplyBatch(ctx, input, command)
			moveRace <- moveRaceResult{input: input, command: command, result: result, err: raceErr}
		}()
	}
	firstRace, secondRace := <-moveRace, <-moveRace
	var winner moveRaceResult
	if firstRace.err == nil && store.IsConflict(secondRace.err) {
		winner = firstRace
	} else if secondRace.err == nil && store.IsConflict(firstRace.err) {
		winner = secondRace
	} else {
		require.FailNow(t, fmt.Sprintf("ApplyBatch(same-entry race) errors = %v, %v", firstRace.err, secondRace.err))
	}
	replayWinner := *winner.input
	replayWinner.AuditEventID = saveExamAttemptAudit(t, ctx, ss, fixture).ID
	winnerReplay, err := workspace.ApplyBatch(ctx, &replayWinner, winner.command)
	require.NoError(t, err)
	requireWorkspaceBatchEcho(t, winner.result, target, winner.input.Mutations, workspaceEcho{model.StarterWorkspaceEntryDirectory, ""})
	requireWorkspaceBatchEcho(t, winnerReplay, target, replayWinner.Mutations, workspaceEcho{model.StarterWorkspaceEntryDirectory, ""})
	require.True(t, winnerReplay.Replayed, "ApplyBatch(race winner replay) = %#v, winner = %#v", winnerReplay, winner.result)
	require.Equal(t, winner.result.Changes, winnerReplay.Changes, "ApplyBatch(race winner replay) = %#v, winner = %#v", winnerReplay, winner.result)

	// Capacity is a property of the batch's final state, and its refusal
	// names no single mutation.
	largeContent := model.AttemptWorkspaceContent{MediaType: "application/octet-stream",
		SizeBytes: admittedCapacity.WorkspaceMaximumFileBytes, SHA256: strings.Repeat("d", 64)}
	largeObjects := stage(largeContent, largeContent, largeContent, largeContent, largeContent)
	largeEntries := make([]model.AttemptWorkspaceEntryID, len(largeObjects))
	for index, large := range largeObjects {
		largeEntries[index] = model.NewAttemptWorkspaceEntryID()
		_, err = apply("size-quota-"+string(rune('a'+index)), store.ExamAttemptWorkspaceMutation{
			Operation: model.AttemptWorkspaceMutationCreateFile, EntryID: largeEntries[index],
			DestinationPath: "large-" + string(rune('a'+index)), ObjectID: large.ObjectID})
		if index < 4 {
			require.NoError(t, err)
		} else {
			requireAttemptWorkspaceBatchCapacity(t, err, "attempt_workspace_size_limit")
		}
	}
	// Freeing space earlier in the same batch makes room: the refused
	// object is still staged and usable.
	swappedSize, err := apply("size-quota-swap",
		store.ExamAttemptWorkspaceMutation{Operation: model.AttemptWorkspaceMutationDeleteEntry, EntryID: largeEntries[0],
			ExpectedPath: "large-a", ExpectedContentVersion: largeObjects[0].ContentVersion},
		store.ExamAttemptWorkspaceMutation{Operation: model.AttemptWorkspaceMutationCreateFile, EntryID: largeEntries[4],
			DestinationPath: "large-e", ObjectID: largeObjects[4].ObjectID})
	require.NoError(t, err, "ApplyBatch(delete then create at the size limit)")
	require.Len(t, swappedSize.Changes, 2)
	require.Equal(t, swappedSize.Changes[0].Cursor+1, swappedSize.Changes[1].Cursor)
	beforeOverflow := state()
	overflow := stage(largeContent)[0]
	_, err = apply("size-quota-exceed",
		store.ExamAttemptWorkspaceMutation{Operation: model.AttemptWorkspaceMutationCreateDirectory,
			EntryID: model.NewAttemptWorkspaceEntryID(), DestinationPath: "quota"},
		store.ExamAttemptWorkspaceMutation{Operation: model.AttemptWorkspaceMutationCreateFile,
			EntryID: model.NewAttemptWorkspaceEntryID(), DestinationPath: "quota/large-f", ObjectID: overflow.ObjectID})
	requireAttemptWorkspaceBatchCapacity(t, err, "attempt_workspace_size_limit")
	requireAttemptWorkspaceStateUnchanged(t, ctx, workspace, journalAccess(access), beforeOverflow)

	// Releasing objects is bulk and leaves anything still referenced alone:
	// the superseded object is protected by its retained outcome, the
	// replacement is current, and only the unused reservations are released.
	abandoned := stageAttemptWorkspaceReservations(t, ctx, workspace, access, 1)[0]
	require.NoError(t, workspace.MarkObjectsReclaimable(ctx, nil), "MarkObjectsReclaimable(no objects)")
	require.NoError(t, workspace.MarkObjectsReclaimable(ctx, []model.AttemptWorkspaceObjectID{
		ready.ObjectID, replacement.ObjectID, abandoned, sibling, oversized}))
	resolvedAfterRelease, err := workspace.ResolveFile(ctx, journalAccess(access), entryID)
	require.NoError(t, err)
	require.Equal(t, replacement.ObjectID, resolvedAfterRelease.AttemptObjectID, "MarkObjectsReclaimable released a current object")
	claimed, err := workspace.ClaimObjectsForCleanup(ctx, &store.AttemptWorkspaceCleanupClaim{Limit: 10, ClaimToken: "attempt-workspace-cleanup"})
	require.NoError(t, err)
	require.Empty(t, claimed)
	renew()
	if len(probes) > 0 && probes[0].MakeObjectCleanupDue != nil {
		for _, id := range []model.AttemptWorkspaceObjectID{ready.ObjectID, replacement.ObjectID, abandoned, sibling, oversized} {
			probes[0].MakeObjectCleanupDue(t, ctx, id)
		}
		claimed, err = workspace.ClaimObjectsForCleanup(ctx, &store.AttemptWorkspaceCleanupClaim{Limit: 10, ClaimToken: "attempt-workspace-cleanup"})
		require.NoError(t, err)
		claimedIDs := make([]model.AttemptWorkspaceObjectID, len(claimed))
		for index, object := range claimed {
			claimedIDs[index] = object.ID
			require.Equal(t, model.AttemptWorkspaceObjectClaimed, object.State)
			require.Equal(t, "attempt-workspace-cleanup", object.ClaimToken)
		}
		require.ElementsMatch(t, []model.AttemptWorkspaceObjectID{abandoned, sibling, oversized}, claimedIDs)
		if probes[0].MakeCleanupClaimStale != nil {
			probes[0].MakeCleanupClaimStale(t, ctx, abandoned)
			claimed, err = workspace.ClaimObjectsForCleanup(ctx, &store.AttemptWorkspaceCleanupClaim{Limit: 10, ClaimToken: "attempt-workspace-cleanup-stale-retry"})
			require.NoError(t, err)
			require.Len(t, claimed, 1)
			require.Equal(t, abandoned, claimed[0].ID)
			require.Equal(t, "attempt-workspace-cleanup-stale-retry", claimed[0].ClaimToken)
			err = workspace.ReleaseObjectCleanup(ctx, &store.AttemptWorkspaceCleanupRelease{ObjectID: abandoned, ClaimToken: "attempt-workspace-cleanup"})
			require.True(t, store.IsConflict(err), "ReleaseObjectCleanup(old token) error")
			require.NoError(t, workspace.ReleaseObjectCleanup(ctx, &store.AttemptWorkspaceCleanupRelease{ObjectID: abandoned, ClaimToken: "attempt-workspace-cleanup-stale-retry"}))
		} else {
			require.NoError(t, workspace.ReleaseObjectCleanup(ctx, &store.AttemptWorkspaceCleanupRelease{ObjectID: abandoned, ClaimToken: "attempt-workspace-cleanup"}))
		}
		claimed, err = workspace.ClaimObjectsForCleanup(ctx, &store.AttemptWorkspaceCleanupClaim{Limit: 10, ClaimToken: "attempt-workspace-cleanup-retry"})
		require.NoError(t, err)
		require.Len(t, claimed, 1)
		require.Equal(t, abandoned, claimed[0].ID)
		require.NoError(t, workspace.CompleteObjectCleanup(ctx, &store.AttemptWorkspaceCleanupCompletion{ObjectID: abandoned, ClaimToken: "attempt-workspace-cleanup-retry"}))
		require.NoError(t, workspace.CompleteObjectCleanup(ctx, &store.AttemptWorkspaceCleanupCompletion{ObjectID: abandoned, ClaimToken: "attempt-workspace-cleanup-retry"}))
		for _, id := range []model.AttemptWorkspaceObjectID{sibling, oversized} {
			require.NoError(t, workspace.CompleteObjectCleanup(ctx, &store.AttemptWorkspaceCleanupCompletion{ObjectID: id, ClaimToken: "attempt-workspace-cleanup"}))
		}
	}
	renew()
	if len(probes) > 0 && probes[0].FillWorkspaceEntryQuota != nil {
		probes[0].FillWorkspaceEntryQuota(t, ctx, connected.Workspace.ID)
		_, err = apply("entry-quota", store.ExamAttemptWorkspaceMutation{Operation: model.AttemptWorkspaceMutationCreateDirectory,
			EntryID: model.NewAttemptWorkspaceEntryID(), DestinationPath: "entry-overflow"})
		requireAttemptWorkspaceBatchCapacity(t, err, "attempt_workspace_entry_limit")
		swappedEntry, swapErr := apply("entry-quota-swap",
			store.ExamAttemptWorkspaceMutation{Operation: model.AttemptWorkspaceMutationDeleteEntry, EntryID: raceEntries[4], ExpectedPath: "race-e"},
			store.ExamAttemptWorkspaceMutation{Operation: model.AttemptWorkspaceMutationCreateDirectory,
				EntryID: model.NewAttemptWorkspaceEntryID(), DestinationPath: "entry-swap"})
		require.NoError(t, swapErr, "ApplyBatch(delete then create at the entry limit)")
		require.Len(t, swappedEntry.Changes, 2)
		beforeEntryOverflow := state()
		_, err = apply("entry-quota-exceed",
			store.ExamAttemptWorkspaceMutation{Operation: model.AttemptWorkspaceMutationCreateDirectory,
				EntryID: model.NewAttemptWorkspaceEntryID(), DestinationPath: "entry-over-a"},
			store.ExamAttemptWorkspaceMutation{Operation: model.AttemptWorkspaceMutationCreateDirectory,
				EntryID: model.NewAttemptWorkspaceEntryID(), DestinationPath: "entry-over-b"})
		requireAttemptWorkspaceBatchCapacity(t, err, "attempt_workspace_entry_limit")
		requireAttemptWorkspaceStateUnchanged(t, ctx, workspace, journalAccess(access), beforeEntryOverflow)
	}
	if len(probes) > 0 && probes[0].MakeJournalGap != nil {
		probes[0].MakeJournalGap(t, ctx, connected.Workspace.ID)
		journal, err = workspace.ListJournal(ctx, store.CandidateWorkspaceJournalOptions{Access: journalAccess(access), AfterCursor: 0, Limit: 200})
		require.NoError(t, err)
		require.True(t, journal.ConsistentWith(0), "ListJournal returned a result inconsistent with its request")
		require.NotNil(t, journal)
		require.True(t, journal.RefreshRequired)
		require.Empty(t, journal.Entries)
	}

	endedAt := model.NowUTC()
	ended, err := ss.ClassMember().SeedEnd(ctx, &store.ClassMemberSeedEnd{ID: fixture.membership.ID, ExpectedRevision: fixture.membership.Revision, EndAt: endedAt})
	require.NoError(t, err)
	// Membership timestamps have millisecond precision while PostgreSQL decides
	// current eligibility with a higher-precision clock. Cross the millisecond
	// boundary before asserting that the just-ended relationship is inactive.
	time.Sleep(100 * time.Millisecond)
	_, err = workspace.ReserveObjects(ctx, &store.ExamAttemptWorkspaceObjectReservation{Access: access})
	require.True(t, store.IsNotFound(err), "ReserveObjects(after membership revoke) error")
	_, err = apply("after-membership-revoke", store.ExamAttemptWorkspaceMutation{Operation: model.AttemptWorkspaceMutationCreateDirectory,
		EntryID: model.NewAttemptWorkspaceEntryID(), DestinationPath: "after-revoke"})
	require.True(t, store.IsNotFound(err), "ApplyBatch(after membership revoke) error")
	restored, err := ss.ClassMember().SeedEnroll(ctx, &model.ClassMember{ClassID: fixture.class.ID,
		UserID: fixture.candidate.ID, StartsAt: model.TimeUTC(endedAt)})
	require.NoError(t, err)
	target, err = workspace.ReserveObjects(ctx, &store.ExamAttemptWorkspaceObjectReservation{Access: access})
	require.NoError(t, err)
	require.NotNil(t, target, "ReserveObjects(with historical and current membership) = %#v; restored=%#v ended=%#v", target, restored, ended)
	require.Equal(t, connected.Workspace.ID, target.WorkspaceID, "ReserveObjects(with historical and current membership) = %#v; restored=%#v ended=%#v", target, restored, ended)
	require.NotNil(t, restored.Membership, "ReserveObjects(with historical and current membership) = %#v; restored=%#v ended=%#v", target, restored, ended)
	require.True(t, ended.EndsAt.Valid, "ReserveObjects(with historical and current membership) = %#v; restored=%#v ended=%#v", target, restored, ended)
	if len(probes) > 0 && probes[0].SetParticipationLeaseExpired != nil {
		testExamAttemptWorkspaceSuspensionAndMultiNodeReplay(t, ctx, ss, workspace, probes[0])
	}
	testAssistantWorkspaceProof(t, ctx, ss, workspace)
	testAttemptEnvironmentWriter(t, ss, workspace, probes...)
	testAttemptWorkspaceBatches(t, ss, workspace)
	testAttemptWorkspaceRecursiveDeletion(t, ss, workspace, probes...)
}

func testExamAttemptWorkspaceSuspensionAndMultiNodeReplay(t *testing.T, ctx context.Context, ss store.Store,
	workspace store.ExamAttemptWorkspaceStore, probe ExamAttemptWorkspaceSQLProbe,
) {
	t.Helper()
	fixture := newExamAttemptFixture(t, ctx, ss)
	credentialHash := model.HashToken(model.NewCredentialToken())
	connect := &store.ExamAttemptConnect{SittingID: fixture.sitting.ID, CandidateUserID: fixture.candidate.ID,
		SessionID: fixture.session.ID, DesktopRegistrationID: fixture.session.DesktopRegistrationID,
		DPoPKeyThumbprint: fixture.session.DPoPKeyThumbprint, AttemptID: model.NewExamAttemptID(), WorkspaceID: model.NewExamAttemptWorkspaceID(),
		ParticipationID: model.NewAttemptParticipationID(), ConnectionID: model.NewAttemptConnectionID(),
		ContinuityCredentialHash: credentialHash,
		AuditEventID:             saveExamAttemptAudit(t, ctx, ss, fixture).ID, AuditAt: model.NowUTC()}
	prepareExamAttemptConnect(t, ctx, ss, connect)
	connected, err := ss.ExamAttempt().Connect(ctx, connect,
		examCommand(fixture.candidate.ID, store.ExamAttemptConnectOperation, "workspace-fence-connect", "workspace-fence-connect"))
	require.NoError(t, err)
	access := store.ExamAttemptWorkspaceEditorAccess{AttemptID: connected.Attempt.ID,
		ParticipationID: connected.Participation.ID, Generation: connected.Participation.Generation,
		CandidateUserID: fixture.candidate.ID, SessionID: fixture.session.ID,
		DesktopRegistrationID: fixture.session.DesktopRegistrationID, DPoPKeyThumbprint: fixture.session.DPoPKeyThumbprint,
		ConnectionID: connected.Connection.ID, ContinuityCredentialHash: credentialHash}

	if probe.ConcurrentPeer != nil {
		entryID := model.NewAttemptWorkspaceEntryID()
		inputs := [2]*store.ExamAttemptWorkspaceBatch{}
		for index := range inputs {
			inputs[index] = newAttemptWorkspaceBatch(t, ctx, ss, fixture, access, store.ExamAttemptWorkspaceMutation{
				Operation: model.AttemptWorkspaceMutationCreateDirectory, EntryID: entryID, DestinationPath: "multi-node"})
		}
		command := attemptWorkspaceBatchCommand(fixture.candidate.ID, "workspace-multi-node")
		type result struct {
			value *store.ExamAttemptWorkspaceBatchResult
			err   error
		}
		started := make(chan struct{})
		results := make(chan result, 2)
		for index, adapter := range []store.ExamAttemptWorkspaceStore{workspace, probe.ConcurrentPeer} {
			input := inputs[index]
			go func() {
				<-started
				value, applyErr := adapter.ApplyBatch(ctx, input, command)
				results <- result{value: value, err: applyErr}
			}()
		}
		close(started)
		first, second := <-results, <-results
		require.NoError(t, first.err)
		require.NoError(t, second.err)
		require.NotNil(t, first.value, "ApplyBatch(two-adapter exact race)")
		require.NotNil(t, second.value, "ApplyBatch(two-adapter exact race)")
		require.NotEqual(t, second.value.Replayed, first.value.Replayed, "ApplyBatch(two-adapter exact race)")
		require.Equal(t, second.value.Changes, first.value.Changes, "ApplyBatch(two-adapter exact race)")
		require.Len(t, first.value.Changes, 1, "ApplyBatch(two-adapter exact race)")
		require.Equal(t, int64(1), first.value.Changes[0].Cursor, "ApplyBatch(two-adapter exact race)")
		require.Equal(t, entryID, first.value.Changes[0].EntryID, "ApplyBatch(two-adapter exact race)")
		for _, input := range inputs {
			requireSuccessfulAudit(t, ctx, ss, input.AuditEventID)
		}
		journal, journalErr := probe.ConcurrentPeer.ListJournal(ctx, store.CandidateWorkspaceJournalOptions{
			Access: journalAccess(access), AfterCursor: 0, Limit: 200})
		require.NoError(t, journalErr)
		require.NotNil(t, journal)
		require.Equal(t, int64(1), journal.CurrentCursor)
		require.Len(t, journal.Entries, 1)
		require.Equal(t, int64(1), journal.Entries[0].Cursor)
		require.Equal(t, entryID, journal.Entries[0].EntryID)
	}

	probe.SetParticipationLeaseExpired(t, ctx, connected.Participation.ID)
	audit := saveExamAttemptSystemAudit(t, ctx, ss, fixture)
	expired, err := ss.ExamAttempt().ExpireParticipation(ctx, &store.ExamAttemptParticipationExpiry{
		AttemptID: connected.Attempt.ID, ParticipationID: connected.Participation.ID,
		Generation: connected.Participation.Generation, EvidenceID: model.NewIntegrityEvidenceID(),
		FlagID: model.NewIntegrityFlagID(), SuspensionID: model.NewAttemptSuspensionID(),
		AuditEventID: audit.ID, AuditAt: model.NowUTC(),
	})
	require.NoError(t, err)
	require.NotNil(t, expired)
	require.NotNil(t, expired.Attempt)
	require.Equal(t, model.ExamAttemptSuspended, expired.Attempt.State)
	_, err = workspace.ReserveObjects(ctx, &store.ExamAttemptWorkspaceObjectReservation{
		Access: access, ObjectIDs: []model.AttemptWorkspaceObjectID{model.NewAttemptWorkspaceObjectID()},
	})
	require.True(t, store.IsConflict(err), "ReserveObjects(suspended Attempt) error")
	_, err = workspace.ReserveObjects(ctx, &store.ExamAttemptWorkspaceObjectReservation{Access: access})
	require.True(t, store.IsConflict(err), "ReserveObjects(suspended Attempt, no objects) error")
	_, err = workspace.ApplyBatch(ctx, newAttemptWorkspaceBatch(t, ctx, ss, fixture, access, store.ExamAttemptWorkspaceMutation{
		Operation: model.AttemptWorkspaceMutationCreateDirectory, EntryID: model.NewAttemptWorkspaceEntryID(), DestinationPath: "suspended"}),
		attemptWorkspaceBatchCommand(fixture.candidate.ID, "workspace-suspended"))
	require.True(t, store.IsConflict(err), "ApplyBatch(suspended Attempt) error")
}

func journalAccess(access store.ExamAttemptWorkspaceEditorAccess) store.CandidateAttemptAccess {
	return store.CandidateAttemptAccess{AttemptID: access.AttemptID, CandidateUserID: access.CandidateUserID,
		SessionID: access.SessionID, DesktopRegistrationID: access.DesktopRegistrationID,
		DPoPKeyThumbprint: access.DPoPKeyThumbprint, ConnectionID: access.ConnectionID,
		ContinuityCredentialHash: access.ContinuityCredentialHash}
}

// testAttemptWorkspaceBatches covers what only a batch of several mutations
// can show: order, atomicity, aggregate audit, replay and bounds.
func testAttemptWorkspaceBatches(t *testing.T, ss store.Store, workspace store.ExamAttemptWorkspaceStore) {
	t.Helper()
	ctx := context.Background()
	fixture := newExamAttemptFixture(t, ctx, ss)
	credential := model.HashToken(model.NewCredentialToken())
	connect := &store.ExamAttemptConnect{SittingID: fixture.sitting.ID, CandidateUserID: fixture.candidate.ID,
		SessionID: fixture.session.ID, DesktopRegistrationID: fixture.session.DesktopRegistrationID,
		DPoPKeyThumbprint: fixture.session.DPoPKeyThumbprint, AttemptID: model.NewExamAttemptID(),
		WorkspaceID: model.NewExamAttemptWorkspaceID(), ParticipationID: model.NewAttemptParticipationID(),
		ConnectionID: model.NewAttemptConnectionID(), ContinuityCredentialHash: credential,
		AuditEventID: saveExamAttemptAudit(t, ctx, ss, fixture).ID, AuditAt: model.NowUTC()}
	prepareExamAttemptConnect(t, ctx, ss, connect)
	connected, err := ss.ExamAttempt().Connect(ctx, connect,
		examCommand(fixture.candidate.ID, store.ExamAttemptConnectOperation, "batch-connect", "batch-connect"))
	require.NoError(t, err)
	access := store.ExamAttemptWorkspaceEditorAccess{AttemptID: connected.Attempt.ID,
		ParticipationID: connected.Participation.ID, Generation: connected.Participation.Generation,
		CandidateUserID: fixture.candidate.ID, SessionID: fixture.session.ID,
		DesktopRegistrationID: fixture.session.DesktopRegistrationID, DPoPKeyThumbprint: fixture.session.DPoPKeyThumbprint,
		ConnectionID: connected.Connection.ID, ContinuityCredentialHash: credential}
	target, err := workspace.ReserveObjects(ctx, &store.ExamAttemptWorkspaceObjectReservation{Access: access})
	require.NoError(t, err)
	content := func(size int64, digit string) model.AttemptWorkspaceContent {
		return model.AttemptWorkspaceContent{MediaType: "text/plain", SizeBytes: size, SHA256: strings.Repeat(digit, 64)}
	}
	state := func() attemptWorkspaceState {
		t.Helper()
		return readAttemptWorkspaceState(t, ctx, workspace, journalAccess(access))
	}
	initial := state()
	var starterFile store.CandidateAttemptWorkspaceItem
	for _, item := range initial.items {
		if item.Kind == model.StarterWorkspaceEntryFile {
			starterFile = item
		}
	}
	require.True(t, starterFile.EntryID.IsValid(), "fixture has no Starter-origin file")

	// A batch applies its mutations in order, each seeing the ones before it,
	// at consecutive cursors, with one aggregate audit and no paths in it.
	objects := stageAttemptWorkspaceObjects(t, ctx, workspace, access, content(4, "1"), content(5, "2"))
	directoryID, fileID := model.NewAttemptWorkspaceEntryID(), model.NewAttemptWorkspaceEntryID()
	mutations := []store.ExamAttemptWorkspaceMutation{
		{Operation: model.AttemptWorkspaceMutationCreateDirectory, EntryID: directoryID, DestinationPath: "pkg"},
		{Operation: model.AttemptWorkspaceMutationCreateFile, EntryID: fileID, DestinationPath: "pkg/util.go", ObjectID: objects[0].ObjectID},
		{Operation: model.AttemptWorkspaceMutationReplaceFile, EntryID: starterFile.EntryID, ExpectedPath: starterFile.Path,
			ExpectedContentVersion: starterFile.ContentVersion, ObjectID: objects[1].ObjectID},
		{Operation: model.AttemptWorkspaceMutationMoveEntry, EntryID: fileID, ExpectedPath: "pkg/util.go", DestinationPath: "pkg/helpers.go"},
		{Operation: model.AttemptWorkspaceMutationDeleteEntry, EntryID: starterFile.EntryID, ExpectedPath: starterFile.Path,
			ExpectedContentVersion: objects[1].ContentVersion},
	}
	batch := newAttemptWorkspaceBatch(t, ctx, ss, fixture, access, mutations...)
	command := attemptWorkspaceBatchCommand(fixture.candidate.ID, "batch-mixed")
	applied, err := workspace.ApplyBatch(ctx, batch, command)
	require.NoError(t, err)
	requireWorkspaceBatchEcho(t, applied, target, mutations,
		workspaceEcho{model.StarterWorkspaceEntryDirectory, ""},
		workspaceEcho{model.StarterWorkspaceEntryFile, objects[0].ContentVersion},
		workspaceEcho{model.StarterWorkspaceEntryFile, objects[1].ContentVersion},
		workspaceEcho{model.StarterWorkspaceEntryFile, objects[0].ContentVersion},
		workspaceEcho{model.StarterWorkspaceEntryFile, ""})
	require.False(t, applied.Replayed)
	require.Equal(t, initial.cursor+1, applied.Changes[0].Cursor)
	require.Equal(t, initial.cursor+int64(len(mutations)), applied.Changes[len(mutations)-1].Cursor)
	afterMixed := state()
	require.Equal(t, applied.Changes[len(mutations)-1].Cursor, afterMixed.cursor)
	paths := make(map[string]store.CandidateAttemptWorkspaceItem, len(afterMixed.items))
	for _, item := range afterMixed.items {
		paths[item.Path] = item
	}
	require.Len(t, afterMixed.items, len(initial.items)+1, "manifest = %#v", afterMixed.items)
	require.Equal(t, directoryID, paths["pkg"].EntryID)
	require.Equal(t, fileID, paths["pkg/helpers.go"].EntryID)
	require.Equal(t, objects[0].ContentVersion, paths["pkg/helpers.go"].ContentVersion)
	require.NotContains(t, paths, "pkg/util.go")
	require.NotContains(t, paths, starterFile.Path)
	journal, err := workspace.ListJournal(ctx, store.CandidateWorkspaceJournalOptions{Access: journalAccess(access),
		AfterCursor: initial.cursor, Limit: model.AttemptWorkspaceJournalReadMaximum})
	require.NoError(t, err)
	require.Equal(t, applied.Changes, journal.Entries)
	audit, err := ss.Audit().Get(ctx, batch.AuditEventID)
	require.NoError(t, err)
	require.Equal(t, model.AuditStatusSuccess, audit.Status)
	auditData := requireAttemptWorkspaceBatchAudit(t, audit, len(mutations), applied)
	require.Equal(t, map[string]int{
		string(model.AttemptWorkspaceMutationCreateDirectory): 1,
		string(model.AttemptWorkspaceMutationCreateFile):      1,
		string(model.AttemptWorkspaceMutationReplaceFile):     1,
		string(model.AttemptWorkspaceMutationMoveEntry):       1,
		string(model.AttemptWorkspaceMutationDeleteEntry):     1,
	}, auditData.OperationCounts)
	for _, path := range []string{"pkg", "util.go", "helpers.go", starterFile.Path} {
		require.NotContains(t, string(audit.Result), path, "batch audit carries a Workspace Path")
	}

	// An exact replay returns the retained changes, with the entries the
	// first run created, though the replaying caller proposes new ones.
	replayMutations := append([]store.ExamAttemptWorkspaceMutation(nil), mutations...)
	replayMutations[0].EntryID, replayMutations[1].EntryID = model.NewAttemptWorkspaceEntryID(), model.NewAttemptWorkspaceEntryID()
	replayBatch := newAttemptWorkspaceBatch(t, ctx, ss, fixture, access, replayMutations...)
	replayed, err := workspace.ApplyBatch(ctx, replayBatch, command)
	require.NoError(t, err)
	require.True(t, replayed.Replayed)
	require.True(t, replayed.ConsistentWith(target, access.WorkspaceAuthor(), replayMutations))
	require.Equal(t, applied.Changes, replayed.Changes)
	require.Equal(t, afterMixed.cursor, state().cursor, "replay advanced the Workspace Cursor")
	replayAudit, err := ss.Audit().Get(ctx, replayBatch.AuditEventID)
	require.NoError(t, err)
	require.Equal(t, model.AuditStatusSuccess, replayAudit.Status)
	replayData := requireAttemptWorkspaceBatchAudit(t, replayAudit, len(mutations), applied)
	require.True(t, replayData.Replayed)
	require.Equal(t, batch.AuditEventID.String(), replayData.OriginalAuditEventID)

	// A mutation that fails in the middle refuses the whole batch and names
	// itself; nothing changes and its staged object stays usable.
	atomic := stageAttemptWorkspaceObjects(t, ctx, workspace, access, content(3, "3"))[0]
	before := state()
	failing := newAttemptWorkspaceBatch(t, ctx, ss, fixture, access,
		store.ExamAttemptWorkspaceMutation{Operation: model.AttemptWorkspaceMutationCreateDirectory,
			EntryID: model.NewAttemptWorkspaceEntryID(), DestinationPath: "atomic"},
		store.ExamAttemptWorkspaceMutation{Operation: model.AttemptWorkspaceMutationCreateFile,
			EntryID: model.NewAttemptWorkspaceEntryID(), DestinationPath: "atomic/a.txt", ObjectID: atomic.ObjectID},
		store.ExamAttemptWorkspaceMutation{Operation: model.AttemptWorkspaceMutationMoveEntry, EntryID: fileID,
			ExpectedPath: "pkg/stale.go", DestinationPath: "pkg/moved.go"},
		store.ExamAttemptWorkspaceMutation{Operation: model.AttemptWorkspaceMutationCreateDirectory,
			EntryID: model.NewAttemptWorkspaceEntryID(), DestinationPath: "atomic-after"})
	_, err = workspace.ApplyBatch(ctx, failing, attemptWorkspaceBatchCommand(fixture.candidate.ID, "batch-atomic-failure"))
	requireAttemptWorkspaceBatchItem(t, err, 2, "attempt_workspace_entry")
	requireAttemptWorkspaceStateUnchanged(t, ctx, workspace, journalAccess(access), before)
	failedAudit, err := ss.Audit().Get(ctx, failing.AuditEventID)
	require.NoError(t, err)
	require.NotEqual(t, model.AuditStatusSuccess, failedAudit.Status, "a refused batch completed its audit as a success")
	_, err = workspace.ApplyBatch(ctx, newAttemptWorkspaceBatch(t, ctx, ss, fixture, access,
		store.ExamAttemptWorkspaceMutation{Operation: model.AttemptWorkspaceMutationCreateDirectory,
			EntryID: model.NewAttemptWorkspaceEntryID(), DestinationPath: "stale-version"},
		store.ExamAttemptWorkspaceMutation{Operation: model.AttemptWorkspaceMutationReplaceFile, EntryID: fileID,
			ExpectedPath: "pkg/helpers.go", ExpectedContentVersion: model.NewWorkspaceContentVersion(), ObjectID: atomic.ObjectID}),
		attemptWorkspaceBatchCommand(fixture.candidate.ID, "batch-stale-version"))
	requireAttemptWorkspaceBatchItem(t, err, 1, "attempt_workspace_content_version")
	requireAttemptWorkspaceStateUnchanged(t, ctx, workspace, journalAccess(access), before)
	retried, err := workspace.ApplyBatch(ctx, newAttemptWorkspaceBatch(t, ctx, ss, fixture, access,
		store.ExamAttemptWorkspaceMutation{Operation: model.AttemptWorkspaceMutationCreateDirectory,
			EntryID: model.NewAttemptWorkspaceEntryID(), DestinationPath: "atomic"},
		store.ExamAttemptWorkspaceMutation{Operation: model.AttemptWorkspaceMutationCreateFile,
			EntryID: model.NewAttemptWorkspaceEntryID(), DestinationPath: "atomic/a.txt", ObjectID: atomic.ObjectID}),
		attemptWorkspaceBatchCommand(fixture.candidate.ID, "batch-atomic-retry"))
	require.NoError(t, err, "a refused batch consumed its staged object")
	require.Equal(t, before.cursor+1, retried.Changes[0].Cursor, "a refused batch consumed cursors")

	// Malformed batches are refused before any lock, naming the bad mutation
	// when there is one.
	invalidBatches := map[string]*store.ExamAttemptWorkspaceBatch{
		"empty": newAttemptWorkspaceBatch(t, ctx, ss, fixture, access),
		"too many mutations": newAttemptWorkspaceBatch(t, ctx, ss, fixture, access,
			attemptWorkspaceDirectoryMutations("too-many", model.AttemptWorkspaceBatchMaximumMutations+1)...),
		"shared object": newAttemptWorkspaceBatch(t, ctx, ss, fixture, access,
			store.ExamAttemptWorkspaceMutation{Operation: model.AttemptWorkspaceMutationCreateFile,
				EntryID: model.NewAttemptWorkspaceEntryID(), DestinationPath: "shared-a", ObjectID: atomic.ObjectID},
			store.ExamAttemptWorkspaceMutation{Operation: model.AttemptWorkspaceMutationCreateFile,
				EntryID: model.NewAttemptWorkspaceEntryID(), DestinationPath: "shared-b", ObjectID: atomic.ObjectID}),
		"cursor without recursive deletion": func() *store.ExamAttemptWorkspaceBatch {
			value := newAttemptWorkspaceBatch(t, ctx, ss, fixture, access, attemptWorkspaceDirectoryMutations("fenced", 1)...)
			value.ExpectedWorkspaceCursor = &before.cursor
			return value
		}(),
		"recursive deletion without cursor": newAttemptWorkspaceBatch(t, ctx, ss, fixture, access,
			store.ExamAttemptWorkspaceMutation{Operation: model.AttemptWorkspaceMutationDeleteEntry,
				EntryID: directoryID, ExpectedPath: "pkg", Recursive: true}),
	}
	for name, invalid := range invalidBatches {
		_, err = workspace.ApplyBatch(ctx, invalid, attemptWorkspaceBatchCommand(fixture.candidate.ID, "batch-invalid-"+name))
		requireInvalidInput(t, err, "ApplyBatch(%s)", name)
		var item *store.ErrAttemptWorkspaceBatchItem
		require.NotErrorAs(t, err, &item, "ApplyBatch(%s) named a mutation", name)
	}
	_, err = workspace.ApplyBatch(ctx, newAttemptWorkspaceBatch(t, ctx, ss, fixture, access,
		attemptWorkspaceDirectoryMutations("valid", 1)[0],
		store.ExamAttemptWorkspaceMutation{Operation: model.AttemptWorkspaceMutationCreateDirectory,
			EntryID: model.NewAttemptWorkspaceEntryID(), DestinationPath: "not//normal"}),
		attemptWorkspaceBatchCommand(fixture.candidate.ID, "batch-invalid-path"))
	requireInvalidInput(t, err, "ApplyBatch(unnormalized path)")
	var invalidItem *store.ErrAttemptWorkspaceBatchItem
	require.ErrorAs(t, err, &invalidItem)
	require.Equal(t, 1, invalidItem.Index)

	// A recursive deletion in a batch fences the cursor the batch starts
	// from, and later mutations of the batch see the subtree gone.
	treeID := model.NewAttemptWorkspaceEntryID()
	tree := []store.ExamAttemptWorkspaceMutation{
		{Operation: model.AttemptWorkspaceMutationCreateDirectory, EntryID: treeID, DestinationPath: "tree"},
		{Operation: model.AttemptWorkspaceMutationCreateDirectory, EntryID: model.NewAttemptWorkspaceEntryID(), DestinationPath: "tree/leaf"},
		{Operation: model.AttemptWorkspaceMutationDeleteEntry, EntryID: treeID, ExpectedPath: "tree", Recursive: true},
		{Operation: model.AttemptWorkspaceMutationCreateDirectory, EntryID: model.NewAttemptWorkspaceEntryID(), DestinationPath: "tree"},
	}
	beforeTree := state()
	staleTree := newAttemptWorkspaceBatch(t, ctx, ss, fixture, access, tree...)
	staleCursor := beforeTree.cursor - 1
	staleTree.ExpectedWorkspaceCursor = &staleCursor
	_, err = workspace.ApplyBatch(ctx, staleTree, attemptWorkspaceBatchCommand(fixture.candidate.ID, "batch-tree-stale"))
	requireAttemptWorkspaceBatchCapacity(t, err, "attempt_workspace_cursor")
	requireAttemptWorkspaceStateUnchanged(t, ctx, workspace, journalAccess(access), beforeTree)
	treeBatch := newAttemptWorkspaceBatch(t, ctx, ss, fixture, access, tree...)
	treeBatch.ExpectedWorkspaceCursor = &beforeTree.cursor
	treeResult, err := workspace.ApplyBatch(ctx, treeBatch, attemptWorkspaceBatchCommand(fixture.candidate.ID, "batch-tree"))
	require.NoError(t, err)
	requireWorkspaceBatchEcho(t, treeResult, target, tree)
	require.True(t, treeResult.Changes[2].Recursive)
	afterTree := state()
	require.Len(t, afterTree.items, len(beforeTree.items)+1)

	// Releasing objects is bulk: unused reservations go, current content
	// stays, and a released object can no longer be consumed.
	unused := stageAttemptWorkspaceObjects(t, ctx, workspace, access, content(2, "4"))[0]
	require.NoError(t, workspace.MarkObjectsReclaimable(ctx, []model.AttemptWorkspaceObjectID{objects[0].ObjectID, unused.ObjectID}))
	current, err := workspace.ResolveFile(ctx, journalAccess(access), fileID)
	require.NoError(t, err)
	require.Equal(t, objects[0].ObjectID, current.AttemptObjectID, "MarkObjectsReclaimable released a current object")
	_, err = workspace.ApplyBatch(ctx, newAttemptWorkspaceBatch(t, ctx, ss, fixture, access,
		store.ExamAttemptWorkspaceMutation{Operation: model.AttemptWorkspaceMutationCreateFile,
			EntryID: model.NewAttemptWorkspaceEntryID(), DestinationPath: "released.txt", ObjectID: unused.ObjectID}),
		attemptWorkspaceBatchCommand(fixture.candidate.ID, "batch-released"))
	requireAttemptWorkspaceBatchItem(t, err, 0, "attempt_workspace_object_state")
	duplicate := model.NewAttemptWorkspaceObjectID()
	requireInvalidInput(t, workspace.MarkObjectsReclaimable(ctx, []model.AttemptWorkspaceObjectID{duplicate, duplicate}),
		"MarkObjectsReclaimable(duplicate object)")
	requireInvalidInput(t, workspace.MarkObjectsReclaimable(ctx, newAttemptWorkspaceObjectIDs(model.AttemptWorkspaceBatchMaximumMutations+1)),
		"MarkObjectsReclaimable(more objects than a batch)")

	// A full batch of file writes fits its retained outcome, including one
	// that supersedes a full batch of objects, and replays from it.
	bulkContents := make([]model.AttemptWorkspaceContent, model.AttemptWorkspaceBatchMaximumMutations)
	for index := range bulkContents {
		bulkContents[index] = content(1, "5")
	}
	requireInvalidInput(t, workspace.MarkObjectsReady(ctx, &store.ExamAttemptWorkspaceObjectsReady{Access: access,
		Objects: make([]store.ExamAttemptWorkspaceReadyObject, model.AttemptWorkspaceBatchMaximumMutations+1)}),
		"MarkObjectsReady(more objects than a batch)")
	bulkObjects := stageAttemptWorkspaceObjects(t, ctx, workspace, access, bulkContents...)
	creates := make([]store.ExamAttemptWorkspaceMutation, len(bulkObjects))
	for index, object := range bulkObjects {
		creates[index] = store.ExamAttemptWorkspaceMutation{Operation: model.AttemptWorkspaceMutationCreateFile,
			EntryID: model.NewAttemptWorkspaceEntryID(), DestinationPath: fmt.Sprintf("bulk-%03d.txt", index), ObjectID: object.ObjectID}
	}
	bulkCreated, err := workspace.ApplyBatch(ctx, newAttemptWorkspaceBatch(t, ctx, ss, fixture, access, creates...),
		attemptWorkspaceBatchCommand(fixture.candidate.ID, "batch-bulk-create"))
	require.NoError(t, err, "ApplyBatch(full batch of creates)")
	requireWorkspaceBatchEcho(t, bulkCreated, target, creates)
	bulkReplacements := stageAttemptWorkspaceObjects(t, ctx, workspace, access, bulkContents...)
	replaces := make([]store.ExamAttemptWorkspaceMutation, len(bulkReplacements))
	for index, object := range bulkReplacements {
		replaces[index] = store.ExamAttemptWorkspaceMutation{Operation: model.AttemptWorkspaceMutationReplaceFile,
			EntryID: creates[index].EntryID, ExpectedPath: creates[index].DestinationPath,
			ExpectedContentVersion: bulkObjects[index].ContentVersion, ObjectID: object.ObjectID}
	}
	replaceBatch := newAttemptWorkspaceBatch(t, ctx, ss, fixture, access, replaces...)
	replaceCommand := attemptWorkspaceBatchCommand(fixture.candidate.ID, "batch-bulk-replace")
	bulkReplaced, err := workspace.ApplyBatch(ctx, replaceBatch, replaceCommand)
	require.NoError(t, err, "ApplyBatch(full batch of replacements)")
	requireWorkspaceBatchEcho(t, bulkReplaced, target, replaces)
	replayReplace := *replaceBatch
	replayReplace.AuditEventID = saveExamAttemptAudit(t, ctx, ss, fixture).ID
	bulkReplayed, err := workspace.ApplyBatch(ctx, &replayReplace, replaceCommand)
	require.NoError(t, err, "ApplyBatch(replay of a full batch)")
	require.True(t, bulkReplayed.Replayed)
	require.Equal(t, bulkReplaced.Changes, bulkReplayed.Changes)
}

// workspaceEcho is the kind and, for a fresh file write, the content version
// one change is expected to carry. Zero fields are not checked.
type workspaceEcho struct {
	kind    model.StarterWorkspaceEntryKind
	version model.WorkspaceContentVersion
}

// requireWorkspaceBatchEcho asserts what the application no longer re-checks
// on a batch outcome: one well-formed change per mutation, in order, at
// consecutive cursors, with one change time, the paths the mutation implies,
// the proposed entry unless replayed, and, where given, the expected kind and
// fresh content version.
func requireWorkspaceBatchEcho(
	t *testing.T,
	result *store.ExamAttemptWorkspaceBatchResult,
	target *store.ExamAttemptWorkspaceMutationTarget,
	mutations []store.ExamAttemptWorkspaceMutation,
	expected ...workspaceEcho,
) {
	t.Helper()
	require.NotNil(t, result)
	require.NotEmpty(t, result.Changes)
	require.True(t, result.ConsistentWith(target, result.Changes[0].Author, mutations), "ApplyBatch returned a result inconsistent with its request")
	require.Equal(t, model.AttemptWorkspaceAuthorCandidate, result.Changes[0].Author.Kind)
	require.Len(t, result.Changes, len(mutations))
	for index, change := range result.Changes {
		mutation := mutations[index]
		require.NoError(t, change.Validate())
		require.Equal(t, mutation.Recursive, change.Recursive)
		require.True(t, change.ChangedAt.Equal(result.Changes[0].ChangedAt), "a batch's changes have different times")
		for _, path := range []string{change.OldPath, change.NewPath} {
			if path != "" {
				normalized, err := model.NormalizeAttemptWorkspacePath(path)
				require.NoError(t, err)
				require.Equal(t, normalized, path)
			}
		}
		switch mutation.Operation {
		case model.AttemptWorkspaceMutationCreateFile, model.AttemptWorkspaceMutationCreateDirectory:
			require.Empty(t, change.OldPath)
			require.Equal(t, mutation.DestinationPath, change.NewPath)
		case model.AttemptWorkspaceMutationReplaceFile:
			require.Equal(t, mutation.ExpectedPath, change.OldPath)
			require.Equal(t, mutation.ExpectedPath, change.NewPath)
		case model.AttemptWorkspaceMutationMoveEntry:
			require.Equal(t, mutation.ExpectedPath, change.OldPath)
			require.Equal(t, mutation.DestinationPath, change.NewPath)
		case model.AttemptWorkspaceMutationDeleteEntry:
			require.Equal(t, mutation.ExpectedPath, change.OldPath)
			require.Empty(t, change.NewPath)
		}
		switch change.EntryKind {
		case model.StarterWorkspaceEntryDirectory:
			require.True(t, change.ContentVersion.IsZero())
		case model.StarterWorkspaceEntryFile:
			require.Equal(t, mutation.Operation != model.AttemptWorkspaceMutationDeleteEntry, change.ContentVersion.IsValid())
		default:
			require.Failf(t, "unknown Workspace entry kind", "kind = %q", change.EntryKind)
		}
		if !result.Replayed {
			require.Equal(t, mutation.EntryID, change.EntryID)
		}
		if index < len(expected) {
			if expected[index].kind != "" {
				require.Equal(t, expected[index].kind, change.EntryKind)
			}
			if !result.Replayed && expected[index].version.IsValid() {
				require.Equal(t, expected[index].version, change.ContentVersion)
			}
		}
	}
}

// attemptWorkspaceBatchAudit is the aggregate completion data of one batch.
type attemptWorkspaceBatchAudit struct {
	OperationCounts      map[string]int
	Replayed             bool
	OriginalAuditEventID string
	Author               model.AttemptWorkspaceAuthor
	Fence                int64
}

// requireAttemptWorkspaceBatchAudit asserts that a batch audit carries only
// safe scope, counts by operation, the cursor range of result and the
// writer's author, plus the lease fence of an environment writer.
func requireAttemptWorkspaceBatchAudit(t *testing.T, audit *model.AuditEvent, mutationCount int,
	result *store.ExamAttemptWorkspaceBatchResult,
) attemptWorkspaceBatchAudit {
	t.Helper()
	var data map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(audit.Result, &data))
	keys := []string{"exam_sitting_id", "candidate_user_id", "workspace_id", "mutation_count", "operation_counts",
		"first_workspace_cursor", "last_workspace_cursor", "author_kind", "author_ref"}
	var decoded attemptWorkspaceBatchAudit
	require.NoError(t, json.Unmarshal(data["author_kind"], &decoded.Author.Kind))
	require.NoError(t, json.Unmarshal(data["author_ref"], &decoded.Author.Ref))
	require.Equal(t, result.Changes[0].Author, decoded.Author, "batch audit author = %s", audit.Result)
	if decoded.Author.Kind == model.AttemptWorkspaceAuthorEnvironment {
		keys = append(keys, "environment_lease_fence")
		require.NoError(t, json.Unmarshal(data["environment_lease_fence"], &decoded.Fence))
	}
	if _, replayed := data["idempotency_replayed"]; replayed {
		keys = append(keys, "idempotency_replayed", "original_audit_event_id")
		require.NoError(t, json.Unmarshal(data["idempotency_replayed"], &decoded.Replayed))
		require.NoError(t, json.Unmarshal(data["original_audit_event_id"], &decoded.OriginalAuditEventID))
	}
	present := make([]string, 0, len(data))
	for key := range data {
		present = append(present, key)
	}
	require.ElementsMatch(t, keys, present, "batch audit data = %s", audit.Result)
	var sittingID, candidateID, workspaceID string
	var count int
	var first, last int64
	require.NoError(t, json.Unmarshal(data["exam_sitting_id"], &sittingID))
	require.NoError(t, json.Unmarshal(data["candidate_user_id"], &candidateID))
	require.NoError(t, json.Unmarshal(data["workspace_id"], &workspaceID))
	require.NoError(t, json.Unmarshal(data["mutation_count"], &count))
	require.NoError(t, json.Unmarshal(data["operation_counts"], &decoded.OperationCounts))
	require.NoError(t, json.Unmarshal(data["first_workspace_cursor"], &first))
	require.NoError(t, json.Unmarshal(data["last_workspace_cursor"], &last))
	require.Equal(t, result.SittingID.String(), sittingID)
	require.Equal(t, result.CandidateUserID.String(), candidateID)
	require.Equal(t, result.WorkspaceID.String(), workspaceID)
	require.Equal(t, mutationCount, count)
	require.Equal(t, result.Changes[0].Cursor, first)
	require.Equal(t, result.Changes[len(result.Changes)-1].Cursor, last)
	return decoded
}

// attemptWorkspaceState is everything a refused batch must leave alone: the
// cursor and the complete manifest. An unchanged cursor also means no journal
// record was appended.
type attemptWorkspaceState struct {
	cursor int64
	items  []store.CandidateAttemptWorkspaceItem
}

func readAttemptWorkspaceState(t *testing.T, ctx context.Context, workspace store.ExamAttemptWorkspaceStore,
	access store.CandidateAttemptAccess,
) attemptWorkspaceState {
	t.Helper()
	page, err := workspace.List(ctx, store.CandidateWorkspaceListOptions{Access: access, ExpectedCursor: -1, Limit: 200})
	require.NoError(t, err)
	state := attemptWorkspaceState{cursor: page.Cursor, items: page.Items}
	for page.HasMore {
		page, err = workspace.List(ctx, store.CandidateWorkspaceListOptions{Access: access, ExpectedCursor: state.cursor,
			AfterEntryID: state.items[len(state.items)-1].EntryID, Limit: 200})
		require.NoError(t, err)
		require.False(t, page.RefreshRequired, "Workspace changed while its state was read")
		state.items = append(state.items, page.Items...)
	}
	return state
}

func requireAttemptWorkspaceStateUnchanged(t *testing.T, ctx context.Context, workspace store.ExamAttemptWorkspaceStore,
	access store.CandidateAttemptAccess, before attemptWorkspaceState,
) {
	t.Helper()
	after := readAttemptWorkspaceState(t, ctx, workspace, access)
	require.Equal(t, before.cursor, after.cursor, "a refused batch advanced the Workspace Cursor")
	require.Equal(t, before.items, after.items, "a refused batch changed the Workspace")
	journal, err := workspace.ListJournal(ctx, store.CandidateWorkspaceJournalOptions{Access: access, AfterCursor: before.cursor, Limit: 1})
	require.NoError(t, err)
	require.Empty(t, journal.Entries, "a refused batch appended to the journal")
}

// requireAttemptWorkspaceBatchItem asserts that err refuses a batch because
// of the mutation at index, with the stable conflict constraint.
func requireAttemptWorkspaceBatchItem(t *testing.T, err error, index int, constraint string) {
	t.Helper()
	var item *store.ErrAttemptWorkspaceBatchItem
	require.ErrorAs(t, err, &item)
	require.Equal(t, index, item.Index, "batch refusal = %v", err)
	requireConflictConstraint(t, item.Err, constraint)
}

// requireAttemptWorkspaceBatchCapacity asserts a whole-batch conflict, such
// as capacity or the cursor fence, which names no single mutation.
func requireAttemptWorkspaceBatchCapacity(t *testing.T, err error, constraint string) {
	t.Helper()
	requireConflictConstraint(t, err, constraint)
	var item *store.ErrAttemptWorkspaceBatchItem
	require.NotErrorAs(t, err, &item, "a whole-batch conflict named a mutation")
}

func requireInvalidInput(t *testing.T, err error, format string, args ...any) {
	t.Helper()
	var invalid *store.ErrInvalidInput
	require.ErrorAs(t, err, &invalid, append([]any{format}, args...)...)
}

func newAttemptWorkspaceObjectIDs(count int) []model.AttemptWorkspaceObjectID {
	ids := make([]model.AttemptWorkspaceObjectID, count)
	for index := range ids {
		ids[index] = model.NewAttemptWorkspaceObjectID()
	}
	return ids
}

func attemptWorkspaceDirectoryMutations(prefix string, count int) []store.ExamAttemptWorkspaceMutation {
	mutations := make([]store.ExamAttemptWorkspaceMutation, count)
	for index := range mutations {
		mutations[index] = store.ExamAttemptWorkspaceMutation{Operation: model.AttemptWorkspaceMutationCreateDirectory,
			EntryID: model.NewAttemptWorkspaceEntryID(), DestinationPath: fmt.Sprintf("%s-%03d", prefix, index)}
	}
	return mutations
}

// stageAttemptWorkspaceReservations reserves count objects in one call and
// leaves them without content.
func stageAttemptWorkspaceReservations(t *testing.T, ctx context.Context, workspace store.ExamAttemptWorkspaceStore,
	access store.ExamAttemptWorkspaceWriteAccess, count int,
) []model.AttemptWorkspaceObjectID {
	t.Helper()
	ids := newAttemptWorkspaceObjectIDs(count)
	_, err := workspace.ReserveObjects(ctx, &store.ExamAttemptWorkspaceObjectReservation{Access: access, ObjectIDs: ids})
	require.NoError(t, err)
	return ids
}

// stageAttemptWorkspaceObjects reserves and readies one object per content,
// with one ReserveObjects and one MarkObjectsReady call, as one batch's
// staging does.
func stageAttemptWorkspaceObjects(t *testing.T, ctx context.Context, workspace store.ExamAttemptWorkspaceStore,
	access store.ExamAttemptWorkspaceWriteAccess, contents ...model.AttemptWorkspaceContent,
) []store.ExamAttemptWorkspaceReadyObject {
	t.Helper()
	ids := stageAttemptWorkspaceReservations(t, ctx, workspace, access, len(contents))
	objects := make([]store.ExamAttemptWorkspaceReadyObject, len(contents))
	for index, content := range contents {
		objects[index] = store.ExamAttemptWorkspaceReadyObject{ObjectID: ids[index],
			ContentVersion: model.NewWorkspaceContentVersion(), Content: content}
	}
	require.NoError(t, workspace.MarkObjectsReady(ctx, &store.ExamAttemptWorkspaceObjectsReady{Access: access, Objects: objects}))
	return objects
}

// newAttemptWorkspaceBatch wraps mutations in a batch with a fresh audit
// attempt for the fixture's sitting.
func newAttemptWorkspaceBatch(t *testing.T, ctx context.Context, ss store.Store, fixture examAttemptFixture,
	access store.ExamAttemptWorkspaceWriteAccess, mutations ...store.ExamAttemptWorkspaceMutation,
) *store.ExamAttemptWorkspaceBatch {
	t.Helper()
	return &store.ExamAttemptWorkspaceBatch{Access: access, Mutations: mutations,
		AuditEventID: saveExamAttemptAudit(t, ctx, ss, fixture).ID, AuditAt: model.NowUTC()}
}

func attemptWorkspaceBatchCommand(userID model.UserID, label string) *store.CommandIdempotency {
	return examCommand(userID, store.ExamAttemptWorkspaceBatchOperation, label, label)
}

// createAttemptWorkspaceTestFile stages one object and creates a file from
// it in a one-mutation batch, for suites that need workspace content.
func createAttemptWorkspaceTestFile(t *testing.T, ctx context.Context, ss store.Store, fixture examAttemptFixture,
	access store.ExamAttemptWorkspaceWriteAccess, path string, content model.AttemptWorkspaceContent, label string,
) (store.ExamAttemptWorkspaceReadyObject, *store.ExamAttemptWorkspaceBatchResult) {
	t.Helper()
	object := stageAttemptWorkspaceObjects(t, ctx, ss.ExamAttemptWorkspace(), access, content)[0]
	result, err := ss.ExamAttemptWorkspace().ApplyBatch(ctx, newAttemptWorkspaceBatch(t, ctx, ss, fixture, access,
		store.ExamAttemptWorkspaceMutation{Operation: model.AttemptWorkspaceMutationCreateFile,
			EntryID: model.NewAttemptWorkspaceEntryID(), DestinationPath: path, ObjectID: object.ObjectID}),
		attemptWorkspaceBatchCommand(fixture.candidate.ID, label))
	require.NoError(t, err)
	require.Len(t, result.Changes, 1)
	return object, result
}

func testAssistantWorkspaceProof(t *testing.T, ctx context.Context, ss store.Store, workspace store.ExamAttemptWorkspaceStore) {
	t.Helper()
	fixture := newExamAttemptFixture(t, ctx, ss)
	_, editor := connectEnvironmentFixture(t, ctx, ss, fixture, "assistant-proof")
	assistant := store.ExamAttemptWorkspaceAssistantAccess{ExamAttemptWorkspaceEditorAccess: editor,
		ConversationID: model.NewAssistantConversationID(), TurnID: model.NewAssistantTurnID(), CallID: model.NewAssistantCallID()}
	_, err := workspace.ReserveObjects(ctx, &store.ExamAttemptWorkspaceObjectReservation{Access: assistant})
	require.NoError(t, err)
	for name, corrupt := range map[string]func(*store.ExamAttemptWorkspaceAssistantAccess){
		"another candidate":  func(a *store.ExamAttemptWorkspaceAssistantAccess) { a.CandidateUserID = model.NewUserID() },
		"another session":    func(a *store.ExamAttemptWorkspaceAssistantAccess) { a.SessionID = model.NewSessionID() },
		"another connection": func(a *store.ExamAttemptWorkspaceAssistantAccess) { a.ConnectionID = model.NewAttemptConnectionID() },
		"wrong credential": func(a *store.ExamAttemptWorkspaceAssistantAccess) {
			a.ContinuityCredentialHash = model.HashToken(model.NewCredentialToken())
		},
		"stale generation": func(a *store.ExamAttemptWorkspaceAssistantAccess) { a.Generation++ },
		"missing call":     func(a *store.ExamAttemptWorkspaceAssistantAccess) { a.CallID = "" },
	} {
		t.Run(name, func(t *testing.T) {
			invalid := assistant
			corrupt(&invalid)
			_, proofErr := workspace.ReserveObjects(ctx, &store.ExamAttemptWorkspaceObjectReservation{Access: invalid})
			require.Error(t, proofErr)
		})
	}
	objects := stageAttemptWorkspaceObjects(t, ctx, workspace, assistant,
		model.AttemptWorkspaceContent{MediaType: "text/plain", SizeBytes: 4, SHA256: strings.Repeat("b", 64)})
	_, err = ss.ClassMember().SeedEnd(ctx, &store.ClassMemberSeedEnd{ID: fixture.membership.ID,
		ExpectedRevision: fixture.membership.Revision, EndAt: model.NowUTC()})
	require.NoError(t, err)
	time.Sleep(100 * time.Millisecond)
	_, err = workspace.ApplyBatch(ctx, newAttemptWorkspaceBatch(t, ctx, ss, fixture, assistant,
		store.ExamAttemptWorkspaceMutation{Operation: model.AttemptWorkspaceMutationCreateFile,
			EntryID: model.NewAttemptWorkspaceEntryID(), DestinationPath: "refused.txt", ObjectID: objects[0].ObjectID}),
		attemptWorkspaceBatchCommand(fixture.candidate.ID, "assistant-revoked"))
	require.True(t, store.IsNotFound(err), "assistant cannot finish a staged write after membership ends")
}
