// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package httpapi

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/sudosylabs/proctor/server/httpapi/route"
	"github.com/sudosylabs/proctor/server/httpapi/route/routetest"
	"github.com/sudosylabs/proctor/server/model"
)

func systemPluginsAPI(t *testing.T, plugins Plugins) http.Handler {
	t.Helper()
	logger, _ := routetest.NewLogger(t)
	principal := model.Principal{
		UserID: model.NewUserID(), SessionID: model.NewSessionID(), CredentialID: model.PrincipalCredentialID(model.NewID()),
		CredentialType: model.CredentialSessionAccess, AuthenticationMethod: "password",
		AuthenticationStrength: model.AuthenticationSingleFactor, ClientType: model.SessionClientCLI, AuthenticatedAt: time.Now(),
	}
	return newFocusedResourceAPI(t, logger, routetest.Authenticator{Principal: principal},
		systemResourceWithApplication(systemHTTPHealth{live: true, ready: true}, BuildInfo{}, plugins,
			unavailableDesktopCompatibilityApplication{}, time.Now))
}

func getSystemPlugins(t *testing.T, handler http.Handler, query string, authorized bool) *httptest.ResponseRecorder {
	t.Helper()
	request := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/api/v1/system/plugins"+query, nil)
	if authorized {
		request.Header.Set("Authorization", "Bearer credential")
	}
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	return response
}

func TestSystemPluginsPagesTheActivePluginsByName(t *testing.T) {
	t.Parallel()

	handler := systemPluginsAPI(t, fakePlugins{active: []PluginSummary{
		{ID: "oidc", Name: "OpenID Connect"}, {ID: "cas", Name: "CAS"}, {ID: "export.zip", Name: "Zip export"},
	}})

	response := getSystemPlugins(t, handler, "?limit=2", true)
	require.Equal(t, http.StatusOK, response.Code, response.Body.String())
	require.Equal(t, "no-store", response.Header().Get("Cache-Control"))
	var first route.List[PluginSummary]
	require.NoError(t, json.Unmarshal(response.Body.Bytes(), &first))
	require.Equal(t, []PluginSummary{{ID: "cas", Name: "CAS"}, {ID: "oidc", Name: "OpenID Connect"}}, first.Items)
	require.NotEmpty(t, first.NextCursor)

	response = getSystemPlugins(t, handler, "?limit=2&cursor="+first.NextCursor, true)
	require.Equal(t, http.StatusOK, response.Code, response.Body.String())
	var second route.List[PluginSummary]
	require.NoError(t, json.Unmarshal(response.Body.Bytes(), &second))
	require.Equal(t, []PluginSummary{{ID: "export.zip", Name: "Zip export"}}, second.Items)
	require.Empty(t, second.NextCursor, "the last page has no continuation")
}

func TestSystemPluginsWithoutPluginsIsAnEmptyPage(t *testing.T) {
	t.Parallel()

	response := getSystemPlugins(t, systemPluginsAPI(t, nil), "", true)
	require.Equal(t, http.StatusOK, response.Code, response.Body.String())
	require.JSONEq(t, `{"items":[]}`, response.Body.String())
}

func TestSystemPluginsRequiresASession(t *testing.T) {
	t.Parallel()

	response := getSystemPlugins(t, systemPluginsAPI(t, nil), "", false)
	require.Equal(t, http.StatusUnauthorized, response.Code, response.Body.String())
}

func TestSystemPluginsRejectsAnInvalidPage(t *testing.T) {
	t.Parallel()

	response := getSystemPlugins(t, systemPluginsAPI(t, nil), "?limit=0", true)
	require.Equal(t, http.StatusBadRequest, response.Code, response.Body.String())
}

// fakePlugins is a fixed plugin report.
type fakePlugins struct {
	active   []PluginSummary
	degraded bool
}

func (p fakePlugins) Active() []PluginSummary { return p.active }
func (p fakePlugins) Degraded() bool          { return p.degraded }

func TestReadinessIsDegradedButServingWhenAPluginHasFailed(t *testing.T) {
	t.Parallel()

	for name, test := range map[string]struct {
		plugins Plugins
		want    string
	}{
		"no plugins":      {plugins: nil, want: `{"status":"ok"}`},
		"healthy plugins": {plugins: fakePlugins{}, want: `{"status":"ok"}`},
		"failed plugin":   {plugins: fakePlugins{degraded: true}, want: `{"status":"degraded"}`},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			request := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/health/ready", nil)
			response := httptest.NewRecorder()
			systemPluginsAPI(t, test.plugins).ServeHTTP(response, request)
			require.Equal(t, http.StatusOK, response.Code, "a failed plugin never takes the node out of service")
			require.JSONEq(t, test.want, response.Body.String())
		})
	}
}
