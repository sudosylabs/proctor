// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package assistant

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestOutputCheckCountsNewFencedLinesAndExplanationConcepts(t *testing.T) {
	t.Parallel()
	known := map[[32]byte]struct{}{normalizedLineHash("  already_written()  "): {}}
	for _, test := range []struct {
		name, text string
		concepts   []string
		limit      int
		reason     string
		lines      int
	}{
		{"plain explanation", "Try a smaller step.", nil, 0, "", 0},
		{"at code budget", "```go\na()\nb()\n```", nil, 2, "", 2},
		{"over code budget", "```go\na()\nb()\nc()\n```", nil, 2, "code_budget", 3},
		{"known code excluded", "```go\n already_written()\nnew_step()\n```", nil, 1, "", 1},
		{"blank lines excluded", "~~~python\n\nnew_step()\n~~~", nil, 1, "", 1},
		{"unclosed fence", "```go\na()\nb()", nil, 1, "code_budget", 2},
		{"avoided explanation", "Consider RECURSION here.", []string{"recursion"}, 10, "avoided_concept", 0},
		{"concept across whitespace", "Consider function\n pointers.", []string{"function pointers"}, 10, "avoided_concept", 0},
		{"code alone is not explanation", "```text\nrecursion\n```", []string{"recursion"}, 10, "", 1},
	} {
		t.Run(test.name, func(t *testing.T) {
			result := checkOutput(test.text, test.limit, test.concepts, known)
			require.Equal(t, test.reason, result.Reason)
			require.Equal(t, test.lines, result.CodeLines)
		})
	}
}
