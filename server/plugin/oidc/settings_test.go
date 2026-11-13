// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package oidc

import (
	"bytes"
	"encoding/json"
	"os"
	"reflect"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/sudosylabs/proctor/server/config"
	"github.com/sudosylabs/proctor/server/platform/externalauth"
	"github.com/sudosylabs/proctor/server/plugin"
)

func TestSettingsAndSecretsAreThePluginsToCheck(t *testing.T) {
	t.Parallel()

	valid := testSettings("https://sso.example.edu/cas/oidc")
	_, err := valid.build()
	require.NoError(t, err)

	for name, test := range map[string]struct {
		change func(*testProvider)
		want   []string
	}{
		"subject other than sub and no openid scope": {func(p *testProvider) {
			p.Settings.Claims.Subject = "uid"
			p.Settings.Scopes = []string{"profile"}
		}, []string{"Settings.Claims.Subject: must be sub for an oidc provider", "Settings.Scopes: must include openid"}},
		"issuer with a trailing slash":  {func(p *testProvider) { p.Settings.Issuer = "https://sso.example.edu/" }, []string{"Settings.Issuer: must not contain a query, fragment, or trailing slash"}},
		"public HTTP issuer":            {func(p *testProvider) { p.Settings.Issuer = "http://sso.example.edu" }, []string{"Settings.Issuer: must be an HTTPS URL"}},
		"no client":                     {func(p *testProvider) { p.Settings.ClientID = "" }, []string{"Settings.ClientID"}},
		"no client secret":              {func(p *testProvider) { p.Secrets.ClientSecret = "" }, []string{"Secrets.ClientSecret"}},
		"repeated scope":                {func(p *testProvider) { p.Settings.Scopes = []string{"openid", "openid"} }, []string{"Settings.Scopes[1]: must be unique"}},
		"no username to provision from": {func(p *testProvider) { p.Settings.Claims.Username = "" }, []string{"Settings.Claims.Username: is required when AutoProvision is enabled"}},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			provider := valid
			provider.Settings.Scopes = append([]string(nil), valid.Settings.Scopes...)
			test.change(&provider)
			_, err := provider.build()
			require.Error(t, err)
			for _, want := range test.want {
				require.Contains(t, err.Error(), want)
			}
			require.NotContains(t, err.Error(), valid.Secrets.ClientSecret, "a settings error exposed the client secret")
		})
	}

	definition := valid.definition()
	definition.Secrets = json.RawMessage(`{"ClientSecret":"proctor-secret","Extra":"proctor-secret"}`)
	_, err = New(definition)
	require.Error(t, err)
	require.NotContains(t, err.Error(), "proctor-secret", "a secrets decoding error exposed the secret")

	definition = valid.definition()
	definition.Settings = json.RawMessage(`{"Issuer":"https://sso.example.edu","ClientSecret":"x"}`)
	_, err = New(definition)
	require.ErrorContains(t, err, `unknown field "ClientSecret"`, "the client secret belongs in Secrets")
}

func TestConfigurationExampleBuildsAProvider(t *testing.T) {
	t.Parallel()

	data, err := os.ReadFile("../../config/examples/oidc-provider.json")
	require.NoError(t, err)
	var provider config.ExternalAuthenticationProvider
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	require.NoError(t, decoder.Decode(&provider))
	require.Equal(t, Type, provider.Type)

	for document, example := range map[string]struct {
		raw    json.RawMessage
		fields int
	}{
		"settings": {provider.Settings, reflect.TypeOf(Settings{}).NumField()},
		"secrets":  {provider.Secrets, reflect.TypeOf(Secrets{}).NumField()},
	} {
		var fields map[string]json.RawMessage
		require.NoError(t, json.Unmarshal(example.raw, &fields))
		require.Len(t, fields, example.fields, "the example names every %s field", document)
		if document == "settings" {
			var claims map[string]json.RawMessage
			require.NoError(t, json.Unmarshal(fields["Claims"], &claims))
			require.Len(t, claims, reflect.TypeOf(externalauth.ClaimMapping{}).NumField(), "the example names every claim")
		}
	}

	_, err = New(plugin.IdentityProviderDefinition{
		ID: provider.ID, AutoProvision: provider.AutoProvision,
		Settings: provider.SettingsDocument(), Secrets: provider.SecretsDocument(),
	})
	require.NoError(t, err)
}
