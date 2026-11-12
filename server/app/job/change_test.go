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
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/sudosylabs/proctor/server/model"
	"github.com/sudosylabs/proctor/server/store"
)

func TestProgressCoalescerAdmitsOncePerWindow(t *testing.T) {
	t.Parallel()

	clock := newManualClock(time.Date(2026, time.August, 1, 9, 0, 0, 0, time.UTC))
	coalescer := newProgressCoalescer(2*time.Second, clock.Now)
	id := model.NewJobID()

	require.True(t, coalescer.admit(id), "the first checkpoint is announced")
	require.False(t, coalescer.admit(id), "a checkpoint at the same instant is coalesced")
	clock.set(clock.Now().Add(1999 * time.Millisecond))
	require.False(t, coalescer.admit(id), "a checkpoint inside the window is coalesced")
	clock.set(clock.Now().Add(time.Millisecond))
	require.True(t, coalescer.admit(id), "the window has passed")
	require.False(t, coalescer.admit(id), "the admitted checkpoint started a new window")
}

func TestProgressCoalescerForgetClearsTheWindow(t *testing.T) {
	t.Parallel()

	clock := newManualClock(time.Date(2026, time.August, 1, 9, 0, 0, 0, time.UTC))
	coalescer := newProgressCoalescer(2*time.Second, clock.Now)
	id := model.NewJobID()

	require.True(t, coalescer.admit(id))
	coalescer.forget(id)
	require.True(t, coalescer.admit(id), "a status change clears the window")
	coalescer.forget(id)
	require.Empty(t, coalescer.last, "a forgotten Job holds no entry")
}

func TestProgressCoalescerKeepsJobsIndependent(t *testing.T) {
	t.Parallel()

	clock := newManualClock(time.Date(2026, time.August, 1, 9, 0, 0, 0, time.UTC))
	coalescer := newProgressCoalescer(2*time.Second, clock.Now)
	first, second := model.NewJobID(), model.NewJobID()

	require.True(t, coalescer.admit(first))
	require.True(t, coalescer.admit(second), "another Job has its own window")
	require.False(t, coalescer.admit(first))
	coalescer.forget(second)
	require.False(t, coalescer.admit(first), "forgetting one Job leaves the other's window")
	require.True(t, coalescer.admit(second))
}

// Three checkpoints inside one window announce the first only, then the
// completion is announced at once with the status the Store returned.
func TestEngineAnnouncesStatusAlwaysAndProgressCoalesced(t *testing.T) {
	t.Parallel()

	claim := jobRunnerClaim(t)
	clock := newManualClock(time.Date(2026, time.August, 1, 9, 0, 0, 0, time.UTC))
	descriptor := testDescriptor(handlerFunc(func(ctx context.Context, execution Execution) Outcome {
		for current := int64(1); current <= 3; current++ {
			if current == 3 {
				clock.set(clock.Now().Add(progressWindow))
			}
			value := CheckpointValue{
				Progress: &model.JobProgress{Current: current, Total: 4, Stage: "rendering"},
				Document: json.RawMessage(`{"cursor":"private"}`),
			}
			if err := execution.Checkpoint(ctx, value); err != nil {
				return RetryableFailure("dependency.unavailable", err)
			}
		}
		return succeededOutcome()
	}))
	descriptor.Checkpoints = true
	descriptor.ProgressStages = []string{"rendering"}
	persistence := &changingStoreFake{jobRunnerStoreFake: &jobRunnerStoreFake{}, job: claim.Job}
	listener := &changeRecorder{}
	engine := newChangeTestEngine(t, persistence, descriptor, clock, listener)

	engine.executeClaim(context.Background(), descriptor, claim)

	progress := func(current int64) *model.JobProgress {
		return &model.JobProgress{Current: current, Total: 4, Stage: "rendering"}
	}
	require.Equal(t, []Change{
		{ID: claim.Job.ID, Type: claim.Job.Type, Status: model.JobStatusRunning, Progress: progress(1)},
		{ID: claim.Job.ID, Type: claim.Job.Type, Status: model.JobStatusRunning, Progress: progress(3)},
		{ID: claim.Job.ID, Type: claim.Job.Type, Status: model.JobStatusSucceeded, Progress: progress(3)},
	}, listener.recorded())
	require.Empty(t, engine.progress.last, "the ended execution holds no window")
}

