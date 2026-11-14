// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package pluginhost

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	"github.com/sudosylabs/proctor/server/plugin"
)

const (
	// DefaultLeaseLifetime is how long a worker's lease lasts when
	// Dependencies leaves it unset. Hold renews it every third of that.
	DefaultLeaseLifetime = 30 * time.Second
	// DefaultWorkerRestartDelay is the first wait before a failed worker is
	// started again when Dependencies leaves it unset. Each further failure
	// doubles it, up to MaxWorkerRestartDelay; a run that lasted that long
	// starts the doubling over.
	DefaultWorkerRestartDelay = time.Second
	// MaxWorkerRestartDelay caps the wait before a failed worker restarts.
	MaxWorkerRestartDelay = time.Minute
	// leaseReleaseTimeout bounds the release of a held lease once its
	// holder is done.
	leaseReleaseTimeout = 5 * time.Second
)

// LeaseStore keeps every plugin's leases. The host names the plugin and
// this node on each call; the store keeps one plugin's leases apart from
// another's and answers with the plugin package's errors.
type LeaseStore interface {
	Claim(ctx context.Context, pluginID, nodeID, key string, lifetime time.Duration) (plugin.Lease, error)
	ClaimAvailable(ctx context.Context, pluginID, nodeID string, keys []string, lifetime time.Duration, limit int) ([]plugin.Lease, error)
	Renew(ctx context.Context, pluginID, nodeID string, lease plugin.Lease, lifetime time.Duration) (plugin.Lease, error)
	Release(ctx context.Context, pluginID, nodeID string, lease plugin.Lease) error
}

// activation is one activation of one plugin: what it registered that lives
// as long as it does, its workers, and whether its capabilities may still
// be used.
type activation struct {
	host  *Host
	entry *entry

	// live is true from the start of Activate until the activation ends:
	// the plugin is deactivated, fails to activate, or fails by panicking.
	live atomic.Bool

	mu      sync.Mutex
	workers []registeredWorker
	running bool
	ctx     context.Context
	cancel  context.CancelFunc
	wg      sync.WaitGroup
}

type registeredWorker struct {
	name string
	run  plugin.Worker
}

func (h *Host) newActivation(e *entry) *activation {
	ctx, cancel := context.WithCancel(context.Background())
	a := &activation{host: h, entry: e, ctx: ctx, cancel: cancel}
	a.live.Store(true)
	return a
}

func (a *activation) usable() bool { return a != nil && a.live.Load() }

// halt ends the activation without waiting: its capabilities answer
// plugin.ErrUnavailable and its workers' contexts end. It is safe to call
// with the entry's lock held.
func (a *activation) halt() {
	if a == nil {
		return
	}
	a.live.Store(false)
	a.cancel()
}

// stop halts the activation and waits, within ctx and the host's timeout,
// for its workers to return.
func (a *activation) stop(ctx context.Context) error {
	if a == nil {
		return nil
	}
	a.halt()
	done := make(chan struct{})
	go func() {
		a.wg.Wait()
		close(done)
	}()
	bounded, cancel := context.WithTimeout(ctx, a.host.timeout)
	defer cancel()
	select {
	case <-done:
		return nil
	case <-bounded.Done():
		a.entry.logger.Error("plugin workers did not stop in time")
		return fmt.Errorf("plugin %q workers: %w", a.entry.manifest.ID, ErrTimedOut)
	}
}

// startWorkers runs every registered worker once the activation succeeded
// and the node has started workers. It runs at most once.
func (a *activation) startWorkers() {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.running || !a.live.Load() {
		return
	}
	a.running = true
	for _, worker := range a.workers {
		a.wg.Add(1)
		go a.supervise(worker)
	}
}

