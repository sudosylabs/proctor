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
	"sync"
	"testing"
	"testing/synctest"
	"time"

	"github.com/stretchr/testify/require"
)

type periodicTestRunner struct {
	mu    sync.Mutex
	calls int
	err   error
	seen  chan struct{}
}

func (r *periodicTestRunner) Run(context.Context) error {
	r.mu.Lock()
	r.calls++
	r.mu.Unlock()
	select {
	case r.seen <- struct{}{}:
	default:
	}
	return r.err
}

func (r *periodicTestRunner) count() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.calls
}

func TestClonePeriodicTasksValidatesAndClones(t *testing.T) {
	t.Parallel()
	runner := &periodicTestRunner{}
	values := []PeriodicTask{{Name: "exam-attempt-participation-expiry", Interval: 2 * time.Second, Runner: runner}}
	cloned, err := clonePeriodicTasks(values)
	require.NoError(t, err)
	values[0] = PeriodicTask{Name: "replacement", Interval: time.Hour, Runner: runner}
	require.Equal(t, "exam-attempt-participation-expiry", cloned[0].Name)
	require.Equal(t, 2*time.Second, cloned[0].Interval)

	tests := []struct {
		name  string
		value []PeriodicTask
	}{
		{name: "empty name", value: []PeriodicTask{{Interval: time.Second, Runner: runner}}},
		{name: "unsafe name", value: []PeriodicTask{{Name: "UPPER", Interval: time.Second, Runner: runner}}},
		{name: "nonpositive interval", value: []PeriodicTask{{Name: "scan", Runner: runner}}},
		{name: "missing runner", value: []PeriodicTask{{Name: "scan", Interval: time.Second}}},
		{name: "duplicate", value: []PeriodicTask{{Name: "scan", Interval: time.Second, Runner: runner}, {Name: "scan", Interval: 2 * time.Second, Runner: runner}}},
	}
	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			_, err := clonePeriodicTasks(test.value)
			require.Error(t, err, "clonePeriodicTasks() accepted invalid definition")
		})
	}
}

func TestRunPeriodicTaskSpreadsItsRunsAroundTheInterval(t *testing.T) {
	t.Parallel()
	synctest.Test(t, func(t *testing.T) {
		runner := &periodicTestRunner{seen: make(chan struct{}, 3)}
		ctx, cancel := context.WithCancel(context.Background())
		done := make(chan struct{})
		logger := newDiscardLogger(t)
		go func() {
			runPeriodicTask(ctx, PeriodicTask{Name: "scan", Interval: 2 * time.Second, Runner: runner}, logger, nil)
			close(done)
		}()

		// The first run comes within one interval, at a random offset.
		waitPeriodicWithin(t, runner.seen, 0, 2*time.Second)
		// Each later run comes after the interval, give or take a tenth.
		waitPeriodicWithin(t, runner.seen, 1800*time.Millisecond, 2200*time.Millisecond)
		waitPeriodicWithin(t, runner.seen, 1800*time.Millisecond, 2200*time.Millisecond)
		cancel()
		<-done
		require.Equal(t, 3, runner.count(), "Run() calls")
	})
}

func TestRunPeriodicTaskReportsErrorAndBacksOff(t *testing.T) {
	t.Parallel()
	synctest.Test(t, func(t *testing.T) {
		runner := &periodicTestRunner{err: errors.New("database unavailable"), seen: make(chan struct{}, 2)}
		diagnostics, logs := newTestLogger(t)
		ctx, cancel := context.WithCancel(context.Background())
		done := make(chan struct{})
		go func() {
			runPeriodicTask(ctx, PeriodicTask{Name: "scan", Interval: 2 * time.Second, Runner: runner}, diagnostics, nil)
			close(done)
		}()
		waitPeriodicWithin(t, runner.seen, 0, 2*time.Second)
		require.Contains(t, logs.String(), "run periodic runtime task scan", "periodic task error was not reported")
		require.Contains(t, logs.String(), "database unavailable", "periodic task error was not reported")
		// After a failure the wait doubles: four seconds, give or take a tenth.
		waitPeriodicWithin(t, runner.seen, 3600*time.Millisecond, 4400*time.Millisecond)
		cancel()
		<-done
	})
}

func TestPeriodicWaitDoublesUpToTenIntervals(t *testing.T) {
	t.Parallel()
	for failures, base := range map[int]time.Duration{0: time.Second, 1: 2 * time.Second, 3: 8 * time.Second, 4: 10 * time.Second, 20: 10 * time.Second} {
		for range 100 {
			wait := periodicWait(time.Second, failures)
			require.GreaterOrEqual(t, wait, time.Duration(float64(base)*0.9), "failures %d", failures)
			require.LessOrEqual(t, wait, time.Duration(float64(base)*1.1), "failures %d", failures)
		}
	}
}

func TestEngineOwnsPeriodicTaskLifecycleAndCopiesDefinitions(t *testing.T) {
	t.Parallel()
	synctest.Test(t, func(t *testing.T) {
		original := &periodicTestRunner{seen: make(chan struct{}, 1)}
		replacement := &periodicTestRunner{seen: make(chan struct{}, 1)}
		tasks := []PeriodicTask{{Name: "scan", Interval: 2 * time.Second, Runner: original}}
		descriptor := testDescriptor(handlerFunc(func(context.Context, Execution) Outcome { return succeededOutcome() }))
		engine, err := New(Dependencies{
			Store: &jobRunnerStoreFake{}, Descriptors: []Descriptor{descriptor}, NodeID: "node-a",
			Diagnostics: newDiscardLogger(t), Policy: Policy{BackstopInterval: time.Hour}, PeriodicTasks: tasks,
		})
		require.NoError(t, err)
		tasks[0] = PeriodicTask{Name: "replacement", Interval: time.Hour, Runner: replacement}
		err = engine.Start(context.Background())
		require.NoError(t, err)
		waitPeriodicWithin(t, original.seen, 0, 2*time.Second)
		select {
		case <-replacement.seen:
			require.FailNow(t, "Engine retained the caller-owned periodic task slice")
		default:
		}
		time.Sleep(1800*time.Millisecond - time.Nanosecond)
		synctest.Wait()
		require.Equal(t, 1, original.count(), "the periodic task ran before its jittered interval")
		err = engine.Close(context.Background())
		require.NoError(t, err)
		time.Sleep(time.Hour)
		synctest.Wait()
		require.Equal(t, 1, original.count(), "periodic task ran after Engine shutdown")
	})
}

// waitPeriodicWithin requires that the task runs again no sooner than
// earliest and no later than latest from now.
func waitPeriodicWithin(t *testing.T, seen <-chan struct{}, earliest, latest time.Duration) {
	t.Helper()
	if earliest > 0 {
		time.Sleep(earliest - time.Nanosecond)
		synctest.Wait()
		select {
		case <-seen:
			require.FailNow(t, "periodic task ran too early")
		default:
		}
	}
	deadline := time.Now().Add(latest - max(earliest-time.Nanosecond, 0))
	for time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
		synctest.Wait()
		select {
		case <-seen:
			return
		default:
		}
	}
	require.FailNow(t, "periodic task did not run in time")
}
