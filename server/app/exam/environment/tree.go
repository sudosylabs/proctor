// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package environment

import (
	"maps"
	"strings"

	"github.com/sudosylabs/proctor/server/model"
)

const (
	kindFile      = model.StarterWorkspaceEntryFile
	kindDirectory = model.StarterWorkspaceEntryDirectory
)

// entry is one Attempt Workspace entry as the owner last read it. SHA256 and
// Size are empty when a journal entry changed the content and nothing has
// read it since.
type entry struct {
	ID      model.AttemptWorkspaceEntryID
	Kind    model.StarterWorkspaceEntryKind
	Path    string
	Version model.WorkspaceContentVersion
	SHA256  string
	Size    int64
	// pending marks an entry a harvest batch is creating, whose ID the
	// Workspace has not given yet.
	pending bool
}

// tree is the Workspace as of Cursor, rebuilt from the manifest and the
// journal on every start: a read view, never an authority.
type tree struct {
	Cursor int64
	byID   map[model.AttemptWorkspaceEntryID]*entry
	byPath map[string]*entry
}

func newTree(cursor int64) *tree {
	return &tree{Cursor: cursor, byID: map[model.AttemptWorkspaceEntryID]*entry{}, byPath: map[string]*entry{}}
}

func (t *tree) clone() *tree {
	copied := newTree(t.Cursor)
	for _, item := range t.byID {
		duplicate := *item
		copied.byID[duplicate.ID] = &duplicate
		copied.byPath[duplicate.Path] = &duplicate
	}
	return copied
}

func (t *tree) add(item entry) {
	stored := item
	t.byID[stored.ID] = &stored
	t.byPath[stored.Path] = &stored
}

func (t *tree) remove(item *entry) {
	for path, child := range maps.Clone(t.byPath) {
		if path == item.Path || strings.HasPrefix(path, item.Path+"/") {
			delete(t.byID, child.ID)
			delete(t.byPath, path)
		}
	}
}

func (t *tree) move(item *entry, destination string) {
	from := item.Path
	for path, child := range maps.Clone(t.byPath) {
		if path == from || strings.HasPrefix(path, from+"/") {
			delete(t.byPath, path)
			child.Path = destination + strings.TrimPrefix(path, from)
			t.byPath[child.Path] = child
		}
	}
}

// apply advances the tree by one journal change. sha and size are the new
// content's when known.
func (t *tree) apply(change model.AttemptWorkspaceJournalEntry, sha string, size int64) {
	switch change.Operation {
	case model.AttemptWorkspaceMutationCreateFile:
		t.add(entry{ID: change.EntryID, Kind: kindFile, Path: change.NewPath, Version: change.ContentVersion, SHA256: sha, Size: size})
	case model.AttemptWorkspaceMutationCreateDirectory:
		t.add(entry{ID: change.EntryID, Kind: kindDirectory, Path: change.NewPath})
	case model.AttemptWorkspaceMutationReplaceFile:
		if item := t.byID[change.EntryID]; item != nil {
			item.Version, item.SHA256, item.Size = change.ContentVersion, sha, size
		}
	case model.AttemptWorkspaceMutationMoveEntry:
		if item := t.byID[change.EntryID]; item != nil {
			t.move(item, change.NewPath)
		}
	case model.AttemptWorkspaceMutationDeleteEntry:
		if item := t.byID[change.EntryID]; item != nil {
			t.remove(item)
		}
	}
	t.Cursor = change.Cursor
}

// projected is what the machine holds: the journal cursor and each file's
// content version. Entries a recursive delete removed may linger in
// Versions until the next save prunes them; their IDs are never reused.
type projected struct {
	Cursor   int64
	Versions map[model.AttemptWorkspaceEntryID]model.WorkspaceContentVersion
}

// advance records that change reached the machine.
func (p *projected) advance(change model.AttemptWorkspaceJournalEntry) {
	switch change.Operation {
	case model.AttemptWorkspaceMutationCreateFile, model.AttemptWorkspaceMutationReplaceFile:
		p.Versions[change.EntryID] = change.ContentVersion
	case model.AttemptWorkspaceMutationDeleteEntry:
		delete(p.Versions, change.EntryID)
	}
	p.Cursor = change.Cursor
}

// projectionMutation is the machine's journal mutation for change, against
// the projected versions before it. The same change always gives the same
// mutation, which is how a harvested change's echo is recognized: the owner
// confirms this mutation for the observation, and applies it again when
// the journal reaches it. content names the uploaded bytes of a create or
// replace.
func projectionMutation(
	before map[model.AttemptWorkspaceEntryID]model.WorkspaceContentVersion,
	change model.AttemptWorkspaceJournalEntry,
	content *ContentRef,
) ProjectionMutation {
	previous := string(before[change.EntryID])
	mutation := ProjectionMutation{Cursor: change.Cursor, EntryID: change.EntryID, Kind: change.EntryKind}
	if mutation.Kind != kindDirectory {
		mutation.Kind = kindFile
	}
	file := mutation.Kind == kindFile
	switch change.Operation {
	case model.AttemptWorkspaceMutationCreateFile, model.AttemptWorkspaceMutationCreateDirectory:
		mutation.Operation, mutation.Path = ProjectionCreate, change.NewPath
		if file {
			mutation.ResultingVersion, mutation.Content = string(change.ContentVersion), content
		}
	case model.AttemptWorkspaceMutationReplaceFile:
		mutation.Operation, mutation.Path = ProjectionWrite, change.NewPath
		mutation.ExpectedVersion = previous
		mutation.ResultingVersion, mutation.Content = string(change.ContentVersion), content
	case model.AttemptWorkspaceMutationMoveEntry:
		mutation.Operation, mutation.Path, mutation.DestinationPath = ProjectionMove, change.OldPath, change.NewPath
		if file {
			// A move keeps the file's content, and its journal entry names it.
			mutation.ExpectedVersion = string(change.ContentVersion)
		}
	case model.AttemptWorkspaceMutationDeleteEntry:
		mutation.Operation, mutation.Path = ProjectionDelete, change.OldPath
		if file {
			mutation.ExpectedVersion = previous
		} else {
			recursive := change.Recursive
			mutation.Recursive = &recursive
		}
	}
	return mutation
}

// carriesContent reports whether a journal change has a body.
func carriesContent(operation model.AttemptWorkspaceMutationKind) bool {
	return operation == model.AttemptWorkspaceMutationCreateFile || operation == model.AttemptWorkspaceMutationReplaceFile
}
