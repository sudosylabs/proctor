// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package store

import (
	"reflect"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/sudosylabs/proctor/server/model"
)

func TestExamAttemptWorkspaceBatchContractUsesSelectiveFencesAndSafeResults(t *testing.T) {
	t.Parallel()
	access := ExamAttemptWorkspaceEditorAccess{AttemptID: model.NewExamAttemptID(),
		ParticipationID: model.NewAttemptParticipationID(), Generation: 2, CandidateUserID: model.NewUserID(),
		SessionID: model.NewSessionID(), DesktopRegistrationID: model.NewDesktopRegistrationID(),
		DPoPKeyThumbprint: strings.Repeat("A", 43), ConnectionID: model.NewAttemptConnectionID(),
		ContinuityCredentialHash: model.HashToken(model.NewCredentialToken())}
	entryID := model.NewAttemptWorkspaceEntryID()
	workspaceID := model.NewExamAttemptWorkspaceID()
	mutation := ExamAttemptWorkspaceMutation{Operation: model.AttemptWorkspaceMutationReplaceFile,
		EntryID: entryID, ExpectedPath: "cmd/main.go", ExpectedContentVersion: model.NewWorkspaceContentVersion(),
		ObjectID: model.NewAttemptWorkspaceObjectID()}
	batch := ExamAttemptWorkspaceBatch{Access: access, Mutations: []ExamAttemptWorkspaceMutation{mutation}}
	result := ExamAttemptWorkspaceBatchResult{WorkspaceID: workspaceID,
		Changes: []model.AttemptWorkspaceJournalEntry{{WorkspaceID: workspaceID, Cursor: 7,
			EntryID: entryID, EntryKind: model.StarterWorkspaceEntryFile, Operation: mutation.Operation,
			OldPath: mutation.ExpectedPath, NewPath: mutation.ExpectedPath,
			ContentVersion: model.NewWorkspaceContentVersion(), ChangedAt: model.NowUTC()}}, Replayed: true}
	require.Equal(t, int64(2), batch.Access.(ExamAttemptWorkspaceEditorAccess).Generation, "Workspace batch contract = %#v / %#v", batch, result)
	require.Nil(t, batch.ExpectedWorkspaceCursor, "a batch without recursive deletion carries no cursor fence")
	require.Empty(t, batch.Mutations[0].DestinationPath, "Workspace batch contract = %#v / %#v", batch, result)
	require.True(t, result.Replayed, "Workspace batch contract = %#v / %#v", batch, result)
	require.Equal(t, int64(7), result.Changes[0].Cursor, "Workspace batch contract = %#v / %#v", batch, result)
	assertNoWorkspaceSecrets(t, reflect.TypeOf(result))
	_, exists := reflect.TypeOf(mutation).FieldByName("Access")
	require.False(t, exists, "a batch mutation carries its own access")
	_, exists = reflect.TypeOf(mutation).FieldByName("ExpectedWorkspaceCursor")
	require.False(t, exists, "a batch mutation carries its own cursor fence")
	_, exists = reflect.TypeOf((*ExamAttemptWorkspaceStore)(nil)).Elem().MethodByName("ApplyBatch")
	require.True(t, exists, "ExamAttemptWorkspaceStore is missing ApplyBatch")
	require.Equal(t, "exam.attempt.workspace.batch", ExamAttemptWorkspaceBatchOperation)
}

func TestErrAttemptWorkspaceBatchItemNamesTheMutationAndUnwraps(t *testing.T) {
	t.Parallel()
	conflict := NewErrConflict("attempt_workspace_entry", "attempt_workspace_path", nil)
	var err error = &ErrAttemptWorkspaceBatchItem{Index: 3, Err: conflict}
	require.Contains(t, err.Error(), "mutation 3")
	require.True(t, IsConflict(err))
	var item *ErrAttemptWorkspaceBatchItem
	require.ErrorAs(t, err, &item)
	require.Equal(t, 3, item.Index)
	var unwrapped *ErrConflict
	require.ErrorAs(t, err, &unwrapped)
	require.Same(t, conflict, unwrapped)
}

