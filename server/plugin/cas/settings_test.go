// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package cas

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

func TestSettingsAreThePluginsToCheck(t *testing.T) {
	t.Parallel()

	valid := testSettings("http://127.0.0.1:8080/cas")
	_, err := valid.build()
	require.NoError(t, err)

	for name, test := range map[string]struct {
		change func(*testProvider)
		want   []string
	}{
		"public HTTP and no email to provision from": {func(p *testProvider) {
			p.Settings.BaseURL = "http://cas.example.edu"
			p.Settings.Claims.Email = ""
		}, []string{"Settings.BaseURL: must be an HTTPS URL", "Settings.Claims.Email: is required when AutoProvision is enabled"}},
		"query in base URL":        {func(p *testProvider) { p.Settings.BaseURL = "https://cas.example.edu/cas?x=1" }, []string{"Settings.BaseURL: must not contain a query or fragment"}},
		"relative validation path": {func(p *testProvider) { p.Settings.ValidationPath = "serviceValidate" }, []string{"Settings.ValidationPath"}},
		"slow and unbounded": {func(p *testProvider) {
			p.Settings.Timeout = config.Duration{}
			p.Settings.MaxResponseBytes = 0
		}, []string{"Settings.Timeout: must be between 1s and 30s", "Settings.MaxResponseBytes"}},
		"unknown subject attribute":                   {func(p *testProvider) { p.Settings.Claims.Subject = "1user" }, []string{"Settings.Claims.Subject"}},
		"organizations without their attribute":       {func(p *testProvider) { p.Settings.Claims.HomeOrganization = "" }, []string{"Settings.Claims.HomeOrganization"}},
		"multi-factor values without their attribute": {func(p *testProvider) { p.Settings.Claims.MultiFactorAttribute = "" }, []string{"Settings.Claims.MultiFactorValues"}},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			provider := valid
			provider.Settings.Claims.AllowedHomeOrganizations = append([]string(nil), valid.Settings.Claims.AllowedHomeOrganizations...)
			test.change(&provider)
			_, buildErr := provider.build()
			require.Error(t, buildErr)
			for _, want := range test.want {
				require.Contains(t, buildErr.Error(), want)
			}
		})
	}

	definition := valid.definition()
	definition.Settings = json.RawMessage(`{"BaseURL":"https://cas.example.edu","Unknown":true}`)
	_, err = New(definition)
	require.ErrorContains(t, err, `unknown field "Unknown"`)

	definition = valid.definition()
	definition.Secrets = json.RawMessage(`{"Token":"x"}`)
	_, err = New(definition)
	require.ErrorContains(t, err, "CAS takes no secrets")
}

func TestConfigurationExampleBuildsAProvider(t *testing.T) {
	t.Parallel()

	data, err := os.ReadFile("../../config/examples/cas-provider.json")
	require.NoError(t, err)
	var provider config.ExternalAuthenticationProvider
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	require.NoError(t, decoder.Decode(&provider))
	require.Equal(t, Type, provider.Type)

	var fields map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(provider.Settings, &fields))
	require.Len(t, fields, reflect.TypeOf(Settings{}).NumField(), "the example names every CAS setting")
	var claims map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(fields["Claims"], &claims))
	require.Len(t, claims, reflect.TypeOf(externalauth.ClaimMapping{}).NumField(), "the example names every claim")

	_, err = New(plugin.IdentityProviderDefinition{
		ID: provider.ID, AutoProvision: provider.AutoProvision,
		Settings: provider.SettingsDocument(), Secrets: provider.SecretsDocument(),
	})
	require.NoError(t, err)
}
