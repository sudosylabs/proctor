// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package metrics

import (
	"time"

	"github.com/prometheus/client_golang/prometheus"
)

// pluginStates are the states a plugin can be in. proctor_plugin_state is 1
// for a plugin's current state and 0 for the others.
var pluginStates = []string{"disabled", "active", "failed"}

// newPluginCollectors creates the plugin collectors. Plugin IDs, operations
// and outcomes come from the compiled-in plugins and the plugin host, never
// from requests, so the series stay bounded.
func (m *Module) newPluginCollectors() {
	m.pluginCalls = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Namespace: "proctor",
			Subsystem: "plugin",
			Name:      "calls_total",
			Help:      "Calls from the server into a plugin, by operation and outcome (ok, error, panic, unavailable, timeout).",
		},
		[]string{"plugin", "operation", "outcome"},
	)
	m.pluginCallDuration = prometheus.NewHistogramVec(
		prometheus.HistogramOpts{
			Namespace: "proctor",
			Subsystem: "plugin",
			Name:      "call_duration_seconds",
			Help:      "Duration of calls from the server into a plugin.",
			Buckets:   prometheus.DefBuckets,
		},
		[]string{"plugin", "operation"},
	)
	m.pluginState = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Namespace: "proctor",
			Subsystem: "plugin",
			Name:      "state",
			Help:      "1 for each compiled-in plugin's current state (disabled, active or failed), 0 for its other states.",
		},
		[]string{"plugin", "state"},
	)
}

// PluginCall records one call into a plugin.
func (m *Module) PluginCall(pluginID, operation, outcome string, elapsed time.Duration) {
	m.pluginCalls.WithLabelValues(pluginID, operation, outcome).Inc()
	m.pluginCallDuration.WithLabelValues(pluginID, operation).Observe(elapsed.Seconds())
}

// PluginState records a plugin's current state.
func (m *Module) PluginState(pluginID, state string) {
	for _, known := range pluginStates {
		value := 0.0
		if known == state {
			value = 1
		}
		m.pluginState.WithLabelValues(pluginID, known).Set(value)
	}
}
