// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package config

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestAssistantConfigurationBoundsProvidersAndRedactsWithoutMutation(t *testing.T) {
	t.Parallel()
	provider := AssistantProvider{Name: "campus", Kind: "openai_compatible", BaseURL: "https://models.example.test/v1",
		APIKey: "synthetic-secret", Timeout: Duration{Duration: time.Minute}, MaxResponseBytes: 1 << 20,
		Capabilities: AssistantCapabilities{Streaming: true}}
	fields := func(value Assistant) []string {
		var result []string
		validateAssistant(value, func(field, _ string) { result = append(result, field) })
		return result
	}
	require.Empty(t, fields(Default().Assistant))
	require.NotEmpty(t, fields(Assistant{Enabled: true}))
	require.NotEmpty(t, fields(Assistant{Providers: []AssistantProvider{provider, provider}}))
	for name, change := range map[string]func(*AssistantProvider){
		"credentials in URL": func(p *AssistantProvider) { p.BaseURL = "https://key@models.example.test" },
		"query in URL":       func(p *AssistantProvider) { p.BaseURL += "?key=secret" },
		"relative URL":       func(p *AssistantProvider) { p.BaseURL = "/v1" },
		"unsupported kind":   func(p *AssistantProvider) { p.Kind = "unknown" },
		"header injection":   func(p *AssistantProvider) { p.APIKey += "\r\nX: bad" },
		"unbounded timeout":  func(p *AssistantProvider) { p.Timeout.Duration = 0 },
		"unbounded response": func(p *AssistantProvider) { p.MaxResponseBytes = 0 },
	} {
		t.Run(name, func(t *testing.T) {
			invalid := provider
			change(&invalid)
			require.NotEmpty(t, fields(Assistant{Providers: []AssistantProvider{invalid}}))
		})
	}
	config := Default()
	config.Assistant = Assistant{Enabled: true, Providers: []AssistantProvider{provider}}
	require.Empty(t, fields(config.Assistant))
	redacted := config.Redacted()
	require.Equal(t, "[redacted]", redacted.Assistant.Providers[0].APIKey)
	require.Equal(t, "synthetic-secret", config.Assistant.Providers[0].APIKey)
	clone := config.Clone()
	clone.Assistant.Providers[0].Name = "other"
	require.Equal(t, "campus", config.Assistant.Providers[0].Name)
}
