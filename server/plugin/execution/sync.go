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
	"time"

	"github.com/sudosylabs/execenv"
	"github.com/sudosylabs/execenv/ignore"

	"github.com/sudosylabs/proctor/server/plugin"
)

// sync runs one round of both file loops and the process log. With drain,
// every observed change is harvested now, whatever the windows say.
func (o *owner) sync(ctx context.Context, drain bool) error {
	environment, ok := o.currentBinding()
	if !ok || !o.ready {
		return errWait
	}
	journal, err := o.readJournal(ctx, environment)
	if err != nil {
		return err
	}
	if err := o.observe(ctx); err != nil {
		return err
	}
	settings := o.plugin.settings()
	now := time.Now()
	if len(o.pending) > 0 && (drain || now.Sub(o.lastObserved) >= settings.quiet() || now.Sub(o.firstPending) >= settings.max()) {
		if err := o.harvest(ctx, environment); err != nil {
			return err
		}
		if journal, err = o.readJournal(ctx, environment); err != nil {
			return err
		}
	}
	if err := o.project(ctx, environment, journal); err != nil {
		return err
	}
	return o.processes(ctx)
}

// readJournal returns the journal after the projected cursor and brings
// the Workspace view up to date with it.
func (o *owner) readJournal(ctx context.Context, environment plugin.BoundEnvironment) ([]plugin.WorkspaceChange, error) {
	var changes []plugin.WorkspaceChange
	after := o.projected.Cursor
	for len(changes) < execenv.MaxProjectionMutations*4 {
		page, err := o.plugin.host.AttemptWorkspaces().ListJournal(ctx, plugin.WorkspaceJournalRequest{
			Environment: environment, AfterCursor: after, Limit: journalPage,
		})
		if err != nil {
			return nil, o.workspaceError(err)
		}
		if page.RefreshRequired {
			// The journal no longer reaches back to what the grant holds,
			// and execenv initializes a projection only once per epoch.
			return nil, errRestart{reason: "journal_pruned"}
		}
		o.current = page.CurrentCursor
		for _, change := range page.Entries {
			if o.ahead != nil && change.Cursor > o.ahead.Cursor {
				sha, size := "", int64(0)
				if known, ok := o.echoes[change.Cursor]; ok && known.mutation.Content != nil {
					sha, size = known.mutation.Content.Digest, known.mutation.Content.Size
				}
				o.ahead.apply(change, sha, size)
			}
			after = change.Cursor
		}
		changes = append(changes, page.Entries...)
		if !page.HasMore || len(page.Entries) == 0 {
			break
		}
	}
	return changes, nil
}

// observe reads the guest changes after the last one read.
func (o *owner) observe(ctx context.Context) error {
	for range 16 {
		page, err := o.env.Observe(ctx, o.fence, o.observed, execenv.MaxProjectionMutations)
		if err != nil {
			return o.envError(err)
		}
		o.hostCursor = page.HostCursor
		if len(page.Observations) == 0 {
			return nil
		}
		now := time.Now()
		if len(o.pending) == 0 {
			o.firstPending = now
		}
		o.lastObserved = now
		o.pending = append(o.pending, page.Observations...)
		o.observed = page.Observations[len(page.Observations)-1].HostSequence
		if len(page.Observations) < execenv.MaxProjectionMutations {
			return nil
		}
	}
	return nil
}

// project applies the journal changes the grant does not hold yet, in
// order. A change the owner harvested itself is applied as the exact
// mutation it confirmed, which execenv recognizes as its echo.
func (o *owner) project(ctx context.Context, environment plugin.BoundEnvironment, changes []plugin.WorkspaceChange) error {
	for len(changes) > 0 {
		batch := changes[:min(len(changes), execenv.MaxProjectionMutations)]
		changes = changes[len(batch):]
		before := make(map[string]string, len(o.projected.Versions))
		for id, version := range o.projected.Versions {
			before[id] = version
		}
		mutations := make([]execenv.ProjectionMutation, 0, len(batch))
		for _, change := range batch {
			var content *execenv.ContentReference
			if change.Operation == plugin.WorkspaceCreateFile || change.Operation == plugin.WorkspaceReplaceFile {
				reference, err := o.changeContent(ctx, environment, change)
				if err != nil {
					return err
				}
				content = reference
			}
			mutation := projectionMutation(before, change, content)
			if known, ok := o.echoes[change.Cursor]; ok {
				mutation = known.mutation
				mutation.Content = content
			}
			mutations = append(mutations, mutation)
			switch change.Operation {
			case plugin.WorkspaceCreateFile, plugin.WorkspaceReplaceFile:
				before[change.EntryID] = change.ContentVersion
			case plugin.WorkspaceDeleteEntry:
				delete(before, change.EntryID)
			}
		}
		from, through := uint64(o.projected.Cursor), uint64(batch[len(batch)-1].Cursor)
		o.applies++
		receipt, err := o.env.ApplyProjection(ctx, execenv.ProjectionApply{
			Fence: o.fence, MutationID: fmt.Sprintf("j%d-%d.%d", from, through, o.applies),
			FromWorkspaceCursor: from, ThroughWorkspaceCursor: through, ExpectedHostCursor: o.hostCursor,
			Mutations: mutations,
		})
		if errors.Is(err, execenv.ErrConflict) {
			// A guest change not yet harvested, or one the Workspace
			// refused, stands in the way. Harvesting usually clears it;
			// a guest that stays diverged gets a new grant.
			o.conflicts++
			if o.conflicts >= divergedLimit {
				return errRestart{reason: "diverged"}
			}
			return nil
		}
		if err != nil {
			return o.envError(err)
		}
		o.conflicts, o.hostCursor = 0, receipt.HostCursor
		for _, change := range batch {
			o.projected.advance(change)
			if known, ok := o.echoes[change.Cursor]; ok {
				o.echoBytes -= len(known.body)
				delete(o.echoes, change.Cursor)
			}
		}
		if err := o.saveProjection(ctx); err != nil {
			return err
		}
		grant, err := o.records.updateGrant(ctx, o.attemptID, func(grant *grantRecord, _ bool) (bool, error) {
			grant.WorkspaceCursor = o.projected.Cursor
			return true, nil
		})
		if err != nil {
			return err
		}
		o.grant = grant
	}
	return nil
}

