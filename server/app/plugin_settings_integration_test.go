// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

//go:build integration

package app_test

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/sudosylabs/proctor/server/app/identity"
	"github.com/sudosylabs/proctor/server/app/invocation"
	"github.com/sudosylabs/proctor/server/config"
	"github.com/sudosylabs/proctor/server/model"
	"github.com/sudosylabs/proctor/server/testlib"
)

func TestPluginSettingsHTTPSavesAuditedSettingsAndRefusesWhatItMust(t *testing.T) {
	dataSource := os.Getenv("PROCTOR_TEST_DATABASE_URL")
	require.NotEmpty(t, dataSource, "PROCTOR_TEST_DATABASE_URL is not set")
	persistence := openAuthenticationStore(t, dataSource)
	mfaConfig := func(cfg *config.Config) {
		cfg.Authentication.MFA.Enabled = true
		cfg.Authentication.MFA.EncryptionKey = base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{23}, 32))
	}
	node := testlib.Setup(t, testlib.WithConfig(mfaConfig), testlib.WithStore(persistence))
	bootstrap := performJSONRequest(node.Handler(), http.MethodPost, "/api/v1/bootstrap", map[string]any{
		"bootstrap_secret": testlib.BootstrapSecret,
		"institution":      map[string]any{"name": "plugin-institution", "display_name": "Plugin Institution"},
		"administrator":    map[string]any{"username": "plugin-admin", "email": "plugin-admin@example.edu"},
		"password":         "correct horse battery staple",
	}, "")
	require.Equal(t, http.StatusCreated, bootstrap.Code, "bootstrap: %d %s", bootstrap.Code, bootstrap.Body.String())
	login, err := node.App.Identity.Login(context.Background(), invocation.Invocation{}, identity.LoginCommand{
		LoginID: "plugin-admin", Password: "correct horse battery staple", ClientType: model.SessionClientCLI,
		DeviceID: "plugin-cli", Source: "127.0.0.1:1",
	})
	require.NoError(t, err)
	request := func(method, path, key, token, body string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, path, bytes.NewBufferString(body))
		req.Header.Set("Authorization", "Bearer "+token)
		if method == http.MethodPut {
			req.Header.Set("Content-Type", "application/json")
			req.Header.Set("Idempotency-Key", key)
		}
		response := httptest.NewRecorder()
		node.Handler().ServeHTTP(response, req)
		return response
	}
	type pluginItem struct {
		ID       string `json:"id"`
		Enabled  bool   `json:"enabled"`
		Required bool   `json:"required"`
		Settings string `json:"settings"`
		Revision int64  `json:"revision"`
	}
	list := func() map[string]pluginItem {
		response := request(http.MethodGet, "/api/v1/plugins", "", login.Tokens.AccessToken, "")
		require.Equal(t, http.StatusOK, response.Code, "list: %d %s", response.Code, response.Body.String())
		var page struct {
			Items []pluginItem `json:"items"`
		}
		require.NoError(t, json.Unmarshal(response.Body.Bytes(), &page))
		items := make(map[string]pluginItem, len(page.Items))
		for _, item := range page.Items {
			items[item.ID] = item
		}
		return items
	}

	before := list()
	require.Equal(t, pluginItem{ID: "cas", Enabled: true, Settings: "{}"}, before["cas"], "nobody configured CAS yet")
	require.True(t, before["export.zip"].Required)

	strengthenRoleAdministratorSession(t, node.Handler(), login.Tokens.AccessToken)
	saved := request(http.MethodPut, "/api/v1/plugins/cas/settings", "turn-cas-off", login.Tokens.AccessToken,
		`{"expected_revision":0,"enabled":false,"settings":"{}"}`)
	require.Equal(t, http.StatusOK, saved.Code, "save: %d %s", saved.Code, saved.Body.String())
	replay := request(http.MethodPut, "/api/v1/plugins/cas/settings", "turn-cas-off", login.Tokens.AccessToken,
		`{"expected_revision":0,"enabled":false,"settings":"{}"}`)
	require.Equal(t, saved.Body.String(), replay.Body.String(), "an exact retry replays the saved result")
	require.Equal(t, pluginItem{ID: "cas", Enabled: false, Settings: "{}", Revision: 1}, list()["cas"])

	stale := request(http.MethodPut, "/api/v1/plugins/cas/settings", "stale-editor", login.Tokens.AccessToken,
		`{"expected_revision":0,"enabled":true,"settings":"{}"}`)
	require.Equal(t, http.StatusConflict, stale.Code, "stale: %d %s", stale.Code, stale.Body.String())
	require.Contains(t, stale.Body.String(), `"code":"plugin.revision_conflict"`)

	required := request(http.MethodPut, "/api/v1/plugins/export.zip/settings", "turn-zip-off", login.Tokens.AccessToken,
		`{"expected_revision":0,"enabled":false,"settings":"{}"}`)
	require.Equal(t, http.StatusConflict, required.Code, "required: %d %s", required.Code, required.Body.String())
	require.Contains(t, required.Body.String(), `"code":"plugin.required"`)

	invalid := request(http.MethodPut, "/api/v1/plugins/oidc/settings", "bad-settings", login.Tokens.AccessToken,
		`{"expected_revision":0,"enabled":true,"settings":"[1,2]"}`)
	require.Equal(t, http.StatusBadRequest, invalid.Code, "invalid: %d %s", invalid.Code, invalid.Body.String())
	require.Contains(t, invalid.Body.String(), `"code":"plugin.invalid"`)

	unknown := request(http.MethodPut, "/api/v1/plugins/missing/settings", "unknown-plugin", login.Tokens.AccessToken,
		`{"expected_revision":0,"enabled":true,"settings":"{}"}`)
	require.Equal(t, http.StatusNotFound, unknown.Code, "unknown: %d %s", unknown.Code, unknown.Body.String())

	ordinary := createIntegrationUser(t, node, "plugin-ordinary", "another correct horse battery staple")
	ordinaryLogin, err := node.App.Identity.Login(context.Background(), invocation.Invocation{}, identity.LoginCommand{
		LoginID: ordinary.Username, Password: "another correct horse battery staple", ClientType: model.SessionClientCLI,
		DeviceID: "ordinary-cli", Source: "127.0.0.1:2",
	})
	require.NoError(t, err)
	denied := request(http.MethodGet, "/api/v1/plugins", "", ordinaryLogin.Tokens.AccessToken, "")
	require.Equal(t, http.StatusForbidden, denied.Code, "ordinary read: %d %s", denied.Code, denied.Body.String())
}
