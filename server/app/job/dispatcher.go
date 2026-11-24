// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package job

import (
	"context"
	"math/rand/v2"
	"sync"
	"time"

	"github.com/sudosylabs/proctor/server/logging"
	"github.com/sudosylabs/proctor/server/model"
	"github.com/sudosylabs/proctor/server/store"
)

const (
	// dispatchRefreshInterval is the least time between two next-due reads a
	// burst of wakes can cause; a wake still signals its type at once.
	dispatchRefreshInterval = time.Second
	// dispatchJitter spreads every wait by up to a fifth either way, so the
	// nodes of a cluster never fall into step.
	dispatchJitter = 0.2
	backoffBase    = 500 * time.Millisecond
	backoffCap     = 30 * time.Second
)

// dispatcher wakes a type's workers only when that type has work: at the
// earliest due time it read, on a wake, or at the backstop that bounds how
// long a lost wake can delay a Job. One per engine replaces a timer per
// worker, so an idle node reads the queue once per backstop.
type dispatcher struct {
	store       store.JobStore
	types       []model.JobType
	ready       map[model.JobType]chan struct{}
	backstop    time.Duration
	diagnostics *logging.Logger
	recorder    Recorder
	now         func() time.Time

	mu         sync.Mutex
	pending    map[model.JobType]struct{}
	pendingAll bool
	notify     chan struct{}
}

func newDispatcher(persistence store.JobStore, registry *Registry, backstop time.Duration,
	diagnostics *logging.Logger, recorder Recorder, now func() time.Time) *dispatcher {
	d := &dispatcher{
		store:       persistence,
		types:       registry.Types(),
		ready:       map[model.JobType]chan struct{}{},
		backstop:    backstop,
		diagnostics: diagnostics,
		recorder:    recorder,
		now:         now,
		pending:     map[model.JobType]struct{}{},
		notify:      make(chan struct{}, 1),
	}
	for _, jobType := range d.types {
		descriptor, _ := registry.Descriptor(jobType)
		d.ready[jobType] = make(chan struct{}, max(descriptor.Concurrency, 1))
	}
	return d
}

// wake asks for the types' workers to look for work now; no types means
// every type. It never blocks, and wakes that arrive together coalesce.
func (d *dispatcher) wake(types ...model.JobType) {
	d.mu.Lock()
	if len(types) == 0 {
		d.pendingAll = true
	}
	for _, jobType := range types {
		d.pending[jobType] = struct{}{}
	}
	d.mu.Unlock()
	select {
	case d.notify <- struct{}{}:
	default:
	}
}

func (d *dispatcher) takePending() []model.JobType {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.pendingAll {
		d.pendingAll = false
		clear(d.pending)
		return d.types
	}
	types := make([]model.JobType, 0, len(d.pending))
	for jobType := range d.pending {
		if _, registered := d.ready[jobType]; registered {
			types = append(types, jobType)
		}
	}
	clear(d.pending)
	return types
}

// signal lets up to all of a type's workers claim. A full channel means every
// worker already has a turn coming, so the signal is dropped.
func (d *dispatcher) signal(jobType model.JobType) {
	ready := d.ready[jobType]
	for range cap(ready) {
		select {
		case ready <- struct{}{}:
		default:
			return
		}
	}
}

func (d *dispatcher) run(ctx context.Context) {
	timer := time.NewTimer(0)
	defer timer.Stop()
	var (
		nextDue     map[model.JobType]time.Time
		lastRefresh time.Time
		stale       = true
		failures    int
	)
	for {
		// woken holds the types a wake just signaled, so the refresh it
		// causes does not hand the same workers a second, empty turn.
		woken := map[model.JobType]struct{}{}
		select {
		case <-ctx.Done():
			return
		case <-d.notify:
			for _, jobType := range d.takePending() {
				d.signal(jobType)
				woken[jobType] = struct{}{}
			}
			stale = true
		case <-timer.C:
			stale = true
		}

		if stale && d.now().Sub(lastRefresh) >= dispatchRefreshInterval {
			due, err := d.store.NextDue(ctx, d.types)
			lastRefresh = d.now()
			if err != nil {
				failures++
				if ctx.Err() == nil {
					d.diagnostics.ErrorContext(ctx, "read next due durable jobs", logging.Err(err))
				}
			} else {
				failures, stale, nextDue = 0, false, due
				d.signalDue(nextDue, woken)
			}
		}
		resetTimer(timer, d.wait(nextDue, stale, lastRefresh, failures))
	}
}

// signalDue wakes every type whose earliest Job is due and forgets it, so
// the dispatcher does not spin on a Job its workers cannot claim; the next
// read finds it again if it is still there.
func (d *dispatcher) signalDue(nextDue map[model.JobType]time.Time, woken map[model.JobType]struct{}) {
	now := d.now()
	for jobType, at := range nextDue {
		if at.After(now) {
			continue
		}
		if _, signaled := woken[jobType]; !signaled {
			d.signal(jobType)
		}
		delete(nextDue, jobType)
	}
}

// wait is the sleep until the next thing to do: a failed read's backoff, a
// deferred refresh, the earliest future Job, or the jittered backstop.
func (d *dispatcher) wait(nextDue map[model.JobType]time.Time, stale bool, lastRefresh time.Time, failures int) time.Duration {
	if failures > 0 {
		return backoff(failures)
	}
	wait := jitter(d.backstop)
	if stale {
		wait = min(wait, max(lastRefresh.Add(dispatchRefreshInterval).Sub(d.now()), 0))
	}
	now := d.now()
	for _, at := range nextDue {
		wait = min(wait, max(at.Sub(now), 0))
	}
	return wait
}

// backoff is the jittered wait after the failures-th failure in a row:
// doubling from half a second up to thirty.
func backoff(failures int) time.Duration {
	delay := backoffBase
	for range min(failures-1, 16) {
		delay *= 2
		if delay >= backoffCap {
			delay = backoffCap
			break
		}
	}
	return jitter(delay)
}

// jitter spreads d by up to dispatchJitter either way.
func jitter(d time.Duration) time.Duration {
	return d + time.Duration((rand.Float64()*2-1)*dispatchJitter*float64(d)) //nolint:gosec // G404: timing jitter, not a secret.
}
