// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package attempt

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/sudosylabs/proctor/server/model"
)

func recursiveDelete(path string) WorkspaceBatchMutation {
	return WorkspaceBatchMutation{Operation: model.AttemptWorkspaceMutationDeleteEntry, EntryID: model.NewAttemptWorkspaceEntryID(),
		ExpectedPath: path, Recursive: true}
}

func TestRecursiveWorkspaceDeletionRequiresCandidateSnapshot(t *testing.T) {
	t.Parallel()
	zero, negative := int64(0), int64(-1)
	plainDelete := recursiveDelete("src")
	plainDelete.Recursive = false
	versioned := recursiveDelete("src")
	versioned.ExpectedContentVersion = model.NewWorkspaceContentVersion()
	for _, test := range []struct {
		name      string
		mutations []WorkspaceBatchMutation
		cursor    *int64
		field     string
		item      string
	}{
		{name: "missing cursor", mutations: []WorkspaceBatchMutation{recursiveDelete("src")}, field: "workspace_recursive_delete"},
		{name: "missing cursor later in the batch", mutations: []WorkspaceBatchMutation{createDirectory("lib"), recursiveDelete("src")},
			field: "workspace_recursive_delete"},
		{name: "negative cursor", mutations: []WorkspaceBatchMutation{recursiveDelete("src")}, cursor: &negative, field: "workspace_recursive_delete"},
		{name: "content version", mutations: []WorkspaceBatchMutation{versioned}, cursor: &zero, field: "workspace_command", item: "0"},
		{name: "cursor without recursive", mutations: []WorkspaceBatchMutation{plainDelete}, cursor: &zero, field: "workspace_recursive_delete"},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			f := newFixture(t)
			workspaceTargetFixture(f)
			_, err := applyBatch(f, nil, test.cursor, test.mutations...)
			requireBatchFault(t, err, "exam.attempt.invalid", test.field, test.item)
			require.Empty(t, f.workspace.reservations)
			require.Empty(t, f.workspace.batches)
		})
	}
}

func TestRecursiveWorkspaceDeletionCarriesSnapshotAndSemanticIdentity(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	workspaceTargetFixture(f)
	mutation := recursiveDelete("src")
	cursor := int64(0)
	result, err := applyBatch(f, nil, &cursor, createDirectory("lib"), mutation)
	require.NoError(t, err, "result=%#v error=%v", result, err)
	require.Len(t, result.Changes, 2)
	require.True(t, result.Changes[1].Recursive)
	batch := f.workspace.batch()
	require.NotNil(t, batch)
	require.True(t, batch.Mutations[1].Recursive)
	require.False(t, batch.Mutations[0].Recursive)
	require.NotNil(t, batch.ExpectedWorkspaceCursor, "the batch carries the snapshot it starts from")
	require.Equal(t, int64(0), *batch.ExpectedWorkspaceCursor)
	first := *f.workspace.idempotency

	cursor = 1
	_, err = applyBatch(f, nil, &cursor, createDirectory("lib"), mutation)
	require.NoError(t, err)
	require.Equal(t, int64(1), *f.workspace.batch().ExpectedWorkspaceCursor)
	require.NotEqual(t, first.Fingerprint, f.workspace.idempotency.Fingerprint, "recursive snapshot was not part of the semantic fingerprint")
	require.Equal(t, first.KeyDigest, f.workspace.idempotency.KeyDigest)
}
