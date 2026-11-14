// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package attempt

import (
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/sudosylabs/proctor/server/model"
)

func TestWorkspaceWriteBudgetBoundsMutationsAndBytesPerAttempt(t *testing.T) {
	t.Parallel()
	rate := workspaceWriteRate{mutationsPerSecond: 10, mutationBurst: 20, bytesPerSecond: 100, byteBurst: 1000}
	budget := newWorkspaceWriteBudget(rate)
	attempt, other := model.NewExamAttemptID(), model.NewExamAttemptID()
	at := time.Date(2026, time.November, 14, 9, 0, 0, 0, time.UTC)

	require.True(t, budget.spend(attempt, 20, 0, at), "a full bucket takes the whole burst")
	require.False(t, budget.spend(attempt, 1, 0, at), "an empty bucket refuses")
	require.True(t, budget.spend(other, 20, 1000, at), "another Attempt has its own bucket")

	require.True(t, budget.spend(attempt, 10, 0, at.Add(time.Second)), "a second refills ten mutations")
	require.False(t, budget.spend(attempt, 1, 0, at.Add(time.Second)))

	require.True(t, budget.spend(attempt, 0, 1000, at.Add(time.Second)), "bytes are a separate bucket")
	require.False(t, budget.spend(attempt, 0, 1, at.Add(time.Second)))

	// A refusal takes nothing: the mutations still left are still there.
	later := at.Add(3 * time.Second)
	require.False(t, budget.spend(attempt, 5, 500, later), "200 bytes refilled is too few for 500")
	require.True(t, budget.spend(attempt, 20, 0, later), "the refused spend left the mutations untouched")

	require.True(t, budget.spend(attempt, 0, 1000, at.Add(time.Hour)), "refill stops at the burst, then serves it whole")
	require.False(t, budget.spend(attempt, 0, 1, at.Add(time.Hour)))
}

func TestWorkspaceWriteBudgetForgetsOnlyFullBuckets(t *testing.T) {
	t.Parallel()
	rate := workspaceWriteRate{mutationsPerSecond: 1, mutationBurst: 10, bytesPerSecond: 1, byteBurst: 10}
	budget := newWorkspaceWriteBudget(rate)
	at := time.Date(2026, time.November, 14, 9, 0, 0, 0, time.UTC)

	busy := model.NewExamAttemptID()
	require.True(t, budget.spend(busy, 10, 0, at))
	for range workspaceBudgetSweepSize - 1 {
		require.True(t, budget.spend(model.NewExamAttemptID(), 0, 0, at))
	}
	require.Len(t, budget.attempts, workspaceBudgetSweepSize)

	// The next new Attempt sweeps: every untouched bucket is full and goes,
	// the drained one stays and keeps its debt.
	require.True(t, budget.spend(model.NewExamAttemptID(), 0, 0, at.Add(time.Second)))
	require.Len(t, budget.attempts, 2)
	require.False(t, budget.spend(busy, 2, 0, at.Add(time.Second)), "the swept budget remembers the drained Attempt")
}

func TestWorkspaceWriteRatesFitOneMaximumBatch(t *testing.T) {
	t.Parallel()
	require.GreaterOrEqual(t, workspaceWriteRates.mutationBurst, float64(model.AttemptWorkspaceBatchMaximumMutations))
	require.GreaterOrEqual(t, workspaceWriteRates.byteBurst, float64(model.AttemptWorkspaceBatchMaximumRequestBytes))
}

func TestWorkspaceBatchOverItsBudgetIsRefusedBeforeAnythingIsStaged(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	workspaceTargetFixture(f)
	f.service.workspaceBudget = newWorkspaceWriteBudget(workspaceWriteRate{
		mutationsPerSecond: 1, mutationBurst: 2, bytesPerSecond: 1, byteBurst: 4,
	})

	_, err := applyBatch(f, nil, nil, createDirectory("a"), createDirectory("b"))
	require.NoError(t, err, "the burst covers the first batch")

	_, err = applyBatch(f, nil, nil, createDirectory("c"))
	requireBatchFault(t, err, "exam.attempt.workspace.rate_limited", "", "")

	_, err = applyBatch(f, batchContent(f, "hello"), nil, createFile("d.txt", strings.Repeat("a", 64), 5))
	requireBatchFault(t, err, "exam.attempt.workspace.rate_limited", "", "")

	require.Len(t, f.workspace.reservations, 1, "a refused batch reserves nothing")
	require.Len(t, f.workspace.batches, 1, "a refused batch applies nothing")
}
