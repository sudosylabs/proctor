// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package execution

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"mime"
	"path"
	"strings"
	"time"

	"github.com/sudosylabs/execenv"
	"github.com/sudosylabs/execenv/ignore"

	"github.com/sudosylabs/proctor/server/plugin"
)

// maxHarvestPlanBytes keeps a harvest batch's record inside the record
// bound.
const maxHarvestPlanBytes = 48 << 10

// harvestRecord is a batch the owner is committing. Until it is cleared, a
// takeover retries exactly it under the same idempotency key, so a batch
// whose outcome was lost is replayed instead of written twice.
type harvestRecord struct {
	GrantID        string           `json:"grant_id"`
	Epoch          string           `json:"epoch"`
	Key            string           `json:"key"`
	ExpectedCursor *int64           `json:"expected_cursor,omitempty"`
	Mutations      []plannedHarvest `json:"mutations"`
}

// plannedHarvest is one mutation of a batch and the observation it comes
// from. Observation is set when the mutation is that observation's exact
// outcome, which the owner then confirms to execenv.
type plannedHarvest struct {
	Operation              plugin.WorkspaceOperation `json:"operation"`
	EntryID                string                    `json:"entry_id,omitempty"`
	ExpectedPath           string                    `json:"expected_path,omitempty"`
	DestinationPath        string                    `json:"destination_path,omitempty"`
	ExpectedContentVersion string                    `json:"expected_content_version,omitempty"`
	Recursive              bool                      `json:"recursive,omitempty"`
	MediaType              string                    `json:"media_type,omitempty"`
	SHA256                 string                    `json:"sha256,omitempty"`
	Size                   int64                     `json:"size,omitempty"`
	Observation            uint64                    `json:"observation,omitempty"`
	ObservationOperation   string                    `json:"observation_operation,omitempty"`
	Content                *execenv.ContentReference `json:"content,omitempty"`
	ObservedRecursive      *bool                     `json:"observed_recursive,omitempty"`
}

func (p plannedHarvest) mutation() plugin.WorkspaceMutation {
	return plugin.WorkspaceMutation{
		Operation: p.Operation, EntryID: p.EntryID, ExpectedPath: p.ExpectedPath, DestinationPath: p.DestinationPath,
		ExpectedContentVersion: p.ExpectedContentVersion, Recursive: p.Recursive, MediaType: p.MediaType,
		SHA256: p.SHA256, Size: p.Size,
	}
}

// harvestStats counts what one harvest could not store.
type harvestStats struct {
	refused int
	codes   map[string]int
	ignored int
}

// coalesce drops guest changes the Workspace never needs: every write of
// a file but the last before its next other change, and every change of a
// file created and deleted within the same harvest.
func coalesce(observations []execenv.ExecutionObservation) []execenv.ExecutionObservation {
	created, deleted := map[string]bool{}, map[string]bool{}
	for _, observation := range observations {
		if observation.Kind != execenv.ProjectionFile {
			continue
		}
		switch observation.Operation {
		case execenv.ObservationCreateFile:
			created[observation.NodeIdentity] = true
		case execenv.ObservationDelete:
			deleted[observation.NodeIdentity] = true
		}
	}
	keep := make([]bool, len(observations))
	laterWrite := map[string]bool{}
	for index := len(observations) - 1; index >= 0; index-- {
		observation := observations[index]
		identity := observation.NodeIdentity
		if observation.Kind == execenv.ProjectionFile && created[identity] && deleted[identity] {
			continue
		}
		if observation.Operation == execenv.ObservationWriteFile {
			if laterWrite[identity] {
				continue
			}
			laterWrite[identity] = true
		} else {
			laterWrite[identity] = false
		}
		keep[index] = true
	}
	result := make([]execenv.ExecutionObservation, 0, len(observations))
	for index, observation := range observations {
		if keep[index] {
			result = append(result, observation)
		}
	}
	return result
}