func TestCandidateWorkspaceRecoveryCursorsNeverCarryPaths(t *testing.T) {
	t.Parallel()
	access := CandidateAttemptAccess{AttemptID: model.NewExamAttemptID(), CandidateUserID: model.NewUserID(),
		SessionID: model.NewSessionID(), DesktopRegistrationID: model.NewDesktopRegistrationID(),
		DPoPKeyThumbprint: strings.Repeat("A", 43), ConnectionID: model.NewAttemptConnectionID(),
		ContinuityCredentialHash: model.HashToken(model.NewCredentialToken())}
	initial := CandidateWorkspaceListOptions{Access: access, ExpectedCursor: -1, Limit: model.AttemptWorkspaceJournalReadMaximum}
	manifest := CandidateWorkspaceListOptions{Access: access, ExpectedCursor: 41,
		AfterEntryID: model.NewAttemptWorkspaceEntryID(), Limit: model.AttemptWorkspaceJournalReadMaximum}
	page := CandidateAttemptWorkspacePage{WorkspaceID: model.NewExamAttemptWorkspaceID(), Cursor: 41, RefreshRequired: true}
	journal := CandidateWorkspaceJournalOptions{Access: access, AfterCursor: 40, Limit: model.AttemptWorkspaceJournalReadMaximum}
	recovery := CandidateWorkspaceJournalPage{WorkspaceID: page.WorkspaceID, CurrentCursor: 41, RefreshRequired: true}
	require.Equal(t, int64(-1), initial.ExpectedCursor, "manifest=%#v page=%#v journal=%#v recovery=%#v", manifest, page, journal, recovery)
	require.True(t, initial.AfterEntryID.IsZero(), "manifest=%#v page=%#v journal=%#v recovery=%#v", manifest, page, journal, recovery)
	require.Equal(t, int64(41), manifest.ExpectedCursor, "manifest=%#v page=%#v journal=%#v recovery=%#v", manifest, page, journal, recovery)
	require.Equal(t, int64(41), page.Cursor, "manifest=%#v page=%#v journal=%#v recovery=%#v", manifest, page, journal, recovery)
	require.Equal(t, int64(40), journal.AfterCursor, "manifest=%#v page=%#v journal=%#v recovery=%#v", manifest, page, journal, recovery)
	require.True(t, recovery.RefreshRequired, "manifest=%#v page=%#v journal=%#v recovery=%#v", manifest, page, journal, recovery)
	_, exists := reflect.TypeOf(manifest).FieldByName("AfterPath")
	require.False(t, exists, "candidate manifest cursor exposes a Workspace Path")
}

func TestExamAttemptWorkspaceBatchResolvesSafeAuditAndEffectScope(t *testing.T) {
	t.Parallel()
	access := ExamAttemptWorkspaceEditorAccess{AttemptID: model.NewExamAttemptID(), ParticipationID: model.NewAttemptParticipationID(),
		Generation: 2, CandidateUserID: model.NewUserID(), SessionID: model.NewSessionID(),
		DesktopRegistrationID: model.NewDesktopRegistrationID(), DPoPKeyThumbprint: strings.Repeat("A", 43),
		ConnectionID: model.NewAttemptConnectionID(), ContinuityCredentialHash: model.HashToken(model.NewCredentialToken())}
	target := ExamAttemptWorkspaceMutationTarget{ExamID: model.NewExamID(), SittingID: model.NewExamSittingID(),
		ClassID: model.NewClassID(), CandidateUserID: access.CandidateUserID, WorkspaceID: model.NewExamAttemptWorkspaceID()}
	input := ExamAttemptWorkspaceBatch{Access: access, Mutations: []ExamAttemptWorkspaceMutation{{
		Operation: model.AttemptWorkspaceMutationCreateDirectory, EntryID: model.NewAttemptWorkspaceEntryID(), DestinationPath: "cmd",
	}}, AuditEventID: model.NewAuditEventID(), AuditAt: model.NowUTC()}
	result := ExamAttemptWorkspaceBatchResult{SittingID: target.SittingID, ClassID: target.ClassID,
		CandidateUserID: target.CandidateUserID, WorkspaceID: target.WorkspaceID}
	require.NotEmpty(t, input.AuditEventID, "target=%#v input=%#v result=%#v", target, input, result)
	require.False(t, input.AuditAt.IsZero(), "target=%#v input=%#v result=%#v", target, input, result)
	require.Equal(t, target.SittingID, result.SittingID, "target=%#v input=%#v result=%#v", target, input, result)
	require.Equal(t, target.ClassID, result.ClassID, "target=%#v input=%#v result=%#v", target, input, result)
	require.Equal(t, access.CandidateUserID, result.CandidateUserID, "target=%#v input=%#v result=%#v", target, input, result)
	_, exists := reflect.TypeOf((*ExamAttemptWorkspaceStore)(nil)).Elem().MethodByName("ReserveObjects")
	require.True(t, exists, "ExamAttemptWorkspaceStore is missing ReserveObjects, which also resolves the target")
	assertNoWorkspaceSecrets(t, reflect.TypeOf(target))
}

