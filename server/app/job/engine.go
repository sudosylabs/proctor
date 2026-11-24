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

// Engine runs the registered job types on one node. It does nothing until
// Start and stops at Close.
type Engine struct {
	deps     Dependencies
	registry *Registry
	dispatch *dispatcher
	// claims bounds how many claim transactions run at once, so a wave of
	// work can never take the whole pool.
	claims chan struct{}
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

// Dependencies are the collaborators and policy of the engine, validated once
// by New.
type Dependencies struct {
	Store       store.JobStore
	Descriptors []Descriptor
	NodeID      string
	Diagnostics *logging.Logger
	Recorder    Recorder
	// Tracer, when set, encloses each claimed execution in a span.
	Tracer Tracer
	Policy Policy
	// Now reads the instant a recurrence proposes and progress is coalesced
	// at; nil means time.Now. Waiting always uses real timers.
	Now           func() time.Time
	Recurrences   []Recurrence
	PeriodicTasks []PeriodicTask
}

// Policy is the engine's timing. BackstopInterval bounds how long a Job can
// wait when every wake about it was lost; workers otherwise claim only when
// woken or when the earliest due Job comes due. MaximumConcurrentClaims bounds
// the claim transactions running at once; New sets 4 when it is not
// positive. ProposalRetryDelay is how long a failed recurrence proposal waits
// before it is retried; New sets one minute when it is not positive.
type Policy struct {
	BackstopInterval        time.Duration
	MaximumConcurrentClaims int
	ProposalRetryDelay      time.Duration
}

// New validates deps and returns an idle engine; Start begins claiming work.
func New(deps Dependencies) (*Engine, error) {
	registry, err := NewRegistry(deps.Descriptors)
	if err != nil {
		return nil, err
	}
	if deps.Store == nil || deps.NodeID == "" || deps.Diagnostics == nil || deps.Policy.BackstopInterval <= 0 {
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
	if deps.Policy.MaximumConcurrentClaims <= 0 {
		deps.Policy.MaximumConcurrentClaims = 4
	}
	if deps.Now == nil {
		deps.Now = time.Now
	}
	deps.Recurrences, deps.PeriodicTasks = recurrences, periodicTasks
	return &Engine{
		deps:     deps,
		registry: registry,
		dispatch: newDispatcher(deps.Store, registry, deps.Policy.BackstopInterval, deps.Diagnostics, deps.Recorder, deps.Now),
		claims:   make(chan struct{}, deps.Policy.MaximumConcurrentClaims),
		progress: newProgressCoalescer(progressWindow, deps.Now),
	}, nil
}

// Descriptor returns the descriptor of a registered type, or an error for an
// unregistered one.
func (r *Engine) Descriptor(jobType model.JobType) (Descriptor, error) {
	if r == nil {
		return Descriptor{}, errors.New("job engine is nil")
	}
	descriptor, err := r.registry.Descriptor(jobType)
	descriptor.Handler = nil
	return descriptor, err
}

// Types returns the registered job types in stable order.
func (r *Engine) Types() []model.JobType {
	if r == nil {
		return nil
	}
	return r.registry.Types()
}

// Start launches as many claim loops per type as its Concurrency, one
// proposal loop per recurrence and one loop per periodic task. It fails when
// the engine was already started or closed.
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
			go r.claimLoop(runCtx, descriptor, r.dispatch.ready[jobType])
		}
	}
	r.wg.Add(1)
	go func() {
		defer r.wg.Done()
		r.dispatch.run(runCtx)
	}()

	for _, recurrence := range r.deps.Recurrences {
		r.wg.Add(1)
		go func(value Recurrence) {
			defer r.wg.Done()
			runRecurrenceProposal(runCtx, value, r.deps.Diagnostics, r.deps.Now, r.deps.Policy.ProposalRetryDelay, r.Wake, r.deps.Recorder)
		}(recurrence)
	}

	for _, task := range r.deps.PeriodicTasks {
		r.wg.Add(1)
		go func(value PeriodicTask) {
			defer r.wg.Done()
			runPeriodicTask(runCtx, value, r.deps.Diagnostics, r.deps.Recorder)
		}(task)
	}

	return nil
}

// Close cancels claiming and every running execution, then waits for the
// workers until ctx ends or the longest registered job timeout passes,
// whichever is first. No execution may legitimately outlive its timeout. An
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

// Wake makes every type's workers look for work now rather than at the next
// due time or backstop. It never blocks, and wakes before Start are kept.
func (r *Engine) Wake() {
	r.dispatch.wake()
}

// WakeTypes makes the listed types' workers look for work now. Call it after
// committing work of those types; it never blocks.
func (r *Engine) WakeTypes(types ...model.JobType) {
	if len(types) == 0 {
		return
	}
	r.dispatch.wake(types...)
}
