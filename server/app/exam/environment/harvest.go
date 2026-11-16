// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package environment

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"path"
	"strings"

	"github.com/sudosylabs/proctor/server/model"
)

const (
	// maxHarvestPlanBytes bounds the metadata of one harvest batch.
	maxHarvestPlanBytes = 256 << 10
	// maxNotedChanges bounds the changes one overwritten or refused record
	// names.
	maxNotedChanges = 16
)

// The refusal codes that mean a guest change lost to a newer workspace
// state, so journal order decided against it. Every other refusal is the
// workspace's own rule, such as a quota.
var losingCodes = map[string]bool{
	"exam.attempt.workspace.content_conflict":    true,
	"exam.attempt.workspace.entry_conflict":      true,
	"exam.attempt.workspace.path_conflict":       true,
	"exam.attempt.workspace.cursor_conflict":     true,
	"exam.attempt.workspace.directory_not_empty": true,
}

// wholeBatchCodes are the refusals of a whole batch that a retry will not
// change.
var wholeBatchCodes = map[string]bool{
	"exam.attempt.workspace.entry_limit":     true,
	"exam.attempt.workspace.size_limit":      true,
	"exam.attempt.workspace.cursor_conflict": true,
	"exam.attempt.invalid":                   true,
}

// coalesce drops guest changes the workspace never needs: every write of a
// file but the last before its next other change, and every change of a
// file created and deleted within the same harvest.
func coalesce(observations []Observation) []Observation {
	created, deleted := map[string]bool{}, map[string]bool{}
	for _, observation := range observations {
		if observation.Kind != kindFile {
			continue
		}
		switch observation.Operation {
		case ObserveCreateFile:
			created[observation.NodeIdentity] = true
		case ObserveDelete:
			deleted[observation.NodeIdentity] = true
		}
	}
	keep := make([]bool, len(observations))
	laterWrite := map[string]bool{}
	for index := len(observations) - 1; index >= 0; index-- {
		observation := observations[index]
		identity := observation.NodeIdentity
		if observation.Kind == kindFile && created[identity] && deleted[identity] {
			continue
		}
		if observation.Operation == ObserveWriteFile {
			if laterWrite[identity] {
				continue
			}
			laterWrite[identity] = true
		} else {
			laterWrite[identity] = false
		}
		keep[index] = true
	}
	result := make([]Observation, 0, len(observations))
	for index, observation := range observations {
		if keep[index] {
			result = append(result, observation)
		}
	}
	return result
}

// discardedCode is the code an overwritten record names for a guest change
// the journal's projection replaced in place before it was harvested: it
// lost to a newer workspace version, as a refused stale write does.
const discardedCode = "exam.attempt.workspace.content_conflict"

// plannedChange is one mutation of a harvest batch and the guest change it
// comes from. confirm is set when the mutation is that change's exact
// outcome, which the owner then confirms to the machine.
type plannedChange struct {
	mutation WorkspaceMutation
	source   Observation
	confirm  bool
	content  *ContentRef
}

// writerTag is the process tag a guest change is journaled under, or empty
// when the machine named none or one the journal cannot carry.
func writerTag(observation Observation) model.AttemptEnvironmentProcessTag {
	if observation.ProcessTag.IsValid() {
		return observation.ProcessTag
	}
	return ""
}

// harvestNotes is what one harvest could not store.
type harvestNotes struct {
	overwritten int
	lost        []map[string]any
	refused     int
	codes       map[string]int
	paths       []string
}

// harvester plans and commits one harvest against the workspace view.
type harvester struct {
	owner  *owner
	policy IgnorePolicy
	// ignoredBy names what the policy's ignore decisions come from.
	ignoredBy string
	sim       *tree
	ignored   map[string]bool
	dropped   map[uint64]bool
	limit     int
	notes     harvestNotes
}

// The outcomes of one batch commit.
type batchOutcome uint8

const (
	batchCommitted batchOutcome = iota
	// batchDropped means one change was refused or its content went stale, and
	// the rest are planned again without it.
	batchDropped
	// batchSplit means the whole batch was refused; its changes are tried one
	// at a time.
	batchSplit
)

