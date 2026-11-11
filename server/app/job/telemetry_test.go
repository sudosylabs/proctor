// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package job

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/sudosylabs/proctor/server/model"
)

type executionSpanKey struct{}

type jobTracerFake struct {
	mu       sync.Mutex
	started  []model.JobType
	outcomes []ExecutionOutcome
}

func (f *jobTracerFake) StartExecution(ctx context.Context, jobType model.JobType) (context.Context, func(ExecutionOutcome)) {
	f.mu.Lock()
	f.started = append(f.started, jobType)
	f.mu.Unlock()
	return context.WithValue(ctx, executionSpanKey{}, jobType), func(outcome ExecutionOutcome) {
		f.mu.Lock()
		f.outcomes = append(f.outcomes, outcome)
		f.mu.Unlock()
	}
}

func TestEngineTracesEachExecutionWithTheHandlerInsideTheSpan(t *testing.T) {
	t.Parallel()

	claim := jobRunnerClaim(t)
	handlerSpans := make(chan any, 1)
	descriptor := testDescriptor(handlerFunc(func(ctx context.Context, _ Execution) Outcome {
		handlerSpans <- ctx.Value(executionSpanKey{})
		return succeededOutcome()
	}))
	persistence := &jobRunnerStoreFake{claim: claim}
	tracer := &jobTracerFake{}
	engine, err := New(Dependencies{
		Store: persistence, Descriptors: []Descriptor{descriptor}, NodeID: "node-a",
		Diagnostics: newDiscardLogger(t), Tracer: tracer, Policy: Policy{PollInterval: time.Millisecond},
	})
	require.NoError(t, err)
	require.NoError(t, engine.Start(context.Background()))
	engine.Wake()
	select {
	case span := <-handlerSpans:
		require.Equal(t, descriptor.Type, span, "handler context carries the execution span")
	case <-time.After(time.Second):
		require.FailNow(t, "Engine did not execute queued work")
	}
	deadline := time.Now().Add(time.Second)
	for {
		tracer.mu.Lock()
		finished := len(tracer.outcomes)
		tracer.mu.Unlock()
		if finished > 0 {
			break
		}
		require.False(t, time.Now().After(deadline), "execution span did not finish")
		time.Sleep(time.Millisecond)
	}
	require.NoError(t, engine.Close(context.Background()))
	tracer.mu.Lock()
	defer tracer.mu.Unlock()
	require.Equal(t, []model.JobType{descriptor.Type}, tracer.started)
	require.Equal(t, []ExecutionOutcome{ExecutionSucceeded}, tracer.outcomes)
}
