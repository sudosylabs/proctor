// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package attempt

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/sudosylabs/proctor/server/model"
	"github.com/sudosylabs/proctor/server/store"
)

func TestWorkspaceJournalReturnsOrderedRecoveryWithoutPathsInSelector(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	workspaceID, entryID := model.NewExamAttemptWorkspaceID(), model.NewAttemptWorkspaceEntryID()
	f.workspace.journalPage = &store.CandidateWorkspaceJournalPage{WorkspaceID: workspaceID, CurrentCursor: 5,
		Entries: []model.AttemptWorkspaceJournalEntry{{WorkspaceID: workspaceID, Cursor: 5, EntryID: entryID,
			EntryKind: model.StarterWorkspaceEntryDirectory, Operation: model.AttemptWorkspaceMutationCreateDirectory,
			NewPath: "src", ChangedAt: f.at}}}
	page, err := f.service.ListCandidateExamWorkspaceJournal(context.Background(), f.invocation, WorkspaceJournalQuery{Access: CandidateAccess{
		AttemptID: f.attemptID, ConnectionID: f.connectionID, ContinuityCredential: model.NewCredentialToken(),
	}, AfterCursor: 4, Limit: 20})
	require.NoError(t, err, "page=%#v options=%#v error", page, f.workspace.journalOptions)
	require.Equal(t, int64(5), page.CurrentCursor, "page=%#v options=%#v error", page, f.workspace.journalOptions)
	require.Len(t, page.Entries, 1, "page=%#v options=%#v error", page, f.workspace.journalOptions)
	require.Equal(t, int64(4), f.workspace.journalOptions.AfterCursor, "page=%#v options=%#v error", page, f.workspace.journalOptions)
}
