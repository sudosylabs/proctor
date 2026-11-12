// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

// Package pluginhost runs the plugins compiled into the server.
//
// Activate turns on every enabled plugin once, in the order given, and keeps
// what each one registers. Close turns them off again in reverse order. A
// plugin whose Activate fails or panics is marked failed and left out; the
// server starts without it. Every call the server later makes into a plugin
// goes through the plugin's guard, which turns a panic into an error and,
// after PanicLimit panics, marks the plugin failed so that its capabilities
// answer plugin.ErrUnavailable until the server restarts.
//
// The package knows nothing about the application. The module-root server
// package hands what plugins registered to the components that use them.
package pluginhost

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"sync"
	"time"

	"github.com/sudosylabs/proctor/server/plugin"
)

// PanicLimit is how many contained panics mark an active plugin failed.
const PanicLimit = 3

// DefaultActivationTimeout bounds one plugin's Activate or Deactivate when
// Dependencies leaves the timeout unset.
const DefaultActivationTimeout = 30 * time.Second

// State is where a plugin is in its lifecycle.
type State string

// The states a plugin can be in.
const (
	// StateDisabled is a plugin the configuration turns off. It is never
	// activated.
	StateDisabled State = "disabled"
	// StateActive is a plugin that activated and is serving.
	StateActive State = "active"
	// StateFailed is a plugin whose Activate failed, or that panicked
	// PanicLimit times. Its capabilities are unavailable.
	StateFailed State = "failed"
)

// Settings is what the server wants for one plugin.
type Settings struct {
	// Enable overrides the manifest's EnabledByDefault when it is set. A
	// required plugin runs whatever it says.
	Enable *bool
	// Settings is handed to the plugin as Host.Settings.
	Settings json.RawMessage
	// Secrets is handed to the plugin as Host.Secrets.
	Secrets json.RawMessage
}

// Dependencies is what Activate needs.
type Dependencies struct {
	// Plugins are the compiled-in plugins, in activation order.
	Plugins []plugin.Plugin
	// Settings is what the server wants for each plugin, keyed by plugin ID.
	// A key that names no compiled-in plugin is a configuration error.
	Settings map[string]Settings
	// Logger is the server log. Each plugin's Host.Logger writes through it.
	Logger *slog.Logger
	// ActivationTimeout bounds each Activate, Reconfigure and Deactivate
	// call; zero means DefaultActivationTimeout.
	ActivationTimeout time.Duration
	// Recorder receives call outcomes and state changes for metrics. Nil
	// records nothing.
	Recorder Recorder
}

// Status is what the server reports about one plugin.
type Status struct {
	ID       string
	Name     string
	State    State
	Required bool
}

// Host holds the compiled-in plugins and what the active ones registered.
type Host struct {
	logger    *slog.Logger
	timeout   time.Duration
	entries   []*entry
	listeners []func()

	mu            sync.Mutex
	contributions contributions

	// lifecycle serializes Reconcile and Close; closed stops Reconcile.
	lifecycle sync.Mutex
	closed    bool
	closeErr  error
}

// entry is one plugin and everything the host knows about it.
type entry struct {
	manifest plugin.Manifest
	plugin   plugin.Plugin
	logger   *slog.Logger
	recorder Recorder

	mu     sync.Mutex
	state  State
	panics int
	// activated is set once Activate has returned without error; the plugin
	// is then deactivated before it is turned off, reactivated or closed,
	// even if it later failed.
	activated bool
	// applied is the Settings last applied or attempted; a failed plugin is
	// retried only when they change.
	applied *wanted
}

// wanted is a resolved Settings: whether the plugin should run, and with
// what.
type wanted struct {
	enabled  bool
	settings json.RawMessage
	secrets  json.RawMessage
}

func (w *wanted) sameConfiguration(other wanted) bool {
	return w != nil && bytes.Equal(w.settings, other.settings) && bytes.Equal(w.secrets, other.secrets)
}

