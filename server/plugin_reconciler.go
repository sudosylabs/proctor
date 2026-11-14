// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package server

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/sudosylabs/proctor/server/config"
	"github.com/sudosylabs/proctor/server/logging"
	"github.com/sudosylabs/proctor/server/pluginhost"
	"github.com/sudosylabs/proctor/server/store"
)

// pluginReconcileInterval is how often each node checks for changed plugin
// settings, so every node converges within about this long.
const pluginReconcileInterval = 5 * time.Second

// pluginReconciler keeps this node's plugins in step with the settings an
// administrator saves. It checks the change count every interval, or at
// once when Trigger is called after a local save, and reconciles when the
// count moved. A failed read is logged and retried on the next check.
type pluginReconciler struct {
	saved    store.PluginSettingsStore
	host     *pluginhost.Host
	cfg      config.Config
	logger   *logging.Logger
	interval time.Duration
	known    map[string]struct{}

	trigger chan struct{}
	stop    chan struct{}
	done    chan struct{}
	last    int64

	lifecycle sync.Mutex
	started   bool
	stopped   bool
}

func newPluginReconciler(
	saved store.PluginSettingsStore,
	host *pluginhost.Host,
	cfg config.Config,
	logger *logging.Logger,
	startCount int64,
) *pluginReconciler {
	known := pluginIDSet(host.Statuses(), func(status pluginhost.Status) string { return status.ID })
	return &pluginReconciler{
		saved: saved, host: host, cfg: cfg, logger: logger, interval: pluginReconcileInterval, known: known,
		trigger: make(chan struct{}, 1), stop: make(chan struct{}), done: make(chan struct{}), last: startCount,
	}
}

// Start runs the reconcile loop until Close. The loop outlives ctx, which
// only bounds startup, but keeps its values. A closed reconciler does not
// start.
func (r *pluginReconciler) Start(ctx context.Context) error {
	r.lifecycle.Lock()
	defer r.lifecycle.Unlock()
	if r.started || r.stopped {
		return nil
	}
	r.started = true
	go r.run(context.WithoutCancel(ctx))
	return nil
}

// Trigger asks for a check now, without waiting for it.
func (r *pluginReconciler) Trigger() {
	select {
	case r.trigger <- struct{}{}:
	default:
	}
}

// Close stops the loop and waits for a check in progress, within ctx.
func (r *pluginReconciler) Close(ctx context.Context) error {
	r.lifecycle.Lock()
	started := r.started
	if !r.stopped {
		r.stopped = true
		close(r.stop)
	}
	r.lifecycle.Unlock()
	if !started {
		return nil
	}
	select {
	case <-r.done:
		return nil
	case <-ctx.Done():
		return fmt.Errorf("stop plugin reconciler: %w", ctx.Err())
	}
}

func (r *pluginReconciler) run(base context.Context) {
	defer close(r.done)
	ctx, cancel := context.WithCancel(base)
	defer cancel()
	go func() {
		select {
		case <-r.stop:
			cancel()
		case <-ctx.Done():
		}
	}()
	ticker := time.NewTicker(r.interval)
	defer ticker.Stop()
	for {
		select {
		case <-r.stop:
			return
		case <-ticker.C:
		case <-r.trigger:
		}
		r.check(ctx)
	}
}

// check reconciles when the saved settings changed since the last check.
func (r *pluginReconciler) check(ctx context.Context) {
	count, err := r.saved.ChangeCount(ctx)
	if err != nil {
		r.logger.Warn("could not check plugin settings", logging.Err(err))
		return
	}
	if count == r.last {
		return
	}
	rows, err := r.saved.List(ctx)
	if err != nil {
		r.logger.Warn("could not read plugin settings", logging.Err(err))
		return
	}
	r.host.Reconcile(ctx, desiredPlugins(r.known, rows, r.cfg, r.logger))
	r.last = count
}