// harvester plans and commits one harvest.
type harvester struct {
	owner       *owner
	environment plugin.BoundEnvironment
	policy      ignore.Policy
	sim         *tree
	cursor      int64
	batch       []plannedHarvest
	batchFirst  uint64
	batchLast   uint64
	batchSize   int
	expected    bool
	ignored     map[string]bool
	stats       harvestStats
}

// harvest stores every pending guest change in the Attempt Workspace, or
// records why it could not, then acknowledges them.
func (o *owner) harvest(ctx context.Context, environment plugin.BoundEnvironment) error {
	if err := o.retryHarvest(ctx); err != nil {
		return err
	}
	policy, err := o.policy(ctx, environment)
	if err != nil {
		return err
	}
	h := &harvester{
		owner: o, environment: environment, policy: policy, sim: o.ahead.clone(), cursor: o.current,
		ignored: map[string]bool{}, stats: harvestStats{codes: map[string]int{}},
	}
	for _, observation := range coalesce(o.pending) {
		if err := h.add(ctx, observation); err != nil {
			return err
		}
	}
	if err := h.commit(ctx); err != nil {
		return err
	}
	through := o.pending[len(o.pending)-1].HostSequence
	if err := o.env.AcknowledgeObservations(ctx, o.fence, through); err != nil {
		return o.envError(err)
	}
	grant, err := o.records.updateGrant(ctx, o.attemptID, func(grant *grantRecord, _ bool) (bool, error) {
		if through <= grant.ObservationCursor {
			return false, nil
		}
		grant.ObservationCursor = through
		return true, nil
	})
	if err != nil {
		return err
	}
	o.grant, o.pending = grant, nil
	if h.stats.refused > 0 {
		detail := map[string]any{"refused": h.stats.refused, "codes": h.stats.codes}
		if err := o.gap(ctx, gapHarvestRefused, time.Now().UTC(), mustJSON(detail)); err != nil && !errors.Is(err, errWait) {
			return err
		}
	}
	return nil
}

// add plans one observation, committing the batch first when the
// observation acts on an entry the batch is still creating.
func (h *harvester) add(ctx context.Context, observation execenv.ExecutionObservation) error {
	planned, skip, needsID := h.plan(observation)
	if needsID {
		if err := h.commit(ctx); err != nil {
			return err
		}
		planned, skip, needsID = h.plan(observation)
		if needsID {
			skip = true
		}
	}
	if skip {
		return nil
	}
	size := len(mustJSON(planned))
	if len(h.batch) >= execenv.MaxProjectionMutations || h.batchSize+size > maxHarvestPlanBytes {
		if err := h.commit(ctx); err != nil {
			return err
		}
		// The batch is gone; plan again against what it left.
		planned, skip, _ = h.plan(observation)
		if skip {
			return nil
		}
	}
	if len(h.batch) == 0 {
		h.batchFirst = observation.HostSequence
	}
	h.batchLast = observation.HostSequence
	h.batch, h.batchSize = append(h.batch, planned), h.batchSize+size
	h.expected = h.expected || planned.Recursive
	h.simulate(planned)
	return nil
}

