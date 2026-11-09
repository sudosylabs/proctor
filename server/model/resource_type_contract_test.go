// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package model

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestResourceTypeEnumMatchesTheCatalogue keeps the contract's ResourceType
// enum equal to the resource types Resource.Validate accepts.
func TestResourceTypeEnumMatchesTheCatalogue(t *testing.T) {
	t.Parallel()
	_, currentFile, _, ok := runtime.Caller(0)
	require.True(t, ok)
	encoded, err := os.ReadFile(filepath.Join(filepath.Dir(currentFile), "..", "openapi.json"))
	require.NoError(t, err)
	var document struct {
		Components struct {
			Schemas map[string]struct {
				Enum []string `json:"enum"`
			} `json:"schemas"`
		} `json:"components"`
	}
	require.NoError(t, json.Unmarshal(encoded, &document))
	want := make([]string, 0, len(resourceTypes))
	for _, resourceType := range resourceTypes {
		want = append(want, string(resourceType))
	}
	require.Equal(t, want, document.Components.Schemas["ResourceType"].Enum)
}
