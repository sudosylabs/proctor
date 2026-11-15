// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package environment_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"maps"
	"path"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/sudosylabs/proctor/server/app/exam/environment"
	"github.com/sudosylabs/proctor/server/model"
	"github.com/sudosylabs/proctor/server/store"
)

// environmentsFake is the environment row, written by the owner under its
// revision and moved on by facts.
type environmentsFake struct {
	store.AttemptEnvironmentStore
	mu  sync.Mutex
	row model.AttemptEnvironment
}

func (f *environmentsFake) Get(_ context.Context, attemptID model.ExamAttemptID) (*model.AttemptEnvironment, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if attemptID != f.row.AttemptID {
		return nil, store.NewErrNotFound("attempt_environment", attemptID.String())
	}
	row := f.row
	row.ProjectedVersions = maps.Clone(row.ProjectedVersions)
	return &row, nil
}

func (f *environmentsFake) SaveOwner(_ context.Context, input *store.AttemptEnvironmentOwnerSave) (*model.AttemptEnvironment, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if input.ExpectedRevision != f.row.Revision {
		return nil, store.NewErrConflict("attempt_environment", "attempt_environment_revision", nil)
	}
	saved := f.row
	owner := input.Environment
	saved.State, saved.Incarnation, saved.Host, saved.GrantID, saved.Epoch = owner.State, owner.Incarnation, owner.Host,
		owner.GrantID, owner.Epoch
	saved.ControlFence, saved.ProjectionCursor, saved.ObservationCursor = owner.ControlFence, owner.ProjectionCursor,
		owner.ObservationCursor
	saved.ProcessCursor, saved.ProjectedVersions = owner.ProcessCursor, maps.Clone(owner.ProjectedVersions)
	saved.EvidenceSequence, saved.WaitingSince = owner.EvidenceSequence, owner.WaitingSince
	saved.Revision++
	saved.UpdatedAt = time.Now().UTC()
	if err := saved.Validate(); err != nil {
		return nil, store.NewErrInvalidInput("attempt_environment", "owner_save", nil).Wrap(err)
	}
	f.row = saved
	result := saved
	result.ProjectedVersions = maps.Clone(saved.ProjectedVersions)
	return &result, nil
}

func (f *environmentsFake) snapshot() model.AttemptEnvironment {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.row
}

// desire applies a fact: what the environment should be.
func (f *environmentsFake) desire(desired model.AttemptEnvironmentDesired) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.row.Desired = desired
	f.row.Revision++
}

// evidenceFake keeps every appended record.
type evidenceFake struct {
	store.ExamAttemptWorkspaceStore
	mu      sync.Mutex
	records []store.ExamAttemptEvidenceAppend
	inputs  []store.ExamAttemptTerminalInputAppend
}

func (f *evidenceFake) AppendAttemptEvidence(_ context.Context, input *store.ExamAttemptEvidenceAppend) (*store.ExamAttemptEvidenceAppendResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	author := input.Access.WorkspaceAuthor()
	if !model.ValidAttemptEvidenceClaim(input.Kind, author, input.Sequence, input.Detail) {
		return nil, store.NewErrInvalidInput("attempt_evidence", "claim", nil)
	}
	for _, record := range f.records {
		if record.Kind == input.Kind && record.Access.Incarnation == input.Access.Incarnation && record.Sequence == input.Sequence {
			return nil, store.NewErrConflict("attempt_evidence", "attempt_evidence_sequence", nil)
		}
	}
	f.records = append(f.records, *input)
	return &store.ExamAttemptEvidenceAppendResult{}, nil
}

func (f *evidenceFake) AppendTerminalInput(_ context.Context, input *store.ExamAttemptTerminalInputAppend) (*store.ExamAttemptEvidenceAppendResult, error) {
	if !input.Valid() {
		return nil, store.NewErrInvalidInput("attempt_evidence", "terminal_input", nil)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, record := range f.inputs {
		if record.Incarnation == input.Incarnation && record.Tag == input.Tag && record.Sequence == input.Sequence {
			if string(record.Detail) != string(input.Detail) {
				return nil, store.NewErrConflict("attempt_evidence", "attempt_evidence_sequence", nil)
			}
			return &store.ExamAttemptEvidenceAppendResult{Repeated: true}, nil
		}
	}
	f.inputs = append(f.inputs, *input)
	return &store.ExamAttemptEvidenceAppendResult{}, nil
}

// terminalInputs returns every recorded terminal input record.
func (f *evidenceFake) terminalInputs() []store.ExamAttemptTerminalInputAppend {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.inputs)
}

