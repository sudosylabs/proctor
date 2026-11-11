// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package job

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/sudosylabs/proctor/server/store"
)

func TestEngineHeartbeatsLongWorkAndCompletesWithItsFence(t *testing.T) {
	t.Parallel()
	claim := jobRunnerClaim(t)
	descriptor := testDescriptor(handlerFunc(func(ctx context.Context, _ Execution) Outcome {
		select {
		case <-time.After(20 * time.Millisecond):
			return succeededOutcome()
		case <-ctx.Done():
			return RetryableFailure("job.timeout", ctx.Err())
		}
	}))
	registry, err := NewRegistry([]Descriptor{descriptor})
	require.NoError(t, err)
	persistence := &jobRunnerStoreFake{}
	runner, err := newTestEngine(persistence, registry, "node-a", newDiscardLogger(t), time.Second)
	require.NoError(t, err)
	runner.executeClaim(context.Background(), descriptor, claim)
	persistence.mu.Lock()
	defer persistence.mu.Unlock()
	require.NotEqual(t, 0, persistence.heartbeats, "execution: heartbeats")
	require.NotNil(t, persistence.completion, "execution: heartbeats")
	require.Equal(t, store.JobCompletionSucceeded, persistence.completion.Kind, "execution: heartbeats")
	require.Equal(t, claim.Attempt.ClaimToken, persistence.completion.ClaimToken, "execution: heartbeats")
}

func TestEngineToleratesHeartbeatFailuresWithinLeaseSlack(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name     string
		failure  error
		complete bool
	}{
		{"unknown outcome keeps the claim", errors.New("database unavailable"), true},
		{"lost claim abandons the attempt", store.NewErrConflict("job_attempt", "claim_lost", nil), false},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			claim := jobRunnerClaim(t)
			descriptor := testDescriptor(handlerFunc(func(ctx context.Context, _ Execution) Outcome {
				select {
				case <-time.After(60 * time.Millisecond):
					return succeededOutcome()
				case <-ctx.Done():
					return RetryableFailure("job.timeout", ctx.Err())
				}
			}))
			descriptor.HeartbeatInterval = 5 * time.Millisecond
			descriptor.LeaseDuration = time.Minute
			registry, err := NewRegistry([]Descriptor{descriptor})
			require.NoError(t, err)
			persistence := &jobRunnerStoreFake{heartbeatErrors: []error{test.failure}}
			runner, err := newTestEngine(persistence, registry, "node-a", newDiscardLogger(t), time.Second)
			require.NoError(t, err)
			runner.executeClaim(context.Background(), descriptor, claim)
			persistence.mu.Lock()
			defer persistence.mu.Unlock()
			if !test.complete {
				require.Nil(t, persistence.completion, "lost claim completed")
				return
			}
			require.NotNil(t, persistence.completion, "one failed heartbeat abandoned the attempt")
			require.Equal(t, store.JobCompletionSucceeded, persistence.completion.Kind)
			require.Greater(t, persistence.heartbeats, 1, "heartbeats after the failure")
		})
	}
}

func TestEngineObservesDurableCancellationCooperatively(t *testing.T) {
	t.Parallel()
	claim := jobRunnerClaim(t)
	descriptor := testDescriptor(handlerFunc(func(ctx context.Context, _ Execution) Outcome {
		<-ctx.Done()
		return Canceled("job.canceled")
	}))
	descriptor.HeartbeatInterval = 5 * time.Millisecond
	descriptor.Cancelable = true
	persistence := &jobRunnerStoreFake{cancelRequested: true}
	registry, err := NewRegistry([]Descriptor{descriptor})
	require.NoError(t, err)
	runner, err := newTestEngine(persistence, registry, "node-a", newDiscardLogger(t), time.Second)
	require.NoError(t, err)
	runner.executeClaim(context.Background(), descriptor, claim)
	persistence.mu.Lock()
	defer persistence.mu.Unlock()
	require.NotNil(t, persistence.completion)
	require.Equal(t, store.JobCompletionCanceled, persistence.completion.Kind)
}