// harvest stores every pending guest change in the attempt workspace, or
// records why it could not. The observations are acknowledged by sync once
// the owner's cursors are saved.
func (o *owner) harvest(ctx context.Context) error {
	if err := o.refreshAhead(ctx); err != nil {
		return err
	}
	policy, err := o.policy(ctx)
	if err != nil {
		return err
	}
	h := &harvester{owner: o, policy: policy, ignored: map[string]bool{}, dropped: map[uint64]bool{},
		limit: MaxProjectionMutations, notes: harvestNotes{codes: map[string]int{}}}
	h.ignoredBy = model.AttemptEnvironmentIgnoredByFile
	if policy.Fallback() {
		h.ignoredBy = model.AttemptEnvironmentIgnoredByDefault
	}
	observations := coalesce(o.pending)
	start, splitUntil := 0, 0
	for start < len(observations) {
		if start >= splitUntil {
			h.limit = MaxProjectionMutations
		}
		h.sim = o.ahead.clone()
		batch, consumed := h.planBatch(observations[start:])
		if len(batch) == 0 {
			start += consumed
			continue
		}
		outcome, err := h.commit(ctx, batch)
		if err != nil {
			return err
		}
		switch outcome {
		case batchCommitted:
			start += consumed
		case batchSplit:
			h.limit, splitUntil = 1, start+consumed
		case batchDropped:
		}
	}
	o.harvested = o.pending[len(o.pending)-1].HostSequence
	o.pending = nil
	return h.record(ctx)
}

// planBatch plans the next batch from observations and says how many it
// went through. A batch ends before a change that needs an entry the batch
// is still creating, so the workspace gives that entry its ID first, and
// before a change by another writer: one batch has one author.
func (h *harvester) planBatch(observations []Observation) ([]plannedChange, int) {
	var batch []plannedChange
	size := 0
	for index, observation := range observations {
		if h.dropped[observation.HostSequence] {
			continue
		}
		if observation.DiscardedBy != 0 {
			// The journal's projection replaced it in place.
			h.lose(observation, discardedCode)
			continue
		}
		planned, skip, needsID := h.plan(observation)
		if needsID {
			if len(batch) > 0 {
				return batch, index
			}
			skip = true
		}
		if skip {
			continue
		}
		encoded, _ := json.Marshal(planned.mutation)
		if len(batch) >= h.limit || size+len(encoded) > maxHarvestPlanBytes ||
			len(batch) > 0 && writerTag(batch[0].source) != writerTag(observation) {
			return batch, index
		}
		batch, size = append(batch, planned), size+len(encoded)
		h.simulate(planned.mutation)
	}
	return batch, len(observations)
}