// details returns the details of every record of kind.
func (f *evidenceFake) details(kind model.AttemptEvidenceKind) []map[string]any {
	f.mu.Lock()
	defer f.mu.Unlock()
	var details []map[string]any
	for _, record := range f.records {
		if record.Kind == kind {
			var detail map[string]any
			_ = json.Unmarshal(record.Detail, &detail)
			details = append(details, detail)
		}
	}
	return details
}

func (f *evidenceFake) gaps(gap string) int {
	count := 0
	for _, detail := range f.details(model.AttemptEvidenceGap) {
		if detail["gap"] == gap {
			count++
		}
	}
	return count
}

// workspaceEntry is one entry of the Workspace fake.
type workspaceEntry struct {
	id      model.AttemptWorkspaceEntryID
	kind    model.StarterWorkspaceEntryKind
	path    string
	version model.WorkspaceContentVersion
	body    []byte
}

// workspaceFake is an Attempt Workspace with a journal and the batch rules
// the owner depends on: expected versions and paths, all or nothing, and
// idempotent replays.
type workspaceFake struct {
	mu       sync.Mutex
	entries  map[model.AttemptWorkspaceEntryID]*workspaceEntry
	journal  []model.AttemptWorkspaceJournalEntry
	outcomes map[string][]model.AttemptWorkspaceJournalEntry
	id       model.ExamAttemptWorkspaceID
}

func newWorkspaceFake() *workspaceFake {
	return &workspaceFake{entries: map[model.AttemptWorkspaceEntryID]*workspaceEntry{},
		outcomes: map[string][]model.AttemptWorkspaceJournalEntry{}, id: model.NewExamAttemptWorkspaceID()}
}

func digest(body []byte) string {
	sum := sha256.Sum256(body)
	return hex.EncodeToString(sum[:])
}

func (w *workspaceFake) byPath(name string) *workspaceEntry {
	for _, item := range w.entries {
		if item.path == name {
			return item
		}
	}
	return nil
}

// file returns a file's body, and whether the Workspace holds it.
func (w *workspaceFake) file(name string) (string, bool) {
	w.mu.Lock()
	defer w.mu.Unlock()
	item := w.byPath(name)
	if item == nil || item.kind != model.StarterWorkspaceEntryFile {
		return "", false
	}
	return string(item.body), true
}

func (w *workspaceFake) cursor() int64 {
	w.mu.Lock()
	defer w.mu.Unlock()
	return int64(len(w.journal))
}

func (w *workspaceFake) authors(name string) []model.AttemptWorkspaceAuthor {
	w.mu.Lock()
	defer w.mu.Unlock()
	var authors []model.AttemptWorkspaceAuthor
	for _, change := range w.journal {
		if change.NewPath == name || change.OldPath == name {
			authors = append(authors, change.Author)
		}
	}
	return authors
}

// edit is the candidate's editor writing a file, creating it when needed.
func (w *workspaceFake) edit(name, body string) {
	w.mu.Lock()
	defer w.mu.Unlock()
	author := model.NewCandidateWorkspaceAuthor(model.NewAttemptConnectionID())
	if item := w.byPath(name); item != nil {
		w.commit(model.AttemptWorkspaceJournalEntry{EntryID: item.id, EntryKind: item.kind, Operation: model.AttemptWorkspaceMutationReplaceFile,
			OldPath: name, NewPath: name, ContentVersion: model.NewWorkspaceContentVersion(), Author: author}, []byte(body))
		return
	}
	w.commit(model.AttemptWorkspaceJournalEntry{EntryID: model.NewAttemptWorkspaceEntryID(), EntryKind: model.StarterWorkspaceEntryFile,
		Operation: model.AttemptWorkspaceMutationCreateFile, NewPath: name, ContentVersion: model.NewWorkspaceContentVersion(),
		Author: author}, []byte(body))
}

func (w *workspaceFake) mkdir(name string) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.commit(model.AttemptWorkspaceJournalEntry{EntryID: model.NewAttemptWorkspaceEntryID(), EntryKind: model.StarterWorkspaceEntryDirectory,
		Operation: model.AttemptWorkspaceMutationCreateDirectory, NewPath: name,
		Author: model.NewCandidateWorkspaceAuthor(model.NewAttemptConnectionID())}, nil)
}

