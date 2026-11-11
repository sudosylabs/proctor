// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package job

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/sudosylabs/proctor/server/model"
	"github.com/sudosylabs/proctor/server/store"
)

func TestEngineRecordsUnsupportedClaimAsAnExecution(t *testing.T) {
	t.Parallel()

	claim := jobRunnerClaim(t)
	claim.Job.Type = model.JobTypeMailCleanup
	descriptor := testDescriptor(handlerFunc(func(context.Context, Execution) Outcome { return succeededOutcome() }))
	persistence := &jobRunnerStoreFake{claim: claim}
	recorder := &jobMetricsFake{}
	engine, err := New(Dependencies{
		Store: persistence, Descriptors: []Descriptor{descriptor}, NodeID: "node-a",
		Diagnostics: newDiscardLogger(t), Recorder: recorder, Policy: Policy{PollInterval: time.Millisecond},
	})
	require.NoError(t, err)
	err = engine.Start(context.Background())
	require.NoError(t, err)
	engine.Wake()
	deadline := time.Now().Add(time.Second)
	for {
		persistence.mu.Lock()
		completion := persistence.completion
		persistence.mu.Unlock()
		if completion != nil {
			break
		}
		require.False(t, time.Now().After(deadline), "Engine did not complete the unsupported claim")
		time.Sleep(time.Millisecond)
	}
	err = engine.Close(context.Background())
	require.NoError(t, err)
	recorder.mu.Lock()
	defer recorder.mu.Unlock()
	require.Equal(t, 1, recorder.started, "execution metrics = started")
	require.Equal(t, 1, recorder.finished, "execution metrics = started")
	require.Len(t, recorder.outcomes, 1, "execution metrics = started")
	require.Equal(t, ExecutionPermanentFailure, recorder.outcomes[0], "execution metrics = started")
}

func TestEngineSchedulesRetryWithBoundedDeterministicBackoff(t *testing.T) {
	t.Parallel()
	descriptor := testDescriptor(handlerFunc(func(context.Context, Execution) Outcome {
		return RetryableFailure("dependency.unavailable", errors.New("temporary"))
	}))
	descriptor.BaseRetryDelay = 100 * time.Millisecond
	descriptor.MaximumRetryDelay = 450 * time.Millisecond

	for _, test := range []struct {
		attempt int
		want    time.Duration
	}{{attempt: 1, want: 119400 * time.Microsecond}, {attempt: 2, want: 238800 * time.Microsecond}, {attempt: 3, want: 450 * time.Millisecond}, {attempt: 4, want: 450 * time.Millisecond}} {
		t.Run(fmt.Sprintf("attempt=%v", test.attempt), func(t *testing.T) {
			claim := jobRunnerClaim(t)
			claim.Job.ID = model.JobID("job-fixed")
			claim.Attempt.Number = test.attempt
			persistence := &jobRunnerStoreFake{}
			registry, err := NewRegistry([]Descriptor{descriptor})
			require.NoError(t, err)
			engine, err := newTestEngine(persistence, registry, "node-a", newDiscardLogger(t), time.Second)
			require.NoError(t, err)
			engine.executeClaim(context.Background(), descriptor, claim)
			persistence.mu.Lock()
			completion := persistence.completion
			persistence.mu.Unlock()
			require.NotNil(t, completion, "attempt %d retry completion", test.attempt)
			require.Equal(t, store.JobCompletionRetryableFailure, completion.Kind, "attempt %d retry completion", test.attempt)
			require.Equal(t, test.want, completion.RetryDelay, "attempt %d retry completion", test.attempt)
		})
	}
}

