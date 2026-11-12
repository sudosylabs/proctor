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
	"fmt"
	"sync"
	"time"

	"github.com/sudosylabs/proctor/server/logging"
	"github.com/sudosylabs/proctor/server/model"
	"github.com/sudosylabs/proctor/server/store"
)

// Engine runs the registered Job types on one node: it claims Jobs from the
// store, runs their handlers under a renewed lease, proposes recurrences and
// runs periodic tasks. It does nothing until Start and stops at Close.
type Engine struct {
	deps     Dependencies
	registry *Registry
	wake     chan struct{}
	// changes hears committed transitions once attached; progress coalesces
	// the checkpoint announcements.
	changes  ChangeListener
	progress *progressCoalescer

	mu      sync.Mutex
	started bool
	closed  bool
	cancel  context.CancelFunc
	wg      sync.WaitGroup
}

// Dependencies are the collaborators and policy of the Job engine; New
// validates them once, fills policy defaults, and the Engine keeps them in
// deps.
type Dependencies struct {
	Store       store.JobStore
	Descriptors []Descriptor
	NodeID      string
	Diagnostics *logging.Logger
	Recorder    Recorder
	// Tracer, when set, encloses each claimed execution in a span.
	Tracer        Tracer
	Policy        Policy
	Clock         Clock
	Recurrences   []Recurrence
	PeriodicTasks []PeriodicTask
}

// Policy is the engine's timing. PollInterval is how long a claim loop that
// found no work waits before asking the store again. ProposalRetryDelay is
// how long a failed recurrence proposal waits before it is retried; New sets
// one minute when it is not positive.
type Policy struct {
	PollInterval       time.Duration
	ProposalRetryDelay time.Duration
}

// New validates deps once and constructs an idle Job engine; Start begins
// claiming work.
func New(deps Dependencies) (*Engine, error) {
	registry, err := NewRegistry(deps.Descriptors)
	if err != nil {
		return nil, err
	}
	if deps.Store == nil || deps.NodeID == "" || deps.Diagnostics == nil || deps.Policy.PollInterval <= 0 {
		return nil, errors.New("invalid job engine dependencies")
	}
	recurrences, err := cloneRecurrences(deps.Recurrences)
	if err != nil {
		return nil, err
	}
	periodicTasks, err := clonePeriodicTasks(deps.PeriodicTasks)
	if err != nil {
		return nil, err
	}
	if deps.Policy.ProposalRetryDelay <= 0 {
		deps.Policy.ProposalRetryDelay = time.Minute
	}
	if deps.Clock == nil {
		deps.Clock = systemClock{}
	}
	deps.Recurrences, deps.PeriodicTasks = recurrences, periodicTasks
	return &Engine{
		deps:     deps,
		registry: registry,
		wake:     make(chan struct{}, 1),
		progress: newProgressCoalescer(progressWindow, deps.Clock.Now),
	}, nil
}

// Descriptor returns the immutable execution contract for a registered type.
func (r *Engine) Descriptor(jobType model.JobType) (Descriptor, error) {
	if r == nil {
		return Descriptor{}, errors.New("job engine is nil")
	}
	descriptor, err := r.registry.Descriptor(jobType)
	descriptor.Handler = nil
	return descriptor, err
}

// Types returns the registered Job types in stable order.
func (r *Engine) Types() []model.JobType {
	if r == nil {
		return nil
	}
	return r.registry.Types()
}

// Start launches, under ctx, as many claim loops per registered type as its
// Concurrency, one proposal loop per recurrence and one loop per periodic
// task. It fails when the engine was already started or closed.
func (r *Engine) Start(ctx context.Context) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		return errors.New("job engine is closed")
	}
	if r.started {
		return errors.New("job engine is already started")
	}

	runCtx, cancel := context.WithCancel(ctx)
	r.cancel = cancel
	r.started = true
	for _, jobType := range r.registry.Types() {
		descriptor, err := r.registry.Descriptor(jobType)
		if err != nil {
			cancel()
			return err
		}

		for range descriptor.Concurrency {
			r.wg.Add(1)
			go r.claimLoop(runCtx, descriptor)
		}
	}

	for _, recurrence := range r.deps.Recurrences {
		r.wg.Add(1)
		go func(value Recurrence) {
			defer r.wg.Done()
			runRecurrenceProposal(runCtx, value, r.deps.Diagnostics, r.deps.Clock, r.deps.Policy.ProposalRetryDelay, r.Wake, r.deps.Recorder)
		}(recurrence)
	}

	for _, task := range r.deps.PeriodicTasks {
		r.wg.Add(1)
		go func(value PeriodicTask) {
			defer r.wg.Done()
			runPeriodicTask(runCtx, value, r.deps.Diagnostics, r.deps.Clock, r.deps.Recorder)
		}(task)
	}

	return nil
}

// Close cancels claiming and every running execution, then waits for the
// workers to return until ctx, the caller's shutdown budget, ends or the
// longest registered Job timeout passes, whichever comes first. No execution
// may legitimately outlive its own timeout, so the wait never exceeds it. An
// ended wait returns an error while workers may still be returning.
func (r *Engine) Close(ctx context.Context) error {
	r.mu.Lock()
	if r.closed {
		r.mu.Unlock()
		return nil
	}
	r.closed = true
	cancel := r.cancel
	r.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	drained := make(chan struct{})
	go func() {
		r.wg.Wait()
		close(drained)
	}()
	wait, cancelWait := context.WithTimeout(ctx, r.registry.MaximumTimeout())
	defer cancelWait()
	select {
	case <-drained:
		return nil
	case <-wait.Done():
		return fmt.Errorf("durable job engine shutdown deadline exceeded: %w", wait.Err())
	}
}

// Wake makes one waiting claim loop look for work now rather than at its next
// poll. It never blocks, and one pending wake is remembered when no loop is
// waiting.
func (r *Engine) Wake() {
	select {
	case r.wake <- struct{}{}:
	default:
	}
}
