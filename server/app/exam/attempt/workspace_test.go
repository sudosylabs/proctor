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
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/sudosylabs/proctor/server/app/apperror"
	"github.com/sudosylabs/proctor/server/model"
	"github.com/sudosylabs/proctor/server/store"
)

func TestCreateWorkspaceDirectoryRevalidatesMutationAccessAuditsAndPublishesSafeChange(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	credential := model.NewCredentialToken()
	participationID := model.NewAttemptParticipationID()
	entryID := model.NewAttemptWorkspaceEntryID()
	workspaceID := model.NewExamAttemptWorkspaceID()
	change := model.AttemptWorkspaceJournalEntry{WorkspaceID: workspaceID, Cursor: 9, EntryID: entryID,
		EntryKind: model.StarterWorkspaceEntryDirectory, Operation: model.AttemptWorkspaceMutationCreateDirectory,
		NewPath: "src", ChangedAt: f.at}
	f.workspace.target = &store.ExamAttemptWorkspaceMutationTarget{ExamID: f.sitting.ExamID, SittingID: f.sitting.ID,
		ClassID: f.sitting.ClassID, CandidateUserID: f.userID, WorkspaceID: workspaceID}
	f.workspace.mutationResult = &store.ExamAttemptWorkspaceMutationResult{SittingID: f.sitting.ID, ClassID: f.sitting.ClassID,
		CandidateUserID: f.userID, WorkspaceID: workspaceID,
		Entry: &store.CandidateAttemptWorkspaceItem{EntryID: entryID, Kind: model.StarterWorkspaceEntryDirectory, Path: "src"}, Change: change}

	result, err := f.service.CreateCandidateExamWorkspaceDirectory(context.Background(), f.invocation, CreateWorkspaceDirectoryCommand{Access: WorkspaceMutationAccess{CandidateAccess: CandidateAccess{AttemptID: f.attemptID, ConnectionID: f.connectionID,
		ContinuityCredential: credential}, ParticipationID: participationID, Generation: 4},
		Path: "src", IdempotencyKey: "test-key",
	})
	require.NoError(t, err)
	principal := f.invocation.Principal()
	workspaceAccess := f.workspace.resolvedAccess
	require.Equal(t, f.attemptID, workspaceAccess.AttemptID, "access=%#v mutation=%#v result=%#v effects=%#v", workspaceAccess, f.workspace.mutation, result, f.publisher.Calls())
	require.Equal(t, participationID, workspaceAccess.ParticipationID, "access=%#v mutation=%#v result=%#v effects=%#v", workspaceAccess, f.workspace.mutation, result, f.publisher.Calls())
	require.Equal(t, int64(4), workspaceAccess.Generation, "access=%#v mutation=%#v result=%#v effects=%#v", workspaceAccess, f.workspace.mutation, result, f.publisher.Calls())
	require.Equal(t, principal.UserID, workspaceAccess.CandidateUserID, "access=%#v mutation=%#v result=%#v effects=%#v", workspaceAccess, f.workspace.mutation, result, f.publisher.Calls())
	require.Equal(t, principal.SessionID, workspaceAccess.SessionID, "access=%#v mutation=%#v result=%#v effects=%#v", workspaceAccess, f.workspace.mutation, result, f.publisher.Calls())
	require.Equal(t, f.connectionID, workspaceAccess.ConnectionID, "access=%#v mutation=%#v result=%#v effects=%#v", workspaceAccess, f.workspace.mutation, result, f.publisher.Calls())
	require.Equal(t, model.HashToken(credential), workspaceAccess.ContinuityCredentialHash, "access=%#v mutation=%#v result=%#v effects=%#v", workspaceAccess, f.workspace.mutation, result, f.publisher.Calls())
	require.NotNil(t, f.workspace.mutation, "access=%#v mutation=%#v result=%#v effects=%#v", workspaceAccess, f.workspace.mutation, result, f.publisher.Calls())
	require.Equal(t, entryID, f.workspace.mutation.EntryID, "access=%#v mutation=%#v result=%#v effects=%#v", workspaceAccess, f.workspace.mutation, result, f.publisher.Calls())
	require.Equal(t, "src", f.workspace.mutation.DestinationPath, "access=%#v mutation=%#v result=%#v effects=%#v", workspaceAccess, f.workspace.mutation, result, f.publisher.Calls())
	require.Equal(t, model.AttemptWorkspaceMutationCreateDirectory, f.workspace.mutation.Operation, "access=%#v mutation=%#v result=%#v effects=%#v", workspaceAccess, f.workspace.mutation, result, f.publisher.Calls())
	require.True(t, f.workspace.mutation.AuditEventID.IsValid(), "access=%#v mutation=%#v result=%#v effects=%#v", workspaceAccess, f.workspace.mutation, result, f.publisher.Calls())
	require.Equal(t, int64(9), result.Change.Cursor, "access=%#v mutation=%#v result=%#v effects=%#v", workspaceAccess, f.workspace.mutation, result, f.publisher.Calls())
	require.Equal(t, workspaceChangedCalls(t, result), f.publisher.Calls(), "access=%#v mutation=%#v result=%#v effects=%#v", workspaceAccess, f.workspace.mutation, result, f.publisher.Calls())
	auditCapture := fmt.Sprintf("%#v", f.audit.values)
	require.Len(t, f.audit.values, 2)
	require.Equal(t, f.attemptID.String(), f.audit.values["exam_attempt_id"])
	require.Equal(t, string(model.AttemptWorkspaceMutationCreateDirectory), f.audit.values["operation"])
	require.NotContains(t, auditCapture, credential)
	require.NotContains(t, auditCapture, model.HashToken(credential))
	require.NotContains(t, auditCapture, participationID.String())
	// Contract (guardrails 8 and 14): the audit begins before the durable write,
	// and the write commits before its effect.
	require.Equal(t, []string{"workspace.resolve", "audit", "workspace.apply", "effects"}, f.order)
	wantIdempotency, prepareErr := prepareWorkspaceMutationIdempotency(f.invocation, "test-key", f.attemptID,
		model.AttemptWorkspaceMutationCreateDirectory, struct {
			Path string `json:"path"`
		}{"src"})
	require.NoError(t, prepareErr)
	require.Equal(t, wantIdempotency, f.workspace.idempotency)
}