// commit applies one journal change; the caller holds the lock.
func (w *workspaceFake) commit(change model.AttemptWorkspaceJournalEntry, body []byte) model.AttemptWorkspaceJournalEntry {
	change.WorkspaceID, change.Cursor, change.ChangedAt = w.id, int64(len(w.journal))+1, time.Now().UTC()
	switch change.Operation {
	case model.AttemptWorkspaceMutationCreateFile, model.AttemptWorkspaceMutationCreateDirectory:
		w.entries[change.EntryID] = &workspaceEntry{id: change.EntryID, kind: change.EntryKind, path: change.NewPath,
			version: change.ContentVersion, body: body}
	case model.AttemptWorkspaceMutationReplaceFile:
		item := w.entries[change.EntryID]
		item.version, item.body = change.ContentVersion, body
	case model.AttemptWorkspaceMutationMoveEntry:
		for _, item := range w.entries {
			if item.path == change.OldPath || strings.HasPrefix(item.path, change.OldPath+"/") {
				item.path = change.NewPath + strings.TrimPrefix(item.path, change.OldPath)
			}
		}
	case model.AttemptWorkspaceMutationDeleteEntry:
		for id, item := range w.entries {
			if item.path == change.OldPath || strings.HasPrefix(item.path, change.OldPath+"/") {
				delete(w.entries, id)
			}
		}
	}
	w.journal = append(w.journal, change)
	return change
}

func (w *workspaceFake) List(_ context.Context, _ environment.WorkspaceAccess, _ int64, _ model.AttemptWorkspaceEntryID, _ int) (environment.WorkspacePage, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	page := environment.WorkspacePage{Cursor: int64(len(w.journal))}
	for _, item := range w.entries {
		listed := environment.WorkspaceItem{EntryID: item.id, Kind: item.kind, Path: item.path, ContentVersion: item.version}
		if item.kind == model.StarterWorkspaceEntryFile {
			listed.SHA256, listed.Size = digest(item.body), int64(len(item.body))
		}
		page.Items = append(page.Items, listed)
	}
	slices.SortFunc(page.Items, func(a, b environment.WorkspaceItem) int { return strings.Compare(a.Path, b.Path) })
	return page, nil
}

func (w *workspaceFake) Journal(_ context.Context, _ environment.WorkspaceAccess, after int64, limit int) (environment.WorkspaceJournal, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	page := environment.WorkspaceJournal{CurrentCursor: int64(len(w.journal))}
	if after < int64(len(w.journal)) {
		rest := w.journal[after:]
		page.Entries = slices.Clone(rest[:min(len(rest), limit)])
		page.HasMore = len(rest) > limit
	}
	return page, nil
}

func (w *workspaceFake) Open(_ context.Context, _ environment.WorkspaceAccess, entryID model.AttemptWorkspaceEntryID) (environment.WorkspaceFile, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	item := w.entries[entryID]
	if item == nil || item.kind != model.StarterWorkspaceEntryFile {
		return environment.WorkspaceFile{}, environment.ErrWorkspaceEntryNotFound
	}
	return environment.WorkspaceFile{Body: io.NopCloser(bytes.NewReader(item.body)), SHA256: digest(item.body),
		Size: int64(len(item.body)), ContentVersion: item.version}, nil
}