func TestExamAttemptWorkspaceObjectContractSeparatesStagingFromCleanup(t *testing.T) {
	t.Parallel()
	access := ExamAttemptWorkspaceEditorAccess{AttemptID: model.NewExamAttemptID(), ParticipationID: model.NewAttemptParticipationID(),
		Generation: 3, CandidateUserID: model.NewUserID(), SessionID: model.NewSessionID(),
		DesktopRegistrationID: model.NewDesktopRegistrationID(), DPoPKeyThumbprint: strings.Repeat("A", 43),
		ConnectionID: model.NewAttemptConnectionID(), ContinuityCredentialHash: model.HashToken(model.NewCredentialToken())}
	objectID := model.NewAttemptWorkspaceObjectID()
	reservation := ExamAttemptWorkspaceObjectReservation{Access: access, ObjectIDs: []model.AttemptWorkspaceObjectID{objectID}}
	ready := ExamAttemptWorkspaceObjectsReady{Access: access, Objects: []ExamAttemptWorkspaceReadyObject{{ObjectID: objectID,
		ContentVersion: model.NewWorkspaceContentVersion(),
		Content:        model.AttemptWorkspaceContent{MediaType: "text/plain", SizeBytes: 7, SHA256: strings.Repeat("a", 64)}}}}
	require.Equal(t, ready.Objects[0].ObjectID, reservation.ObjectIDs[0], "reservation=%#v ready=%#v", reservation, ready)
	require.Equal(t, int64(7), ready.Objects[0].Content.SizeBytes, "reservation=%#v ready=%#v", reservation, ready)
	methods := reflect.TypeOf((*ExamAttemptWorkspaceStore)(nil)).Elem()
	for _, name := range []string{"ReserveObjects", "MarkObjectsReady", "MarkObjectsReclaimable", "ClaimObjectsForCleanup", "CompleteObjectCleanup", "ReleaseObjectCleanup"} {
		_, exists := methods.MethodByName(name)
		require.True(t, exists, "ExamAttemptWorkspaceStore is missing %s", name)
	}
	for _, removed := range []string{"ResolveMutationTarget", "ReserveObject", "MarkObjectReady", "ApplyMutation", "MarkObjectReclaimable"} {
		_, exists := methods.MethodByName(removed)
		require.False(t, exists, "ExamAttemptWorkspaceStore still has the single-mutation %s", removed)
	}
}

func assertNoWorkspaceSecrets(t *testing.T, typ reflect.Type) {
	t.Helper()
	for index := 0; index < typ.NumField(); index++ {
		name := strings.ToLower(typ.Field(index).Name)
		for _, forbidden := range []string{"credential", "session", "object", "backend", "storage"} {
			require.NotContains(t, name, forbidden, "outbound")
		}
	}
}