// plan maps one guest change to a Workspace mutation. skip leaves it out;
// needsID asks for the batch to be committed first.
func (h *harvester) plan(observation execenv.ExecutionObservation) (plannedHarvest, bool, bool) {
	item := h.sim.byPath[observation.Path]
	link := func(p plannedHarvest) plannedHarvest {
		p.Observation, p.ObservationOperation = observation.HostSequence, string(observation.Operation)
		p.Content, p.ObservedRecursive = observation.Content, observation.Recursive
		return p
	}
	switch observation.Operation {
	case execenv.ObservationCreateFile, execenv.ObservationCreateDirectory:
		directory := observation.Operation == execenv.ObservationCreateDirectory
		if item != nil {
			if !item.pending && (directory == (item.Kind == kindDirectory)) &&
				(directory || (observation.Content != nil && item.SHA256 == observation.Content.Digest)) {
				// Already in the Workspace, as after a harvest whose
				// acknowledgement was lost.
				return plannedHarvest{}, true, false
			}
			h.refuse("exam.attempt.workspace.path_conflict")
			return plannedHarvest{}, true, false
		}
		if h.ignoredPath(observation.Path, directory) {
			h.ignored[observation.NodeIdentity] = true
			h.stats.ignored++
			return plannedHarvest{}, true, false
		}
		if parent := path.Dir(observation.Path); parent != "." {
			if container := h.sim.byPath[parent]; container == nil || container.Kind != kindDirectory {
				h.ignored[observation.NodeIdentity] = true
				return plannedHarvest{}, true, false
			}
		}
		if directory {
			return link(plannedHarvest{Operation: plugin.WorkspaceCreateDirectory, DestinationPath: observation.Path}), false, false
		}
		return link(h.createFile(observation)), false, false
	case execenv.ObservationWriteFile:
		if item == nil {
			if h.ignored[observation.NodeIdentity] || h.ignoredPath(observation.Path, false) {
				return plannedHarvest{}, true, false
			}
			// A guest file the Workspace does not hold, such as one an
			// earlier harvest could not store: offer it as new.
			return h.createFile(observation), false, false
		}
		if item.pending {
			return plannedHarvest{}, false, true
		}
		if item.Kind != kindFile {
			h.refuse("exam.attempt.workspace.entry_conflict")
			return plannedHarvest{}, true, false
		}
		if observation.Content == nil || item.SHA256 == observation.Content.Digest {
			return plannedHarvest{}, true, false
		}
		return link(plannedHarvest{
			Operation: plugin.WorkspaceReplaceFile, EntryID: item.ID, ExpectedPath: item.Path,
			ExpectedContentVersion: baseVersion(observation, item), MediaType: mediaType(observation.Path),
			SHA256: observation.Content.Digest, Size: observation.Content.Size,
		}), false, false
	case execenv.ObservationMove:
		if item == nil {
			return plannedHarvest{}, true, false
		}
		if item.pending {
			return plannedHarvest{}, false, true
		}
		if h.sim.byPath[observation.DestinationPath] != nil {
			h.refuse("exam.attempt.workspace.path_conflict")
			return plannedHarvest{}, true, false
		}
		return link(plannedHarvest{
			Operation: plugin.WorkspaceMoveEntry, EntryID: item.ID, ExpectedPath: item.Path,
			DestinationPath: observation.DestinationPath,
		}), false, false
	case execenv.ObservationDelete:
		if item == nil {
			return plannedHarvest{}, true, false
		}
		if item.pending {
			return plannedHarvest{}, false, true
		}
		planned := plannedHarvest{Operation: plugin.WorkspaceDeleteEntry, EntryID: item.ID, ExpectedPath: item.Path}
		if item.Kind == kindFile {
			planned.ExpectedContentVersion = baseVersion(observation, item)
		} else {
			planned.Recursive = observation.Recursive != nil && *observation.Recursive
		}
		return link(planned), false, false
	}
	return plannedHarvest{}, true, false
}

func (h *harvester) createFile(observation execenv.ExecutionObservation) plannedHarvest {
	planned := plannedHarvest{Operation: plugin.WorkspaceCreateFile, DestinationPath: observation.Path, MediaType: mediaType(observation.Path)}
	if observation.Content != nil {
		planned.SHA256, planned.Size = observation.Content.Digest, observation.Content.Size
		planned.Content = observation.Content
	}
	return planned
}

// ignoredPath reports whether the Attempt Workspace's ignore rules keep a
// path the Workspace does not hold on the grant.
func (h *harvester) ignoredPath(name string, directory bool) bool {
	return !h.policy.Observed(name, directory, false)
}

func (h *harvester) refuse(code string) {
	h.stats.refused++
	h.stats.codes[code]++
}

// baseVersion is the content version a guest change was made against: the
// projected version execenv reports, or for a file the guest created and a
// harvest stored, the version that harvest gave it.
func baseVersion(observation execenv.ExecutionObservation, item *entry) string {
	if observation.ExpectedContentVersion != "" {
		return string(observation.ExpectedContentVersion)
	}
	return item.Version
}

