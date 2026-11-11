// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package route

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestPathParsesEveryPartKind(t *testing.T) {
	t.Parallel()

	got := path("/programme-levels/{programme_level_id}/sources/{source_id:uuid}/receipts/{batch_sequence:sequence}/providers/{provider_id:provider}/login")

	want := routePath{parts: []pathPart{
		literal("programme-levels"), canonicalID("programme_level_id"),
		literal("sources"), canonicalUUID("source_id"),
		literal("receipts"), positiveSequence("batch_sequence"),
		literal("providers"), providerID("provider_id"),
		literal("login"),
	}}
	assert.Equal(t, want, got)
}

func TestPathCompilesUnderTheAPIPrefix(t *testing.T) {
	t.Parallel()

	template, normalized, err := path("/classes/{class_id}/members").compile("/api/v1")

	require.NoError(t, err)
	assert.Equal(t, "/api/v1/classes/{class_id:"+canonicalIDRoutePattern()+"}/members", template)
	assert.Equal(t, "/api/v1/classes/{canonical_id}/members", normalized)
}

func TestRootPathIsNotPrefixed(t *testing.T) {
	t.Parallel()

	template, normalized, err := rootPath("/health/live").compile("/api/v1")

	require.NoError(t, err)
	assert.Equal(t, "/health/live", template)
	assert.Equal(t, "/health/live", normalized)
}

func TestPathRejectsMalformedTemplatesAtCompile(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		template string
		want     string
	}{
		{"empty template", "", "must start with /"},
		{"root only", "/", "empty segment"},
		{"missing leading slash", "classes", "must start with /"},
		{"empty segment", "/classes//members", "empty segment"},
		{"trailing slash", "/classes/", "empty segment"},
		{"unclosed brace", "/classes/{class_id", "malformed parameter"},
		{"unopened brace", "/classes/class_id}", "malformed parameter"},
		{"text around parameter", "/classes/x{class_id}", "malformed parameter"},
		{"nested kind separator", "/classes/{class_id:uuid:extra}", "malformed parameter"},
		{"unknown kind", "/classes/{class_id:integer}", "unknown parameter kind"},
		{"empty kind", "/classes/{class_id:}", "unknown parameter kind"},
		{"upper case literal", "/Classes", "not canonical"},
		{"upper case parameter", "/classes/{ClassID}", "not canonical"},
		{"empty parameter name", "/classes/{}", "not canonical"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			_, _, err := path(test.template).compile("/api/v1")

			require.Error(t, err)
			assert.Contains(t, err.Error(), test.want)
		})
	}
}

func TestPathRejectsUnsupportedParameterKindAtCompile(t *testing.T) {
	t.Parallel()

	unsupported := routePath{parts: []pathPart{pathParameter{name: "id", kind: parameterKind(99)}}}

	_, _, err := unsupported.compile("/api/v1")

	require.Error(t, err)
	assert.Contains(t, err.Error(), "unsupported parameter kind")
}
