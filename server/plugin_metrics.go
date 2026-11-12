// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package server

import (
	"time"

	"github.com/sudosylabs/proctor/server/metrics"
	"github.com/sudosylabs/proctor/server/pluginhost"
)

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
