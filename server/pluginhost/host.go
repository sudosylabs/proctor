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

// Settings is the configuration of one plugin.
type Settings struct {
	// Enable overrides the manifest's EnabledByDefault when it is set.
	Enable *bool
	// Settings is handed to the plugin as Host.Settings.
	Settings json.RawMessage
}

// Dependencies is what Activate needs.
type Dependencies struct {
	// Plugins are the compiled-in plugins, in activation order.
	Plugins []plugin.Plugin
	// Settings is the Plugins configuration section, keyed by plugin ID. A
	// key that names no compiled-in plugin is a configuration error.
	Settings map[string]Settings
	// Logger is the server log. Each plugin's Host.Logger writes through it.
	Logger *slog.Logger
	// ActivationTimeout bounds each Activate and Deactivate call; zero means
	// DefaultActivationTimeout.
	ActivationTimeout time.Duration
}

// Status is what the server reports about one plugin.
type Status struct {
	ID    string
	Name  string
	State State
}

// Host holds the compiled-in plugins and what the active ones registered.
type Host struct {
	logger  *slog.Logger
	timeout time.Duration
	entries []*entry

	mu            sync.Mutex
	contributions contributions

	closeOnce sync.Once
	closeErr  error
}

// entry is one plugin and everything the host knows about it.
type entry struct {
	manifest plugin.Manifest
	plugin   plugin.Plugin
	logger   *slog.Logger

	mu     sync.Mutex
	state  State
	panics int
}

// Activate validates every manifest and setting, then activates each enabled
// plugin in order. It returns an error only for a mistake in what it was
// given: an invalid or repeated manifest, or settings for an unknown plugin.
// A plugin that fails to activate does not fail Activate.
func Activate(ctx context.Context, deps Dependencies) (*Host, error) {
	if deps.Logger == nil {
		return nil, errors.New("plugin host logger is required")
	}
	timeout := deps.ActivationTimeout
	if timeout <= 0 {
		timeout = DefaultActivationTimeout
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
			state:    StateDisabled,
		})
	}
	for id := range deps.Settings {
		if _, known := seen[id]; !known {
			return nil, fmt.Errorf("plugin settings name %q, which is not compiled in", id)
		}
	}

	for _, e := range host.entries {
		settings := deps.Settings[e.manifest.ID]
		enabled := e.manifest.EnabledByDefault
		if settings.Enable != nil {
			enabled = *settings.Enable
		}
		if !enabled {
			e.logger.Info("plugin is disabled")
			continue
		}
		host.activate(ctx, e, slices.Clone(settings.Settings))
	}
	return host, nil
}

// activate runs one plugin's Activate under the host's timeout and records
// the result.
func (h *Host) activate(ctx context.Context, e *entry, settings json.RawMessage) {
	activationCtx, cancel := context.WithTimeout(ctx, h.timeout)
	defer cancel()

	registration := &registrar{host: h, entry: e, settings: settings, open: true}
	err := e.call("activate", func() error { return e.plugin.Activate(activationCtx, registration) })
	registration.close()
	if err != nil {
		h.discardRegistrations(e)
	}

	e.mu.Lock()
	defer e.mu.Unlock()
	if err != nil {
		e.state = StateFailed
		e.logger.Error("plugin failed to activate", slog.Any("error", err))
		return
	}
	e.state = StateActive
	e.logger.Info("plugin activated")
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
	statuses := make([]Status, 0, len(h.entries))
	for _, e := range h.entries {
		statuses = append(statuses, Status{ID: e.manifest.ID, Name: e.manifest.Name, State: e.currentState()})
	}
	return statuses
}

// Close deactivates every plugin that activated, in reverse order, each
// under the host's timeout and within ctx. It runs once; later calls return
// the first result. A nil Host has nothing to close.
func (h *Host) Close(ctx context.Context) error {
	if h == nil {
		return nil
	}
	h.closeOnce.Do(func() {
		var result error
		for _, e := range slices.Backward(h.entries) {
			result = errors.Join(result, h.deactivate(ctx, e))
		}
		h.closeErr = result
	})
	return h.closeErr
}

// deactivate runs Deactivate for a plugin that activated, including one that
// later failed by panicking: it still holds what Activate acquired.
func (h *Host) deactivate(ctx context.Context, e *entry) error {
	e.mu.Lock()
	activated := e.state == StateActive || e.panics >= PanicLimit
	e.mu.Unlock()
	if !activated {
		return nil
	}

	deactivationCtx, cancel := context.WithTimeout(ctx, h.timeout)
	defer cancel()
	if err := e.call("deactivate", func() error { return e.plugin.Deactivate(deactivationCtx) }); err != nil {
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

	mu   sync.Mutex
	open bool
}

var _ plugin.Host = (*registrar)(nil)

func (r *registrar) Logger() *slog.Logger      { return r.entry.logger }
func (r *registrar) Settings() json.RawMessage { return slices.Clone(r.settings) }

func (r *registrar) close() {
	r.mu.Lock()
	r.open = false
	r.mu.Unlock()
}