// simulate applies a planned mutation to the plan's view of the Workspace.
func (h *harvester) simulate(planned plannedHarvest) {
	switch planned.Operation {
	case plugin.WorkspaceCreateFile:
		h.sim.add(entry{ID: "pending:" + planned.DestinationPath, Kind: kindFile, Path: planned.DestinationPath,
			SHA256: planned.SHA256, Size: planned.Size, pending: true})
	case plugin.WorkspaceCreateDirectory:
		h.sim.add(entry{ID: "pending:" + planned.DestinationPath, Kind: kindDirectory, Path: planned.DestinationPath, pending: true})
	case plugin.WorkspaceReplaceFile:
		if item := h.sim.byID[planned.EntryID]; item != nil {
			item.SHA256, item.Size = planned.SHA256, planned.Size
		}
	case plugin.WorkspaceMoveEntry:
		if item := h.sim.byID[planned.EntryID]; item != nil {
			h.sim.move(item, planned.DestinationPath)
		}
	case plugin.WorkspaceDeleteEntry:
		if item := h.sim.byID[planned.EntryID]; item != nil {
			h.sim.remove(item, true)
		}
	}
}

// commit writes the current batch, if any.
func (h *harvester) commit(ctx context.Context) error {
	if len(h.batch) == 0 {
		return nil
	}
	o := h.owner
	record := harvestRecord{
		GrantID: o.grant.GrantID, Epoch: o.grant.Epoch, Mutations: h.batch,
		Key: fmt.Sprintf("execution.harvest:%s:%s:%d-%d", o.grant.GrantID, o.grant.Epoch, h.batchFirst, h.batchLast),
	}
	if h.expected {
		cursor := h.cursor
		record.ExpectedCursor = &cursor
	}
	h.batch, h.batchSize, h.expected = nil, 0, false
	if _, err := o.records.put(ctx, kindHarvest, attemptKey(o.attemptID), record, 0); err != nil {
		return err
	}
	stats, last, err := o.commitHarvest(ctx, record)
	if err != nil {
		return err
	}
	h.stats.refused += stats.refused
	for code, count := range stats.codes {
		h.stats.codes[code] += count
	}
	// Plan the rest against what the journal says now, with the IDs the
	// batch's creates were given.
	if _, err := o.readJournal(ctx, h.environment); err != nil {
		return err
	}
	h.sim, h.cursor = o.ahead.clone(), max(o.current, last)
	return nil
}

// retryHarvest finishes a batch left in the record by this owner or a
// previous one.
func (o *owner) retryHarvest(ctx context.Context) error {
	var record harvestRecord
	_, found, err := o.records.get(ctx, kindHarvest, attemptKey(o.attemptID), &record)
	if err != nil || !found {
		return err
	}
	if record.GrantID != o.grant.GrantID || record.Epoch != o.grant.Epoch {
		return o.records.remove(ctx, kindHarvest, attemptKey(o.attemptID))
	}
	_, _, err = o.commitHarvest(ctx, record)
	return err
}

