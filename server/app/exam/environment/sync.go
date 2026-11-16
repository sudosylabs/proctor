// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package environment

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"slices"
	"strconv"
	"strings"

	"github.com/sudosylabs/proctor/server/model"
)

// workspaceError turns a refused Workspace read or write into what the
// owner does next.
func (o *owner) workspaceError(err error) error {
	switch {
	case errors.Is(err, ErrWorkspaceOwnerStale):
		return errLeaseLost
	case errors.Is(err, ErrWorkspaceSealed):
		return errSealed
	}
	return err
}

// prepare initializes the projection of a new machine, or reads the
// Workspace to continue one another owner started.
func (o *owner) prepare(ctx context.Context) error {
	manifest, err := o.listManifest(ctx)
	if err != nil {
		return err
	}
	if o.row.State == model.AttemptEnvironmentCreating {
		if o.row.Incarnation == 0 && manifest.byPath[IgnoreFile] == nil {
			written, err := o.writeDefaultIgnore(ctx)
			if err != nil {
				return err
			}
			if written {
				if manifest, err = o.listManifest(ctx); err != nil {
					return err
				}
			}
		}
		return o.initialize(ctx, manifest)
	}
	o.ahead = manifest
	o.ready = true
	return nil
}

// defaultIgnoreHeader opens the default ignore file, so the candidate sees
// what it does.
const defaultIgnoreHeader = `# Paths that stay on the execution machine: they are never saved to your
# Workspace or submitted. The syntax is .gitignore's. A pattern stops only
# new paths; a file already saved keeps syncing until you delete it in the
# editor. This file is saved and submitted itself.
`

// DefaultIgnoreFile is the IgnoreFile a new Attempt Workspace that uses
// execution gets: the header and DefaultIgnorePatterns, one per line.
func DefaultIgnoreFile() []byte {
	return []byte(defaultIgnoreHeader + strings.Join(DefaultIgnorePatterns, "\n") + "\n")
}

// writeDefaultIgnore gives the Workspace the default IgnoreFile, as the
// environment, when the first machine of an Attempt is first prepared and
// the Workspace has none: a Starter's own copy, or one the candidate wrote,
// is never replaced. Its idempotency key is the Attempt's, so an owner that
// restarts before recording the projection replays the same write instead
// of making another. A refusal, such as a candidate's file created
// meanwhile or a full Workspace, leaves it out; the defaults apply anyway.
func (o *owner) writeDefaultIgnore(ctx context.Context) (bool, error) {
	body := DefaultIgnoreFile()
	sum := sha256.Sum256(body)
	served := false
	_, err := o.deps.Workspace.Apply(ctx, o.access(), WorkspaceBatch{
		Mutations: []WorkspaceMutation{{Operation: model.AttemptWorkspaceMutationCreateFile, DestinationPath: IgnoreFile,
			MediaType: "text/plain", SHA256: hex.EncodeToString(sum[:]), Size: int64(len(body))}},
		Content: func(context.Context) (io.Reader, error) {
			if served {
				return nil, io.ErrUnexpectedEOF
			}
			served = true
			return bytes.NewReader(body), nil
		},
		IdempotencyKey: "execution.default-ignore:" + o.attemptID.String(),
		Resources:      o.row.Resources,
	})
	var refusal *WorkspaceRefusal
	if errors.As(err, &refusal) {
		return false, nil
	}
	if err != nil {
		return false, o.workspaceError(err)
	}
	return true, nil
}

// listManifest reads the whole Workspace manifest at one cursor.
func (o *owner) listManifest(ctx context.Context) (*tree, error) {
	for range 8 {
		result := newTree(0)
		expected, after, restart := int64(-1), model.AttemptWorkspaceEntryID(""), false
		for {
			page, err := o.deps.Workspace.List(ctx, o.access(), expected, after, manifestPage)
			if err != nil {
				return nil, o.workspaceError(err)
			}
			if page.RefreshRequired {
				restart = true
				break
			}
			result.Cursor = page.Cursor
			for _, item := range page.Items {
				result.add(entry{ID: item.EntryID, Kind: item.Kind, Path: item.Path, Version: item.ContentVersion,
					SHA256: item.SHA256, Size: item.Size})
			}
			if !page.HasMore || len(page.Items) == 0 {
				break
			}
			expected, after = page.Cursor, page.Items[len(page.Items)-1].EntryID
		}
		if !restart {
			return result, nil
		}
	}
	return nil, errors.New("the Attempt Workspace kept changing while it was read")
}

