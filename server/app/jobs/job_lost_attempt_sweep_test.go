// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package jobs

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/require"
)

type recoveryFake struct {
	results []int
	err     error
	calls   int
}

func (f *recoveryFake) RecoverLostAttempts(_ context.Context, limit int) (int, error) {
	f.calls++
	if f.err != nil {
		return 0, f.err
	}
	if len(f.results) == 0 {
		return 0, nil
	}
	next := f.results[0]
	f.results = f.results[1:]
	return min(next, limit), nil
}

// TestLostAttemptSweepDrainsInBoundedBatches pins the sweep's shape: it keeps
// going while batches come back full, stops at a short one, and never runs
// more than its bound in one go.
func TestLostAttemptSweepDrainsInBoundedBatches(t *testing.T) {
	t.Parallel()

	short := &recoveryFake{results: []int{100, 100, 7}}
	require.NoError(t, LostAttemptSweep{Jobs: short}.Run(context.Background()))
	require.Equal(t, 3, short.calls, "a short batch ends the run")

	backlog := &recoveryFake{results: make([]int, 50)}
	for index := range backlog.results {
		backlog.results[index] = 100
	}
	require.NoError(t, LostAttemptSweep{Jobs: backlog}.Run(context.Background()))
	require.Equal(t, lostAttemptSweepBatches, backlog.calls, "one run is bounded")

	failing := &recoveryFake{err: errors.New("database unavailable")}
	require.Error(t, LostAttemptSweep{Jobs: failing}.Run(context.Background()))
	require.Equal(t, 1, failing.calls)
}