// commitHarvest applies one recorded batch, confirms each outcome to
// execenv and clears the record. A refusal leaves the guest change on the
// grant and is only counted. It returns the last cursor written.
func (o *owner) commitHarvest(ctx context.Context, record harvestRecord) (harvestStats, int64, error) {
	stats := harvestStats{codes: map[string]int{}}
	environment, ok := o.currentBinding()
	if !ok {
		return stats, 0, errWait
	}
	mutations := make([]plugin.WorkspaceMutation, len(record.Mutations))
	var contents []execenv.ContentReference
	for index, planned := range record.Mutations {
		mutations[index] = planned.mutation()
		if planned.Operation == plugin.WorkspaceCreateFile || planned.Operation == plugin.WorkspaceReplaceFile {
			if planned.Content == nil {
				return stats, 0, fmt.Errorf("harvest batch names no content")
			}
			contents = append(contents, *planned.Content)
		}
	}
	bodies := make([][]byte, 0, len(contents))
	for _, reference := range contents {
		reader, err := o.env.OpenObservationContent(ctx, o.fence, reference)
		if err != nil {
			return stats, 0, o.envError(err)
		}
		body, err := io.ReadAll(io.LimitReader(reader, execenv.MaxFileBytes+1))
		_ = reader.Close()
		if err != nil {
			return stats, 0, err
		}
		bodies = append(bodies, body)
	}
	result, err := o.plugin.host.AttemptWorkspaces().ApplyBatch(ctx, plugin.WorkspaceBatch{
		Environment: environment, Mutations: mutations, ExpectedCursor: record.ExpectedCursor,
		Content: &byteContents{bodies: bodies}, IdempotencyKey: record.Key,
	})
	var refusal *plugin.CapabilityError
	if errors.As(err, &refusal) {
		if isBindingRefusal(refusal.Code) {
			o.readAt = time.Time{}
			return stats, 0, fmt.Errorf("%w: harvest refused %s", errWait, refusal.Code)
		}
		stats.refused, stats.codes[refusal.Code] = len(record.Mutations), len(record.Mutations)
		return stats, 0, o.records.remove(ctx, kindHarvest, attemptKey(o.attemptID))
	}
	if err != nil {
		return stats, 0, err
	}
	if len(result.Changes) != len(record.Mutations) {
		return stats, 0, fmt.Errorf("harvest batch answered %d changes for %d mutations", len(result.Changes), len(record.Mutations))
	}
	body := 0
	for index, planned := range record.Mutations {
		change := result.Changes[index]
		carries := planned.Operation == plugin.WorkspaceCreateFile || planned.Operation == plugin.WorkspaceReplaceFile
		var kept []byte
		if carries {
			kept = bodies[body]
			body++
		}
		if planned.Observation == 0 {
			continue
		}
		before := map[string]string{}
		switch planned.Operation {
		case plugin.WorkspaceReplaceFile, plugin.WorkspaceDeleteEntry:
			before[change.EntryID] = planned.ExpectedContentVersion
		case plugin.WorkspaceMoveEntry:
			before[change.EntryID] = change.ContentVersion
		}
		mutation := projectionMutation(before, change, planned.Content)
		if err := o.env.ConfirmObservationProjection(ctx, execenv.ObservationProjection{
			Fence: o.fence, HostSequence: planned.Observation, Mutation: mutation,
		}); err != nil {
			if errors.Is(err, execenv.ErrConflict) {
				// The outcome does not match the guest change, so execenv
				// will not treat it as an echo; the projection conflict
				// that follows replaces the grant if it persists.
				o.logger.Warn("execution harvest outcome not confirmed")
				continue
			}
			return stats, 0, o.envError(err)
		}
		if kept != nil && o.echoBytes+len(kept) > echoBodyBudget {
			kept = nil
		}
		o.echoes[change.Cursor] = echo{mutation: mutation, body: kept}
		o.echoBytes += len(kept)
	}
	last := int64(0)
	if len(result.Changes) > 0 {
		last = result.Changes[len(result.Changes)-1].Cursor
	}
	return stats, last, o.records.remove(ctx, kindHarvest, attemptKey(o.attemptID))
}

// isBindingRefusal reports a refusal of the binding itself, not of the
// batch: the Attempt or generation no longer accepts work.
func isBindingRefusal(code string) bool {
	switch code {
	case "exam.attempt.unavailable", "exam.attempt.state_conflict", "exam.attempt.sitting_not_open",
		"exam.attempt.connection_closed":
		return true
	}
	return strings.HasPrefix(code, "exam.attempt.environment")
}

// byteContents hands a batch its bodies in order.
type byteContents struct {
	bodies [][]byte
	next   int
}

func (c *byteContents) Next(context.Context) (io.Reader, error) {
	if c.next >= len(c.bodies) {
		return nil, io.ErrUnexpectedEOF
	}
	body := c.bodies[c.next]
	c.next++
	return bytes.NewReader(body), nil
}

// mediaType guesses a harvested file's media type from its name.
func mediaType(name string) string {
	if guessed := mime.TypeByExtension(path.Ext(name)); guessed != "" {
		if media, _, err := mime.ParseMediaType(guessed); err == nil {
			return media
		}
	}
	return "application/octet-stream"
}