// plan maps one guest change to a workspace mutation. skip leaves it out;
// needsID asks for the batch to be committed first.
func (h *harvester) plan(observation Observation) (plannedChange, bool, bool) {
	item := h.sim.byPath[observation.Path]
	link := func(mutation WorkspaceMutation) plannedChange {
		return plannedChange{mutation: mutation, source: observation, confirm: true, content: observation.Content}
	}
	switch observation.Operation {
	case ObserveCreateFile, ObserveCreateDirectory:
		directory := observation.Operation == ObserveCreateDirectory
		if item != nil {
			if !item.pending && directory == (item.Kind == kindDirectory) &&
				(directory || (observation.Content != nil && item.SHA256 == observation.Content.SHA256)) {
				// Already in the workspace.
				return plannedChange{}, true, false
			}
			h.lose(observation, "exam.attempt.workspace.path_conflict")
			return plannedChange{}, true, false
		}
		if !h.policy.Observed(observation.Path, directory) {
			h.ignored[observation.NodeIdentity] = true
			h.status(observation.Path, model.AttemptEnvironmentPathIgnored, h.ignoredBy)
			return plannedChange{}, true, false
		}
		if parent := path.Dir(observation.Path); parent != "." {
			if container := h.sim.byPath[parent]; container == nil || container.Kind != kindDirectory {
				// Under a directory the workspace does not hold.
				h.ignored[observation.NodeIdentity] = true
				h.status(observation.Path, model.AttemptEnvironmentPathIgnored, model.AttemptEnvironmentIgnoredByParent)
				return plannedChange{}, true, false
			}
		}
		if directory {
			return link(WorkspaceMutation{Operation: model.AttemptWorkspaceMutationCreateDirectory, DestinationPath: observation.Path}), false, false
		}
		return link(createFile(observation)), false, false
	case ObserveWriteFile:
		if item == nil {
			if h.ignored[observation.NodeIdentity] || observation.Content == nil {
				return plannedChange{}, true, false
			}
			if !h.policy.Observed(observation.Path, false) {
				h.status(observation.Path, model.AttemptEnvironmentPathIgnored, h.ignoredBy)
				return plannedChange{}, true, false
			}
			if parent := path.Dir(observation.Path); parent != "." {
				if container := h.sim.byPath[parent]; container == nil || container.Kind != kindDirectory {
					h.status(observation.Path, model.AttemptEnvironmentPathIgnored, model.AttemptEnvironmentIgnoredByParent)
					return plannedChange{}, true, false
				}
			}
			// A guest file the workspace does not hold, such as one an
			// earlier harvest could not store: offer it as new.
			return plannedChange{mutation: createFile(observation), source: observation, content: observation.Content}, false, false
		}
		if item.pending {
			return plannedChange{}, false, true
		}
		if item.Kind != kindFile {
			h.lose(observation, "exam.attempt.workspace.entry_conflict")
			return plannedChange{}, true, false
		}
		if observation.Content == nil || item.SHA256 == observation.Content.SHA256 {
			return plannedChange{}, true, false
		}
		return link(WorkspaceMutation{
			Operation: model.AttemptWorkspaceMutationReplaceFile, EntryID: item.ID, ExpectedPath: item.Path,
			ExpectedContentVersion: baseVersion(observation, item), MediaType: mediaType(observation.Path),
			SHA256: observation.Content.SHA256, Size: observation.Content.Size,
		}), false, false
	case ObserveMove:
		if item == nil {
			// A path the workspace never held moved away.
			h.owner.row.ClearSyncPath(observation.Path)
			return plannedChange{}, true, false
		}
		if item.pending {
			return plannedChange{}, false, true
		}
		if h.sim.byPath[observation.DestinationPath] != nil {
			h.lose(observation, "exam.attempt.workspace.path_conflict")
			return plannedChange{}, true, false
		}
		return link(WorkspaceMutation{
			Operation: model.AttemptWorkspaceMutationMoveEntry, EntryID: item.ID, ExpectedPath: item.Path,
			DestinationPath: observation.DestinationPath,
		}), false, false
	case ObserveDelete:
		if item == nil {
			// A path the workspace never held is gone.
			h.owner.row.ClearSyncPath(observation.Path)
			return plannedChange{}, true, false
		}
		if item.pending {
			return plannedChange{}, false, true
		}
		mutation := WorkspaceMutation{Operation: model.AttemptWorkspaceMutationDeleteEntry, EntryID: item.ID, ExpectedPath: item.Path}
		if item.Kind == kindFile {
			mutation.ExpectedContentVersion = baseVersion(observation, item)
		} else {
			mutation.Recursive = observation.Recursive != nil && *observation.Recursive
		}
		return link(mutation), false, false
	}
	return plannedChange{}, true, false
}

func createFile(observation Observation) WorkspaceMutation {
	mutation := WorkspaceMutation{Operation: model.AttemptWorkspaceMutationCreateFile, DestinationPath: observation.Path,
		MediaType: mediaType(observation.Path)}
	if observation.Content != nil {
		mutation.SHA256, mutation.Size = observation.Content.SHA256, observation.Content.Size
	}
	return mutation
}

// baseVersion is the content version a guest change was made against: the
// projected version the machine reports, or for a file the guest created
// and a harvest stored, the version that harvest gave it.
func baseVersion(observation Observation, item *entry) model.WorkspaceContentVersion {
	if observation.ExpectedVersion != "" {
		return model.WorkspaceContentVersion(observation.ExpectedVersion)
	}
	return item.Version
}

// simulate applies a planned mutation to the plan's view of the workspace.
func (h *harvester) simulate(mutation WorkspaceMutation) {
	switch mutation.Operation {
	case model.AttemptWorkspaceMutationCreateFile:
		h.sim.add(entry{ID: model.AttemptWorkspaceEntryID("pending:" + mutation.DestinationPath), Kind: kindFile,
			Path: mutation.DestinationPath, SHA256: mutation.SHA256, Size: mutation.Size, pending: true})
	case model.AttemptWorkspaceMutationCreateDirectory:
		h.sim.add(entry{ID: model.AttemptWorkspaceEntryID("pending:" + mutation.DestinationPath), Kind: kindDirectory,
			Path: mutation.DestinationPath, pending: true})
	case model.AttemptWorkspaceMutationReplaceFile:
		if item := h.sim.byID[mutation.EntryID]; item != nil {
			item.SHA256, item.Size = mutation.SHA256, mutation.Size
		}
	case model.AttemptWorkspaceMutationMoveEntry:
		if item := h.sim.byID[mutation.EntryID]; item != nil {
			h.sim.move(item, mutation.DestinationPath)
		}
	case model.AttemptWorkspaceMutationDeleteEntry:
		if item := h.sim.byID[mutation.EntryID]; item != nil {
			h.sim.remove(item)
		}
	}
}