// Activate validates every manifest and the settings, then brings each
// plugin to what the settings want, in order. It returns an error only for a
// mistake in what it was given: an invalid or repeated manifest, or settings
// for an unknown plugin. A plugin that fails to activate does not fail
// Activate.
func Activate(ctx context.Context, deps Dependencies) (*Host, error) {
	if deps.Logger == nil {
		return nil, errors.New("plugin host logger is required")
	}
	timeout := deps.ActivationTimeout
	if timeout <= 0 {
		timeout = DefaultActivationTimeout
	}

	recorder := deps.Recorder
	if recorder == nil {
		recorder = discardRecorder{}
	}
	host := &Host{logger: deps.Logger, timeout: timeout}
	seen := make(map[string]struct{}, len(deps.Plugins))
	for _, p := range deps.Plugins {
		if p == nil {
			return nil, errors.New("plugin is nil")
		}
		manifest := p.Manifest()
		if err := manifest.Validate(); err != nil {
			return nil, fmt.Errorf("plugin %q: %w", manifest.ID, err)
		}
		if _, repeated := seen[manifest.ID]; repeated {
			return nil, fmt.Errorf("plugin %q is compiled in twice", manifest.ID)
		}
		seen[manifest.ID] = struct{}{}
		host.entries = append(host.entries, &entry{
			manifest: manifest,
			plugin:   p,
			logger:   deps.Logger.With(slog.String("plugin", manifest.ID)),
			recorder: recorder,
			state:    StateDisabled,
		})
	}
	for id := range deps.Settings {
		if _, known := seen[id]; !known {
			return nil, fmt.Errorf("plugin settings name %q, which is not compiled in", id)
		}
	}

	host.lifecycle.Lock()
	defer host.lifecycle.Unlock()
	for _, e := range host.entries {
		host.apply(ctx, e, deps.Settings[e.manifest.ID])
	}
	return host, nil
}

// AddChangeListener calls listener, on the goroutine that ran Reconcile,
// after each Reconcile that turned a plugin on or off or changed its
// settings, so a consumer can pick up what plugins now offer. Listeners are
// added while the server is built, before it serves.
func (h *Host) AddChangeListener(listener func()) {
	h.lifecycle.Lock()
	defer h.lifecycle.Unlock()
	h.listeners = append(h.listeners, listener)
}

// Reconcile brings every plugin to what desired wants, in order, and calls
// the change listeners when anything changed. A plugin desired leaves out gets its
// defaults; an ID desired names that is not compiled in is ignored, because
// saved settings can outlive a plugin. It does nothing once the host is
// closed.
func (h *Host) Reconcile(ctx context.Context, desired map[string]Settings) {
	h.lifecycle.Lock()
	changed := false
	if !h.closed {
		for _, e := range h.entries {
			changed = h.apply(ctx, e, desired[e.manifest.ID]) || changed
		}
	}
	listeners := slices.Clone(h.listeners)
	h.lifecycle.Unlock()
	if changed {
		for _, listener := range listeners {
			listener()
		}
	}
}

// resolve turns settings into what the plugin should be.
func (e *entry) resolve(settings Settings) wanted {
	enabled := e.manifest.EnabledByDefault
	if settings.Enable != nil {
		enabled = *settings.Enable
	}
	if e.manifest.Required {
		if !enabled {
			e.logger.Warn("plugin is required and stays on")
		}
		enabled = true
	}
	return wanted{enabled: enabled, settings: slices.Clone(settings.Settings), secrets: slices.Clone(settings.Secrets)}
}

// apply brings one plugin to what settings want and reports whether
// anything changed. h.lifecycle is held.
func (h *Host) apply(ctx context.Context, e *entry, settings Settings) bool {
	want := e.resolve(settings)
	e.mu.Lock()
	state, activated, applied := e.state, e.activated, e.applied
	e.applied = &want
	e.mu.Unlock()

	if !want.enabled {
		if activated {
			_ = h.deactivate(ctx, e)
		}
		if state == StateDisabled && applied != nil {
			return false
		}
		e.mu.Lock()
		e.setState(StateDisabled)
		e.mu.Unlock()
		e.logger.Info("plugin is disabled")
		return applied != nil
	}

	same := applied.sameConfiguration(want) && applied.enabled
	switch {
	case same && (state == StateActive || state == StateFailed):
		// Nothing changed. A failed plugin waits for new settings or a
		// restart rather than failing again on every pass.
		return false
	case activated && state == StateActive && h.reconfigure(ctx, e, want):
		return true
	case activated:
		_ = h.deactivate(ctx, e)
	}
	h.activate(ctx, e, want)
	return true
}

// reconfigure hands an active plugin its new settings in place when it
// supports that, and reports whether it did.
func (h *Host) reconfigure(ctx context.Context, e *entry, want wanted) bool {
	reconfigurer, ok := e.plugin.(plugin.Reconfigurer)
	if !ok {
		return false
	}
	view := &registrar{host: h, entry: e, settings: want.settings, secrets: want.secrets}
	err := e.callWithin(ctx, h.timeout, "reconfigure", func(bounded context.Context) error {
		return reconfigurer.Reconfigure(bounded, view)
	})
	if err != nil {
		e.logger.Error("plugin failed to reconfigure; reactivating it", slog.Any("error", err))
		return false
	}
	e.logger.Info("plugin reconfigured")
	return true
}