// changeContent uploads the bytes of a create or replace: the harvested
// bytes the owner kept for its own change, else the Workspace's current
// content of the entry.
func (o *owner) changeContent(ctx context.Context, environment plugin.BoundEnvironment, change plugin.WorkspaceChange) (*execenv.ContentReference, error) {
	if known, ok := o.echoes[change.Cursor]; ok && known.body != nil {
		reference, err := o.env.UploadProjectionContent(ctx, o.fence, bytes.NewReader(known.body))
		if err != nil {
			return nil, o.envError(err)
		}
		return &reference, nil
	}
	reference, sha, size, err := o.uploadEntry(ctx, environment, change.EntryID)
	if err != nil {
		return nil, err
	}
	if item := o.ahead.byID[change.EntryID]; item != nil && item.Version == change.ContentVersion {
		item.SHA256, item.Size = sha, size
	}
	return reference, nil
}

// ignoreCache holds the Attempt Workspace's own ignore file by version.
type ignoreCache struct {
	version string
	rules   *ignore.Rules
	body    []byte
	present bool
}

// policy is the ignore decision of the Attempt Workspace's own copy of
// IgnoreFile, or of the defaults while it has none that parses.
func (o *owner) policy(ctx context.Context, environment plugin.BoundEnvironment) (ignore.Policy, error) {
	if o.ignore.rules == nil {
		rules, err := ignore.New(IgnoreFile, DefaultIgnorePatterns)
		if err != nil {
			return ignore.Policy{}, err
		}
		o.ignore.rules = rules
	}
	item := o.ahead.byPath[IgnoreFile]
	switch {
	case item == nil || item.Kind != kindFile:
		o.ignore.version, o.ignore.body, o.ignore.present = "", nil, false
	case item.Version != o.ignore.version:
		file, err := o.plugin.host.AttemptWorkspaces().OpenFile(ctx, environment, item.ID)
		if err != nil {
			return ignore.Policy{}, o.workspaceError(err)
		}
		body, err := io.ReadAll(io.LimitReader(file.Body, ignore.MaxFileBytes+1))
		_ = file.Body.Close()
		if err != nil {
			return ignore.Policy{}, err
		}
		o.ignore.version, o.ignore.body, o.ignore.present = item.Version, body, true
	}
	return o.ignore.rules.Policy(o.ignore.body, o.ignore.present), nil
}

// processes stores the process log as evidence, then acknowledges it.
func (o *owner) processes(ctx context.Context) error {
	page, err := o.env.ObserveProcesses(ctx, o.fence, o.grant.ProcessCursor, execenv.MaxProcessPage)
	if errors.Is(err, execenv.ErrLagged) {
		// Another owner already stored and acknowledged further.
		return nil
	}
	if err != nil {
		return o.envError(err)
	}
	if len(page.Events) == 0 {
		return nil
	}
	for _, chunk := range processChunks(page.Events) {
		gap := false
		for _, event := range chunk {
			gap = gap || event.Kind == execenv.ProcessDropped
		}
		detail := mustJSON(map[string]any{"events": chunk})
		if err := o.evidence(ctx, evidenceProcess, gap, chunk[0].At, detail, int64(chunk[0].Sequence)); err != nil {
			return err
		}
	}
	if err := o.env.AcknowledgeProcesses(ctx, o.fence, page.Next); err != nil {
		return o.envError(err)
	}
	grant, err := o.records.updateGrant(ctx, o.attemptID, func(grant *grantRecord, _ bool) (bool, error) {
		if page.Next <= grant.ProcessCursor {
			return false, nil
		}
		grant.ProcessCursor = page.Next
		return true, nil
	})
	if err != nil {
		return err
	}
	o.grant = grant
	return nil
}

// maxProcessChunkBytes keeps a process record's detail well inside the
// evidence bound.
const maxProcessChunkBytes = 48 << 10

// processChunks splits events into records of bounded detail.
func processChunks(events []execenv.ProcessEvent) [][]execenv.ProcessEvent {
	var chunks [][]execenv.ProcessEvent
	var current []execenv.ProcessEvent
	size := 0
	for _, event := range events {
		encoded := len(mustJSON(event)) + 1
		if len(current) > 0 && size+encoded > maxProcessChunkBytes {
			chunks, current, size = append(chunks, current), nil, 0
		}
		current, size = append(current, event), size+encoded
	}
	if len(current) > 0 {
		chunks = append(chunks, current)
	}
	return chunks
}
