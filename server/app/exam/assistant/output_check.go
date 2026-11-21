// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package assistant

import (
	"crypto/sha256"
	"strings"
)

type outputCheckResult struct {
	CodeLines int
	Reason    string
}

func normalizedLineHash(line string) [32]byte { return sha256.Sum256([]byte(strings.TrimSpace(line))) }

// checkOutput deliberately uses no model: a failed reply gets at most one rewrite.
func checkOutput(text string, maximumLines int, concepts []string, knownLines map[[32]byte]struct{}) outputCheckResult {
	result := outputCheckResult{}
	var explanation strings.Builder
	var fence byte
	var fenceLength int
	for _, line := range strings.Split(text, "\n") {
		trimmed := strings.TrimSpace(line)
		marker, length := fenceMarker(trimmed)
		if fence == 0 && length >= 3 {
			fence = marker
			fenceLength = length
			continue
		}
		if fence != 0 {
			if marker == fence && length >= fenceLength && strings.TrimSpace(trimmed[length:]) == "" {
				fence = 0
				continue
			}
			if trimmed != "" {
				if _, known := knownLines[normalizedLineHash(line)]; !known {
					result.CodeLines++
				}
			}
			continue
		}
		explanation.WriteString(line)
		explanation.WriteByte(' ')
	}
	if result.CodeLines > maximumLines {
		result.Reason = "code_budget"
		return result
	}
	normalized := strings.ToLower(strings.Join(strings.Fields(explanation.String()), " "))
	for _, concept := range concepts {
		if concept != "" && strings.Contains(normalized, strings.ToLower(strings.Join(strings.Fields(concept), " "))) {
			result.Reason = "avoided_concept"
			break
		}
	}
	return result
}
func fenceMarker(line string) (byte, int) {
	if line == "" || (line[0] != '`' && line[0] != '~') {
		return 0, 0
	}
	marker := line[0]
	length := 0
	for length < len(line) && line[length] == marker {
		length++
	}
	return marker, length
}