func (w *workspaceFake) Apply(ctx context.Context, access environment.WorkspaceAccess, batch environment.WorkspaceBatch) ([]model.AttemptWorkspaceJournalEntry, error) {
	// Bodies are read before the batch is checked, as the Attempt service
	// stages them.
	bodies := map[int][]byte{}
	for index, mutation := range batch.Mutations {
		if mutation.Operation == model.AttemptWorkspaceMutationCreateFile || mutation.Operation == model.AttemptWorkspaceMutationReplaceFile {
			reader, err := batch.Content(ctx)
			if err != nil {
				return nil, fmt.Errorf("stage: %w", err)
			}
			body, err := io.ReadAll(reader)
			if err != nil {
				return nil, fmt.Errorf("stage: %w", err)
			}
			if digest(body) != mutation.SHA256 || int64(len(body)) != mutation.Size {
				return nil, &environment.WorkspaceRefusal{Code: "exam.attempt.workspace.content_mismatch", Item: index}
			}
			bodies[index] = body
		}
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	if outcome, ok := w.outcomes[batch.IdempotencyKey]; ok {
		return outcome, nil
	}
	refuse := func(index int, code string) error {
		return &environment.WorkspaceRefusal{Code: "exam.attempt.workspace." + code, Item: index}
	}
	// Check every mutation against a copy, so a refusal changes nothing.
	saved := map[model.AttemptWorkspaceEntryID]workspaceEntry{}
	for id, item := range w.entries {
		saved[id] = *item
	}
	journal := len(w.journal)
	restore := func() {
		w.entries = map[model.AttemptWorkspaceEntryID]*workspaceEntry{}
		for id, item := range saved {
			copied := item
			w.entries[id] = &copied
		}
		w.journal = w.journal[:journal]
	}
	author := model.NewEnvironmentWorkspaceAuthor(access.Incarnation, access.Tag)
	var changes []model.AttemptWorkspaceJournalEntry
	for index, mutation := range batch.Mutations {
		parentHeld := func(name string) bool {
			parent := path.Dir(name)
			if parent == "." {
				return true
			}
			item := w.byPath(parent)
			return item != nil && item.kind == model.StarterWorkspaceEntryDirectory
		}
		change := model.AttemptWorkspaceJournalEntry{Operation: mutation.Operation, Author: author}
		switch mutation.Operation {
		case model.AttemptWorkspaceMutationCreateFile, model.AttemptWorkspaceMutationCreateDirectory:
			if w.byPath(mutation.DestinationPath) != nil || !parentHeld(mutation.DestinationPath) {
				restore()
				return nil, refuse(index, "path_conflict")
			}
			change.EntryID, change.NewPath = model.NewAttemptWorkspaceEntryID(), mutation.DestinationPath
			change.EntryKind = model.StarterWorkspaceEntryDirectory
			if mutation.Operation == model.AttemptWorkspaceMutationCreateFile {
				change.EntryKind, change.ContentVersion = model.StarterWorkspaceEntryFile, model.NewWorkspaceContentVersion()
			}
		case model.AttemptWorkspaceMutationReplaceFile:
			item := w.entries[mutation.EntryID]
			if item == nil || item.path != mutation.ExpectedPath || item.kind != model.StarterWorkspaceEntryFile {
				restore()
				return nil, refuse(index, "entry_conflict")
			}
			if item.version != mutation.ExpectedContentVersion {
				restore()
				return nil, refuse(index, "content_conflict")
			}
			change.EntryID, change.EntryKind, change.OldPath, change.NewPath = item.id, item.kind, item.path, item.path
			change.ContentVersion = model.NewWorkspaceContentVersion()
		case model.AttemptWorkspaceMutationMoveEntry:
			item := w.entries[mutation.EntryID]
			if item == nil || item.path != mutation.ExpectedPath {
				restore()
				return nil, refuse(index, "entry_conflict")
			}
			if w.byPath(mutation.DestinationPath) != nil || !parentHeld(mutation.DestinationPath) {
				restore()
				return nil, refuse(index, "path_conflict")
			}
			change.EntryID, change.EntryKind, change.OldPath, change.NewPath = item.id, item.kind, item.path, mutation.DestinationPath
			change.ContentVersion = item.version
		case model.AttemptWorkspaceMutationDeleteEntry:
			item := w.entries[mutation.EntryID]
			if item == nil || item.path != mutation.ExpectedPath {
				restore()
				return nil, refuse(index, "entry_conflict")
			}
			if item.kind == model.StarterWorkspaceEntryFile && item.version != mutation.ExpectedContentVersion {
				restore()
				return nil, refuse(index, "content_conflict")
			}
			change.EntryID, change.EntryKind, change.OldPath, change.Recursive = item.id, item.kind, item.path, mutation.Recursive
		}
		if err := func() error {
			probe := change
			probe.WorkspaceID, probe.Cursor, probe.ChangedAt = w.id, 1, time.Now()
			return probe.Validate()
		}(); err != nil {
			restore()
			return nil, &environment.WorkspaceRefusal{Code: "exam.attempt.invalid", Item: index}
		}
		changes = append(changes, w.commit(change, bodies[index]))
	}
	w.outcomes[batch.IdempotencyKey] = changes
	return changes, nil
}
