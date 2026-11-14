// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package metrics

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/sudosylabs/proctor/server/config"
)

func TestPluginMetricsReportCallsAndOneCurrentState(t *testing.T) {
	settings := config.Default().Metrics
	settings.Enabled = true
	module, err := New(settings, BuildInfo{}, Sources{})
	require.NoError(t, err)
	module.SetReady(true)

	module.PluginState("oidc", "active")
	module.PluginCall("oidc", "begin authentication", "ok", 20*time.Millisecond)
	module.PluginCall("oidc", "begin authentication", "panic", time.Millisecond)
	module.PluginState("oidc", "failed")

	response := httptest.NewRecorder()
	module.handler.ServeHTTP(response, httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/metrics", nil))
	require.Equal(t, http.StatusOK, response.Code)
	body := response.Body.String()
	require.Contains(t, body, `proctor_plugin_calls_total{operation="begin authentication",outcome="ok",plugin="oidc"} 1`)
	require.Contains(t, body, `proctor_plugin_calls_total{operation="begin authentication",outcome="panic",plugin="oidc"} 1`)
	require.Contains(t, body, `proctor_plugin_call_duration_seconds_count{operation="begin authentication",plugin="oidc"} 2`)
	require.Contains(t, body, `proctor_plugin_state{plugin="oidc",state="failed"} 1`)
	require.Contains(t, body, `proctor_plugin_state{plugin="oidc",state="active"} 0`, "only the current state is 1")
	require.Contains(t, body, `proctor_plugin_state{plugin="oidc",state="disabled"} 0`)
}