// initialize projects the whole Workspace into a new machine, once.
func (o *owner) initialize(ctx context.Context, manifest *tree) error {
	o.ahead = manifest
	items := slices.Collect(maps.Values(manifest.byID))
	// Parents precede their descendants.
	slices.SortFunc(items, func(a, b *entry) int {
		if depth := strings.Count(a.Path, "/") - strings.Count(b.Path, "/"); depth != 0 {
			return depth
		}
		return strings.Compare(a.Path, b.Path)
	})
	entries := make([]ProjectionEntry, 0, len(items))
	for _, item := range items {
		projection := ProjectionEntry{EntryID: item.ID, Kind: kindDirectory, Path: item.Path}
		if item.Kind == kindFile {
			reference, err := o.uploadEntry(ctx, item.ID, "")
			if err != nil {
				return err
			}
			projection.Kind, projection.Version, projection.Content = kindFile, string(item.Version), reference
		}
		entries = append(entries, projection)
	}
	receipt, err := o.machine.InitializeProjection(ctx, ProjectionInit{
		Fence: o.fence, MutationID: "init", Cursor: manifest.Cursor, Entries: entries,
		IgnoreFile: IgnoreFile, IgnoreFallback: slices.Clone(DefaultIgnorePatterns),
	})
	if errors.Is(err, ErrMachineConflict) {
		// Another owner initialized this machine and did not record it:
		// what it holds is unknown.
		return restartError{reason: "unrecorded_projection"}
	}
	if err != nil {
		return o.machineError(err)
	}
	o.proj = projected{Cursor: manifest.Cursor, Versions: map[model.AttemptWorkspaceEntryID]model.WorkspaceContentVersion{}}
	for _, item := range items {
		if item.Kind == kindFile {
			o.proj.Versions[item.ID] = item.Version
		}
	}
	o.ahead, o.hostCursor, o.ready = manifest, receipt.HostCursor, true
	o.row.State = model.AttemptEnvironmentRunning
	o.recordProjection()
	return o.save(ctx)
}

// uploadEntry uploads a Workspace file's current content into the machine.
// A file that no longer exists uploads as empty: a later journal entry
// removes it. When version is set and the file has moved past it, the
// fallback observed content is uploaded instead, if any.
func (o *owner) uploadEntry(ctx context.Context, entryID model.AttemptWorkspaceEntryID, version model.WorkspaceContentVersion) (*ContentRef, error) {
	file, err := o.deps.Workspace.Open(ctx, o.access(), entryID)
	var body io.Reader = bytes.NewReader(nil)
	switch {
	case err == nil:
		defer func() { _ = file.Body.Close() }()
		body = file.Body
		if item := o.aheadEntry(entryID); item != nil && item.Version == file.ContentVersion {
			item.SHA256, item.Size = file.SHA256, file.Size
		}
		if version != "" && file.ContentVersion != version {
			return nil, errVersionMoved
		}
	case errors.Is(err, ErrWorkspaceEntryNotFound):
		if version != "" {
			return nil, errVersionMoved
		}
	default:
		return nil, o.workspaceError(err)
	}
	reference, err := o.machine.UploadContent(ctx, o.fence, body)
	if err != nil {
		return nil, o.machineError(err)
	}
	return &reference, nil
}

func (o *owner) aheadEntry(entryID model.AttemptWorkspaceEntryID) *entry {
	if o.ahead == nil {
		return nil
	}
	return o.ahead.byID[entryID]
}

// errVersionMoved: the Workspace file no longer has the version asked for.
var errVersionMoved = errors.New("the Workspace file moved past the version")

// recordProjection copies the projection into the row, keeping only the
// versions of files the Workspace still holds.
func (o *owner) recordProjection() {
	versions := o.proj.Versions
	if o.ahead != nil && o.ahead.Cursor == o.proj.Cursor {
		for id := range versions {
			if _, live := o.ahead.byID[id]; !live {
				delete(versions, id)
			}
		}
	}
	o.row.ProjectionCursor, o.row.ProjectedVersions = o.proj.Cursor, maps.Clone(versions)
}

