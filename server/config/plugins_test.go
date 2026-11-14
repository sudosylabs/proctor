// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package config

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestValidatePlugins(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		plugins map[string]Plugin
		want    []FieldError
	}{
		{name: "none"},
		{name: "no secrets", plugins: map[string]Plugin{"oidc": {}}},
		{name: "null secrets", plugins: map[string]Plugin{"oidc": {Secrets: json.RawMessage(`null`)}}},
		{name: "object secrets", plugins: map[string]Plugin{"code-run": {Secrets: json.RawMessage(` {"token":"x"}`)}}},
		{
			name:    "invalid id",
			plugins: map[string]Plugin{"Code_Run": {}},
			want:    []FieldError{{Field: `plugins["Code_Run"]`, Message: "must be 1-64 lowercase letters, digits, dots or hyphens, starting with a letter"}},
		},
		{
			name:    "secrets that are not an object",
			plugins: map[string]Plugin{"oidc": {Secrets: json.RawMessage(`["x"]`)}},
			want:    []FieldError{{Field: `plugins["oidc"].secrets`, Message: "must be a JSON object"}},
		},
		{
			name:    "secrets too large",
			plugins: map[string]Plugin{"oidc": {Secrets: json.RawMessage(`{"x":"` + strings.Repeat("a", maxPluginSecretsBytes) + `"}`)}},
			want:    []FieldError{{Field: `plugins["oidc"].secrets`, Message: "must be at most 65536 bytes"}},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			var got []FieldError
			validatePlugins(test.plugins, func(field, message string) {
				got = append(got, FieldError{Field: field, Message: message})
			})
			require.Equal(t, test.want, got)
		})
	}
}

func TestPluginsCloneDeeplyAndRedactSecrets(t *testing.T) {
	t.Parallel()

	cfg := Default()
	cfg.Plugins = map[string]Plugin{
		"code-run": {Secrets: json.RawMessage(`{"token":"plugin-secret-token"}`)},
		"oidc":     {},
	}

	cloned := cfg.Clone()
	cloned.Plugins["code-run"].Secrets[2] = 'X'
	require.JSONEq(t, `{"token":"plugin-secret-token"}`, string(cfg.Plugins["code-run"].Secrets), "the clone shares Secrets")

	data, err := cfg.RedactedJSON()
	require.NoError(t, err)
	require.NotContains(t, string(data), "plugin-secret-token")
	require.Contains(t, string(data), `"Secrets": "[redacted]"`)
	require.JSONEq(t, `{"token":"plugin-secret-token"}`, string(cfg.Plugins["code-run"].Secrets), "redaction changes only the copy")
}
