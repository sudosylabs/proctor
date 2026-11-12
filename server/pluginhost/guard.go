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
	"runtime/debug"
	"time"

	"github.com/sudosylabs/proctor/server/plugin"
)

// ErrTimedOut is returned when a plugin's Activate or Deactivate does not
// return within the host's timeout.
var ErrTimedOut = errors.New("plugin did not return in time")

// The outcomes Recorder.PluginCall reports.
const (
	OutcomeOK          = "ok"
	OutcomeError       = "error"
	OutcomePanic       = "panic"
	OutcomeUnavailable = "unavailable"
	OutcomeTimeout     = "timeout"
)

// Recorder receives what the host observes about plugins, for metrics. Its
// methods must be safe for concurrent use and return promptly.
type Recorder interface {
	// PluginCall reports one call into a plugin: its operation, one of the
	// Outcome constants, and how long it took.
	PluginCall(pluginID, operation, outcome string, elapsed time.Duration)
	// PluginState reports a plugin's new state.
	PluginState(pluginID string, state State)
}

// call runs fn, one call from the server into the plugin, and turns a panic
// into an error. A panic is logged with its stack, which holds code
// locations only, and counted; the PanicLimit-th marks an active plugin
// failed. Every call is reported to the Recorder.
func (e *entry) call(operation string, fn func() error) (err error) {
	started := time.Now()
	defer func() {
		outcome := OutcomeOK
		if recovered := recover(); recovered != nil {
			e.logger.Error("plugin panicked",
				slog.String("operation", operation),
				slog.String("stack", string(debug.Stack())),
			)
			e.recordPanic()
			err = fmt.Errorf("plugin %q panicked during %s: %w", e.manifest.ID, operation, plugin.ErrUnavailable)
			outcome = OutcomePanic
		} else if err != nil {
			outcome = OutcomeError
		}
		e.recorder.PluginCall(e.manifest.ID, operation, outcome, time.Since(started))
	}()
	return fn()
}

// callWithin runs fn like call, but stops waiting once timeout passes or ctx
// ends, and then returns ErrTimedOut. Go cannot stop the plugin's
// goroutine; it is left to finish on its own, and the plugin is treated as
// failed.
func (e *entry) callWithin(ctx context.Context, timeout time.Duration, operation string, fn func(context.Context) error) error {
	bounded, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	started := time.Now()
	result := make(chan error, 1)
	go func() { result <- e.call(operation, func() error { return fn(bounded) }) }()
	select {
	case err := <-result:
		return err
	case <-bounded.Done():
		e.recorder.PluginCall(e.manifest.ID, operation, OutcomeTimeout, time.Since(started))
		return fmt.Errorf("plugin %q %s: %w", e.manifest.ID, operation, ErrTimedOut)
	}
}

func (e *entry) recordPanic() {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.panics++
	if e.state == StateActive && e.panics >= PanicLimit {
		e.setState(StateFailed)
		e.logger.Error("plugin failed after repeated panics", slog.Int("panics", e.panics))
	}
}

// setState records a new state; e.mu is held.
func (e *entry) setState(state State) {
	e.state = state
	e.recorder.PluginState(e.manifest.ID, state)
}

// serve runs fn, a call into one of the plugin's capabilities. When the
// plugin has failed it returns plugin.ErrUnavailable without calling it.
func (e *entry) serve(operation string, fn func() error) error {
	if e.currentState() != StateActive {
		e.recorder.PluginCall(e.manifest.ID, operation, OutcomeUnavailable, 0)
		return fmt.Errorf("plugin %q: %w", e.manifest.ID, plugin.ErrUnavailable)
	}
	return e.call(operation, fn)
}

// discardRecorder is the Recorder when Dependencies leaves it unset.
type discardRecorder struct{}

func (discardRecorder) PluginCall(string, string, string, time.Duration) {}
func (discardRecorder) PluginState(string, State)                        {}
