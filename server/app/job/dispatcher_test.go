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
	"testing/synctest"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/sudosylabs/proctor/server/model"
	"github.com/sudosylabs/proctor/server/store"
)

// dispatchStoreFake counts the reads the dispatcher and workers make. Every
// claim finds nothing; due says when each type next has work.
type dispatchStoreFake struct {
	store.JobStore
	mu       sync.Mutex
	due      map[model.JobType]time.Time
	nextDues int
	claims   map[model.JobType]int
}

func (f *dispatchStoreFake) NextDue(_ context.Context, types []model.JobType) (map[model.JobType]time.Time, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.nextDues++
	due := map[model.JobType]time.Time{}
	for _, jobType := range types {
		if at, ok := f.due[jobType]; ok {
			due[jobType] = at
		}
	}
	return due, nil
}

func (f *dispatchStoreFake) ClaimNext(_ context.Context, request *store.JobClaimRequest) (*store.JobClaim, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.claims[request.Types[0]]++
	delete(f.due, request.Types[0])
	return nil, store.NewErrNotFound("job", "claimable")
}

func (f *dispatchStoreFake) counts() (int, map[model.JobType]int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	claims := map[model.JobType]int{}
	for jobType, count := range f.claims {
		claims[jobType] = count
	}
	return f.nextDues, claims
}

func newDispatchTestEngine(t *testing.T, persistence *dispatchStoreFake, backstop time.Duration, types ...model.JobType) *Engine {
	t.Helper()
	descriptors := make([]Descriptor, 0, len(types))
	for _, jobType := range types {
		descriptor := testDescriptor(handlerFunc(func(context.Context, Execution) Outcome { return succeededOutcome() }))
		descriptor.Type = jobType
		descriptor.Concurrency = 3
		descriptors = append(descriptors, descriptor)
	}
	engine, err := New(Dependencies{Store: persistence, Descriptors: descriptors, NodeID: "node-a",
		Diagnostics: newDiscardLogger(t), Policy: Policy{BackstopInterval: backstop}})
	require.NoError(t, err)
	return engine
}

// TestIdleEngineReadsTheQueueOncePerBackstop is the point of the dispatcher:
// a node with nothing to do no longer claims twice a second per worker.
func TestIdleEngineReadsTheQueueOncePerBackstop(t *testing.T) {
	t.Parallel()
	synctest.Test(t, func(t *testing.T) {
		persistence := &dispatchStoreFake{due: map[model.JobType]time.Time{}, claims: map[model.JobType]int{}}
		engine := newDispatchTestEngine(t, persistence, 30*time.Second, model.JobTypeMailDeliver, model.JobTypeCleanup)
		require.NoError(t, engine.Start(context.Background()))

		time.Sleep(10 * time.Minute)
		synctest.Wait()
		reads, claims := persistence.counts()
		require.Empty(t, claims, "no worker claims while nothing is due")
		require.InDelta(t, 20, reads, 6, "one next-due read per jittered backstop over ten minutes")
		require.NoError(t, engine.Close(context.Background()))
	})
}

// TestWakeClaimsAtOnceAndOnlyForTheWokenType pins the wake path: the woken
// type's workers claim immediately, and no other type's workers stir.
func TestWakeClaimsAtOnceAndOnlyForTheWokenType(t *testing.T) {
	t.Parallel()
	synctest.Test(t, func(t *testing.T) {
		persistence := &dispatchStoreFake{due: map[model.JobType]time.Time{}, claims: map[model.JobType]int{}}
		engine := newDispatchTestEngine(t, persistence, time.Hour, model.JobTypeMailDeliver, model.JobTypeCleanup)
		require.NoError(t, engine.Start(context.Background()))
		synctest.Wait()

		engine.WakeTypes(model.JobTypeMailDeliver)
		synctest.Wait()
		_, claims := persistence.counts()
		require.Positive(t, claims[model.JobTypeMailDeliver], "the woken type claimed")
		require.LessOrEqual(t, claims[model.JobTypeMailDeliver], 3, "at most one turn per worker")
		require.Zero(t, claims[model.JobTypeCleanup], "another type's workers stayed asleep")
		require.NoError(t, engine.Close(context.Background()))
	})
}

// TestDispatcherClaimsWhenTheEarliestJobComesDue pins scheduling: a Job due
// in ten minutes is claimed then, not at the next backstop an hour away.
func TestDispatcherClaimsWhenTheEarliestJobComesDue(t *testing.T) {
	t.Parallel()
	synctest.Test(t, func(t *testing.T) {
		persistence := &dispatchStoreFake{
			due:    map[model.JobType]time.Time{model.JobTypeCleanup: time.Now().Add(10 * time.Minute)},
			claims: map[model.JobType]int{},
		}
		engine := newDispatchTestEngine(t, persistence, time.Hour, model.JobTypeCleanup)
		require.NoError(t, engine.Start(context.Background()))

		time.Sleep(10*time.Minute - time.Second)
		synctest.Wait()
		_, claims := persistence.counts()
		require.Zero(t, claims[model.JobTypeCleanup], "nothing claimed before it was due")

		time.Sleep(2 * time.Second)
		synctest.Wait()
		_, claims = persistence.counts()
		require.Positive(t, claims[model.JobTypeCleanup], "claimed when due")
		require.NoError(t, engine.Close(context.Background()))
	})
}

// TestBackoffAndJitterStayInBounds pins the waits after failures and the
// spread every wait gets.
func TestBackoffAndJitterStayInBounds(t *testing.T) {
	t.Parallel()
	for range 1000 {
		value := jitter(10 * time.Second)
		require.GreaterOrEqual(t, value, 8*time.Second)
		require.LessOrEqual(t, value, 12*time.Second)
	}
	for failures, base := range map[int]time.Duration{1: 500 * time.Millisecond, 2: time.Second, 3: 2 * time.Second, 7: 30 * time.Second, 40: 30 * time.Second} {
		for range 100 {
			value := backoff(failures)
			require.GreaterOrEqual(t, value, time.Duration(float64(base)*0.8), "failure %d", failures)
			require.LessOrEqual(t, value, time.Duration(float64(base)*1.2), "failure %d", failures)
		}
	}
}