func TestCreateWorkspaceFileStagesBeforeAtomicMutationAndSuppressesPrivateEffects(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	credential := model.NewCredentialToken()
	participationID := model.NewAttemptParticipationID()
	entryID := model.NewAttemptWorkspaceEntryID()
	workspaceID := model.NewExamAttemptWorkspaceID()
	version := model.NewWorkspaceContentVersion()
	checksum := strings.Repeat("a", 64)
	f.workspace.target = &store.ExamAttemptWorkspaceMutationTarget{ExamID: f.sitting.ExamID, SittingID: f.sitting.ID,
		ClassID: f.sitting.ClassID, CandidateUserID: f.userID, WorkspaceID: workspaceID}
	f.workspace.mutationResult = workspaceMutationResultFixture(f, workspaceID, entryID, model.StarterWorkspaceEntryFile,
		model.AttemptWorkspaceMutationCreateFile, "", "main.go", version, false)
	f.content.attemptContent = &model.AttemptWorkspaceContent{MediaType: "text/plain", SizeBytes: 4, SHA256: checksum}

	result, err := f.service.CreateCandidateExamWorkspaceFile(context.Background(), f.invocation, CreateWorkspaceFileCommand{Access: WorkspaceMutationAccess{CandidateAccess: CandidateAccess{AttemptID: f.attemptID, ConnectionID: f.connectionID,
		ContinuityCredential: credential}, ParticipationID: participationID, Generation: 2},
		Path: "main.go", MediaType: "text/plain", ExpectedSHA256: checksum, Body: strings.NewReader("main"), Size: 4,
		IdempotencyKey: "test-key",
	})
	require.NoError(t, err)
	require.NotNil(t, f.workspace.reservation, "reservation=%#v ready=%#v mutation=%#v result=%#v", f.workspace.reservation, f.workspace.ready, f.workspace.mutation, result)
	require.NotNil(t, f.workspace.ready, "reservation=%#v ready=%#v mutation=%#v result=%#v", f.workspace.reservation, f.workspace.ready, f.workspace.mutation, result)
	require.NotNil(t, f.workspace.mutation, "reservation=%#v ready=%#v mutation=%#v result=%#v", f.workspace.reservation, f.workspace.ready, f.workspace.mutation, result)
	require.Equal(t, f.content.attemptObjectID, f.workspace.reservation.ObjectID, "reservation=%#v ready=%#v mutation=%#v result=%#v", f.workspace.reservation, f.workspace.ready, f.workspace.mutation, result)
	require.Equal(t, f.content.attemptObjectID, f.workspace.ready.ObjectID, "reservation=%#v ready=%#v mutation=%#v result=%#v", f.workspace.reservation, f.workspace.ready, f.workspace.mutation, result)
	require.Equal(t, version, f.workspace.ready.ContentVersion, "reservation=%#v ready=%#v mutation=%#v result=%#v", f.workspace.reservation, f.workspace.ready, f.workspace.mutation, result)
	require.Equal(t, f.content.attemptObjectID, f.workspace.mutation.ObjectID, "reservation=%#v ready=%#v mutation=%#v result=%#v", f.workspace.reservation, f.workspace.ready, f.workspace.mutation, result)
	require.Equal(t, "main.go", f.workspace.mutation.DestinationPath, "reservation=%#v ready=%#v mutation=%#v result=%#v", f.workspace.reservation, f.workspace.ready, f.workspace.mutation, result)
	require.NotNil(t, result.Entry, "reservation=%#v ready=%#v mutation=%#v result=%#v", f.workspace.reservation, f.workspace.ready, f.workspace.mutation, result)
	require.Equal(t, version, result.Entry.ContentVersion, "reservation=%#v ready=%#v mutation=%#v result=%#v", f.workspace.reservation, f.workspace.ready, f.workspace.mutation, result)
	require.Equal(t, workspaceChangedCalls(t, result), f.publisher.Calls(), "reservation=%#v ready=%#v mutation=%#v result=%#v", f.workspace.reservation, f.workspace.ready, f.workspace.mutation, result)
	capture := fmt.Sprintf("%#v", f.audit.values)
	require.NotContains(t, capture, "main.go")
	require.NotContains(t, capture, checksum)
	require.NotContains(t, capture, credential)
	require.NotContains(t, capture, model.HashToken(credential))
	// Contract: bytes are staged only inside a reserved upload, the audit begins
	// before the durable apply (guardrail 8), and the apply commits before its
	// effect (guardrail 14).
	require.Equal(t, []string{"workspace.resolve", "workspace.reserve", "content.stage", "workspace.ready", "audit", "workspace.apply", "effects"}, f.order)
	wantIdempotency, prepareErr := prepareWorkspaceMutationIdempotency(f.invocation, "test-key", f.attemptID,
		model.AttemptWorkspaceMutationCreateFile, struct {
			Path, MediaType, SHA256 string
			Size                    int64
		}{"main.go", "text/plain", checksum, 4})
	require.NoError(t, prepareErr)
	require.Equal(t, wantIdempotency, f.workspace.idempotency)
}