// sync runs one round both ways and stores the command log. While
// settling, every observed change is harvested now, whatever the windows
// say, and a refused projection is reported.
func (o *owner) sync(ctx context.Context, settling bool) error {
	drain := settling || o.drainNext
	o.drainNext = false
	now := o.deps.Now()
	// The journal is read when a Workspace commit woke the owner, while
	// something is left to project, and on the idle backstop; never on
	// every observation round.
	var changes []model.AttemptWorkspaceJournalEntry
	if settling || o.journalDue || now.Sub(o.journalReadAt) >= o.deps.Timing.Idle {
		var err error
		if changes, err = o.readJournal(ctx); err != nil {
			return err
		}
	}
	if err := o.observe(ctx, drain); err != nil {
		return err
	}
	if len(o.pending) > 0 && (drain || now.Sub(o.lastObserved) >= o.deps.Timing.HarvestQuiet ||
		now.Sub(o.firstPending) >= o.deps.Timing.HarvestMax) {
		if err := o.harvest(ctx); err != nil {
			return err
		}
		var err error
		if changes, err = o.readJournal(ctx); err != nil {
			return err
		}
	}
	projectErr := o.project(ctx, changes)
	// A full round or a refused projection leaves more to read.
	o.journalDue = projectErr != nil || len(changes) >= journalRound
	if errors.Is(projectErr, errProjectionConflict) {
		if err := o.diverging(ctx); err != nil {
			return err
		}
	} else if projectErr != nil {
		return projectErr
	}
	acknowledgeProcesses, err := o.processes(ctx)
	if err != nil {
		return err
	}
	// The cursors are saved before the machine is told it may forget, so a
	// later owner never asks for what it already released.
	o.recordProjection()
	o.row.ObservationCursor = int64(o.harvested) //nolint:gosec // G115: host sequences stay below 2^53.
	if o.ownerStateChanged() {
		if err := o.save(ctx); err != nil {
			return err
		}
	}
	if o.harvested > 0 {
		if err := o.machine.AcknowledgeObservations(ctx, o.fence, o.harvested); err != nil {
			return o.machineError(err)
		}
	}
	if acknowledgeProcesses > 0 {
		if err := o.machine.AcknowledgeProcesses(ctx, o.fence, acknowledgeProcesses); err != nil {
			return o.machineError(err)
		}
	}
	if settling && errors.Is(projectErr, errProjectionConflict) {
		return errProjectionConflict
	}
	return nil
}

// diverging handles a projection the guest's own changes refuse. The next
// round harvests what the guest changed, so each change is stored or kept
// as lost, and then projects the journal again with Overwrite: the
// journal's version replaces the guest's in place. When even that is
// refused without new guest activity (the guest moved a path across the
// overwritten one's boundary, or removed its parent), the machine is
// replaced after divergedLimit such rounds.
func (o *owner) diverging(ctx context.Context) error {
	before := o.hostCursor
	if err := o.observe(ctx, true); err != nil {
		return err
	}
	o.drainNext, o.overwriteNext = true, true
	if o.hostCursor != before || len(o.pending) > 0 {
		return nil
	}
	o.diverged++
	if o.diverged >= divergedLimit {
		return restartError{reason: "diverged"}
	}
	return nil
}

// readJournal returns the journal after the projected cursor and brings
// the Workspace view up to date with it.
func (o *owner) readJournal(ctx context.Context) ([]model.AttemptWorkspaceJournalEntry, error) {
	o.journalReadAt = o.deps.Now()
	var changes []model.AttemptWorkspaceJournalEntry
	after := o.proj.Cursor
	for len(changes) < journalRound {
		page, err := o.deps.Workspace.Journal(ctx, o.access(), after, journalPage)
		if err != nil {
			return nil, o.workspaceError(err)
		}
		if page.RefreshRequired {
			// The journal no longer reaches back to what the machine holds,
			// and a machine is initialized only once.
			return nil, restartError{reason: "journal_pruned"}
		}
		for _, change := range page.Entries {
			if change.Cursor != after+1 {
				return nil, errors.New("the Attempt Workspace journal is not contiguous")
			}
			o.advanceAhead(change)
			after = change.Cursor
		}
		changes = append(changes, page.Entries...)
		if !page.HasMore || len(page.Entries) == 0 {
			break
		}
	}
	return changes, nil
}

