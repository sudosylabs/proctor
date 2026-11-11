// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package paging

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/sudosylabs/proctor/server/store"
)

func TestCountFirstPageCountsOnlyTheFirstPageAndChecksTheStore(t *testing.T) {
	t.Parallel()
	calls := 0
	count := func(total int) func(int) (int, error) {
		return func(maximum int) (int, error) {
			calls++
			require.Equal(t, store.MaximumListCount, maximum)
			return total, nil
		}
	}
	total, consistent, err := CountFirst(true, 2, count(7))
	require.NoError(t, err)
	require.True(t, consistent)
	require.Equal(t, Total{Counted: true, Total: 7}, total)

	total, consistent, err = CountFirst(true, 1, count(store.MaximumListCount))
	require.NoError(t, err)
	require.True(t, consistent)
	require.Equal(t, Total{Counted: true, Total: store.MaximumListCount, TotalIsLowerBound: true}, total)

	calls = 0
	total, consistent, err = CountFirst(false, 2, count(7))
	require.NoError(t, err)
	require.True(t, consistent)
	require.Equal(t, Total{}, total)
	require.Zero(t, calls, "a later page is not counted")

	for name, stored := range map[string]int{"fewer than the page": 1, "past the bound": store.MaximumListCount + 1} {
		t.Run(name, func(t *testing.T) {
			_, consistent, countErr := CountFirst(true, 2, count(stored))
			require.NoError(t, countErr)
			require.False(t, consistent)
		})
	}
	failure := errors.New("count failed")
	_, _, err = CountFirst(true, 0, func(int) (int, error) { return 0, failure })
	require.ErrorIs(t, err, failure)
}
