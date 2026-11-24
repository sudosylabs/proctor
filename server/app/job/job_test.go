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
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/sudosylabs/proctor/server/logging"
	"github.com/sudosylabs/proctor/server/model"
	"github.com/sudosylabs/proctor/server/store"
)

type jobRunnerStoreFake struct {
	mu              sync.Mutex
	heartbeats      int
	heartbeatErrors []error
	checkpoints     int
	checkpoint      *store.JobCheckpoint
	completion      *store.JobCompletion
	cancelRequested bool
	reservation     *store.JobWorkReservation
	claim           *store.JobClaim
	claimed         bool
	claimRequests   chan struct{}
	// heartbeated, when set, receives a signal after each recorded heartbeat.
	heartbeated chan struct{}
}

func (s *jobRunnerStoreFake) ReserveWork(_ context.Context, reservation *store.JobWorkReservation) (*store.JobWorkReservationResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	copy := *reservation
	s.reservation = &copy
	return &store.JobWorkReservationResult{Reserved: true, Consumed: reservation.Units}, nil
}

func (*jobRunnerStoreFake) Enqueue(context.Context, *store.JobEnqueue) (*model.Job, bool, error) {
	return nil, false, nil
}

func (*jobRunnerStoreFake) RecoverLostAttempts(context.Context, int) (int, error) {
	return 0, nil
}

// NextDue reports every type as due now, so the dispatcher signals its
// workers at every backstop, as the polling engine once claimed.
func (*jobRunnerStoreFake) NextDue(_ context.Context, types []model.JobType) (map[model.JobType]time.Time, error) {
	due := make(map[model.JobType]time.Time, len(types))
	for _, jobType := range types {
		due[jobType] = time.Now()
	}
	return due, nil
}

func (*jobRunnerStoreFake) QueueDepth(context.Context, []model.JobType) (map[model.JobType]store.JobQueueDepth, error) {
	return map[model.JobType]store.JobQueueDepth{}, nil
}

func (s *jobRunnerStoreFake) ClaimNext(context.Context, *store.JobClaimRequest) (*store.JobClaim, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.claimRequests != nil {
		select {
		case s.claimRequests <- struct{}{}:
		default:
		}
	}
	if s.claim == nil || s.claimed {
		return nil, store.NewErrNotFound("job", "claimable")
	}
	s.claimed = true
	return s.claim, nil
}

func (s *jobRunnerStoreFake) Heartbeat(_ context.Context, _ *store.JobHeartbeat) (*model.JobAttempt, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.heartbeats++
	if s.heartbeated != nil {
		select {
		case s.heartbeated <- struct{}{}:
		default:
		}
	}
	if len(s.heartbeatErrors) > 0 {
		err := s.heartbeatErrors[0]
		s.heartbeatErrors = s.heartbeatErrors[1:]
		if err != nil {
			return nil, err
		}
	}
	return &model.JobAttempt{}, nil
}

func (s *jobRunnerStoreFake) Checkpoint(_ context.Context, checkpoint *store.JobCheckpoint) (*model.Job, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	copy := *checkpoint
	s.checkpoint = &copy
	s.checkpoints++
	return &model.Job{}, nil
}

func (s *jobRunnerStoreFake) Complete(_ context.Context, completion *store.JobCompletion) (*model.Job, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	copy := *completion
	s.completion = &copy
	return &model.Job{}, nil
}

func (*jobRunnerStoreFake) Get(context.Context, model.JobID) (*model.Job, error) { return nil, nil }

func (*jobRunnerStoreFake) ListAttempts(context.Context, model.JobID) ([]model.JobAttempt, error) {
	return nil, nil
}

func (*jobRunnerStoreFake) Count(context.Context, store.JobListOptions, int) (int, error) {
	return 0, nil
}

func (*jobRunnerStoreFake) List(context.Context, store.JobListOptions) ([]*model.Job, error) {
	return nil, nil
}

func (*jobRunnerStoreFake) ListAttemptsPage(context.Context, store.JobAttemptListOptions) ([]model.JobAttempt, error) {
	return nil, nil
}

func (s *jobRunnerStoreFake) CancellationRequested(context.Context, model.JobAttemptID, model.JobClaimToken) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.cancelRequested, nil
}

func (*jobRunnerStoreFake) Cancel(context.Context, *store.JobMutation) (*model.Job, error) {
	return nil, nil
}

func (*jobRunnerStoreFake) Retry(context.Context, *store.JobMutation) (*model.Job, error) {
	return nil, nil
}