// refreshAhead brings the Workspace view up to the journal's end.
func (o *owner) refreshAhead(ctx context.Context) error {
	for range journalRound / journalPage {
		page, err := o.deps.Workspace.Journal(ctx, o.access(), o.ahead.Cursor, journalPage)
		if err != nil {
			return o.workspaceError(err)
		}
		if page.RefreshRequired {
			manifest, err := o.listManifest(ctx)
			if err != nil {
				return err
			}
			o.ahead = manifest
			return nil
		}
		for _, change := range page.Entries {
			o.advanceAhead(change)
		}
		if !page.HasMore || len(page.Entries) == 0 {
			return nil
		}
	}
	return nil
}

func (o *owner) advanceAhead(change model.AttemptWorkspaceJournalEntry) {
	if change.Cursor != o.ahead.Cursor+1 {
		return
	}
	sha, size := "", int64(0)
	if known, ok := o.echoes[change.Cursor]; ok && known.content != nil {
		sha, size = known.content.SHA256, known.content.Size
	}
	o.ahead.apply(change, sha, size)
}

// observe reads the guest changes after the last one read. With all, it
// reads until the guest has nothing more to report.
func (o *owner) observe(ctx context.Context, all bool) error {
	pages := 16
	if all {
		pages = 4096 / MaxProjectionMutations
	}
	for range pages {
		page, err := o.machine.Observe(ctx, ObserveRequest{Fence: o.fence, After: o.observed, Limit: MaxProjectionMutations})
		if errors.Is(err, ErrMachineConflict) {
			// The machine no longer holds the projection this owner made.
			return restartError{reason: "unrecorded_projection"}
		}
		if err != nil {
			return o.machineError(err)
		}
		o.hostCursor = page.HostCursor
		if len(page.Observations) == 0 {
			return nil
		}
		now := o.deps.Now()
		if len(o.pending) == 0 {
			o.firstPending = now
		}
		o.lastObserved = now
		o.pending = append(o.pending, page.Observations...)
		o.observed = page.Observations[len(page.Observations)-1].HostSequence
		if len(page.Observations) < MaxProjectionMutations {
			return nil
		}
	}
	return nil
}

// project applies the journal changes the machine does not hold yet, in
// order. A change the owner harvested itself is applied as the exact
// mutation it confirmed, which the machine recognizes as its echo. After a
// refused projection every other create, write and delete overwrites what
// the guest left in its way; each guest change that discards is harvested
// as lost to the journal.
func (o *owner) project(ctx context.Context, changes []model.AttemptWorkspaceJournalEntry) error {
	overwrite := o.overwriteNext
	for len(changes) > 0 {
		batch := changes[:min(len(changes), MaxProjectionMutations)]
		changes = changes[len(batch):]
		before := maps.Clone(o.proj.Versions)
		mutations := make([]ProjectionMutation, 0, len(batch))
		for _, change := range batch {
			var content *ContentRef
			if carriesContent(change.Operation) {
				reference, err := o.changeContent(ctx, change)
				if err != nil {
					return err
				}
				content = reference
			}
			mutation := projectionMutation(before, change, content)
			if known, ok := o.echoes[change.Cursor]; ok {
				mutation = known.mutation
				mutation.Content = content
			} else if overwrite && mutation.Operation != ProjectionMove {
				mutation.Overwrite = true
			}
			mutations = append(mutations, mutation)
			switch change.Operation {
			case model.AttemptWorkspaceMutationCreateFile, model.AttemptWorkspaceMutationReplaceFile:
				before[change.EntryID] = change.ContentVersion
			case model.AttemptWorkspaceMutationDeleteEntry:
				delete(before, change.EntryID)
			}
		}
		from, through := o.proj.Cursor, batch[len(batch)-1].Cursor
		o.applies++
		receipt, err := o.machine.ApplyProjection(ctx, ProjectionApply{
			Fence: o.fence, MutationID: projectionMutationID(from, through, o.fence.Revision, o.applies),
			From: from, Through: through, ExpectedHostCursor: o.hostCursor, Mutations: mutations,
		})
		if errors.Is(err, ErrMachineConflict) {
			return errProjectionConflict
		}
		if err != nil {
			return o.machineError(err)
		}
		o.diverged, o.hostCursor = 0, receipt.HostCursor
		o.discarded(receipt.Discarded, through)
		for _, change := range batch {
			o.proj.advance(change)
			delete(o.echoes, change.Cursor)
		}
	}
	o.overwriteNext = false
	return nil
}

