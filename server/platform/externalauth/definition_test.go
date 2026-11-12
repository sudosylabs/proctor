// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package externalauth

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/sudosylabs/proctor/server/config"
	"github.com/sudosylabs/proctor/server/plugin"
)

func TestDecodeDefinitionReadsTheConfiguredProvider(t *testing.T) {
	t.Parallel()

	configured := config.ExternalAuthenticationProvider{
		ID: "campus-oidc", Type: config.ExternalAuthenticationTypeOIDC, DisplayName: "Campus", Enabled: true,
		AutoProvision: true,
		Claims:        config.ExternalClaimMapping{Subject: "sub", AllowedHomeOrganizations: []string{"example.test"}},
		OIDC: &config.OIDCProvider{
			Issuer: "https://oidc.example.test", ClientID: "proctor", Scopes: []string{"openid"},
			Timeout: config.Duration{Duration: 5 * time.Second}, MaxResponseBytes: 64 << 10,
		},
	}
	settings, err := json.Marshal(configured)
	require.NoError(t, err)

	decoded, err := DecodeDefinition(plugin.IdentityProviderDefinition{ID: "campus-oidc", Settings: settings})
	require.NoError(t, err)
	require.Equal(t, configured, decoded, "the provider settings survive the round trip")
}

func TestDecodeDefinitionRejectsWhatItCannotTrust(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		definition plugin.IdentityProviderDefinition
		want       string
	}{
		{
			name:       "unknown member",
			definition: plugin.IdentityProviderDefinition{ID: "campus", Settings: json.RawMessage(`{"ID":"campus","Secret":"x"}`)},
			want:       `decode provider settings: json: unknown field "Secret"`,
		},
		{
			name:       "trailing data",
			definition: plugin.IdentityProviderDefinition{ID: "campus", Settings: json.RawMessage(`{"ID":"campus"} {}`)},
			want:       "decode provider settings: trailing data",
		},
		{
			name:       "another provider",
			definition: plugin.IdentityProviderDefinition{ID: "campus", Settings: json.RawMessage(`{"ID":"other"}`)},
			want:       "provider settings name a different provider",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			_, err := DecodeDefinition(test.definition)
			require.EqualError(t, err, test.want)
		})
	}
}