// commit writes one batch, confirms each outcome to the machine and brings
// the workspace view up to date. A refused or stale change is noted and
// dropped, and the caller plans the rest again.
func (h *harvester) commit(ctx context.Context, batch []plannedChange) (batchOutcome, error) {
	o := h.owner
	mutations := make([]WorkspaceMutation, len(batch))
	var bodies []int
	recursive := false
	for index, planned := range batch {
		mutations[index] = planned.mutation
		recursive = recursive || planned.mutation.Recursive
		if carriesContent(planned.mutation.Operation) {
			if planned.content == nil {
				return 0, errors.New("a harvested file names no content")
			}
			bodies = append(bodies, index)
		}
	}
	var expected *int64
	if recursive {
		cursor := o.ahead.Cursor
		expected = &cursor
	}
	stale, next := -1, 0
	var opened []io.Closer
	content := func(ctx context.Context) (io.Reader, error) {
		if next >= len(bodies) {
			return nil, io.ErrUnexpectedEOF
		}
		index := bodies[next]
		next++
		body, err := o.machine.OpenObservedContent(ctx, o.fence, *batch[index].content)
		if err != nil {
			if errors.Is(err, ErrMachineStale) || errors.Is(err, ErrMachineNotFound) {
				stale = index
			}
			return nil, err
		}
		opened = append(opened, body)
		return &staleReader{reader: body, stale: func() { stale = index }}, nil
	}
	// The batch is journaled by the process that wrote it, when known.
	access := o.access()
	access.Tag = writerTag(batch[0].source)
	entries, err := o.deps.Workspace.Apply(ctx, access, WorkspaceBatch{
		Mutations: mutations, ExpectedCursor: expected, Content: content,
		IdempotencyKey: harvestKey(o.row.GrantID, o.row.Epoch, access.Tag, mutations, expected),
		Resources:      o.row.Resources,
	})
	for _, body := range opened {
		_ = body.Close()
	}
	if err != nil && stale >= 0 {
		// A later observation carries the newer content.
		h.dropped[batch[stale].source.HostSequence] = true
		return batchDropped, nil
	}
	var refusal *WorkspaceRefusal
	if errors.As(err, &refusal) {
		switch {
		case refusal.Item >= 0 && refusal.Item < len(batch):
			h.note(batch[refusal.Item].source, refusal.Code)
			return batchDropped, nil
		case !wholeBatchCodes[refusal.Code]:
			// A budget or a passing failure: the harvest runs again later.
			return 0, fmt.Errorf("the Attempt Workspace deferred a harvest: %w", err)
		case len(batch) > 1:
			return batchSplit, nil
		default:
			h.note(batch[0].source, refusal.Code)
			return batchDropped, nil
		}
	}
	if err != nil {
		return 0, o.workspaceError(err)
	}
	if len(entries) != len(batch) {
		return 0, fmt.Errorf("a harvest batch answered %d changes for %d mutations", len(entries), len(batch))
	}
	for _, planned := range batch {
		// A stored change is synced.
		h.owner.row.ClearSyncPath(planned.source.Path)
		if planned.mutation.DestinationPath != "" {
			h.owner.row.ClearSyncPath(planned.mutation.DestinationPath)
		}
	}
	for index, planned := range batch {
		if !planned.confirm {
			continue
		}
		change := entries[index]
		before := map[model.AttemptWorkspaceEntryID]model.WorkspaceContentVersion{}
		if planned.mutation.Operation == model.AttemptWorkspaceMutationReplaceFile ||
			planned.mutation.Operation == model.AttemptWorkspaceMutationDeleteEntry {
			before[change.EntryID] = planned.mutation.ExpectedContentVersion
		}
		mutation := projectionMutation(before, change, planned.content)
		err := o.machine.ConfirmObservation(ctx, o.fence, planned.source.HostSequence, mutation)
		if errors.Is(err, ErrMachineConflict) {
			// The outcome is not the guest's change, so its projection is
			// not an echo.
			continue
		}
		if err != nil {
			return 0, o.machineError(err)
		}
		o.echoes[change.Cursor] = echo{mutation: mutation, observation: planned.source.HostSequence, content: planned.content}
	}
	return batchCommitted, o.refreshAhead(ctx)
}

