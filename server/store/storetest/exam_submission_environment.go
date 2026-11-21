// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package storetest

import (
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/sudosylabs/proctor/server/model"
	"github.com/sudosylabs/proctor/server/store"
)

// editorSealAccess is the voluntary seal access of the editor's connection.
func editorSealAccess(editor store.ExamAttemptWorkspaceEditorAccess, revisionID model.ExamRevisionID, cursor int64) store.ExamSubmissionSealAccess {
	return store.ExamSubmissionSealAccess{AttemptID: editor.AttemptID, ParticipationID: editor.ParticipationID,
		Generation: editor.Generation, ConnectionID: editor.ConnectionID, CandidateUserID: editor.CandidateUserID,
		SessionID: editor.SessionID, ContinuityCredentialHash: editor.ContinuityCredentialHash,
		ExpectedCurrentRevisionID: revisionID, ExpectedWorkspaceCursor: cursor}
}

// testEnvironmentChangesBeforeTheSeal checks that a voluntary seal's
// expected cursor fences the candidate's and assistant's changes: the environment's
// later changes are sealed, a later editor change still conflicts, and every
// seal stops the environment.
func testEnvironmentChangesBeforeTheSeal(t *testing.T, ctx context.Context, ss store.Store, submissions store.ExamSubmissionStore) {
	t.Helper()
	directory := func(path string) store.ExamAttemptWorkspaceMutation {
		return store.ExamAttemptWorkspaceMutation{Operation: model.AttemptWorkspaceMutationCreateDirectory,
			EntryID: model.NewAttemptWorkspaceEntryID(), DestinationPath: path}
	}
	write := func(t *testing.T, fixture examAttemptFixture, access store.ExamAttemptWorkspaceWriteAccess, path string) int64 {
		t.Helper()
		applied, err := ss.ExamAttemptWorkspace().ApplyBatch(ctx, newAttemptWorkspaceBatch(t, ctx, ss, fixture, access, directory(path)),
			attemptWorkspaceBatchCommand(fixture.candidate.ID, path))
		require.NoError(t, err)
		return applied.Changes[0].Cursor
	}
	seal := func(t *testing.T, fixture examAttemptFixture, access store.ExamSubmissionSealAccess, key string) (*store.ExamSubmissionSealResult, error) {
		t.Helper()
		input := &store.ExamSubmissionSeal{SubmissionID: model.NewSubmissionID(), ReceiptCode: model.NewSubmissionReceiptCode(),
			Access: access, AuditEventID: saveExamAttemptAudit(t, ctx, ss, fixture).ID, AuditAt: model.NowUTC(),
			DomainEventID: model.NewDomainEventID(), DomainEventJobID: model.NewJobID()}
		attachSubmissionReceipt(t, ctx, ss, fixture.candidate, input)
		return submissions.Seal(ctx, input, examCommand(fixture.candidate.ID, store.ExamSubmissionSealOperation, key, key))
	}

	t.Run("voluntary seal takes the environment's later changes", func(t *testing.T) {
		fixture := newExecutionAttemptFixture(t, ctx, ss)
		connected, editor := connectEnvironmentFixture(t, ctx, ss, fixture, "seal-environment-connect")
		_, environment := ownTestEnvironment(t, ctx, ss, connected.Attempt.ID, fixture.sitting.ID, connected.Participation.Generation)
		edited := write(t, fixture, editor, "edited")
		harvested := write(t, fixture, environment, "harvested")
		require.Equal(t, edited+1, harvested)

		access := editorSealAccess(editor, fixture.sitting.ExamRevisionID, edited)
		target, err := submissions.ResolveSealTarget(ctx, access)
		require.NoError(t, err)
		sealed, err := seal(t, fixture, access, "seal-environment")
		require.NoError(t, err)
		require.True(t, sealed.ConsistentWith(target, access, sealed.Receipt.SubmissionID, sealed.Receipt.ReceiptCode))
		require.Equal(t, harvested, sealed.Receipt.WorkspaceCursor, "the harvested change is sealed")
		replayed, err := seal(t, fixture, access, "seal-environment")
		require.NoError(t, err)
		require.True(t, replayed.Replayed, "the same seal replays")
		requireEnvironmentRefused(t, ctx, ss, environment, "exam_attempt_state", "after the seal")
	})

	t.Run("voluntary seal still fences the candidate's later change", func(t *testing.T) {
		fixture := newExecutionAttemptFixture(t, ctx, ss)
		connected, editor := connectEnvironmentFixture(t, ctx, ss, fixture, "seal-editor-connect")
		_, environment := ownTestEnvironment(t, ctx, ss, connected.Attempt.ID, fixture.sitting.ID, connected.Participation.Generation)
		edited := write(t, fixture, editor, "edited")
		write(t, fixture, environment, "harvested")
		write(t, fixture, editor, "edited-again")
		_, err := seal(t, fixture, editorSealAccess(editor, fixture.sitting.ExamRevisionID, edited), "seal-stale")
		requireConflictConstraint(t, err, "attempt_workspace_cursor")
		_, err = seal(t, fixture, editorSealAccess(editor, fixture.sitting.ExamRevisionID, edited+10), "seal-future")
		requireConflictConstraint(t, err, "attempt_workspace_cursor")
	})

	t.Run("assistant changes require the candidate's current seal cursor", func(t *testing.T) {
		fixture := newExecutionAttemptFixture(t, ctx, ss)
		connected, editor := connectEnvironmentFixture(t, ctx, ss, fixture, "seal-assistant-connect")
		assistant := store.ExamAttemptWorkspaceAssistantAccess{ExamAttemptWorkspaceEditorAccess: editor,
			ConversationID: model.NewAssistantConversationID(), TurnID: model.NewAssistantTurnID(), CallID: model.NewAssistantCallID()}
		edited := write(t, fixture, editor, "edited")
		object, applied := createAttemptWorkspaceTestFile(t, ctx, ss, fixture, assistant, "assistant.txt",
			model.AttemptWorkspaceContent{MediaType: "text/plain", SizeBytes: 4, SHA256: strings.Repeat("a", 64)}, "assistant-file")
		author := assistant.WorkspaceAuthor()
		require.Equal(t, author, applied.Changes[0].Author)
		candidate := journalAccess(editor)
		journal, err := ss.ExamAttemptWorkspace().ListJournal(ctx, store.CandidateWorkspaceJournalOptions{Access: candidate, AfterCursor: edited, Limit: 200})
		require.NoError(t, err)
		require.Len(t, journal.Entries, 1)
		require.Equal(t, author, journal.Entries[0].Author)
		live, err := ss.ExamAttemptWorkspace().ResolveFile(ctx, candidate, applied.Changes[0].EntryID)
		require.NoError(t, err)
		require.Equal(t, object.ContentVersion, live.ContentVersion)

		_, err = seal(t, fixture, editorSealAccess(editor, fixture.sitting.ExamRevisionID, edited), "seal-assistant-stale")
		requireConflictConstraint(t, err, "attempt_workspace_cursor")
		sealed, err := seal(t, fixture, editorSealAccess(editor, fixture.sitting.ExamRevisionID, applied.Changes[0].Cursor), "seal-assistant-current")
		require.NoError(t, err)
		manifest, err := submissions.ListManifest(ctx, store.ExamSubmissionManifestListOptions{SubmissionID: sealed.Receipt.SubmissionID, Limit: 200})
		require.NoError(t, err)
		var found bool
		for _, item := range manifest.Items {
			if item.EntryID == applied.Changes[0].EntryID {
				found = true
				require.Equal(t, author, item.Author)
			}
		}
		require.True(t, found)
		_, err = ss.ExamAttemptWorkspace().ReserveObjects(ctx, &store.ExamAttemptWorkspaceObjectReservation{Access: assistant})
		require.Error(t, err, "a sealed attempt refuses assistant writes")
		require.Equal(t, connected.Attempt.ID, assistant.WorkspaceAttemptID())
	})

	t.Run("manager end stops the environment", func(t *testing.T) {
		fixture := newExecutionAttemptFixture(t, ctx, ss)
		connected, _ := connectEnvironmentFixture(t, ctx, ss, fixture, "manager-end-environment-connect")
		_, environment := ownTestEnvironment(t, ctx, ss, connected.Attempt.ID, fixture.sitting.ID, connected.Participation.Generation)
		request := store.ExamSubmissionManagerEndRequest{ExamID: fixture.examID, SittingID: fixture.sitting.ID,
			AttemptID: connected.Attempt.ID, ActorUserID: fixture.manager.ID,
			ExpectedAttemptRevision: connected.Attempt.Revision, PrivateReason: "candidate requested an assisted early end"}
		preparation, err := submissions.PrepareManagerEnd(ctx, request)
		require.NoError(t, err)
		requireEnvironmentWrites(t, ctx, ss, fixture, environment, "before-manager-end", "the end is only prepared")
		input := &store.ExamSubmissionManagerEnd{Request: request, Target: preparation.Target,
			SubmissionID: model.NewSubmissionID(), ReceiptCode: model.NewSubmissionReceiptCode(),
			AuditEventID: saveExamSittingAudit(t, ctx, ss, fixture.manager.ID, fixture.examID, fixture.unitID).ID,
			AuditAt:      preparation.SealAt}
		attachManagerEndedSubmissionReceipt(t, fixture.candidate, input)
		_, err = submissions.EndByManager(ctx, input,
			examCommand(fixture.manager.ID, store.ExamSubmissionManagerEndOperation, "manager-end-environment", "manager-end-environment"))
		require.NoError(t, err)
		requireEnvironmentRefused(t, ctx, ss, environment, "exam_attempt_state", "after the manager end")
	})
}
