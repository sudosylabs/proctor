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
	budget := newWorkspaceWriteBudget(fixedRates(map[model.AttemptWorkspaceAuthorKind]workspaceWriteRate{model.AttemptWorkspaceAuthorCandidate: rate}))
	attempt, other := model.NewExamAttemptID(), model.NewExamAttemptID()
	at := time.Date(2026, time.November, 14, 9, 0, 0, 0, time.UTC)

	require.True(t, budget.spend(attempt, model.AttemptWorkspaceAuthorCandidate, model.EnvironmentResources{}, 20, 0, at), "a full bucket takes the whole burst")
	require.False(t, budget.spend(attempt, model.AttemptWorkspaceAuthorCandidate, model.EnvironmentResources{}, 1, 0, at), "an empty bucket refuses")
	require.True(t, budget.spend(other, model.AttemptWorkspaceAuthorCandidate, model.EnvironmentResources{}, 20, 1000, at), "another Attempt has its own bucket")

	require.True(t, budget.spend(attempt, model.AttemptWorkspaceAuthorCandidate, model.EnvironmentResources{}, 10, 0, at.Add(time.Second)), "a second refills ten mutations")
	require.False(t, budget.spend(attempt, model.AttemptWorkspaceAuthorCandidate, model.EnvironmentResources{}, 1, 0, at.Add(time.Second)))

	require.True(t, budget.spend(attempt, model.AttemptWorkspaceAuthorCandidate, model.EnvironmentResources{}, 0, 1000, at.Add(time.Second)), "bytes are a separate bucket")
	require.False(t, budget.spend(attempt, model.AttemptWorkspaceAuthorCandidate, model.EnvironmentResources{}, 0, 1, at.Add(time.Second)))

	// A refusal takes nothing: the mutations still left are still there.
	later := at.Add(3 * time.Second)
	require.False(t, budget.spend(attempt, model.AttemptWorkspaceAuthorCandidate, model.EnvironmentResources{}, 5, 500, later), "200 bytes refilled is too few for 500")
	require.True(t, budget.spend(attempt, model.AttemptWorkspaceAuthorCandidate, model.EnvironmentResources{}, 20, 0, later), "the refused spend left the mutations untouched")

	require.True(t, budget.spend(attempt, model.AttemptWorkspaceAuthorCandidate, model.EnvironmentResources{}, 0, 1000, at.Add(time.Hour)), "refill stops at the burst, then serves it whole")
	require.False(t, budget.spend(attempt, model.AttemptWorkspaceAuthorCandidate, model.EnvironmentResources{}, 0, 1, at.Add(time.Hour)))
}

func TestWorkspaceWriteBudgetKeepsOneBucketPerAuthorKind(t *testing.T) {
	t.Parallel()
	rate := workspaceWriteRate{mutationsPerSecond: 1, mutationBurst: 4, bytesPerSecond: 1, byteBurst: 4}
	budget := newWorkspaceWriteBudget(fixedRates(map[model.AttemptWorkspaceAuthorKind]workspaceWriteRate{
		model.AttemptWorkspaceAuthorCandidate:   rate,
		model.AttemptWorkspaceAuthorEnvironment: {mutationsPerSecond: 1, mutationBurst: 8, bytesPerSecond: 1, byteBurst: 8},
	}))
	attempt := model.NewExamAttemptID()
	at := time.Date(2026, time.November, 15, 9, 0, 0, 0, time.UTC)

	require.True(t, budget.spend(attempt, model.AttemptWorkspaceAuthorEnvironment, model.EnvironmentResources{}, 8, 0, at), "the environment takes its own burst")
	require.False(t, budget.spend(attempt, model.AttemptWorkspaceAuthorEnvironment, model.EnvironmentResources{}, 1, 0, at))
	require.True(t, budget.spend(attempt, model.AttemptWorkspaceAuthorCandidate, model.EnvironmentResources{}, 4, 0, at), "a busy environment leaves the editor its budget")
	require.False(t, budget.spend(attempt, model.AttemptWorkspaceAuthorKind("grader"), model.EnvironmentResources{}, 0, 0, at), "a kind without a rate is refused")
}

func TestWorkspaceWriteBudgetForgetsOnlyFullBuckets(t *testing.T) {
	t.Parallel()
	rate := workspaceWriteRate{mutationsPerSecond: 1, mutationBurst: 10, bytesPerSecond: 1, byteBurst: 10}
	budget := newWorkspaceWriteBudget(fixedRates(map[model.AttemptWorkspaceAuthorKind]workspaceWriteRate{model.AttemptWorkspaceAuthorCandidate: rate}))
	at := time.Date(2026, time.November, 14, 9, 0, 0, 0, time.UTC)

	busy := model.NewExamAttemptID()
	require.True(t, budget.spend(busy, model.AttemptWorkspaceAuthorCandidate, model.EnvironmentResources{}, 10, 0, at))
	for range workspaceBudgetSweepSize - 1 {
		require.True(t, budget.spend(model.NewExamAttemptID(), model.AttemptWorkspaceAuthorCandidate, model.EnvironmentResources{}, 0, 0, at))
	}
	require.Len(t, budget.buckets, workspaceBudgetSweepSize)

	// The next new Attempt sweeps: every untouched bucket is full and goes,
	// the drained one stays and keeps its debt.
	require.True(t, budget.spend(model.NewExamAttemptID(), model.AttemptWorkspaceAuthorCandidate, model.EnvironmentResources{}, 0, 0, at.Add(time.Second)))
	require.Len(t, budget.buckets, 2)
	require.False(t, budget.spend(busy, model.AttemptWorkspaceAuthorCandidate, model.EnvironmentResources{}, 2, 0, at.Add(time.Second)), "the swept budget remembers the drained Attempt")
}