func TestReplaceWorkspaceFilePassesOwnedIdempotencyToStore(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	workspaceID, entryID := model.NewExamAttemptWorkspaceID(), model.NewAttemptWorkspaceEntryID()
	currentVersion, nextVersion := model.NewWorkspaceContentVersion(), model.NewWorkspaceContentVersion()
	checksum := strings.Repeat("c", 64)
	f.workspace.target = &store.ExamAttemptWorkspaceMutationTarget{ExamID: f.sitting.ExamID, SittingID: f.sitting.ID,
		ClassID: f.sitting.ClassID, CandidateUserID: f.userID, WorkspaceID: workspaceID}
	f.workspace.mutationResult = workspaceMutationResultFixture(f, workspaceID, entryID, model.StarterWorkspaceEntryFile,
		model.AttemptWorkspaceMutationReplaceFile, "main.go", "main.go", nextVersion, false)
	f.content.attemptContent = &model.AttemptWorkspaceContent{MediaType: "text/plain", SizeBytes: 4, SHA256: checksum}
	command := ReplaceWorkspaceFileCommand{Access: validWorkspaceMutationAccess(f),
		EntryID: entryID, ExpectedPath: "main.go", ExpectedContentVersion: currentVersion, MediaType: "text/plain",
		ExpectedSHA256: checksum, Body: strings.NewReader("main"), Size: 4, IdempotencyKey: "replace-key"}
	_, err := f.service.ReplaceCandidateExamWorkspaceFile(context.Background(), f.invocation, command)
	require.NoError(t, err)
	want, err := prepareWorkspaceMutationIdempotency(f.invocation, "replace-key", f.attemptID,
		model.AttemptWorkspaceMutationReplaceFile, struct {
			EntryID, Path, Version, MediaType, SHA256 string
			Size                                      int64
		}{entryID.String(), "main.go", currentVersion.String(), "text/plain", checksum, 4})
	require.NoError(t, err)
	require.Equal(t, want, f.workspace.idempotency)
}

