// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package pluginhost

import (
	"fmt"
	"log/slog"
	"runtime/debug"

	"github.com/sudosylabs/proctor/server/plugin"
)

// call runs fn, one call from the server into the plugin, and turns a panic
// into an error. A panic is logged with its stack, which holds code
// locations only, and counted; the PanicLimit-th marks an active plugin
// failed.
func (e *entry) call(operation string, fn func() error) (err error) {
	defer func() {
		recovered := recover()
		if recovered == nil {
			return
		}
		e.logger.Error("plugin panicked",
			slog.String("operation", operation),
			slog.String("stack", string(debug.Stack())),
		)
		e.recordPanic()
		err = fmt.Errorf("plugin %q panicked during %s: %w", e.manifest.ID, operation, plugin.ErrUnavailable)
	}()
	return fn()
}

func (e *entry) recordPanic() {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.panics++
	if e.state == StateActive && e.panics >= PanicLimit {
		e.state = StateFailed
		e.logger.Error("plugin failed after repeated panics", slog.Int("panics", e.panics))
	}
}

// serve runs fn, a call into one of the plugin's capabilities. When the
// plugin has failed it returns plugin.ErrUnavailable without calling it.
func (e *entry) serve(operation string, fn func() error) error {
	if e.currentState() != StateActive {
		return fmt.Errorf("plugin %q: %w", e.manifest.ID, plugin.ErrUnavailable)
	}
	return e.call(operation, fn)
}
