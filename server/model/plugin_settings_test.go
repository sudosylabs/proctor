// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package model

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestNormalizePluginSettingsDocument(t *testing.T) {
	t.Parallel()

	normalized, err := NormalizePluginSettingsDocument(json.RawMessage(` { "b": 1.50, "a": {"y": true, "x": null} } `))
	require.NoError(t, err)
	require.Equal(t, `{"a":{"x":null,"y":true},"b":1.50}`, string(normalized), "keys sorted, numbers as written")

	for name, document := range map[string]string{
		"empty":          ``,
		"array":          `[]`,
		"null":           `null`,
		"string":         `"x"`,
		"trailing value": `{} {}`,
		"malformed":      `{"a":}`,
		"too large":      `{"a":"` + strings.Repeat("x", PluginSettingsMaxBytes) + `"}`,
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			_, normalizeErr := NormalizePluginSettingsDocument(json.RawMessage(document))
			require.ErrorIs(t, normalizeErr, errPluginSettingsInvalid)
		})
	}
}

func TestPluginIDIsValid(t *testing.T) {
	t.Parallel()

	require.True(t, PluginID("export.zip").IsValid())
	require.True(t, PluginID("display-topology").IsValid())
	require.False(t, PluginID("").IsValid())
	require.False(t, PluginID("Display").IsValid())
	require.False(t, PluginID("1st").IsValid())
	require.False(t, PluginID("a_b").IsValid())
	require.False(t, PluginID("a"+strings.Repeat("b", 64)).IsValid())
}

func TestPluginSettingsReplaceKeepsTheRevisionOnANoOp(t *testing.T) {
	t.Parallel()

	created := time.Date(2026, 11, 12, 9, 0, 0, 0, time.UTC)
	settings, err := NewPluginSettings("display-topology", PluginSettingsChange{Enabled: true, Settings: json.RawMessage(`{"mode":"observe"}`)}, created)
	require.NoError(t, err)
	require.Equal(t, int64(1), settings.Revision)

	later := created.Add(time.Minute)
	require.NoError(t, settings.Replace(1, PluginSettingsChange{Enabled: true, Settings: json.RawMessage(`{"mode":"observe"}`)}, later))
	require.Equal(t, int64(1), settings.Revision, "an exact no-op keeps the revision")
	require.True(t, settings.UpdatedAt.Equal(created))

	require.NoError(t, settings.Replace(1, PluginSettingsChange{Enabled: false, Settings: json.RawMessage(`{"mode":"observe"}`)}, later))
	require.Equal(t, int64(2), settings.Revision)
	require.False(t, settings.Enabled)
	require.True(t, settings.UpdatedAt.Equal(later))

	require.ErrorIs(t, settings.Replace(1, PluginSettingsChange{Settings: json.RawMessage(`{}`)}, later), ErrPluginSettingsRevisionConflict)
	require.ErrorIs(t, settings.Replace(2, PluginSettingsChange{Settings: json.RawMessage(`{ }`)}, later), errPluginSettingsInvalid,
		"a change must carry a normalized document")
	require.Equal(t, map[string]any{"plugin_id": "display-topology", "revision": int64(2), "enabled": false, "settings_bytes": 18},
		settings.Auditable(), "the audit carries no setting values")
}

func TestNewPluginSettingsRejectsInvalidInput(t *testing.T) {
	t.Parallel()

	at := time.Date(2026, 11, 12, 9, 0, 0, 0, time.UTC)
	_, err := NewPluginSettings("Not Valid", PluginSettingsChange{Settings: json.RawMessage(`{}`)}, at)
	require.ErrorIs(t, err, errPluginSettingsInvalid)
	_, err = NewPluginSettings("valid", PluginSettingsChange{Settings: json.RawMessage(`[]`)}, at)
	require.ErrorIs(t, err, errPluginSettingsInvalid)
	_, err = NewPluginSettings("valid", PluginSettingsChange{Settings: json.RawMessage(`{}`)}, time.Time{})
	require.ErrorIs(t, err, errPluginSettingsInvalid)
}
