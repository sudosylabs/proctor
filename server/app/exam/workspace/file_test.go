// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package workspace

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/sudosylabs/proctor/server/app/realtime/realtimetest"
	"github.com/sudosylabs/proctor/server/model"
	"github.com/sudosylabs/proctor/server/store"
)

func TestServiceCreatesAFileOnlyAfterOpaqueContentAndDurableFinalize(t *testing.T) {
	t.Parallel()
	fixture := newServiceFixture(t)
	body := []byte("package main\n")
	result, err := fixture.service.CreateExamStarterWorkspaceFile(context.Background(), fixture.invocation, CreateFileCommand{
		ExamID: fixture.examID, ExpectedDraftRevision: 1, Path: "cmd/main.go", MediaType: "text/x-go",
		ExpectedSHA256: strings.Repeat("a", 64), Body: bytes.NewReader(body), Size: int64(len(body)), IdempotencyKey: "test-key",
	})
	require.NoError(t, err)
	require.Equal(t, "cmd/main.go", result.Entry.Path, "result/store = %#v/%#v", result, fixture.persistence)
	require.Equal(t, int64(2), result.DraftRevision, "result/store = %#v/%#v", result, fixture.persistence)
	require.NotNil(t, fixture.persistence.reservation, "result/store = %#v/%#v", result, fixture.persistence)
	require.NotNil(t, fixture.persistence.mutation, "result/store = %#v/%#v", result, fixture.persistence)
	require.True(t, fixture.content.stagedBeforeFinalize, "content must be staged before the mutation is finalized")
	// Contract: the committed create publishes exactly one change, naming the
	// new entry, the committed Draft revision and the mutation instant.
	require.Equal(t, []realtimetest.Call{
		starterWorkspaceChanged(t, fixture.examID, result.Entry.ID, 2, ChangeFileCreated, time.Date(2026, 8, 14, 10, 0, 0, 0, time.UTC)),
	}, fixture.publisher.Calls())
	require.Nil(t, fixture.auditor.values["path"])
	require.Nil(t, fixture.auditor.values["sha256"])
	wantIdempotency, prepareErr := prepareWorkspaceIdempotency(fixture.invocation, idempotencyOperationCreateFile,
		"test-key", fixture.examID, 1, "", "", "cmd/main.go", "text/x-go", int64(len(body)), strings.Repeat("a", 64))
	require.NoError(t, prepareErr)
	require.Equal(t, wantIdempotency, fixture.persistence.idempotency)
}

func TestFileFinalizationPreservesReservationMicroseconds(t *testing.T) {
	t.Parallel()
	fixture := newServiceFixture(t)
	at := time.Date(2026, 8, 14, 10, 0, 0, 123456789, time.UTC)
	fixture.service.now = func() time.Time { return at }
	result, err := fixture.service.CreateExamStarterWorkspaceFile(context.Background(), fixture.invocation, CreateFileCommand{
		ExamID: fixture.examID, ExpectedDraftRevision: 1, Path: "empty.txt", MediaType: "text/plain",
		ExpectedSHA256: strings.Repeat("a", 64), Body: bytes.NewReader(nil), Size: 0, IdempotencyKey: "microsecond-file",
	})
	require.NoError(t, err)
	want := model.TimeUTC(at)
	require.True(t, fixture.persistence.reservation.Object.CreatedAt.Equal(want), "reservation, finalization, and publication must preserve the same microsecond instant")
	require.True(t, fixture.persistence.mutation.ChangedAt.Equal(want), "reservation, finalization, and publication must preserve the same microsecond instant")
	// Contract: the one published change carries the same microsecond instant.
	require.Equal(t, []realtimetest.Call{
		starterWorkspaceChanged(t, fixture.examID, result.Entry.ID, result.DraftRevision, ChangeFileCreated, want),
	}, fixture.publisher.Calls())
}

func TestServiceDoesNotDeleteOrReclaimAStagedObjectAfterUnknownFinalize(t *testing.T) {
	t.Parallel()
	fixture := newServiceFixture(t)
	fixture.persistence.finalizeErr = context.DeadlineExceeded
	_, err := fixture.service.CreateExamStarterWorkspaceFile(context.Background(), fixture.invocation, CreateFileCommand{
		ExamID: fixture.examID, ExpectedDraftRevision: 1, Path: "main.go", MediaType: "text/x-go",
		ExpectedSHA256: strings.Repeat("a", 64), Body: strings.NewReader("x"), Size: 1, IdempotencyKey: "test-key",
	})
	require.Equal(t, "exam.starter_workspace.unavailable", faultCode(err), "error/reclaimed")
	require.False(t, fixture.persistence.reclaimed, "error/reclaimed")
}