func TestEngineAnnouncesNothingWhenTheStoreRefuses(t *testing.T) {
	t.Parallel()

	claim := jobRunnerClaim(t)
	clock := newManualClock(time.Date(2026, time.August, 1, 9, 0, 0, 0, time.UTC))
	var checkpointErr error
	descriptor := testDescriptor(handlerFunc(func(ctx context.Context, execution Execution) Outcome {
		checkpointErr = execution.Checkpoint(ctx, CheckpointValue{
			Progress: &model.JobProgress{Current: 1, Total: 2, Stage: "rendering"},
			Document: json.RawMessage(`{}`),
		})
		return succeededOutcome()
	}))
	descriptor.Checkpoints = true
	descriptor.ProgressStages = []string{"rendering"}
	persistence := &changingStoreFake{jobRunnerStoreFake: &jobRunnerStoreFake{}, job: claim.Job, err: errors.New("fence lost")}
	listener := &changeRecorder{}
	engine := newChangeTestEngine(t, persistence, descriptor, clock, listener)

	engine.executeClaim(context.Background(), descriptor, claim)

	require.Error(t, checkpointErr)
	require.Empty(t, listener.recorded())
}

func TestEngineAnnouncesOnlyOperatorVisibleJobsAndProgressOnlyWhereDeclared(t *testing.T) {
	t.Parallel()

	clock := newManualClock(time.Date(2026, time.August, 1, 9, 0, 0, 0, time.UTC))
	succeed := handlerFunc(func(context.Context, Execution) Outcome { return succeededOutcome() })

	domain := testDescriptor(succeed)
	domain.Visibility = VisibilityDomain
	claim := jobRunnerClaim(t)
	listener := &changeRecorder{}
	engine := newChangeTestEngine(t, &changingStoreFake{jobRunnerStoreFake: &jobRunnerStoreFake{}, job: claim.Job}, domain, clock, listener)
	engine.executeClaim(context.Background(), domain, claim)
	require.Empty(t, listener.recorded(), "a domain Job is not announced")

	// A type without progress stages never announces progress, even when
	// the stored Job carries some.
	claim = jobRunnerClaim(t)
	claim.Job.Progress = &model.JobProgress{Current: 1, Total: 2, Stage: "hidden"}
	listener = &changeRecorder{}
	operator := testDescriptor(succeed)
	engine = newChangeTestEngine(t, &changingStoreFake{jobRunnerStoreFake: &jobRunnerStoreFake{}, job: claim.Job}, operator, clock, listener)
	engine.executeClaim(context.Background(), operator, claim)
	require.Equal(t, []Change{{ID: claim.Job.ID, Type: claim.Job.Type, Status: model.JobStatusSucceeded}}, listener.recorded())
}

func TestEngineAnnouncesTheClaimAndOperatorControls(t *testing.T) {
	t.Parallel()

	claim := jobRunnerClaim(t)
	descriptor := testDescriptor(handlerFunc(func(context.Context, Execution) Outcome { return succeededOutcome() }))
	descriptor.Cancelable = true
	descriptor.ExplicitRetryStatuses = []model.JobStatus{model.JobStatusFailed}
	persistence := &changingStoreFake{jobRunnerStoreFake: &jobRunnerStoreFake{claim: claim}, job: claim.Job}
	listener := &changeRecorder{}
	engine := newChangeTestEngine(t, persistence, descriptor, newManualClock(time.Now()), listener)
	require.NoError(t, engine.Start(context.Background()))
	engine.Wake()
	require.Eventually(t, func() bool { return len(listener.recorded()) == 2 }, time.Second, time.Millisecond)
	require.NoError(t, engine.Close(context.Background()))
	require.Equal(t, []Change{
		{ID: claim.Job.ID, Type: claim.Job.Type, Status: model.JobStatusRunning},
		{ID: claim.Job.ID, Type: claim.Job.Type, Status: model.JobStatusSucceeded},
	}, listener.recorded())

	at := time.Date(2026, time.August, 11, 8, 0, 0, 0, time.UTC)
	queued, err := model.NewJob(model.NewJobID(), descriptor.Type, json.RawMessage(`{}`), "control", at, at, 8)
	require.NoError(t, err)
	operator := &operatorStoreFake{jobRunnerStoreFake: &jobRunnerStoreFake{}, job: queued}
	listener = &changeRecorder{}
	engine = newChangeTestEngine(t, operator, descriptor, newManualClock(at), listener)
	audit := AuditReference{EventID: model.NewAuditEventID(), At: at}
	target, err := engine.PrepareCancellation(context.Background(), queued.ID)
	require.NoError(t, err)
	_, err = engine.Cancel(context.Background(), target, audit)
	require.NoError(t, err)

	running, err := queued.Start(at.Add(time.Second))
	require.NoError(t, err)
	operator.job, err = running.Fail("job.failed", at.Add(2*time.Second))
	require.NoError(t, err)
	target, err = engine.PrepareRetry(context.Background(), queued.ID)
	require.NoError(t, err)
	_, err = engine.Retry(context.Background(), target, audit)
	require.NoError(t, err)

	require.Equal(t, []Change{
		{ID: queued.ID, Type: queued.Type, Status: model.JobStatusCanceled},
		{ID: queued.ID, Type: queued.Type, Status: model.JobStatusQueued},
	}, listener.recorded())
}

