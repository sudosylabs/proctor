// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package model

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestOnboardingImportFileNameIsATrimmedBoundedBaseName(t *testing.T) {
	t.Parallel()
	for raw, want := range map[string]string{
		"roster.csv":             "roster.csv",
		"  Year 1 roster.csv \t": "Year 1 roster.csv",
		"élèves.csv":             "élèves.csv",
		".hidden":                ".hidden",
		strings.Repeat("é", 255): strings.Repeat("é", 255),
		"..roster":               "..roster",
	} {
		t.Run(raw, func(t *testing.T) {
			t.Parallel()
			name, valid := NormalizeOnboardingImportFileName(raw)
			require.True(t, valid)
			require.Equal(t, want, name)
		})
	}
	for name, raw := range map[string]string{
		"empty":          "",
		"blank":          "   ",
		"dot":            ".",
		"dot dot":        " .. ",
		"slash":          "imports/roster.csv",
		"backslash":      `C:\roster.csv`,
		"control":        "roster\x00.csv",
		"newline inside": "roster\n.csv",
		"delete":         "roster\x7f.csv",
		"too long":       strings.Repeat("a", 256),
		"invalid utf-8":  "roster\xff.csv",
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			_, valid := NormalizeOnboardingImportFileName(raw)
			require.False(t, valid)
		})
	}
	require.False(t, ValidOnboardingImportFileName(" roster.csv"), "an untrimmed name is not stored as is")
}