func TestEngineRelinquishesIncompatibleWorkWithoutConsumingItsRetryBudget(t *testing.T) {
	t.Parallel()
	claim := jobRunnerClaim(t)
	descriptor := testDescriptor(handlerFunc(func(context.Context, Execution) Outcome {
		return Relinquished("worker.capability_mismatch", errors.New("another node owns the required capability"))
	}))
	descriptor.PublicErrorCodes = append(descriptor.PublicErrorCodes, "worker.capability_mismatch")
	registry, err := NewRegistry([]Descriptor{descriptor})
	require.NoError(t, err)
	persistence := &jobRunnerStoreFake{}
	runner, err := newTestEngine(persistence, registry, "node-old", newDiscardLogger(t), time.Second)
	require.NoError(t, err)

	runner.executeClaim(context.Background(), descriptor, claim)

	persistence.mu.Lock()
	defer persistence.mu.Unlock()
	require.NotNil(t, persistence.completion)
	require.Equal(t, store.JobCompletionRelinquished, persistence.completion.Kind)
	require.Equal(t, "worker.capability_mismatch", persistence.completion.PublicErrorCode)
	require.Positive(t, int64(persistence.completion.RetryDelay))
}

func TestEngineContainsPanicsAsRetryableOutcomes(t *testing.T) {
	t.Parallel()
	claim := jobRunnerClaim(t)
	descriptor := testDescriptor(handlerFunc(func(context.Context, Execution) Outcome { panic("secret payload") }))
	registry, err := NewRegistry([]Descriptor{descriptor})
	require.NoError(t, err)
	persistence := &jobRunnerStoreFake{}
	runner, err := newTestEngine(persistence, registry, "node-a", newDiscardLogger(t), time.Second)
	require.NoError(t, err)
	runner.executeClaim(context.Background(), descriptor, claim)
	persistence.mu.Lock()
	defer persistence.mu.Unlock()
	require.NotNil(t, persistence.completion)
	require.Equal(t, store.JobCompletionRetryableFailure, persistence.completion.Kind)
	require.Equal(t, "job.handler_panic", persistence.completion.PublicErrorCode)
}

func TestEngineFailsAnAttemptWithoutAJobBeforeItsHandlerRuns(t *testing.T) {
	t.Parallel()
	claim := jobRunnerClaim(t)
	claim.Job = nil
	handlerRan := false
	descriptor := testDescriptor(handlerFunc(func(_ context.Context, execution Execution) Outcome {
		handlerRan = true
		return Outcome{Kind: OutcomeSucceeded, Result: execution.Job.Command}
	}))
	registry, err := NewRegistry([]Descriptor{descriptor})
	require.NoError(t, err)
	persistence := &jobRunnerStoreFake{}
	runner, err := newTestEngine(persistence, registry, "node-a", newDiscardLogger(t), time.Second)
	require.NoError(t, err)
	runner.executeClaim(context.Background(), descriptor, claim)
	persistence.mu.Lock()
	defer persistence.mu.Unlock()
	require.False(t, handlerRan)
	require.NotNil(t, persistence.completion)
	require.Equal(t, store.JobCompletionPermanentFailure, persistence.completion.Kind)
	require.Equal(t, "job.command.invalid", persistence.completion.PublicErrorCode)
}

func TestEngineFencesOccurrenceWorkReservations(t *testing.T) {
	t.Parallel()
	claim := jobRunnerClaim(t)
	var reserved bool
	descriptor := testDescriptor(handlerFunc(func(ctx context.Context, execution Execution) Outcome {
		var err error
		reserved, err = execution.ReserveWork(ctx, 2, 5)
		if err != nil {
			return RetryableFailure("dependency.unavailable", err)
		}
		return succeededOutcome()
	}))
	registry, err := NewRegistry([]Descriptor{descriptor})
	require.NoError(t, err)
	persistence := &jobRunnerStoreFake{}
	runner, err := newTestEngine(persistence, registry, "node-a", newDiscardLogger(t), time.Second)
	require.NoError(t, err)
	runner.executeClaim(context.Background(), descriptor, claim)
	require.True(t, reserved, "reservation")
	require.NotNil(t, persistence.reservation, "reservation")
	require.Equal(t, claim.Attempt.ID, persistence.reservation.AttemptID, "reservation")
	require.Equal(t, claim.Attempt.ClaimToken, persistence.reservation.ClaimToken, "reservation")
	require.Equal(t, 2, persistence.reservation.Units, "reservation")
	require.Equal(t, 5, persistence.reservation.Limit, "reservation")
}