// activate runs one plugin's Activate under the host's timeout and records
// the result. When Activate fails or panics, Deactivate runs at once so the
// plugin can release what it acquired before failing. When Activate does
// not return in time, it may still be running, so Deactivate is never
// called for it.
func (h *Host) activate(ctx context.Context, e *entry, want wanted) {
	e.mu.Lock()
	e.panics = 0
	e.mu.Unlock()
	registration := &registrar{host: h, entry: e, settings: want.settings, secrets: want.secrets, open: true}
	err := e.callWithin(ctx, h.timeout, "activate", func(bounded context.Context) error {
		return e.plugin.Activate(bounded, registration)
	})
	registration.close()
	if err == nil {
		e.mu.Lock()
		e.activated = true
		e.setState(StateActive)
		e.mu.Unlock()
		e.logger.Info("plugin activated")
		return
	}

	h.discardRegistrations(e)
	e.mu.Lock()
	e.setState(StateFailed)
	e.mu.Unlock()
	e.logger.Error("plugin failed to activate", slog.Any("error", err))
	if errors.Is(err, ErrTimedOut) {
		return
	}
	if cleanupErr := e.callWithin(ctx, h.timeout, "deactivate", e.plugin.Deactivate); cleanupErr != nil {
		e.logger.Error("plugin failed to clean up after a failed activation", slog.Any("error", cleanupErr))
	}
}

// discardRegistrations forgets what a plugin registered before its
// activation failed.
func (h *Host) discardRegistrations(e *entry) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.contributions.discard(e)
}

// Statuses reports every compiled-in plugin, in activation order.
func (h *Host) Statuses() []Status {
	if h == nil {
		return nil
	}
	statuses := make([]Status, 0, len(h.entries))
	for _, e := range h.entries {
		statuses = append(statuses, Status{
			ID:       e.manifest.ID,
			Name:     e.manifest.Name,
			State:    e.currentState(),
			Required: e.manifest.Required,
		})
	}
	return statuses
}

// Close deactivates every plugin that activated, in reverse order, each
// under the host's timeout and within ctx. It runs once; later calls return
// the first result, and Reconcile does nothing afterwards. A nil Host has
// nothing to close.
func (h *Host) Close(ctx context.Context) error {
	if h == nil {
		return nil
	}
	h.lifecycle.Lock()
	defer h.lifecycle.Unlock()
	if h.closed {
		return h.closeErr
	}
	h.closed = true
	var result error
	for _, e := range slices.Backward(h.entries) {
		result = errors.Join(result, h.deactivate(ctx, e))
	}
	h.closeErr = result
	return result
}

// deactivate runs Deactivate for a plugin that activated, including one that
// later failed by panicking: it still holds what Activate acquired.
func (h *Host) deactivate(ctx context.Context, e *entry) error {
	e.mu.Lock()
	activated := e.activated
	e.mu.Unlock()
	if !activated {
		return nil
	}

	// Turn the plugin's capabilities off first, so no new call reaches it
	// while it releases what it holds.
	e.mu.Lock()
	e.activated = false
	if e.state == StateActive {
		e.setState(StateDisabled)
	}
	e.mu.Unlock()
	h.discardRegistrations(e)
	if err := e.callWithin(ctx, h.timeout, "deactivate", e.plugin.Deactivate); err != nil {
		e.logger.Error("plugin failed to deactivate", slog.Any("error", err))
		return fmt.Errorf("deactivate plugin %q: %w", e.manifest.ID, err)
	}
	return nil
}

func (e *entry) currentState() State {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.state
}

// registrar is the plugin.Host one plugin's Activate receives. It accepts
// registrations only while Activate runs.
type registrar struct {
	host     *Host
	entry    *entry
	settings json.RawMessage
	secrets  json.RawMessage

	mu   sync.Mutex
	open bool
}

var _ plugin.Host = (*registrar)(nil)

func (r *registrar) Logger() *slog.Logger      { return r.entry.logger }
func (r *registrar) Settings() json.RawMessage { return slices.Clone(r.settings) }
func (r *registrar) Secrets() json.RawMessage  { return slices.Clone(r.secrets) }

func (r *registrar) close() {
	r.mu.Lock()
	r.open = false
	r.mu.Unlock()
}