func TestWorkspaceFileReplayReclaimsLosingStageAndPublishesNoEffect(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	workspaceID, entryID := model.NewExamAttemptWorkspaceID(), model.NewAttemptWorkspaceEntryID()
	version, checksum := model.NewWorkspaceContentVersion(), strings.Repeat("b", 64)
	f.workspace.target = &store.ExamAttemptWorkspaceMutationTarget{ExamID: f.sitting.ExamID, SittingID: f.sitting.ID,
		ClassID: f.sitting.ClassID, CandidateUserID: f.userID, WorkspaceID: workspaceID}
	f.workspace.mutationResult = workspaceMutationResultFixture(f, workspaceID, entryID, model.StarterWorkspaceEntryFile,
		model.AttemptWorkspaceMutationCreateFile, "", "retry.txt", version, true)
	f.workspace.proposedEntryID = model.NewAttemptWorkspaceEntryID()
	f.workspace.proposedVersion = model.NewWorkspaceContentVersion()
	f.content.attemptContent = &model.AttemptWorkspaceContent{MediaType: "text/plain", SizeBytes: 1, SHA256: checksum}
	_, err := f.service.CreateCandidateExamWorkspaceFile(context.Background(), f.invocation, CreateWorkspaceFileCommand{Access: validWorkspaceMutationAccess(f), Path: "retry.txt", MediaType: "text/plain", ExpectedSHA256: checksum,
		Body: strings.NewReader("x"), Size: 1, IdempotencyKey: "test-key",
	})
	require.NoError(t, err, "error=%v reclaimable=%v effects=%#v", err, f.workspace.reclaimable, f.publisher.Calls())
	require.Len(t, f.workspace.reclaimable, 1, "error=%v reclaimable=%v effects=%#v", err, f.workspace.reclaimable, f.publisher.Calls())
	require.Empty(t, f.publisher.Calls(), "error=%v reclaimable=%v effects=%#v", err, f.workspace.reclaimable, f.publisher.Calls())
	require.Equal(t, f.workspace.proposedEntryID, f.workspace.mutation.EntryID, "error=%v reclaimable=%v effects=%#v", err, f.workspace.reclaimable, f.publisher.Calls())
	require.NotEqual(t, entryID, f.workspace.mutation.EntryID, "error=%v reclaimable=%v effects=%#v", err, f.workspace.reclaimable, f.publisher.Calls())
	require.Equal(t, f.workspace.proposedVersion, f.workspace.ready.ContentVersion, "error=%v reclaimable=%v effects=%#v", err, f.workspace.reclaimable, f.publisher.Calls())
	require.Equal(t, version, resultVersion(f.workspace.mutationResult), "error=%v reclaimable=%v effects=%#v", err, f.workspace.reclaimable, f.publisher.Calls())
}

