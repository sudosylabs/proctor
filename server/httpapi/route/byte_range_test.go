// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package route

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestByteRangesAreOneSatisfiableRange(t *testing.T) {
	t.Parallel()

	whole, partial, err := ParseByteRange("", 100)
	require.NoError(t, err)
	require.False(t, partial)
	require.Equal(t, ByteRange{Start: 0, Length: 100}, whole)

	for header, want := range map[string]ByteRange{
		"bytes=0-9":    {Start: 0, Length: 10},
		"bytes=90-":    {Start: 90, Length: 10},
		"bytes=-5":     {Start: 95, Length: 5},
		"bytes=-500":   {Start: 0, Length: 100},
		"bytes=50-999": {Start: 50, Length: 50},
		"bytes=99-99":  {Start: 99, Length: 1},
	} {
		got, isPartial, rangeErr := ParseByteRange(header, 100)
		require.NoError(t, rangeErr, header)
		require.True(t, isPartial, header)
		require.Equal(t, want, got, header)
	}

	for _, header := range []string{
		"bytes=100-", "bytes=10-5", "bytes=-0", "bytes=0-1,5-6", "items=0-1", "bytes=a-b", "bytes=01-2", "bytes=-", "bytes=+1-2",
	} {
		_, _, rangeErr := ParseByteRange(header, 100)
		require.ErrorIs(t, rangeErr, ErrRangeNotSatisfiable, header)
	}
	_, _, err = ParseByteRange("bytes=0-0", 0)
	require.ErrorIs(t, err, ErrRangeNotSatisfiable, "an empty representation has no byte to range over")
}