func TestEngineCompletesHandlerThatSucceedsAtItsTimeout(t *testing.T) {
	t.Parallel()
	claim := jobRunnerClaim(t)
	descriptor := testDescriptor(handlerFunc(func(ctx context.Context, _ Execution) Outcome {
		<-ctx.Done()
		return succeededOutcome()
	}))
	descriptor.Timeout = 10 * time.Millisecond
	registry, err := NewRegistry([]Descriptor{descriptor})
	require.NoError(t, err)
	persistence := &jobRunnerStoreFake{}
	runner, err := newTestEngine(persistence, registry, "node-a", newDiscardLogger(t), time.Second)
	require.NoError(t, err)
	runner.executeClaim(context.Background(), descriptor, claim)
	persistence.mu.Lock()
	defer persistence.mu.Unlock()
	require.NotNil(t, persistence.completion)
	require.Equal(t, store.JobCompletionSucceeded, persistence.completion.Kind, "committed success was re-run as a timeout")
}

func TestEngineRejectsASucceededOutcomeWithoutResult(t *testing.T) {
	t.Parallel()
	claim := jobRunnerClaim(t)
	descriptor := testDescriptor(handlerFunc(func(context.Context, Execution) Outcome {
		return Outcome{Kind: OutcomeSucceeded}
	}))
	registry, err := NewRegistry([]Descriptor{descriptor})
	require.NoError(t, err)
	persistence := &jobRunnerStoreFake{}
	runner, err := newTestEngine(persistence, registry, "node-a", newDiscardLogger(t), time.Second)
	require.NoError(t, err)
	runner.executeClaim(context.Background(), descriptor, claim)
	persistence.mu.Lock()
	defer persistence.mu.Unlock()
	require.NotNil(t, persistence.completion)
	require.Equal(t, store.JobCompletionPermanentFailure, persistence.completion.Kind)
	require.Equal(t, "job.result.invalid", persistence.completion.PublicErrorCode)
}

func TestEngineEnforcesTypedCheckpointContract(t *testing.T) {
	t.Parallel()

	claim := jobRunnerClaim(t)
	var checkpointErr error
	descriptor := testDescriptor(handlerFunc(func(ctx context.Context, execution Execution) Outcome {
		checkpointErr = execution.Checkpoint(ctx, CheckpointValue{Progress: &model.JobProgress{Current: 1, Total: 3, Stage: "rendering"}, Document: json.RawMessage(`{"cursor":1}`)})
		return succeededOutcome()
	}))
	descriptor.Checkpoints = true
	descriptor.ProgressStages = []string{"rendering"}
	registry, err := NewRegistry([]Descriptor{descriptor})
	require.NoError(t, err)
	persistence := &jobRunnerStoreFake{}
	runner, err := newTestEngine(persistence, registry, "node-a", newDiscardLogger(t), time.Second)
	require.NoError(t, err)
	runner.executeClaim(context.Background(), descriptor, claim)
	require.NoError(t, checkpointErr)
	persistence.mu.Lock()
	require.Equal(t, 1, persistence.checkpoints, "checkpoint = %#v count", persistence.checkpoint)
	require.Equal(t, "rendering", persistence.checkpoint.Progress.Stage, "checkpoint = %#v count", persistence.checkpoint)
	persistence.mu.Unlock()

	undeclared := testDescriptor(handlerFunc(func(ctx context.Context, execution Execution) Outcome {
		checkpointErr = execution.Checkpoint(ctx, CheckpointValue{Progress: &model.JobProgress{Current: 1, Total: 1, Stage: "secret"}, Document: json.RawMessage(`{}`)})
		return succeededOutcome()
	}))
	runner.executeClaim(context.Background(), undeclared, jobRunnerClaim(t))
	require.Error(t, checkpointErr, "Checkpoint() accepted undeclared version and stage")
	persistence.mu.Lock()
	defer persistence.mu.Unlock()
	require.Equal(t, 1, persistence.checkpoints, "undeclared checkpoint reached store: count")
}