// discarded marks the pending guest changes an overwrite through cursor
// discarded, so the harvest keeps each as lost to the journal. One the
// harvest already went through was recorded then, and one not read yet
// carries DiscardedBy on its page.
func (o *owner) discarded(sequences []uint64, through int64) {
	if len(sequences) == 0 {
		return
	}
	gone := make(map[uint64]bool, len(sequences))
	for _, sequence := range sequences {
		gone[sequence] = true
	}
	for index := range o.pending {
		if gone[o.pending[index].HostSequence] && o.pending[index].DiscardedBy == 0 {
			o.pending[index].DiscardedBy = through
		}
	}
	o.drainNext = o.drainNext || len(o.pending) > 0
}

// projectionMutationID names one apply uniquely within the machine's epoch.
func projectionMutationID(from, through, fence int64, apply int) string {
	return "j" + strconv.FormatInt(from, 36) + "-" + strconv.FormatInt(through, 36) + ".f" +
		strconv.FormatInt(fence, 36) + "." + strconv.FormatInt(int64(apply), 36)
}

// changeContent uploads the body of a create or replace. A harvested
// change's echo must carry the exact bytes the guest wrote: the Workspace's
// file while it still has that version, else the observed content.
func (o *owner) changeContent(ctx context.Context, change model.AttemptWorkspaceJournalEntry) (*ContentRef, error) {
	known, harvested := o.echoes[change.Cursor]
	if !harvested {
		return o.uploadEntry(ctx, change.EntryID, "")
	}
	reference, err := o.uploadEntry(ctx, change.EntryID, change.ContentVersion)
	if !errors.Is(err, errVersionMoved) {
		return reference, err
	}
	body, err := o.machine.OpenObservedContent(ctx, o.fence, *known.content)
	if errors.Is(err, ErrMachineStale) || errors.Is(err, ErrMachineNotFound) {
		// Neither copy is left: the projection is refused and the machine
		// diverges.
		return o.uploadEntry(ctx, change.EntryID, "")
	}
	if err != nil {
		return nil, o.machineError(err)
	}
	defer func() { _ = body.Close() }()
	uploaded, err := o.machine.UploadContent(ctx, o.fence, body)
	if errors.Is(err, ErrMachineStale) {
		return o.uploadEntry(ctx, change.EntryID, "")
	}
	if err != nil {
		return nil, o.machineError(err)
	}
	return &uploaded, nil
}

// ignoreCache holds the Workspace's own ignore file by version.
type ignoreCache struct {
	version model.WorkspaceContentVersion
	body    []byte
	present bool
}

// policy is the ignore decision of the Workspace's own copy of IgnoreFile,
// or of the defaults while it has none that parses.
func (o *owner) policy(ctx context.Context) (IgnorePolicy, error) {
	item := o.ahead.byPath[IgnoreFile]
	switch {
	case item == nil || item.Kind != kindFile:
		o.ignore = ignoreCache{}
	case item.Version != o.ignore.version || !o.ignore.present:
		file, err := o.deps.Workspace.Open(ctx, o.access(), item.ID)
		if errors.Is(err, ErrWorkspaceEntryNotFound) {
			o.ignore = ignoreCache{}
			break
		}
		if err != nil {
			return nil, o.workspaceError(err)
		}
		body, err := io.ReadAll(io.LimitReader(file.Body, MaxIgnoreFileBytes+1))
		_ = file.Body.Close()
		if err != nil {
			return nil, fmt.Errorf("read the ignore file: %w", err)
		}
		o.ignore = ignoreCache{version: file.ContentVersion, body: body, present: true}
	}
	return o.rules.Policy(o.ignore.body, o.ignore.present), nil
}

// maxProcessChunkBytes keeps a command log record's detail well inside the
// evidence bound.
const maxProcessChunkBytes = 48 << 10

// processPages bounds the command log pages one round stores. The owner is
// woken by guest file changes, not by programs that write none, so a round
// reads what gathered since the last.
const processPages = 16

// processes stores the command log as evidence and returns the position to
// acknowledge once the owner's cursors are saved, or 0.
func (o *owner) processes(ctx context.Context) (uint64, error) {
	var acknowledge uint64
	for range processPages {
		next, full, err := o.processPage(ctx)
		if err != nil {
			return acknowledge, err
		}
		acknowledge = max(acknowledge, next)
		if !full {
			break
		}
	}
	return acknowledge, nil
}

