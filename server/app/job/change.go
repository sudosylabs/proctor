// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package job

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/sudosylabs/proctor/server/model"
)

// progressWindow is the shortest time between two progress announcements of
// one job on one node. A status change is announced at once.
const progressWindow = 2 * time.Second

// Change is one committed transition of an operator-visible job. Progress is
// set only for a type that reports it; the command, checkpoint document,
// result and errors never travel.
type Change struct {
	ID       model.JobID
	Type     model.JobType
	Status   model.JobStatus
	Progress *model.JobProgress
}

// ChangeListener hears each Change after the store operation that made it has
// returned. It is called concurrently from workers and operator calls and
// must not block.
type ChangeListener interface {
	JobChanged(context.Context, Change)
}

// AttachChangeListener is called once, before Start; until then the engine
// announces nothing.
func (r *Engine) AttachChangeListener(listener ChangeListener) error {
	if listener == nil {
		return errors.New("job change listener is nil")
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.started || r.closed {
		return errors.New("job change listener must be attached before the engine starts")
	}
	if r.changes != nil {
		return errors.New("job change listener is already attached")
	}
	r.changes = listener
	return nil
}

// announceStatus bypasses and clears the progress window: the status
// announcement carries the latest progress, so the next checkpoint is
// announced at once.
func (r *Engine) announceStatus(ctx context.Context, record *model.Job) {
	if record == nil {
		return
	}
	r.progress.forget(record.ID)
	r.announce(ctx, record)
}

func (r *Engine) announceProgress(ctx context.Context, record *model.Job) {
	if record == nil || r.changes == nil || !r.progress.admit(record.ID) {
		return
	}
	r.announce(ctx, record)
}

func (r *Engine) announce(ctx context.Context, record *model.Job) {
	if r.changes == nil {
		return
	}
	descriptor, err := r.registry.Descriptor(record.Type)
	if err != nil || descriptor.Visibility != VisibilityOperator {
		return
	}
	change := Change{ID: record.ID, Type: record.Type, Status: record.Status}
	if len(descriptor.ProgressStages) > 0 && record.Progress != nil {
		progress := *record.Progress
		change.Progress = &progress
	}
	r.changes.JobChanged(ctx, change)
}

// progressCoalescer admits at most one progress announcement per job within
// its window. A job is forgotten when its status changes or its execution on
// this node ends, so the table holds only the jobs this node is running.
type progressCoalescer struct {
	window time.Duration
	now    func() time.Time

	mu   sync.Mutex
	last map[model.JobID]time.Time
}

func newProgressCoalescer(window time.Duration, now func() time.Time) *progressCoalescer {
	return &progressCoalescer{window: window, now: now, last: make(map[model.JobID]time.Time)}
}

func (c *progressCoalescer) admit(id model.JobID) bool {
	at := c.now()
	c.mu.Lock()
	defer c.mu.Unlock()
	if last, seen := c.last[id]; seen && at.Sub(last) < c.window {
		return false
	}
	c.last[id] = at
	return true
}

func (c *progressCoalescer) forget(id model.JobID) {
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.last, id)
}
