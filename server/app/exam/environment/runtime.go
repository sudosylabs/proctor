// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package environment

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/sudosylabs/proctor/server/model"
	"github.com/sudosylabs/proctor/server/store"
)

// Owner acts on one Attempt's environment while its node holds the lease.
// Run returns when the environment is done, when ctx ends (the lease was
// lost or the node is stopping), or on a failure the runtime reports. A wake
// says the Attempt's facts or Workspace changed; Run must not rely on it,
// since a wake can be lost.
type Owner interface {
	Run(ctx context.Context, lease store.AttemptRuntimeLease, wake <-chan struct{}) error
}

// OwnerFactory makes the owner of one Attempt's environment.
type OwnerFactory func(attemptID model.ExamAttemptID) Owner

// The runtime's defaults.
const (
	DefaultLeaseLifetime = 30 * time.Second
	DefaultBackstop      = 10 * time.Second
	// releaseTimeout bounds the release of a lease once its owner returns.
	releaseTimeout = 5 * time.Second
)

// RuntimeConfig is what NewRuntime needs.
type RuntimeConfig struct {
	Leases   store.AttemptRuntimeLeaseStore
	NodeID   string
	NewOwner OwnerFactory
	// Owners bounds how many environments this node owns at once.
	Owners int
	// LeaseLifetime is how long a lease lasts; it is renewed every third of
	// that. Zero means DefaultLeaseLifetime.
	LeaseLifetime time.Duration
	// Backstop is how often the node claims environments nobody holds. Zero
	// means DefaultBackstop.
	Backstop time.Duration
	// Report receives failures the runtime cannot return to anyone. Nil
	// drops them.
	Report func(attemptID model.ExamAttemptID, err error)
}

// Runtime owns environments on this node: it claims their leases, keeps
// them renewed, and runs one owner per held environment. It is inert until
// Start, and Close stops every owner and releases its lease.
type Runtime struct {
	config RuntimeConfig

	mu      sync.Mutex
	held    map[model.ExamAttemptID]*heldOwner
	pending map[model.ExamAttemptID]struct{}
	started bool
	closed  bool
	cancel  context.CancelFunc
	signal  chan struct{}
	wg      sync.WaitGroup
}

type heldOwner struct {
	wake   chan struct{}
	cancel context.CancelFunc
}

// NewRuntime validates config and returns an inert runtime.
func NewRuntime(config RuntimeConfig) (*Runtime, error) {
	if config.Leases == nil || config.NodeID == "" || config.NewOwner == nil || config.Owners < 1 {
		return nil, errors.New("attempt environment runtime needs leases, a node ID, an owner factory and an owner bound")
	}
	if config.LeaseLifetime <= 0 {
		config.LeaseLifetime = DefaultLeaseLifetime
	}
	if config.Backstop <= 0 {
		config.Backstop = DefaultBackstop
	}
	return &Runtime{
		config:  config,
		held:    make(map[model.ExamAttemptID]*heldOwner),
		pending: make(map[model.ExamAttemptID]struct{}),
		signal:  make(chan struct{}, 1),
	}, nil
}

// Start begins claiming environments. It runs once.
func (r *Runtime) Start(context.Context) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.started || r.closed {
		return nil
	}
	r.started = true
	ctx, cancel := context.WithCancel(context.Background())
	r.cancel = cancel
	r.wg.Add(1)
	go r.claimLoop(ctx)
	return nil
}

// Close stops every owner, releases its lease, and waits for them within
// ctx. It runs once.
func (r *Runtime) Close(ctx context.Context) error {
	r.mu.Lock()
	if r.closed {
		r.mu.Unlock()
		return nil
	}
	r.closed = true
	if r.cancel != nil {
		r.cancel()
	}
	r.mu.Unlock()
	done := make(chan struct{})
	go func() {
		r.wg.Wait()
		close(done)
	}()
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return errors.New("attempt environment owners did not stop in time")
	}
}

// Wake wakes the owner of attemptID when this node holds it, and otherwise
// tries to claim it at once.
func (r *Runtime) Wake(attemptID model.ExamAttemptID) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		return
	}
	if held, ok := r.held[attemptID]; ok {
		select {
		case held.wake <- struct{}{}:
		default:
		}
		return
	}
	r.pending[attemptID] = struct{}{}
	select {
	case r.signal <- struct{}{}:
	default:
	}
}

