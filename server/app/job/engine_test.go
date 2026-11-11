// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package job

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestEngineClaimsAndCompletesThroughItsPublicLifecycle(t *testing.T) {
	t.Parallel()
	claim := jobRunnerClaim(t)
	descriptor := testDescriptor(handlerFunc(func(context.Context, Execution) Outcome { return succeededOutcome() }))
	persistence := &jobRunnerStoreFake{claim: claim}
	recorder := &jobMetricsFake{}
	engine, err := New(Dependencies{
		Store: persistence, Descriptors: []Descriptor{descriptor}, NodeID: "node-a",
		Diagnostics: newDiscardLogger(t), Recorder: recorder, Policy: Policy{PollInterval: time.Millisecond},
	})
	require.NoError(t, err)
	described, describeErr := engine.Descriptor(descriptor.Type)
	require.NoError(t, describeErr, "Descriptor() exposed executable handler")
	require.Nil(t, described.Handler, "Descriptor() exposed executable handler")
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
		require.False(t, time.Now().After(deadline), "Engine did not claim and complete queued work")
		time.Sleep(time.Millisecond)
	}
	err = engine.Close(context.Background())
	require.NoError(t, err)
	recorder.mu.Lock()
	defer recorder.mu.Unlock()
	require.Equal(t, 1, recorder.started, "execution metrics = started")
	require.Equal(t, 1, recorder.finished, "execution metrics = started")
	seen := map[string]bool{}
	for _, activity := range recorder.activities {
		seen[activity.Operation+"/"+activity.Outcome] = true
	}
	require.True(t, seen["claim/success"])
	require.True(t, seen["complete/success"])
}

func TestEngineShutdownIsBounded(t *testing.T) {
	t.Parallel()
	descriptor := testDescriptor(handlerFunc(func(context.Context, Execution) Outcome {
		return succeededOutcome()
	}))
	descriptor.Timeout = 20 * time.Millisecond
	registry, err := NewRegistry([]Descriptor{descriptor})
	require.NoError(t, err)
	runner, err := newTestEngine(&jobRunnerStoreFake{}, registry, "node-a", newDiscardLogger(t), time.Second)
	require.NoError(t, err)
	blocked := make(chan struct{})
	runner.wg.Add(1)
	go func() {
		defer runner.wg.Done()
		<-blocked
	}()
	started := time.Now()
	err = runner.Close(context.Background())
	require.Error(t, err, "Close() waited without a shutdown deadline")
	close(blocked)
	elapsed := time.Since(started)
	require.GreaterOrEqual(t, elapsed, descriptor.Timeout)
	require.LessOrEqual(t, elapsed, time.Second)
}

func TestEngineShutdownEndsWithTheCallersBudget(t *testing.T) {
	t.Parallel()
	descriptor := testDescriptor(handlerFunc(func(context.Context, Execution) Outcome {
		return succeededOutcome()
	}))
	// The longest Job timeout would allow a far longer wait than the budget.
	descriptor.Timeout = time.Minute
	registry, err := NewRegistry([]Descriptor{descriptor})
	require.NoError(t, err)
	runner, err := newTestEngine(&jobRunnerStoreFake{}, registry, "node-a", newDiscardLogger(t), time.Second)
	require.NoError(t, err)
	blocked := make(chan struct{})
	t.Cleanup(func() { close(blocked) })
	runner.wg.Add(1)
	go func() {
		defer runner.wg.Done()
		<-blocked
	}()
	const budget = 50 * time.Millisecond
	// Start the clock before the deadline so a slow host cannot make Close()
	// look early.
	started := time.Now()
	ctx, cancel := context.WithTimeout(context.Background(), budget)
	defer cancel()
	err = runner.Close(ctx)
	elapsed := time.Since(started)
	require.ErrorIs(t, err, context.DeadlineExceeded, "Close() outcome for a worker that never returns")
	require.GreaterOrEqual(t, elapsed, budget)
	// Generous for loaded race runs, yet far short of the one-minute Job timeout.
	require.Less(t, elapsed, time.Second, "Close() outlived the shutdown budget it was handed")
}