func TestAttachChangeListenerOnlyOnceBeforeStart(t *testing.T) {
	t.Parallel()

	descriptor := testDescriptor(handlerFunc(func(context.Context, Execution) Outcome { return succeededOutcome() }))
	engine, err := New(Dependencies{
		Store: &jobRunnerStoreFake{}, Descriptors: []Descriptor{descriptor}, NodeID: "node-a",
		Diagnostics: newDiscardLogger(t), Policy: Policy{PollInterval: time.Hour},
	})
	require.NoError(t, err)
	require.Error(t, engine.AttachChangeListener(nil))
	require.NoError(t, engine.AttachChangeListener(&changeRecorder{}))
	require.Error(t, engine.AttachChangeListener(&changeRecorder{}), "a second listener")

	started, err := New(Dependencies{
		Store: &jobRunnerStoreFake{}, Descriptors: []Descriptor{descriptor}, NodeID: "node-a",
		Diagnostics: newDiscardLogger(t), Policy: Policy{PollInterval: time.Hour},
	})
	require.NoError(t, err)
	require.NoError(t, started.Start(context.Background()))
	defer func() { require.NoError(t, started.Close(context.Background())) }()
	require.Error(t, started.AttachChangeListener(&changeRecorder{}), "a listener after Start")
}

func newChangeTestEngine(t *testing.T, persistence store.JobStore, descriptor Descriptor, clock Clock, listener ChangeListener) *Engine {
	t.Helper()
	engine, err := New(Dependencies{
		Store: persistence, Descriptors: []Descriptor{descriptor}, NodeID: "node-a",
		Diagnostics: newDiscardLogger(t), Policy: Policy{PollInterval: time.Hour}, Clock: clock,
	})
	require.NoError(t, err)
	require.NoError(t, engine.AttachChangeListener(listener))
	return engine
}

// changingStoreFake answers checkpoints and completions with the Job as the
// Store would return it, or fails them all with err.
type changingStoreFake struct {
	*jobRunnerStoreFake
	mu  sync.Mutex
	job *model.Job
	err error
}

func (f *changingStoreFake) Checkpoint(ctx context.Context, checkpoint *store.JobCheckpoint) (*model.Job, error) {
	if _, err := f.jobRunnerStoreFake.Checkpoint(ctx, checkpoint); err != nil {
		return nil, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.err != nil {
		return nil, f.err
	}
	next := *f.job
	progress := *checkpoint.Progress
	next.Progress = &progress
	f.job = &next
	return &next, nil
}

func (f *changingStoreFake) Complete(ctx context.Context, completion *store.JobCompletion) (*model.Job, error) {
	if _, err := f.jobRunnerStoreFake.Complete(ctx, completion); err != nil {
		return nil, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.err != nil {
		return nil, f.err
	}
	next := *f.job
	next.Status = model.JobStatusSucceeded
	f.job = &next
	return &next, nil
}

type changeRecorder struct {
	mu      sync.Mutex
	changes []Change
}

func (r *changeRecorder) JobChanged(_ context.Context, change Change) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.changes = append(r.changes, change)
}

func (r *changeRecorder) recorded() []Change {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]Change(nil), r.changes...)
}

func (c *manualClock) set(at time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = at
}