func (*jobRunnerStoreFake) DeleteTerminalHistory(context.Context, *store.JobHistoryCleanup) (*store.JobHistoryCleanupResult, error) {
	return nil, nil
}

type jobMetricsFake struct {
	mu         sync.Mutex
	activities []Activity
	started    int
	finished   int
	outcomes   []ExecutionOutcome
}

func (r *jobMetricsFake) Started(model.JobType) {
	r.mu.Lock()
	r.started++
	r.mu.Unlock()
}

func (r *jobMetricsFake) Finished(_ model.JobType, outcome ExecutionOutcome, _ time.Duration) {
	r.mu.Lock()
	r.finished++
	r.outcomes = append(r.outcomes, outcome)
	r.mu.Unlock()
}

func (r *jobMetricsFake) Record(activity Activity) {
	r.mu.Lock()
	r.activities = append(r.activities, activity)
	r.mu.Unlock()
}

type handlerFunc func(context.Context, Execution) Outcome

func (f handlerFunc) Run(ctx context.Context, execution Execution) Outcome {
	return f(ctx, execution)
}

// settableClock is a clock the test moves by hand.
type settableClock struct {
	mu  sync.Mutex
	now time.Time
}

func newSettableClock(at time.Time) *settableClock { return &settableClock{now: at} }

func (c *settableClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *settableClock) set(at time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = at
}

// bubbleClock reads at plus the time elapsed in the test's synctest bubble
// since it was made, so an engine started in the bubble begins at a chosen
// instant and its real timers move it forward.
func bubbleClock(at time.Time) func() time.Time {
	start := time.Now()
	return func() time.Time { return at.Add(time.Since(start)) }
}

// newTestLogger returns a real logger writing JSON records to a buffer the
// test may read.
func newTestLogger(tb testing.TB) (*logging.Logger, *logging.Buffer) {
	tb.Helper()
	var logs logging.Buffer
	logger, err := logging.New()
	require.NoError(tb, err)
	tb.Cleanup(func() { _ = logger.Shutdown() })
	require.NoError(tb, logger.Configure(logging.Config{
		MaxFieldBytes: 1024,
		Targets: []logging.Target{{
			Name: "test", Type: "console", Level: "debug", Format: "json", Writer: &logs,
		}},
	}))
	return logger, &logs
}

// newDiscardLogger returns a real logger whose records the test never reads.
func newDiscardLogger(tb testing.TB) *logging.Logger {
	tb.Helper()
	logger, _ := newTestLogger(tb)
	return logger
}

func testDescriptor(handler Handler) Descriptor {
	return Descriptor{Type: model.JobTypeProfilePictureGenerateDefault, PublicErrorCodes: []string{"dependency.unavailable", "job.command.invalid", "job.invariant_failed", "user.not_found"}, Timeout: 100 * time.Millisecond, Concurrency: 1, LeaseDuration: 50 * time.Millisecond, HeartbeatInterval: 5 * time.Millisecond, BaseRetryDelay: time.Millisecond, MaximumRetryDelay: time.Second, Visibility: VisibilityOperator, SuccessRetention: 24 * time.Hour, FailureRetention: 48 * time.Hour, Handler: handler}
}

func jobRunnerClaim(t *testing.T) *store.JobClaim {
	t.Helper()
	at := time.Now()
	job, err := model.NewJob(model.NewJobID(), model.JobTypeProfilePictureGenerateDefault, []byte(`{}`), "test", at, at, 8)
	require.NoError(t, err)
	running, err := job.Start(at)
	require.NoError(t, err)
	token, err := model.NewJobClaimToken()
	require.NoError(t, err)
	attempt, err := model.NewJobAttempt(model.NewJobAttemptID(), job.ID, 1, "node-a", token, at, at.Add(time.Minute))
	require.NoError(t, err)
	return &store.JobClaim{Job: running, Attempt: attempt}
}

func succeededOutcome() Outcome {
	return Outcome{Kind: OutcomeSucceeded, Result: json.RawMessage(`{}`)}
}

func newTestEngine(persistence store.JobStore, registry *Registry, nodeID string, diagnostics *logging.Logger, poll time.Duration) (*Engine, error) {
	descriptors := make([]Descriptor, 0, len(registry.types))
	for _, jobType := range registry.types {
		descriptors = append(descriptors, registry.descriptors[jobType])
	}
	return New(Dependencies{Store: persistence, Descriptors: descriptors, NodeID: nodeID, Diagnostics: diagnostics, Policy: Policy{BackstopInterval: poll}})
}