// processPage stores one command log page and returns the position after
// it, or 0, and whether the page was full.
func (o *owner) processPage(ctx context.Context) (uint64, bool, error) {
	if o.processLag {
		return 0, false, nil
	}
	after := uint64(o.row.ProcessCursor) //nolint:gosec // G115: cursors are never negative.
	page, err := o.machine.ObserveProcesses(ctx, o.fence, after, MaxProcessPage)
	if errors.Is(err, ErrMachineLagged) {
		// The machine released what the row says is still to store: the
		// cursor was saved behind an acknowledgement. Nothing more can be
		// read from this machine.
		o.processLag = true
		return 0, false, o.gap(ctx, gapProcessDropped, o.deps.Now(), map[string]any{"reason": "lagged"})
	}
	if err != nil {
		return 0, false, o.machineError(err)
	}
	if len(page.Events) == 0 || page.Next <= after {
		return 0, false, nil
	}
	o.origins.attribute(o.row.Epoch, page.Events)
	for _, chunk := range processChunks(page.Events) {
		at := chunk[0].At
		if at.IsZero() {
			at = o.deps.Now()
		}
		if err := o.evidence(ctx, model.AttemptEvidenceProcess, int64(chunk[0].Sequence), at, //nolint:gosec // G115: sequences stay below 2^53.
			map[string]any{"events": chunk}); err != nil {
			return 0, false, err
		}
	}
	for _, event := range page.Events {
		if event.Kind == ProcessDropped {
			at := event.At
			if at.IsZero() {
				at = o.deps.Now()
			}
			if err := o.gap(ctx, gapProcessDropped, at, map[string]any{"dropped": event.Dropped, "sequence": event.Sequence}); err != nil {
				return 0, false, err
			}
		}
	}
	o.row.ProcessCursor = int64(page.Next) //nolint:gosec // G115: sequences stay below 2^53.
	return page.Next, len(page.Events) >= MaxProcessPage, nil
}

// The kinds of command log event the attribution reads.
const (
	processStarted = "started"
	processExited  = "exited"
)

// maxProcessOrigins bounds the processes one machine's attribution keeps.
const maxProcessOrigins = 1 << 14

// processOrigins attributes every started process to the tag of the started
// process it descends from: a process Start created carries its own tag, and
// a descendant takes its parent's. It follows one machine epoch and is kept
// in memory only, so an owner that restarts, or reads past events another
// owner stored, attributes nothing it did not see start: its records leave
// Origin empty rather than guess.
type processOrigins struct {
	epoch string
	byPID map[int]model.AttemptEnvironmentProcessTag
}

// attribute sets Origin on each event of epoch's command log, in order.
func (p *processOrigins) attribute(epoch string, events []ProcessEvent) {
	if p.epoch != epoch || p.byPID == nil {
		p.epoch, p.byPID = epoch, map[int]model.AttemptEnvironmentProcessTag{}
	}
	for index := range events {
		event := &events[index]
		switch event.Kind {
		case processStarted:
			origin := event.Tag
			if origin == "" && event.ParentPID != 0 {
				origin = p.byPID[event.ParentPID]
			}
			// A start under a known PID is a program started in place, or a
			// reused PID; either way it is that process now.
			delete(p.byPID, event.PID)
			if origin != "" && len(p.byPID) < maxProcessOrigins {
				p.byPID[event.PID] = origin
			}
			event.Origin = origin
		case processExited:
			// The PID may be reused after its exit.
			event.Origin = p.byPID[event.PID]
			delete(p.byPID, event.PID)
		}
	}
}

// processChunks splits events into records of bounded detail.
func processChunks(events []ProcessEvent) [][]ProcessEvent {
	var chunks [][]ProcessEvent
	var current []ProcessEvent
	size := 0
	for _, event := range events {
		encoded, err := json.Marshal(event)
		if err != nil {
			continue
		}
		if len(current) > 0 && size+len(encoded)+1 > maxProcessChunkBytes {
			chunks, current, size = append(chunks, current), nil, 0
		}
		current, size = append(current, event), size+len(encoded)+1
	}
	if len(current) > 0 {
		chunks = append(chunks, current)
	}
	return chunks
}
