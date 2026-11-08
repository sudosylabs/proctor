// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package httpapi

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
)

// requireNoPrivateFieldNames fails when any object key at any depth of the
// encoded response contains one of the forbidden words. It checks names, not
// values: response values carry random identifiers that can spell short words.
func requireNoPrivateFieldNames(t *testing.T, encoded []byte, forbidden ...string) {
	t.Helper()
	var document any
	require.NoError(t, json.Unmarshal(encoded, &document))
	for _, name := range jsonObjectKeys(document) {
		for _, word := range forbidden {
			require.NotContains(t, name, word, "response field %q names private material %q", name, word)
		}
	}
}

func jsonObjectKeys(value any) []string {
	var keys []string
	switch typed := value.(type) {
	case map[string]any:
		for key, nested := range typed {
			keys = append(keys, key)
			keys = append(keys, jsonObjectKeys(nested)...)
		}
	case []any:
		for _, nested := range typed {
			keys = append(keys, jsonObjectKeys(nested)...)
		}
	}
	return keys
}

func TestRequireNoPrivateFieldNamesReadsNestedKeysOnly(t *testing.T) {
	t.Parallel()
	keys := jsonObjectKeys(map[string]any{"id": "hash", "rows": []any{map[string]any{"target_email": "x"}}})
	require.ElementsMatch(t, []string{"id", "rows", "target_email"}, keys)
}
