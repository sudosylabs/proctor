// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package httpapi

import (
	"encoding/json"
	"sort"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/sudosylabs/proctor/server/model"
)

// The contract publishes each closed server catalogue as a component enum.
// Clients validate responses against it, so an enum that lags the Go
// catalogue turns real responses into contract violations. Each test here
// keeps one enum equal to the catalogue it names.

func openAPIComponentEnum(t *testing.T, name string) []string {
	t.Helper()
	encoded, err := readOpenAPIDocumentFile()
	require.NoError(t, err)
	var document struct {
		Components struct {
			Schemas map[string]struct {
				Enum []string `json:"enum"`
			} `json:"schemas"`
		} `json:"components"`
	}
	require.NoError(t, json.Unmarshal(encoded, &document))
	schema, ok := document.Components.Schemas[name]
	require.True(t, ok, "component %s is missing", name)
	require.NotEmpty(t, schema.Enum, "component %s has no enum", name)
	return schema.Enum
}

func stringValues[T ~string](values []T) []string {
	out := make([]string, 0, len(values))
	for _, value := range values {
		out = append(out, string(value))
	}
	return out
}

func TestMailTemplateKeyEnumMatchesTheCatalogue(t *testing.T) {
	t.Parallel()
	require.Equal(t, stringValues(model.AllMailTemplateKeys()), openAPIComponentEnum(t, "MailTemplateKey"))
}

func TestErrorCodeEnumMatchesTheCatalogue(t *testing.T) {
	t.Parallel()
	codes := make([]string, 0, len(applicationErrorMappings)+len(transportProblemCodes))
	seen := map[string]bool{}
	for code := range applicationErrorMappings {
		seen[code] = true
		codes = append(codes, code)
	}
	for code := range transportProblemCodes {
		if !seen[code] {
			codes = append(codes, code)
		}
	}
	sort.Strings(codes)
	require.Equal(t, codes, openAPIComponentEnum(t, "ErrorCode"))
}

func TestAuditActionEnumMatchesTheCatalogue(t *testing.T) {
	t.Parallel()
	require.Equal(t, model.AuditActions(), openAPIComponentEnum(t, "AuditAction"))
}