// AddWorker records a worker for the plugin being activated.
func (r *registrar) AddWorker(name string, run plugin.Worker) error {
	if !validWorkerName(name) {
		return fmt.Errorf("worker name %q: %w", name, plugin.ErrInvalidArgument)
	}
	if run == nil {
		return errors.New("worker is nil")
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if !r.open {
		return errors.New("workers are added during Activate")
	}
	a := r.activation
	a.mu.Lock()
	defer a.mu.Unlock()
	for _, existing := range a.workers {
		if existing.name == name {
			return fmt.Errorf("worker %q is already registered", name)
		}
	}
	a.workers = append(a.workers, registeredWorker{name: name, run: run})
	return nil
}

// supervise runs one worker until the activation ends, starting it again
// after an error or a panic.
func (a *activation) supervise(worker registeredWorker) {
	defer a.wg.Done()
	logger := a.entry.logger.With(slog.String("worker", worker.name))
	delay := a.host.restartDelay
	for a.ctx.Err() == nil {
		started := time.Now()
		leases := a.host.newWorkerLeases(a)
		err := a.entry.call("worker", func() error { return worker.run(a.ctx, leases) })
		leases.close()
		if a.ctx.Err() != nil {
			return
		}
		if err == nil {
			logger.Info("plugin worker finished")
			return
		}
		if time.Since(started) >= MaxWorkerRestartDelay {
			delay = a.host.restartDelay
		}
		logger.Error("plugin worker failed; restarting it", slog.Any("error", err), slog.Duration("delay", delay))
		timer := time.NewTimer(delay)
		select {
		case <-a.ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}
		delay = min(2*delay, max(MaxWorkerRestartDelay, a.host.restartDelay))
	}
}

// workerLeases is the plugin.Leases one run of a worker receives. Closing
// it ends every Hold and waits for their releases.
type workerLeases struct {
	host       *Host
	activation *activation
	ctx        context.Context
	cancel     context.CancelFunc
	holds      sync.WaitGroup
}

var _ plugin.Leases = (*workerLeases)(nil)

func (h *Host) newWorkerLeases(a *activation) *workerLeases {
	ctx, cancel := context.WithCancel(a.ctx)
	return &workerLeases{host: h, activation: a, ctx: ctx, cancel: cancel}
}

func (l *workerLeases) close() {
	l.cancel()
	l.holds.Wait()
}

func (l *workerLeases) pluginID() string { return l.activation.entry.manifest.ID }

func (l *workerLeases) available() error {
	if l.host.leases == nil || l.host.nodeID == "" || !l.activation.usable() {
		return fmt.Errorf("plugin %q leases: %w", l.pluginID(), plugin.ErrUnavailable)
	}
	return nil
}

func (l *workerLeases) Claim(ctx context.Context, key string) (plugin.Lease, error) {
	if err := l.available(); err != nil {
		return plugin.Lease{}, err
	}
	return l.host.leases.Claim(ctx, l.pluginID(), l.host.nodeID, key, l.host.leaseLifetime)
}

func (l *workerLeases) ClaimAvailable(ctx context.Context, keys []string, limit int) ([]plugin.Lease, error) {
	if err := l.available(); err != nil {
		return nil, err
	}
	return l.host.leases.ClaimAvailable(ctx, l.pluginID(), l.host.nodeID, keys, l.host.leaseLifetime, limit)
}

func (l *workerLeases) Renew(ctx context.Context, lease plugin.Lease) (plugin.Lease, error) {
	if err := l.available(); err != nil {
		return plugin.Lease{}, err
	}
	return l.host.leases.Renew(ctx, l.pluginID(), l.host.nodeID, lease, l.host.leaseLifetime)
}

func (l *workerLeases) Release(ctx context.Context, lease plugin.Lease) error {
	if l.host.leases == nil || l.host.nodeID == "" {
		return fmt.Errorf("plugin %q leases: %w", l.pluginID(), plugin.ErrUnavailable)
	}
	return l.host.leases.Release(ctx, l.pluginID(), l.host.nodeID, lease)
}

// Hold claims key and keeps it renewed in the background.
func (l *workerLeases) Hold(ctx context.Context, key string) (context.Context, plugin.Lease, error) {
	claimed := time.Now()
	lease, err := l.Claim(ctx, key)
	if err != nil {
		return nil, plugin.Lease{}, err
	}
	held, lost := context.WithCancel(ctx)
	l.holds.Add(1)
	go l.keep(ctx, held, lost, lease, claimed)
	return held, lease, nil
}

// keep renews lease until the holder's ctx or the worker run ends, then
// releases it. It ends held as soon as the lease is lost, or as soon as it
// cannot prove the lease past the safety margin. Its deadline is counted on
// this node's clock from when each successful request was sent, which is
// never later than PostgreSQL's own start of the lifetime.
func (l *workerLeases) keep(ctx, held context.Context, lost context.CancelFunc, lease plugin.Lease, sent time.Time) {
	defer l.holds.Done()
	defer lost()
	lifetime := l.host.leaseLifetime
	interval, margin, retry := lifetime/3, lifetime/6, min(time.Second, lifetime/10)
	deadline := sent.Add(lifetime)
	timer := time.NewTimer(interval)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			l.release(lease)
			return
		case <-l.ctx.Done():
			l.release(lease)
			return
		case <-timer.C:
		}
		sent := time.Now()
		attempt, cancel := context.WithDeadline(l.ctx, deadline.Add(-margin))
		renewed, err := l.Renew(attempt, lease)
		cancel()
		switch {
		case err == nil:
			lease, deadline = renewed, sent.Add(lifetime)
			timer.Reset(interval)
		case errors.Is(err, plugin.ErrLeaseLost):
			l.activation.entry.logger.Warn("plugin lease lost", slog.Int64("fence", lease.Fence))
			return
		case time.Until(deadline) <= margin:
			l.activation.entry.logger.Warn("plugin lease could not be renewed in time; giving it up",
				slog.Int64("fence", lease.Fence), slog.Any("error", err))
			lost()
			l.release(lease)
			return
		default:
			timer.Reset(retry)
		}
	}
}

// release gives up a lease after its holder is done, within its own bound:
// the holder's context has already ended.
func (l *workerLeases) release(lease plugin.Lease) {
	ctx, cancel := context.WithTimeout(context.Background(), leaseReleaseTimeout)
	defer cancel()
	if err := l.Release(ctx, lease); err != nil {
		l.activation.entry.logger.Warn("plugin lease release failed; it will expire", slog.Any("error", err))
	}
}

// validWorkerName reports whether name is 1 to 64 lowercase letters,
// digits, underscores, dots or hyphens, starting with a letter.
func validWorkerName(name string) bool {
	if name == "" || len(name) > 64 || name[0] < 'a' || name[0] > 'z' {
		return false
	}
	for i := 0; i < len(name); i++ {
		c := name[i]
		if (c < 'a' || c > 'z') && (c < '0' || c > '9') && c != '_' && c != '.' && c != '-' {
			return false
		}
	}
	return true
}