func TestServiceExactCreateFileRetryReturnsCommittedOutcomeWithoutRepublishing(t *testing.T) {
	t.Parallel()
	fixture := newServiceFixture(t)
	command := CreateFileCommand{ExamID: fixture.examID, ExpectedDraftRevision: 1, Path: "main.go", MediaType: "text/plain",
		ExpectedSHA256: strings.Repeat("a", 64), Size: 1, IdempotencyKey: "test-key"}
	command.Body = strings.NewReader("x")
	first, err := fixture.service.CreateExamStarterWorkspaceFile(context.Background(), fixture.invocation, command)
	require.NoError(t, err)
	command.Body = strings.NewReader("x")
	replayed, err := fixture.service.CreateExamStarterWorkspaceFile(context.Background(), fixture.invocation, command)
	require.NoError(t, err)
	require.True(t, replayed.Replayed, "first=%#v replayed=%#v", first, replayed)
	require.Equal(t, first.Entry.ID, replayed.Entry.ID, "first=%#v replayed=%#v", first, replayed)
	require.NotNil(t, replayed.Object, "first=%#v replayed=%#v", first, replayed)
	require.NotNil(t, first.Object, "first=%#v replayed=%#v", first, replayed)
	require.Equal(t, first.Object.ID, replayed.Object.ID, "first=%#v replayed=%#v", first, replayed)
	require.Equal(t, first.DraftRevision, replayed.DraftRevision, "first=%#v replayed=%#v", first, replayed)
	require.Equal(t, 2, fixture.persistence.finalizeCalls, "store=%#v content=%#v", fixture.persistence, fixture.content)
	require.Len(t, fixture.persistence.reservations, 2, "store=%#v content=%#v", fixture.persistence, fixture.content)
	require.Len(t, fixture.content.stagedIDs, 2, "store=%#v content=%#v", fixture.persistence, fixture.content)
	require.Len(t, fixture.persistence.reclaimedIDs, 1, "store=%#v content=%#v", fixture.persistence, fixture.content)
	require.Equal(t, fixture.content.stagedIDs[1], fixture.persistence.reclaimedIDs[0], "store=%#v content=%#v", fixture.persistence, fixture.content)
	// Contract: the first commit publishes one change and the replayed retry
	// publishes nothing.
	require.Equal(t, []realtimetest.Call{
		starterWorkspaceChanged(t, fixture.examID, first.Entry.ID, first.DraftRevision, ChangeFileCreated, time.Date(2026, 8, 14, 10, 0, 0, 0, time.UTC)),
	}, fixture.publisher.Calls())
}

func TestServiceExactReplaceFileRetryReturnsCommittedOutcomeWithoutRepublishing(t *testing.T) {
	t.Parallel()
	fixture := newServiceFixture(t)
	command := ReplaceFileCommand{ExamID: fixture.examID, EntryID: model.NewStarterWorkspaceEntryID(), ExpectedDraftRevision: 1,
		ExpectedContentVersion: model.NewWorkspaceContentVersion(), MediaType: "text/plain", ExpectedSHA256: strings.Repeat("a", 64), Size: 1,
		IdempotencyKey: "test-key"}
	command.Body = strings.NewReader("x")
	first, err := fixture.service.ReplaceExamStarterWorkspaceFile(context.Background(), fixture.invocation, command)
	require.NoError(t, err)
	command.Body = strings.NewReader("x")
	replayed, err := fixture.service.ReplaceExamStarterWorkspaceFile(context.Background(), fixture.invocation, command)
	require.NoError(t, err)
	require.True(t, replayed.Replayed, "first=%#v replayed=%#v", first, replayed)
	require.Equal(t, first.Entry.ID, replayed.Entry.ID, "first=%#v replayed=%#v", first, replayed)
	require.NotNil(t, replayed.Object, "first=%#v replayed=%#v", first, replayed)
	require.NotNil(t, first.Object, "first=%#v replayed=%#v", first, replayed)
	require.Equal(t, first.Object.ID, replayed.Object.ID, "first=%#v replayed=%#v", first, replayed)
	require.Equal(t, first.DraftRevision, replayed.DraftRevision, "first=%#v replayed=%#v", first, replayed)
	require.Len(t, fixture.persistence.reclaimedIDs, 1, "store=%#v content=%#v", fixture.persistence, fixture.content)
	require.Equal(t, fixture.content.stagedIDs[1], fixture.persistence.reclaimedIDs[0], "store=%#v content=%#v", fixture.persistence, fixture.content)
	// Contract: the first commit publishes one change and the replayed retry
	// publishes nothing.
	require.Equal(t, []realtimetest.Call{
		starterWorkspaceChanged(t, fixture.examID, first.Entry.ID, first.DraftRevision, ChangeFileReplaced, time.Date(2026, 8, 14, 10, 0, 0, 0, time.UTC)),
	}, fixture.publisher.Calls())
}