func TestWorkspaceDirectoryReplayAcceptsRetainedEntryInsteadOfFreshProposal(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	workspaceID, retainedID := model.NewExamAttemptWorkspaceID(), model.NewAttemptWorkspaceEntryID()
	f.workspace.target = &store.ExamAttemptWorkspaceMutationTarget{ExamID: f.sitting.ExamID, SittingID: f.sitting.ID,
		ClassID: f.sitting.ClassID, CandidateUserID: f.userID, WorkspaceID: workspaceID}
	f.workspace.mutationResult = workspaceMutationResultFixture(f, workspaceID, retainedID, model.StarterWorkspaceEntryDirectory,
		model.AttemptWorkspaceMutationCreateDirectory, "", "src", "", true)
	f.workspace.proposedEntryID = model.NewAttemptWorkspaceEntryID()
	result, err := f.service.CreateCandidateExamWorkspaceDirectory(context.Background(), f.invocation, CreateWorkspaceDirectoryCommand{Access: validWorkspaceMutationAccess(f), Path: "src", IdempotencyKey: "test-key"})
	require.NoError(t, err, "result=%#v mutation=%#v error", result, f.workspace.mutation)
	require.NotNil(t, result.Entry, "result=%#v mutation=%#v error", result, f.workspace.mutation)
	require.Equal(t, retainedID, result.Entry.EntryID, "result=%#v mutation=%#v error", result, f.workspace.mutation)
	require.Equal(t, f.workspace.proposedEntryID, f.workspace.mutation.EntryID, "result=%#v mutation=%#v error", result, f.workspace.mutation)
	require.NotEqual(t, retainedID, f.workspace.mutation.EntryID, "result=%#v mutation=%#v error", result, f.workspace.mutation)
	require.Empty(t, f.publisher.Calls(), "result=%#v mutation=%#v error", result, f.workspace.mutation)
}

func TestWorkspaceReadyObjectReclamationDistinguishesStableAndUnknownApplyOutcomes(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name        string
		err         error
		wantCode    string
		wantReclaim int
	}{
		{name: "stable conflict", err: store.NewErrConflict("attempt_workspace", "attempt_workspace_path", errors.New("collision")),
			wantCode: "exam.attempt.workspace.path_conflict", wantReclaim: 1},
		{name: "outcome unknown", err: errors.New("database acknowledgement lost"),
			wantCode: "exam.attempt.unavailable", wantReclaim: 0},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			f := newFixture(t)
			f.workspace.target = &store.ExamAttemptWorkspaceMutationTarget{ExamID: f.sitting.ExamID, SittingID: f.sitting.ID,
				ClassID: f.sitting.ClassID, CandidateUserID: f.userID, WorkspaceID: model.NewExamAttemptWorkspaceID()}
			f.workspace.mutationErr = test.err
			checksum := strings.Repeat("d", 64)
			f.content.attemptContent = &model.AttemptWorkspaceContent{MediaType: "text/plain", SizeBytes: 1, SHA256: checksum}
			_, err := f.service.CreateCandidateExamWorkspaceFile(context.Background(), f.invocation, CreateWorkspaceFileCommand{Access: validWorkspaceMutationAccess(f),
				Path: "work.txt", MediaType: "text/plain", ExpectedSHA256: checksum, Body: strings.NewReader("x"), Size: 1,
				IdempotencyKey: "test-key"})
			var fault *apperror.Error
			require.ErrorAs(t, err, &fault)
			require.Equal(t, test.wantCode, fault.Code())
			require.Len(t, f.workspace.reclaimable, test.wantReclaim)
		})
	}
}

