// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package environment

import (
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/sudosylabs/proctor/server/model"
)

func TestCoalesceKeepsOnlyWhatTheWorkspaceNeeds(t *testing.T) {
	t.Parallel()
	observations := []Observation{
		{HostSequence: 1, Operation: ObserveWriteFile, NodeIdentity: "a", Kind: kindFile, Path: "a.txt"},
		{HostSequence: 2, Operation: ObserveWriteFile, NodeIdentity: "a", Kind: kindFile, Path: "a.txt"},
		{HostSequence: 3, Operation: ObserveCreateFile, NodeIdentity: "t", Kind: kindFile, Path: "tmp"},
		{HostSequence: 4, Operation: ObserveWriteFile, NodeIdentity: "t", Kind: kindFile, Path: "tmp"},
		{HostSequence: 5, Operation: ObserveDelete, NodeIdentity: "t", Kind: kindFile, Path: "tmp"},
		{HostSequence: 6, Operation: ObserveMove, NodeIdentity: "a", Kind: kindFile, Path: "a.txt", DestinationPath: "b.txt"},
		{HostSequence: 7, Operation: ObserveWriteFile, NodeIdentity: "a", Kind: kindFile, Path: "b.txt"},
	}
	var kept []uint64
	for _, observation := range coalesce(observations) {
		kept = append(kept, observation.HostSequence)
	}
	require.Equal(t, []uint64{2, 6, 7}, kept, "the last write before a move, and no trace of a file created and deleted")
}

func TestProjectionMutationIsStableForTheSameChange(t *testing.T) {
	t.Parallel()
	entryID, before, after := model.NewAttemptWorkspaceEntryID(), model.NewWorkspaceContentVersion(), model.NewWorkspaceContentVersion()
	versions := map[model.AttemptWorkspaceEntryID]model.WorkspaceContentVersion{entryID: before}
	content := &ContentRef{Size: 3, SHA256: strings.Repeat("a", 64), TransferID: "t1"}
	replace := model.AttemptWorkspaceJournalEntry{Cursor: 9, EntryID: entryID, EntryKind: kindFile,
		Operation: model.AttemptWorkspaceMutationReplaceFile, OldPath: "a.py", NewPath: "a.py", ContentVersion: after}
	mutation := projectionMutation(versions, replace, content)
	require.Equal(t, ProjectionMutation{Cursor: 9, EntryID: entryID, Operation: ProjectionWrite, Kind: kindFile,
		Path: "a.py", ExpectedVersion: string(before), ResultingVersion: string(after), Content: content}, mutation)
	require.Equal(t, mutation, projectionMutation(versions, replace, content))

	move := model.AttemptWorkspaceJournalEntry{Cursor: 10, EntryID: entryID, EntryKind: kindFile,
		Operation: model.AttemptWorkspaceMutationMoveEntry, OldPath: "a.py", NewPath: "b.py", ContentVersion: after}
	require.Equal(t, string(after), projectionMutation(nil, move, nil).ExpectedVersion, "a move names the version it keeps")

	directory := model.AttemptWorkspaceJournalEntry{Cursor: 11, EntryID: model.NewAttemptWorkspaceEntryID(), EntryKind: kindDirectory,
		Operation: model.AttemptWorkspaceMutationDeleteEntry, OldPath: "src", Recursive: true}
	deleted := projectionMutation(nil, directory, nil)
	require.Equal(t, ProjectionDelete, deleted.Operation)
	require.NotNil(t, deleted.Recursive)
	require.True(t, *deleted.Recursive)
}

func TestProcessChunksStayInsideTheEvidenceBound(t *testing.T) {
	t.Parallel()
	events := make([]ProcessEvent, 400)
	for index := range events {
		events[index] = ProcessEvent{Sequence: uint64(index + 1), Kind: "started", PID: 10, At: time.Now(),
			Argv: []string{strings.Repeat("x", 512)}}
	}
	chunks := processChunks(events)
	require.Greater(t, len(chunks), 1)
	total := 0
	for _, chunk := range chunks {
		total += len(chunk)
	}
	require.Equal(t, len(events), total, "no event is lost between chunks")
}

func TestHostReportOffersOnlyWhatFits(t *testing.T) {
	t.Parallel()
	report := HostReport{Usable: true, Images: []string{"python"}, Networks: []model.EnvironmentNetwork{model.EnvironmentNetworkNone},
		Free: model.EnvironmentResources{CPUMillis: 2000, MemoryBytes: 1 << 30, DiskBytes: 4 << 30}}
	want := model.EnvironmentResources{CPUMillis: 1000, MemoryBytes: 512 << 20, DiskBytes: 1 << 30}
	require.True(t, report.Offers("python", model.EnvironmentNetworkNone, want))
	require.False(t, report.Offers("python", model.EnvironmentNetworkAllowlist, want))
	report.GrantMax = model.EnvironmentResources{MemoryBytes: 256 << 20}
	require.False(t, report.Offers("python", model.EnvironmentNetworkNone, want), "more than one machine may have")
	report.GrantMax, report.Usable = model.EnvironmentResources{}, false
	require.False(t, report.Offers("python", model.EnvironmentNetworkNone, want))
}
