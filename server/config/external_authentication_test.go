// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package config

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestExternalAuthenticationProviderCountIsBounded(t *testing.T) {
	cfg := Default()
	for index := range 64 {
		cfg.Authentication.External.Providers = append(
			cfg.Authentication.External.Providers,
			validTestProvider(fmt.Sprintf("provider-%02d", index)),
		)
	}
	require.NoError(t, cfg.Validate())

	tooMany := cfg.Clone()
	tooMany.Authentication.External.Providers = append(
		tooMany.Authentication.External.Providers,
		validTestProvider("provider-64"),
	)
	require.Len(t, cfg.Authentication.External.Providers, 64, "Clone() exposed the provider list")
	err := tooMany.Validate()
	require.Error(t, err)
	require.Contains(t, err.Error(), "authentication.external.providers: must contain at most 64 providers")
}

func validTestProvider(id string) ExternalAuthenticationProvider {
	return ExternalAuthenticationProvider{
		ID: id, Type: "cas", DisplayName: id, Enabled: true,
		Settings: json.RawMessage(`{"BaseURL":"http://127.0.0.1:8080/cas"}`),
	}
}

func TestExternalAuthenticationConfigurationOwnsOnlyTheEnvelope(t *testing.T) {
	cfg := Default()
	provider := validTestProvider("campus-oidc")
	provider.Type = "oidc"
	provider.Secrets = json.RawMessage(`{"ClientSecret":"sensitive-secret"}`)
	cfg.Authentication.External.Providers = []ExternalAuthenticationProvider{provider}
	require.NoError(t, cfg.Validate(), "the protocol's documents are the plugin's to check")

	cloned := cfg.Clone()
	cloned.Authentication.External.Providers[0].Settings[2] = 'X'
	cloned.Authentication.External.Providers[0].Secrets[2] = 'X'
	require.JSONEq(t, `{"BaseURL":"http://127.0.0.1:8080/cas"}`, string(cfg.Authentication.External.Providers[0].Settings), "Clone() exposed provider settings")
	require.JSONEq(t, `{"ClientSecret":"sensitive-secret"}`, string(cfg.Authentication.External.Providers[0].Secrets), "Clone() exposed provider secrets")

	redacted := cfg.Redacted()
	require.JSONEq(t, `"[redacted]"`, string(redacted.Authentication.External.Providers[0].Secrets), "Redacted() exposed provider secrets")
	require.JSONEq(t, `{"BaseURL":"http://127.0.0.1:8080/cas"}`, string(redacted.Authentication.External.Providers[0].Settings))
	require.JSONEq(t, `{"ClientSecret":"sensitive-secret"}`, string(cfg.Authentication.External.Providers[0].Secrets), "Redacted() changed the source")

	for name, test := range map[string]struct {
		change func(*ExternalAuthenticationProvider)
		field  string
	}{
		"type":             {func(p *ExternalAuthenticationProvider) { p.Type = "Not A Plugin" }, "providers[0].type"},
		"settings array":   {func(p *ExternalAuthenticationProvider) { p.Settings = json.RawMessage(`[]`) }, "providers[0].settings: must be a JSON object"},
		"settings invalid": {func(p *ExternalAuthenticationProvider) { p.Settings = json.RawMessage(`{`) }, "providers[0].settings: must be a JSON object"},
		"secrets string":   {func(p *ExternalAuthenticationProvider) { p.Secrets = json.RawMessage(`"secret"`) }, "providers[0].secrets: must be a JSON object"},
		"settings too large": {func(p *ExternalAuthenticationProvider) {
			p.Settings = json.RawMessage(`{"Padding":"` + strings.Repeat("a", 64<<10) + `"}`)
		}, "providers[0].settings: must be at most 65536 bytes"},
		"display name": {func(p *ExternalAuthenticationProvider) { p.DisplayName = "" }, "providers[0].display_name"},
		"id":           {func(p *ExternalAuthenticationProvider) { p.ID = "Campus" }, "providers[0].id"},
	} {
		t.Run(name, func(t *testing.T) {
			broken := cfg.Clone()
			test.change(&broken.Authentication.External.Providers[0])
			err := broken.Validate()
			require.Error(t, err)
			require.Contains(t, err.Error(), "authentication.external."+test.field)
		})
	}
}