func TestWorkspaceMetadataMutationsCarrySelectiveEntryFences(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name      string
		operation model.AttemptWorkspaceMutationKind
		invoke    func(*fixture, model.AttemptWorkspaceEntryID, model.WorkspaceContentVersion) error
		check     func(*testing.T, *store.ExamAttemptWorkspaceMutation)
	}{
		{name: "move", operation: model.AttemptWorkspaceMutationMoveEntry, invoke: func(f *fixture, id model.AttemptWorkspaceEntryID, _ model.WorkspaceContentVersion) error {
			_, err := f.service.MoveCandidateExamWorkspaceEntry(context.Background(), f.invocation, MoveWorkspaceEntryCommand{Access: validWorkspaceMutationAccess(f),
				EntryID: id, ExpectedPath: "old", DestinationPath: "new", IdempotencyKey: "test-key"})
			return err
		}, check: func(t *testing.T, mutation *store.ExamAttemptWorkspaceMutation) {
			require.Equal(t, "old", mutation.ExpectedPath)
			require.Equal(t, "new", mutation.DestinationPath)
		}},
		{name: "delete_file", operation: model.AttemptWorkspaceMutationDeleteEntry, invoke: func(f *fixture, id model.AttemptWorkspaceEntryID, version model.WorkspaceContentVersion) error {
			_, err := f.service.DeleteCandidateExamWorkspaceEntry(context.Background(), f.invocation, DeleteWorkspaceEntryCommand{Access: validWorkspaceMutationAccess(f),
				EntryID: id, ExpectedPath: "old", ExpectedContentVersion: version, IdempotencyKey: "test-key"})
			return err
		}, check: func(t *testing.T, mutation *store.ExamAttemptWorkspaceMutation) {
			require.Equal(t, "old", mutation.ExpectedPath)
			require.False(t, mutation.ExpectedContentVersion.IsZero())
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			f := newFixture(t)
			workspaceID, entryID, version := model.NewExamAttemptWorkspaceID(), model.NewAttemptWorkspaceEntryID(), model.NewWorkspaceContentVersion()
			f.workspace.target = &store.ExamAttemptWorkspaceMutationTarget{ExamID: f.sitting.ExamID, SittingID: f.sitting.ID,
				ClassID: f.sitting.ClassID, CandidateUserID: f.userID, WorkspaceID: workspaceID}
			f.workspace.mutationResult = workspaceMutationResultFixture(f, workspaceID, entryID, model.StarterWorkspaceEntryFile,
				test.operation, "old", map[bool]string{true: "new"}[test.operation == model.AttemptWorkspaceMutationMoveEntry], version, false)
			if test.operation == model.AttemptWorkspaceMutationDeleteEntry {
				f.workspace.mutationResult.Entry = nil
			}
			require.NoError(t, test.invoke(f, entryID, version))
			test.check(t, f.workspace.mutation)
			var semantic any
			switch test.operation {
			case model.AttemptWorkspaceMutationMoveEntry:
				semantic = struct{ EntryID, ExpectedPath, DestinationPath string }{entryID.String(), "old", "new"}
			case model.AttemptWorkspaceMutationDeleteEntry:
				semantic = struct{ EntryID, ExpectedPath, ExpectedContentVersion string }{entryID.String(), "old", version.String()}
			}
			want, prepareErr := prepareWorkspaceMutationIdempotency(f.invocation, "test-key", f.attemptID, test.operation, semantic)
			require.NoError(t, prepareErr)
			require.Equal(t, want, f.workspace.idempotency)
		})
	}
}