func TestServiceReplaceRequiresAndCarriesExpectedContentVersion(t *testing.T) {
	t.Parallel()
	fixture := newServiceFixture(t)
	expected := model.WorkspaceContentVersion("wwwwwwwwwwwwwwwwwwwwwwwwww")
	result, err := fixture.service.ReplaceExamStarterWorkspaceFile(context.Background(), fixture.invocation, ReplaceFileCommand{
		ExamID: fixture.examID, EntryID: model.StarterWorkspaceEntryID("dddddddddddddddddddddddddd"), ExpectedDraftRevision: 1,
		ExpectedContentVersion: expected, MediaType: "text/plain", ExpectedSHA256: strings.Repeat("a", 64), Body: strings.NewReader("x"), Size: 1,
		IdempotencyKey: "test-key",
	})
	require.NoError(t, err, "result=%#v err=%v mutation", result, err)
	require.Equal(t, int64(2), result.DraftRevision, "result=%#v err=%v mutation", result, err)
	require.NotNil(t, fixture.persistence.mutation, "result=%#v err=%v mutation", result, err)
	require.Equal(t, expected, fixture.persistence.mutation.ExpectedContentVersion, "result=%#v err=%v mutation", result, err)
	wantIdempotency, prepareErr := prepareWorkspaceIdempotency(fixture.invocation, idempotencyOperationReplaceFile,
		"test-key", fixture.examID, 1, expected, "dddddddddddddddddddddddddd", "", "text/plain", 1, strings.Repeat("a", 64))
	require.NoError(t, prepareErr)
	require.Equal(t, wantIdempotency, fixture.persistence.idempotency)
}

func TestServiceMapsStaleContentVersionAndReclaimsNewObject(t *testing.T) {
	t.Parallel()
	fixture := newServiceFixture(t)
	fixture.persistence.finalizeErr = store.NewErrConflict("exam_starter_workspace_entry", "workspace_content_version", nil)
	_, err := fixture.service.ReplaceExamStarterWorkspaceFile(context.Background(), fixture.invocation, ReplaceFileCommand{
		ExamID: fixture.examID, EntryID: model.StarterWorkspaceEntryID("dddddddddddddddddddddddddd"), ExpectedDraftRevision: 1,
		ExpectedContentVersion: model.WorkspaceContentVersion("wwwwwwwwwwwwwwwwwwwwwwwwww"), MediaType: "text/plain",
		ExpectedSHA256: strings.Repeat("a", 64), Body: strings.NewReader("x"), Size: 1,
		IdempotencyKey: "test-key",
	})
	require.Equal(t, "exam.starter_workspace.content_conflict", faultCode(err))
	require.True(t, fixture.persistence.reclaimed)
}

func TestServiceClassifiesInvalidStagedBytesSeparatelyFromBackendFailure(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name string
		err  error
		code string
	}{
		{name: "invalid", err: fakeInvalidWorkspaceContentError{}, code: "exam.starter_workspace.invalid"},
		{name: "backend", err: errors.New("opaque backend failure"), code: "exam.starter_workspace.unavailable"},
	} {
		t.Run(test.name, func(t *testing.T) {
			fixture := newServiceFixture(t)
			fixture.content.stageErr = test.err
			_, err := fixture.service.CreateExamStarterWorkspaceFile(context.Background(), fixture.invocation, CreateFileCommand{
				ExamID: fixture.examID, ExpectedDraftRevision: 1, Path: "main.go", MediaType: "text/plain",
				ExpectedSHA256: strings.Repeat("a", 64), Body: strings.NewReader("x"), Size: 1,
				IdempotencyKey: "test-key",
			})
			require.Equal(t, test.code, faultCode(err))
			require.True(t, fixture.persistence.reclaimed)
		})
	}
}

type fakeInvalidWorkspaceContentError struct{}

func (fakeInvalidWorkspaceContentError) Error() string { return "invalid content" }

func (fakeInvalidWorkspaceContentError) InvalidStarterWorkspaceContent() bool { return true }
