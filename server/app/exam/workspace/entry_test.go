// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package workspace

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/sudosylabs/proctor/server/model"
)

func TestMetadataWorkspaceMutationsPassOwnedIdempotencyToStore(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name      string
		operation string
		invoke    func(*serviceFixture, model.StarterWorkspaceEntryID) error
		path      string
	}{
		{name: "create directory", operation: idempotencyOperationCreateDirectory, path: "src", invoke: func(f *serviceFixture, _ model.StarterWorkspaceEntryID) error {
			_, err := f.service.CreateExamStarterWorkspaceDirectory(context.Background(), f.invocation, CreateDirectoryCommand{ExamID: f.examID,
				ExpectedDraftRevision: 1, Path: "src", IdempotencyKey: "test-key"})
			return err
		}},
		{name: "move entry", operation: idempotencyOperationMoveEntry, path: "src/main.go", invoke: func(f *serviceFixture, id model.StarterWorkspaceEntryID) error {
			_, err := f.service.MoveExamStarterWorkspaceEntry(context.Background(), f.invocation, MoveEntryCommand{ExamID: f.examID, EntryID: id,
				ExpectedDraftRevision: 1, Path: "src/main.go", IdempotencyKey: "test-key"})
			return err
		}},
		{name: "remove entry", operation: idempotencyOperationRemoveEntry, invoke: func(f *serviceFixture, id model.StarterWorkspaceEntryID) error {
			_, err := f.service.RemoveExamStarterWorkspaceEntry(context.Background(), f.invocation, RemoveEntryCommand{ExamID: f.examID, EntryID: id,
				ExpectedDraftRevision: 1, IdempotencyKey: "test-key"})
			return err
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture := newServiceFixture(t)
			entryID := model.NewStarterWorkspaceEntryID()
			require.NoError(t, test.invoke(fixture, entryID))
			semanticEntryID := ""
			if test.operation != idempotencyOperationCreateDirectory {
				semanticEntryID = entryID.String()
			}
			want, err := prepareWorkspaceIdempotency(fixture.invocation, test.operation, "test-key", fixture.examID,
				1, "", semanticEntryID, test.path, "", 0, "")
			require.NoError(t, err)
			require.Equal(t, want, fixture.persistence.idempotency)
		})
	}
}

func TestServiceRejectsInvalidPathsBeforeAuthorizationOrStorage(t *testing.T) {
	t.Parallel()
	fixture := newServiceFixture(t)
	_, err := fixture.service.CreateExamStarterWorkspaceDirectory(context.Background(), fixture.invocation, CreateDirectoryCommand{
		ExamID: fixture.examID, ExpectedDraftRevision: 1, Path: "../escape",
		IdempotencyKey: "test-key",
	})
	require.Equal(t, "exam.starter_workspace.invalid", faultCode(err), "error/auth/store")
	require.False(t, fixture.authorizer.called, "error/auth/store")
	require.Nil(t, fixture.persistence.mutation, "error/auth/store")
}
