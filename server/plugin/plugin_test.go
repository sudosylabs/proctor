// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package plugin_test

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/sudosylabs/proctor/server/plugin"
)

func TestManifestValidate(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		manifest plugin.Manifest
		valid    bool
	}{
		{name: "letters", manifest: plugin.Manifest{ID: "oidc", Name: "OpenID Connect"}, valid: true},
		{name: "dots, digits and hyphens", manifest: plugin.Manifest{ID: "export.zip-2", Name: "Zip"}, valid: true},
		{name: "longest id", manifest: plugin.Manifest{ID: "a" + strings.Repeat("b", 63), Name: "Long"}, valid: true},
		{name: "empty id", manifest: plugin.Manifest{Name: "Nameless"}},
		{name: "id too long", manifest: plugin.Manifest{ID: "a" + strings.Repeat("b", 64), Name: "Long"}},
		{name: "upper case", manifest: plugin.Manifest{ID: "OIDC", Name: "OpenID Connect"}},
		{name: "leading digit", manifest: plugin.Manifest{ID: "1password", Name: "Digit"}},
		{name: "underscore", manifest: plugin.Manifest{ID: "code_run", Name: "Code"}},
		{name: "missing name", manifest: plugin.Manifest{ID: "oidc"}},
		{name: "name too long", manifest: plugin.Manifest{ID: "oidc", Name: strings.Repeat("n", 129)}},
		{name: "settings schema", manifest: plugin.Manifest{ID: "display", Name: "Display", SettingsSchema: []byte(`{"type":"object"}`)}, valid: true},
		{name: "schema not an object", manifest: plugin.Manifest{ID: "display", Name: "Display", SettingsSchema: []byte(`["type"]`)}},
		{name: "schema not JSON", manifest: plugin.Manifest{ID: "display", Name: "Display", SettingsSchema: []byte(`{type}`)}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			err := test.manifest.Validate()
			if test.valid {
				require.NoError(t, err)
				return
			}
			require.ErrorIs(t, err, plugin.ErrInvalidManifest)
		})
	}
}
