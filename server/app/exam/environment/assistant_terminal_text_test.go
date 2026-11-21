// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package environment

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestAssistantTerminalTextReplaysIncompleteSequences(t *testing.T) {
	t.Parallel()
	for _, tt := range []struct {
		name, partial, tail, clean string
	}{
		{"UTF-8", "\xe2\x82", "\xac next", "€ next"},
		{"CSI", "\x1b[31", "mnext", "next"},
		{"OSC", "\x1b]0;title\x1b", "\\next", "next"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			var first assistantTerminalText
			const from = uint64(100)
			left := []byte("prior " + tt.partial)
			require.Equal(t, "prior ", string(first.append(left, from)))
			checkpoint := first.checkpoint(from + uint64(len(left)))
			require.EqualValues(t, 106, checkpoint)

			var next assistantTerminalText
			raw := []byte(tt.partial + tt.tail)
			require.Equal(t, tt.clean, string(next.append(raw, checkpoint)))
			require.Equal(t, checkpoint+uint64(len(raw)), next.checkpoint(checkpoint+uint64(len(raw))))

			require.Equal(t, tt.clean, string(first.append([]byte(tt.tail), from+uint64(len(left)))))
		})
	}
}

func TestAssistantTerminalTextSeparatesTheRetainedTail(t *testing.T) {
	t.Parallel()
	var terminal assistantTerminalText
	var retained AssistantStream
	const limit = 4
	raw := []byte("head\x1b]0;the invisible title\a" + "cut-last")
	retained.add(terminal.append(raw, 0), limit)
	require.True(t, retained.Dropped)
	require.Equal(t, "head"+assistantOutputSeparator+"last", string(sessionStreamBytes(retained)))
}
