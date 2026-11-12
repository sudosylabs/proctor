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
		{name: "enable only", plugins: map[string]Plugin{"oidc": {Enable: new(bool)}}},
		{name: "null settings", plugins: map[string]Plugin{"oidc": {Settings: json.RawMessage(`null`)}}},
		{name: "object settings", plugins: map[string]Plugin{"code-run": {Settings: json.RawMessage(` {"endpoint":"x"}`)}}},
		{
			name:    "invalid id",
			plugins: map[string]Plugin{"Code_Run": {}},
			want:    []FieldError{{Field: `plugins["Code_Run"]`, Message: "must be 1-64 lowercase letters, digits, dots or hyphens, starting with a letter"}},
		},
		{
			name:    "settings that are not an object",
			plugins: map[string]Plugin{"oidc": {Settings: json.RawMessage(`["x"]`)}},
			want:    []FieldError{{Field: `plugins["oidc"].settings`, Message: "must be a JSON object"}},
		},
		{
			name:    "settings too large",
			plugins: map[string]Plugin{"oidc": {Settings: json.RawMessage(`{"x":"` + strings.Repeat("a", maxPluginSettingsBytes) + `"}`)}},
			want:    []FieldError{{Field: `plugins["oidc"].settings`, Message: "must be at most 65536 bytes"}},
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

func TestPluginsCloneDeeplyAndRedactSettings(t *testing.T) {
	t.Parallel()

	enable := true
	cfg := Default()
	cfg.Plugins = map[string]Plugin{
		"code-run": {Enable: &enable, Settings: json.RawMessage(`{"token":"plugin-secret-token"}`)},
		"oidc":     {Enable: &enable},
	}

	cloned := cfg.Clone()
	*cloned.Plugins["code-run"].Enable = false
	cloned.Plugins["code-run"].Settings[2] = 'X'
	require.True(t, *cfg.Plugins["code-run"].Enable, "the clone shares Enable")
	require.JSONEq(t, `{"token":"plugin-secret-token"}`, string(cfg.Plugins["code-run"].Settings), "the clone shares Settings")

	data, err := cfg.RedactedJSON()
	require.NoError(t, err)
	require.NotContains(t, string(data), "plugin-secret-token")
	require.Contains(t, string(data), `"Settings": "[redacted]"`)
	require.JSONEq(t, `{"token":"plugin-secret-token"}`, string(cfg.Plugins["code-run"].Settings), "redaction changes only the copy")
}
