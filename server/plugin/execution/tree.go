// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package execution

import (
	"maps"
	"strings"

	"github.com/sudosylabs/execenv"

	"github.com/sudosylabs/proctor/server/plugin"
)

const (
	kindFile      = "file"
	kindDirectory = "directory"
)

// entry is one Attempt Workspace entry as the owner knows it. SHA256 and
// Size are empty when a journal entry changed the content and nothing has
// read it since.
type entry struct {
	ID      string
	Kind    string
	Path    string
	Version string
	SHA256  string
	Size    int64
	// pending marks an entry a harvest batch is creating, whose ID the core
	// has not assigned yet.
	pending bool
}

// tree is a Workspace manifest at Cursor.
type tree struct {
	Cursor int64
	byID   map[string]*entry
	byPath map[string]*entry
}

func newTree(cursor int64) *tree {
	return &tree{Cursor: cursor, byID: map[string]*entry{}, byPath: map[string]*entry{}}
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

func (t *tree) remove(item *entry, subtree bool) {
	delete(t.byID, item.ID)
	delete(t.byPath, item.Path)
	if !subtree {
		return
	}
	for path, child := range maps.Clone(t.byPath) {
		if strings.HasPrefix(path, item.Path+"/") {
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
func (t *tree) apply(change plugin.WorkspaceChange, sha string, size int64) {
	switch change.Operation {
	case plugin.WorkspaceCreateFile:
		t.add(entry{ID: change.EntryID, Kind: kindFile, Path: change.NewPath, Version: change.ContentVersion, SHA256: sha, Size: size})
	case plugin.WorkspaceCreateDirectory:
		t.add(entry{ID: change.EntryID, Kind: kindDirectory, Path: change.NewPath})
	case plugin.WorkspaceReplaceFile:
		if item := t.byID[change.EntryID]; item != nil {
			item.Version, item.SHA256, item.Size = change.ContentVersion, sha, size
		}
	case plugin.WorkspaceMoveEntry:
		if item := t.byID[change.EntryID]; item != nil {
			t.move(item, change.NewPath)
		}
	case plugin.WorkspaceDeleteEntry:
		if item := t.byID[change.EntryID]; item != nil {
			t.remove(item, true)
		}
	}
	t.Cursor = change.Cursor
}

// projected is what the owner has projected into the grant: the journal
// cursor and each projected file's content version. Entries a recursive
// delete removed may linger in Versions; their IDs are never reused.
type projected struct {
	Cursor   int64
	Versions map[string]string
}

// advance records that change reached the grant.
func (p *projected) advance(change plugin.WorkspaceChange) {
	switch change.Operation {
	case plugin.WorkspaceCreateFile, plugin.WorkspaceReplaceFile:
		p.Versions[change.EntryID] = change.ContentVersion
	case plugin.WorkspaceDeleteEntry:
		delete(p.Versions, change.EntryID)
	}
	p.Cursor = change.Cursor
}

// projectionMutation is the execenv journal mutation for change, against
// the projection before it. The same change always gives the same
// mutation, which is how a harvested change's echo is recognized: the owner
// confirms this mutation for the observation, and applies it again when
// the journal reaches it. content names the uploaded bytes of a create or
// replace.
func projectionMutation(before map[string]string, change plugin.WorkspaceChange, content *execenv.ContentReference) execenv.ProjectionMutation {
	kind := execenv.ProjectionFile
	if change.EntryKind == kindDirectory {
		kind = execenv.ProjectionDirectory
	}
	previous := execenv.Version(before[change.EntryID])
	mutation := execenv.ProjectionMutation{
		JournalCursor: uint64(change.Cursor), EntryID: change.EntryID, Kind: kind,
	}
	switch change.Operation {
	case plugin.WorkspaceCreateFile, plugin.WorkspaceCreateDirectory:
		mutation.Operation, mutation.Path = execenv.ProjectionCreate, change.NewPath
		if kind == execenv.ProjectionFile {
			mutation.ResultingContentVersion, mutation.Content = execenv.Version(change.ContentVersion), content
		}
	case plugin.WorkspaceReplaceFile:
		mutation.Operation, mutation.Path = execenv.ProjectionWrite, change.NewPath
		mutation.ExpectedContentVersion = previous
		mutation.ResultingContentVersion, mutation.Content = execenv.Version(change.ContentVersion), content
	case plugin.WorkspaceMoveEntry:
		mutation.Operation, mutation.Path, mutation.DestinationPath = execenv.ProjectionMove, change.OldPath, change.NewPath
		if kind == execenv.ProjectionFile {
			mutation.ExpectedContentVersion = previous
		}
	case plugin.WorkspaceDeleteEntry:
		mutation.Operation, mutation.Path = execenv.ProjectionDelete, change.OldPath
		if kind == execenv.ProjectionFile {
			mutation.ExpectedContentVersion = previous
		} else {
			recursive := change.Recursive
			mutation.Recursive = &recursive
		}
	}
	return mutation
}