// harvestKey is the batch's idempotency key: the same batch on the same
// machine is the same command.
func harvestKey(
	grantID, epoch string,
	tag model.AttemptEnvironmentProcessTag,
	mutations []WorkspaceMutation,
	expected *int64,
) string {
	encoded, _ := json.Marshal(struct {
		Grant     string                             `json:"grant"`
		Epoch     string                             `json:"epoch"`
		Tag       model.AttemptEnvironmentProcessTag `json:"tag,omitempty"`
		Mutations []WorkspaceMutation                `json:"mutations"`
		Expected  *int64                             `json:"expected,omitempty"`
	}{grantID, epoch, tag, mutations, expected})
	sum := sha256.Sum256(encoded)
	return "execution.harvest:" + hex.EncodeToString(sum[:])
}

// staleReader reports a body that ended with the machine's stale error.
type staleReader struct {
	reader io.Reader
	stale  func()
}

func (r *staleReader) Read(buffer []byte) (int, error) {
	n, err := r.reader.Read(buffer)
	if err != nil && (errors.Is(err, ErrMachineStale) || errors.Is(err, ErrMachineNotFound)) {
		r.stale()
	}
	return n, err
}

// note records a change the workspace refused, and drops it from the plan.
func (h *harvester) note(observation Observation, code string) {
	if losingCodes[code] {
		h.lose(observation, code)
		return
	}
	h.dropped[observation.HostSequence] = true
	h.status(observation.Path, model.AttemptEnvironmentPathRefused, code)
	h.notes.refused++
	h.notes.codes[code]++
	if len(h.notes.paths) < maxNotedChanges {
		h.notes.paths = append(h.notes.paths, observation.Path)
	}
}

// lose records a guest change that lost to a newer workspace state. The
// journal's version stays; the projection brings it back to the machine.
func (h *harvester) lose(observation Observation, code string) {
	h.dropped[observation.HostSequence] = true
	h.status(observation.Path, model.AttemptEnvironmentPathRefused, code)
	h.notes.overwritten++
	if len(h.notes.lost) >= maxNotedChanges {
		return
	}
	change := map[string]any{"path": observation.Path, "operation": string(observation.Operation), "code": code}
	if observation.DestinationPath != "" {
		change["destination_path"] = observation.DestinationPath
	}
	if observation.Content != nil {
		change["sha256"], change["size"] = observation.Content.SHA256, observation.Content.Size
	}
	h.notes.lost = append(h.notes.lost, change)
}

// status records the sync status of a guest path the harvest did not store.
// It is kept on the environment row, never in logs or audit.
func (h *harvester) status(name string, status model.AttemptEnvironmentSyncStatus, reason string) {
	h.owner.row.NoteSyncPath(model.AttemptEnvironmentSyncPath{Path: name, Status: status, Reason: reason})
}

// record stores what the harvest could not keep, one record per kind.
func (h *harvester) record(ctx context.Context) error {
	o := h.owner
	now := o.deps.Now()
	if h.notes.overwritten > 0 {
		if err := o.evidence(ctx, model.AttemptEvidenceOverwritten, 0, now,
			map[string]any{"overwritten": h.notes.overwritten, "changes": h.notes.lost}); err != nil {
			return err
		}
	}
	if h.notes.refused > 0 {
		if err := o.evidence(ctx, model.AttemptEvidenceRefused, 0, now,
			map[string]any{"refused": h.notes.refused, "codes": h.notes.codes, "paths": h.notes.paths}); err != nil {
			return err
		}
	}
	return nil
}

// mediaType guesses a harvested file's media type from its name.
func mediaType(name string) string {
	if guessed := mime.TypeByExtension(path.Ext(name)); guessed != "" {
		if media, _, err := mime.ParseMediaType(guessed); err == nil && !strings.Contains(media, "*") {
			return media
		}
	}
	return "application/octet-stream"
}
