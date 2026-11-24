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

func TestRunPeriodicTaskRunsImmediatelyAndOnEachInterval(t *testing.T) {
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

		waitPeriodicCall(t, runner.seen)
		waitPeriodicInterval(t, runner.seen, 2*time.Second)
		waitPeriodicInterval(t, runner.seen, 2*time.Second)
		cancel()
		<-done
		require.Equal(t, 3, runner.count(), "Run() calls")
	})
}

func TestRunPeriodicTaskReportsErrorAndContinues(t *testing.T) {
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
		waitPeriodicCall(t, runner.seen)
		require.Contains(t, logs.String(), "run periodic runtime task scan", "periodic task error was not reported")
		require.Contains(t, logs.String(), "database unavailable", "periodic task error was not reported")
		waitPeriodicInterval(t, runner.seen, 2*time.Second)
		cancel()
		<-done
	})
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
		waitPeriodicCall(t, original.seen)
		select {
		case <-replacement.seen:
			require.FailNow(t, "Engine retained the caller-owned periodic task slice")
		default:
		}
		time.Sleep(2*time.Second - time.Nanosecond)
		synctest.Wait()
		require.Equal(t, 1, original.count(), "the periodic task ran before its interval")
		err = engine.Close(context.Background())
		require.NoError(t, err)
		time.Sleep(time.Hour)
		synctest.Wait()
		require.Equal(t, 1, original.count(), "periodic task ran after Engine shutdown")
	})
}

// waitPeriodicCall requires that the task ran once by the time every
// goroutine in the bubble is waiting again.
func waitPeriodicCall(t *testing.T, seen <-chan struct{}) {
	t.Helper()
	synctest.Wait()
	select {
	case <-seen:
	default:
		require.FailNow(t, "periodic task did not run")
	}
}

// waitPeriodicInterval requires that the task runs again exactly when the
// interval has passed, and not a moment before.
func waitPeriodicInterval(t *testing.T, seen <-chan struct{}, interval time.Duration) {
	t.Helper()
	time.Sleep(interval - time.Nanosecond)
	synctest.Wait()
	select {
	case <-seen:
		require.FailNow(t, "periodic task ran before its interval")
	default:
	}
	time.Sleep(time.Nanosecond)
	waitPeriodicCall(t, seen)
}