// Holds reports whether this node owns attemptID's environment now.
func (r *Runtime) Holds(attemptID model.ExamAttemptID) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	_, ok := r.held[attemptID]
	return ok
}

func (r *Runtime) claimLoop(ctx context.Context) {
	defer r.wg.Done()
	ticker := time.NewTicker(r.config.Backstop)
	defer ticker.Stop()
	r.claimAvailable(ctx)
	for {
		select {
		case <-ctx.Done():
			return
		case <-r.signal:
			r.claimPending(ctx)
		case <-ticker.C:
			r.claimAvailable(ctx)
		}
	}
}

// room is how many more environments this node may own.
func (r *Runtime) room() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.config.Owners - len(r.held)
}

func (r *Runtime) claimAvailable(ctx context.Context) {
	room := r.room()
	if room <= 0 {
		return
	}
	leases, err := r.config.Leases.ClaimAvailable(ctx, r.config.NodeID, r.config.LeaseLifetime, min(room, 1000))
	if err != nil {
		r.report("", err)
		return
	}
	for _, lease := range leases {
		r.run(ctx, lease)
	}
}

func (r *Runtime) claimPending(ctx context.Context) {
	r.mu.Lock()
	pending := r.pending
	r.pending = make(map[model.ExamAttemptID]struct{})
	r.mu.Unlock()
	for attemptID := range pending {
		if r.room() <= 0 || r.Holds(attemptID) {
			continue
		}
		lease, err := r.config.Leases.Claim(ctx, attemptID, r.config.NodeID, r.config.LeaseLifetime)
		if err != nil {
			r.report(attemptID, err)
			continue
		}
		if lease != nil {
			r.run(ctx, *lease)
		}
	}
}

// run starts the owner of a lease this node just claimed.
func (r *Runtime) run(parent context.Context, lease store.AttemptRuntimeLease) {
	ctx, cancel := context.WithCancel(parent)
	held := &heldOwner{wake: make(chan struct{}, 1), cancel: cancel}
	r.mu.Lock()
	if r.closed {
		r.mu.Unlock()
		cancel()
		r.release(lease)
		return
	}
	r.held[lease.AttemptID] = held
	r.mu.Unlock()

	r.wg.Add(2)
	go func() {
		defer r.wg.Done()
		r.keep(ctx, cancel, lease)
	}()
	go func() {
		defer r.wg.Done()
		defer r.release(lease)
		defer func() {
			r.mu.Lock()
			delete(r.held, lease.AttemptID)
			r.mu.Unlock()
			cancel()
		}()
		if err := r.config.NewOwner(lease.AttemptID).Run(ctx, lease, held.wake); err != nil && ctx.Err() == nil {
			r.report(lease.AttemptID, err)
		}
	}()
}

// keep renews the lease every third of its lifetime and ends the owner's
// context once the lease is lost or about to run out.
func (r *Runtime) keep(ctx context.Context, cancel context.CancelFunc, lease store.AttemptRuntimeLease) {
	interval := r.config.LeaseLifetime / 3
	timer := time.NewTimer(interval)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
		}
		renewed, err := r.config.Leases.Renew(ctx, lease, r.config.LeaseLifetime)
		switch {
		case errors.Is(err, store.ErrAttemptRuntimeLeaseLost):
			cancel()
			return
		case err != nil:
			// A failed renewal is retried until the lease would run out.
			if time.Until(lease.ExpiresAt) < interval {
				cancel()
				return
			}
			timer.Reset(time.Second)
			continue
		}
		lease = *renewed
		timer.Reset(interval)
	}
}

func (r *Runtime) release(lease store.AttemptRuntimeLease) {
	ctx, cancel := context.WithTimeout(context.Background(), releaseTimeout)
	defer cancel()
	if err := r.config.Leases.Release(ctx, lease); err != nil {
		r.report(lease.AttemptID, err)
	}
}

func (r *Runtime) report(attemptID model.ExamAttemptID, err error) {
	if r.config.Report != nil && err != nil && !errors.Is(err, context.Canceled) {
		r.config.Report(attemptID, err)
	}
}
