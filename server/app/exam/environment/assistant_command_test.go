// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package environment

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestAssistantStreamKeepsItsHeadAndItsLatestTail(t *testing.T) {
	var stream AssistantStream
	stream.add([]byte("abc"), 4)
	stream.add([]byte("defgh"), 4)
	stream.add([]byte("ijkl"), 4)
	require.Equal(t, "abcd", string(stream.Head))
	require.Equal(t, "ijkl", string(stream.Tail))
	require.True(t, stream.Dropped)
	require.Equal(t, uint64(12), stream.Bytes)

	var short AssistantStream
	short.add([]byte(strings.Repeat("x", 6)), 4)
	require.Equal(t, "xxxx", string(short.Head))
	require.Equal(t, "xx", string(short.Tail))
	require.False(t, short.Dropped, "nothing between head and tail was lost")
}
