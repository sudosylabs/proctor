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
	"slices"
	"sync"
	"time"

	"github.com/sudosylabs/proctor/server/app/events"
	"github.com/sudosylabs/proctor/server/config"
	"github.com/sudosylabs/proctor/server/httpapi"
	"github.com/sudosylabs/proctor/server/logging"
	"github.com/sudosylabs/proctor/server/metrics"
	"github.com/sudosylabs/proctor/server/model"
	"github.com/sudosylabs/proctor/server/plugin"
	"github.com/sudosylabs/proctor/server/plugin/cas"
	"github.com/sudosylabs/proctor/server/plugin/oidc"
	"github.com/sudosylabs/proctor/server/plugin/zipexport"
	"github.com/sudosylabs/proctor/server/pluginhost"
	"github.com/sudosylabs/proctor/server/store"
)

// builtinPlugins are the plugins compiled into the server, in activation
// order. This is the one place a plugin is named.
func builtinPlugins() []plugin.Plugin {
	return []plugin.Plugin{
		cas.NewPlugin(),
		oidc.NewPlugin(),
		zipexport.NewPlugin(),
	}
}

// activatePlugins activates the built-in plugins, then any a test adds,
// with the settings an administrator saved and the secrets in the Plugins
// configuration section. It returns the host and the settings change count
// it read, from which the reconciler continues. A plugin that fails to
// activate is logged and left out; a configuration mistake, such as secrets
// for a plugin that is not compiled in, or unreadable saved settings, fails
// construction.
func activatePlugins(
	ctx context.Context,
	cfg config.Config,
	saved store.PluginSettingsStore,
	logger *logging.Logger,
	recorder pluginhost.Recorder,
	extra []plugin.Plugin,
) (*pluginhost.Host, int64, error) {
	plugins := append(builtinPlugins(), extra...)
	known := make(map[string]struct{}, len(plugins))
	for _, p := range plugins {
		known[p.Manifest().ID] = struct{}{}
	}
	for id := range cfg.Plugins {
		if _, ok := known[id]; !ok {
			return nil, 0, fmt.Errorf("activate plugins: plugin secrets name %q, which is not compiled in", id)
		}
	}
	count, rows, err := readSavedPluginSettings(ctx, saved)
	if err != nil {
		return nil, 0, fmt.Errorf("activate plugins: %w", err)
	}
	host, err := pluginhost.Activate(ctx, pluginhost.Dependencies{
		Plugins:  plugins,
		Settings: desiredPlugins(known, rows, cfg, logger),
		Logger:   logger.Slog(),
		Recorder: recorder,
	})
	if err != nil {
		return nil, 0, fmt.Errorf("activate plugins: %w", err)
	}
	return host, count, nil
}

// readSavedPluginSettings reads the change count, then the saved settings; a
// change between the two is picked up by the next reconcile. A graph without
// persistence has none.
func readSavedPluginSettings(ctx context.Context, saved store.PluginSettingsStore) (int64, []*model.PluginSettings, error) {
	if saved == nil {
		return 0, nil, nil
	}
	count, err := saved.ChangeCount(ctx)
	if err != nil {
		return 0, nil, fmt.Errorf("read plugin settings change count: %w", err)
	}
	rows, err := saved.List(ctx)
	if err != nil {
		return 0, nil, fmt.Errorf("read plugin settings: %w", err)
	}
	return count, rows, nil
}

// desiredPlugins combines the saved settings with the configured secrets for
// every compiled-in plugin. Saved settings for a plugin that is no longer
// compiled in are ignored.
func desiredPlugins(
	known map[string]struct{},
	rows []*model.PluginSettings,
	cfg config.Config,
	logger *logging.Logger,
) map[string]pluginhost.Settings {
	desired := make(map[string]pluginhost.Settings, len(known))
	for id, configured := range cfg.Plugins {
		desired[id] = pluginhost.Settings{Secrets: configured.SecretsDocument()}
	}
	for _, row := range rows {
		id := row.PluginID.String()
		if _, ok := known[id]; !ok {
			logger.Warn("saved settings name a plugin that is not compiled in", logging.String("plugin", id))
			continue
		}
		enable := row.Enabled
		settings := desired[id]
		settings.Enable = &enable
		settings.Settings = slices.Clone(row.Settings)
		desired[id] = settings
	}
	return desired
}

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
	known := make(map[string]struct{})
	for _, status := range host.Statuses() {
		known[status.ID] = struct{}{}
	}
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

// pluginDirectory presents the plugin host to the HTTP API.
type pluginDirectory struct {
	host *pluginhost.Host
}

// Active lists the plugins active at the moment of the call, so a plugin
// that fails while serving drops out of the list.
func (d pluginDirectory) Active() []httpapi.PluginSummary {
	var summaries []httpapi.PluginSummary
	for _, status := range d.host.Statuses() {
		if status.State == pluginhost.StateActive {
			summaries = append(summaries, httpapi.PluginSummary{ID: status.ID, Name: status.Name})
		}
	}
	return summaries
}

// Degraded reports whether an enabled plugin has failed.
func (d pluginDirectory) Degraded() bool {
	for _, status := range d.host.Statuses() {
		if status.State == pluginhost.StateFailed {
			return true
		}
	}
	return false
}

// eventSubscriptions hands the plugins' guarded event handlers to the
// domain-event dispatcher.
func eventSubscriptions(host *pluginhost.Host) []events.Subscription {
	var subscriptions []events.Subscription
	for _, subscription := range host.EventSubscriptions() {
		subscriptions = append(subscriptions, events.Subscription{
			Key:     subscription.Plugin + ":" + string(subscription.Topic),
			Topic:   subscription.Topic,
			Handler: subscription.Handler,
		})
	}
	return subscriptions
}

// pluginRecorder reports plugin calls and states to metrics, or nothing when
// metrics are off.
func pluginRecorder(module *metrics.Module) pluginhost.Recorder {
	if !module.Enabled() {
		return nil
	}
	return pluginMetrics{module: module}
}

// pluginMetrics adapts the metrics module to the plugin host's Recorder;
// neither package may import the other.
type pluginMetrics struct {
	module *metrics.Module
}

func (m pluginMetrics) PluginCall(pluginID, operation, outcome string, elapsed time.Duration) {
	m.module.PluginCall(pluginID, operation, outcome, elapsed)
}

func (m pluginMetrics) PluginState(pluginID string, state pluginhost.State) {
	m.module.PluginState(pluginID, string(state))
}

// liveExportFormat looks the export format up on each use, so it follows
// the plugin that offers it being turned on, off or failing.
func liveExportFormat(host *pluginhost.Host, mediaType string) func() plugin.ExportFormat {
	return func() plugin.ExportFormat {
		format, ok := host.ExportFormat(mediaType)
		if !ok {
			return nil
		}
		return format
	}
}

// newOptionalPluginReconciler returns the reconciler for a graph with saved
// settings, or nil for a lifecycle-only graph without persistence.
func newOptionalPluginReconciler(
	saved store.PluginSettingsStore,
	host *pluginhost.Host,
	cfg config.Config,
	logger *logging.Logger,
	startCount int64,
) *pluginReconciler {
	if saved == nil {
		return nil
	}
	return newPluginReconciler(saved, host, cfg, logger, startCount)
}