func TestWorkspaceWriteRatesFitOneMaximumBatch(t *testing.T) {
	t.Parallel()
	smallest := model.EnvironmentResources{CPUMillis: model.EnvironmentProfileMinimumCPUMillis,
		MemoryBytes: model.EnvironmentProfileMinimumMemoryBytes, DiskBytes: model.EnvironmentProfileMinimumDiskBytes}
	for _, kind := range []model.AttemptWorkspaceAuthorKind{model.AttemptWorkspaceAuthorCandidate, model.AttemptWorkspaceAuthorEnvironment, model.AttemptWorkspaceAuthorAssistant} {
		rate, ok := workspaceWriteRateFor(kind, smallest)
		require.True(t, ok, "every author kind has a rate: %s", kind)
		require.GreaterOrEqual(t, rate.mutationBurst, float64(model.AttemptWorkspaceBatchMaximumMutations), kind)
		require.GreaterOrEqual(t, rate.byteBurst, float64(model.AttemptWorkspaceBatchMaximumRequestBytes), kind)
	}
	_, ok := workspaceWriteRateFor(model.AttemptWorkspaceAuthorEnvironment, model.EnvironmentResources{})
	require.False(t, ok, "an environment without its profile has no rate")
}

func TestEnvironmentWriteRateIsSizedByTheProfile(t *testing.T) {
	t.Parallel()
	resources := func(cpu int64) model.EnvironmentResources {
		return model.EnvironmentResources{CPUMillis: cpu, MemoryBytes: 1 << 30, DiskBytes: 1 << 30}
	}
	cases := []struct {
		name string
		cpu  int64
		want workspaceWriteRate
	}{
		{"below one core counts one", 500, workspaceWriteRate{mutationsPerSecond: 128, mutationBurst: 2048,
			bytesPerSecond: 16 << 20, byteBurst: 256 << 20}},
		{"two cores", 2000, workspaceWriteRate{mutationsPerSecond: 256, mutationBurst: 4096,
			bytesPerSecond: 32 << 20, byteBurst: 512 << 20}},
		{"past eight cores counts eight", 64_000, workspaceWriteRate{mutationsPerSecond: 1024, mutationBurst: 16384,
			bytesPerSecond: 128 << 20, byteBurst: 2048 << 20}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			rate, ok := workspaceWriteRateFor(model.AttemptWorkspaceAuthorEnvironment, resources(tc.cpu))
			require.True(t, ok)
			require.Equal(t, tc.want, rate)
		})
	}
}

func TestWorkspaceBatchOverItsBudgetIsRefusedBeforeAnythingIsStaged(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	workspaceTargetFixture(f)
	f.service.workspaceBudget = newWorkspaceWriteBudget(fixedRates(map[model.AttemptWorkspaceAuthorKind]workspaceWriteRate{
		model.AttemptWorkspaceAuthorCandidate: {mutationsPerSecond: 1, mutationBurst: 2, bytesPerSecond: 1, byteBurst: 4},
	}))

	_, err := applyBatch(f, nil, nil, createDirectory("a"), createDirectory("b"))
	require.NoError(t, err, "the burst covers the first batch")

	_, err = applyBatch(f, nil, nil, createDirectory("c"))
	requireBatchFault(t, err, "exam.attempt.workspace.rate_limited", "", "")

	_, err = applyBatch(f, batchContent(f, "hello"), nil, createFile("d.txt", strings.Repeat("a", 64), 5))
	requireBatchFault(t, err, "exam.attempt.workspace.rate_limited", "", "")

	require.Len(t, f.workspace.reservations, 1, "a refused batch reserves nothing")
	require.Len(t, f.workspace.batches, 1, "a refused batch applies nothing")
}

// fixedRates gives each kind the same rate whatever its resources.
func fixedRates(rates map[model.AttemptWorkspaceAuthorKind]workspaceWriteRate) func(model.AttemptWorkspaceAuthorKind, model.EnvironmentResources) (workspaceWriteRate, bool) {
	return func(kind model.AttemptWorkspaceAuthorKind, _ model.EnvironmentResources) (workspaceWriteRate, bool) {
		rate, ok := rates[kind]
		return rate, ok
	}
}

func TestAssistantWriteBudgetIsSeparateAndRefillsAtItsLowerRate(t *testing.T) {
	t.Parallel()
	budget := newWorkspaceWriteBudget(workspaceWriteRateFor)
	attempt := model.NewExamAttemptID()
	at := time.Date(2026, time.November, 17, 9, 0, 0, 0, time.UTC)
	resources := model.EnvironmentResources{}
	require.True(t, budget.spend(attempt, model.AttemptWorkspaceAuthorAssistant, resources, 256, 128<<20, at))
	require.False(t, budget.spend(attempt, model.AttemptWorkspaceAuthorAssistant, resources, 1, 0, at))
	require.True(t, budget.spend(attempt, model.AttemptWorkspaceAuthorCandidate, resources, 32, 4<<20, at))
	require.False(t, budget.spend(attempt, model.AttemptWorkspaceAuthorAssistant, resources, 17, 0, at.Add(time.Second)))
	require.False(t, budget.spend(attempt, model.AttemptWorkspaceAuthorAssistant, resources, 16, (2<<20)+1, at.Add(time.Second)))
	require.True(t, budget.spend(attempt, model.AttemptWorkspaceAuthorAssistant, resources, 16, 2<<20, at.Add(time.Second)))
}
