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

	"github.com/sudosylabs/proctor/server/app/apperror"
	"github.com/sudosylabs/proctor/server/model"
	"github.com/sudosylabs/proctor/server/store"
)

func TestStarterOriginWorkspaceFileOpensPinnedStarterBytesWithAttemptObjectIdentity(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	entryID := model.NewAttemptWorkspaceEntryID()
	starterID := model.NewStarterWorkspaceObjectID()
	attemptObjectID := model.NewAttemptWorkspaceObjectID()
	version := model.NewWorkspaceContentVersion()
	f.workspace.file = &store.CandidateWorkspaceContent{
		Entry: store.CandidateAttemptWorkspaceItem{EntryID: entryID, Kind: model.StarterWorkspaceEntryFile,
			Path: "cmd/main.go", MediaType: "text/x-go", SizeBytes: 4, SHA256: strings.Repeat("a", 64)},
		StorageOrigin: model.AttemptWorkspaceStorageStarter, StarterObjectID: starterID,
		AttemptObjectID: attemptObjectID, ContentVersion: version,
	}
	opened, err := f.service.OpenCandidateExamWorkspaceFile(context.Background(), f.invocation, OpenWorkspaceFileQuery{Access: CandidateAccess{
		AttemptID: f.attemptID, ConnectionID: f.connectionID, ContinuityCredential: model.NewCredentialToken(),
	}, EntryID: entryID})
	require.NoError(t, err)
	defer opened.Body.Close()
	require.Equal(t, starterID, f.content.starterID, "opened=%#v starter", opened)
	require.Equal(t, version, opened.ContentVersion, "opened=%#v starter", opened)
	require.Equal(t, "text/x-go", opened.MediaType, "opened=%#v starter", opened)
}

func TestWorkspaceListUsesPathFreeSnapshotCursor(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	workspaceID := model.NewExamAttemptWorkspaceID()
	f.workspace.page = &store.CandidateAttemptWorkspacePage{WorkspaceID: workspaceID, Cursor: 7,
		Items: []store.CandidateAttemptWorkspaceItem{{EntryID: model.NewAttemptWorkspaceEntryID(),
			Kind: model.StarterWorkspaceEntryDirectory, Path: "cmd"}}, HasMore: true}
	page, err := f.service.ListCandidateExamWorkspace(context.Background(), f.invocation, WorkspaceQuery{Access: CandidateAccess{
		AttemptID: f.attemptID, ConnectionID: f.connectionID, ContinuityCredential: model.NewCredentialToken(),
	}, ExpectedCursor: -1, Limit: 20})
	require.NoError(t, err)
	require.Equal(t, workspaceID, page.WorkspaceID, "page=%#v options=%#v", page, f.workspace.options)
	require.Equal(t, int64(7), page.Cursor, "page=%#v options=%#v", page, f.workspace.options)
	require.Len(t, page.Items, 1, "page=%#v options=%#v", page, f.workspace.options)
	require.True(t, page.HasMore, "page=%#v options=%#v", page, f.workspace.options)
	require.False(t, page.RefreshRequired, "page=%#v options=%#v", page, f.workspace.options)
	require.Equal(t, int64(-1), f.workspace.options.ExpectedCursor, "page=%#v options=%#v", page, f.workspace.options)
	require.True(t, f.workspace.options.AfterEntryID.IsZero(), "page=%#v options=%#v", page, f.workspace.options)
}

func TestWorkspaceListRejectsUnboundedOrInvalidCursorsBeforeStore(t *testing.T) {
	t.Parallel()
	candidateAccess := CandidateAccess{ContinuityCredential: model.NewCredentialToken()}
	for _, query := range []WorkspaceQuery{
		{Access: candidateAccess, ExpectedCursor: -1, Limit: 0},
		{Access: candidateAccess, ExpectedCursor: -1, Limit: 201},
		{Access: candidateAccess, ExpectedCursor: -2, Limit: 20},
		{Access: candidateAccess, ExpectedCursor: -1, AfterEntryID: model.NewAttemptWorkspaceEntryID(), Limit: 20},
	} {
		f := newFixture(t)
		query.Access.AttemptID, query.Access.ConnectionID = f.attemptID, f.connectionID
		_, err := f.service.ListCandidateExamWorkspace(context.Background(), f.invocation, query)
		var fault *apperror.Error
		require.ErrorAs(t, err, &fault, "query=%#v error=%v calls", query, err)
		require.Equal(t, "exam.attempt.invalid", fault.Code(), "query=%#v error=%v calls", query, err)
		require.Equal(t, 0, f.workspace.lists, "query=%#v error=%v calls", query, err)
	}
}

func TestOpenAttemptOriginWorkspaceFileUsesOpaqueAttemptObject(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	entryID, objectID, version := model.NewAttemptWorkspaceEntryID(), model.NewAttemptWorkspaceObjectID(), model.NewWorkspaceContentVersion()
	f.workspace.file = &store.CandidateWorkspaceContent{Entry: store.CandidateAttemptWorkspaceItem{EntryID: entryID,
		Kind: model.StarterWorkspaceEntryFile, Path: "answer.txt", ContentVersion: version, MediaType: "text/plain",
		SizeBytes: 4, SHA256: strings.Repeat("c", 64)}, StorageOrigin: model.AttemptWorkspaceStorageAttempt,
		AttemptObjectID: objectID, ContentVersion: version}
	f.content.openAttemptBody = "work"
	opened, err := f.service.OpenCandidateExamWorkspaceFile(context.Background(), f.invocation, OpenWorkspaceFileQuery{Access: CandidateAccess{AttemptID: f.attemptID,
		ConnectionID: f.connectionID, ContinuityCredential: model.NewCredentialToken()}, EntryID: entryID})
	require.NoError(t, err)
	defer opened.Body.Close()
	require.Equal(t, objectID, f.content.openAttemptID, "opened=%#v object", opened)
	require.Equal(t, version, opened.ContentVersion, "opened=%#v object", opened)
}
