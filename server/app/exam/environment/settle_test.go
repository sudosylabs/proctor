// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package environment

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/sudosylabs/proctor/server/model"
	"github.com/sudosylabs/proctor/server/store"
)

// settleRow is one environment's settle counters in settleStoreFake.
type settleRow struct {
	requested, completed int64
	requestedAt          time.Time
}

// settleStoreFake keeps settle counters per Attempt, counts every call, and
// records gaps.
type settleStoreFake struct {
	store.AttemptEnvironmentStore
	mu       sync.Mutex
	rows     map[model.ExamAttemptID]*settleRow
	gets     int
	requests int
	gaps     []store.AttemptEnvironmentSettleGap
	// onGet runs after each read, outside the lock.
	onGet func(model.ExamAttemptID)
}

func newSettleStoreFake() *settleStoreFake {
	return &settleStoreFake{rows: map[model.ExamAttemptID]*settleRow{}}
}

func (f *settleStoreFake) add(attemptID model.ExamAttemptID, row settleRow) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.rows[attemptID] = &row
}

func (f *settleStoreFake) RequestSettle(_ context.Context, attemptID model.ExamAttemptID) (int64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.requests++
	row := f.rows[attemptID]
	if row == nil {
		return 0, nil
	}
	row.requested++
	row.requestedAt = time.Now()
	return row.requested, nil
}

func (f *settleStoreFake) Get(_ context.Context, attemptID model.ExamAttemptID) (*model.AttemptEnvironment, error) {
	f.mu.Lock()
	f.gets++
	row := f.rows[attemptID]
	onGet := f.onGet
	var environment *model.AttemptEnvironment
	if row != nil {
		environment = &model.AttemptEnvironment{AttemptID: attemptID, SettleRequested: row.requested,
			SettleCompleted: row.completed, SettleRequestedAt: row.requestedAt}
	}
	f.mu.Unlock()
	if onGet != nil {
		onGet(attemptID)
	}
	if environment == nil {
		return nil, store.NewErrNotFound("attempt_environment", attemptID.String())
	}
	return environment, nil
}

func (f *settleStoreFake) RecordSettleIncomplete(_ context.Context, gap *store.AttemptEnvironmentSettleGap) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	row := f.rows[gap.AttemptID]
	if row.completed >= gap.Settle {
		return nil
	}
	f.gaps = append(f.gaps, *gap)
	row.completed = gap.Settle
	return nil
}

// complete plays the owner finishing every settle asked of attemptID.
func (f *settleStoreFake) complete(attemptID model.ExamAttemptID) {
	f.mu.Lock()
	defer f.mu.Unlock()
	row := f.rows[attemptID]
	row.completed = row.requested
}

func (f *settleStoreFake) counts() (gets, requests int, gaps []store.AttemptEnvironmentSettleGap) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.gets, f.requests, append([]store.AttemptEnvironmentSettleGap(nil), f.gaps...)
}

// newSettles returns the settle signals a node builds. They are never
// started, so a settle signals this node's waiters and reaches no peer.
func newSettles(t *testing.T) *ClusterSettles {
	t.Helper()
	settles, err := newClusterSettles(&wakeTransportFake{}, nil)
	require.NoError(t, err)
	return settles
}

// signalingWaker plays an owner that settles and signals once it is woken,
// from its own goroutine as a remote owner would.
type signalingWaker struct {
	environments *settleStoreFake
	signals      *ClusterSettles
}

func (w signalingWaker) Wake(attemptID model.ExamAttemptID) {
	go func() {
		w.environments.complete(attemptID)
		w.signals.Settled(attemptID)
	}()
}

func TestSettlerWaitsForTheOwnersSignalWithoutPolling(t *testing.T) {
	t.Parallel()
	attemptID := model.NewExamAttemptID()
	environments, signals := newSettleStoreFake(), newSettles(t)
	environments.add(attemptID, settleRow{})
	settler, err := NewSettler(SettlerDependencies{Environments: environments, Signals: signals,
		Waker: signalingWaker{environments, signals}, Budget: time.Hour, NewEvidenceID: model.NewIntegrityEvidenceID})
	require.NoError(t, err)

	settler.Settle(t.Context(), attemptID)
	gets, requests, gaps := environments.counts()
	require.Empty(t, gaps, "an owner that settles in time leaves no gap")
	require.Equal(t, 1, requests)
	require.LessOrEqual(t, gets, 2, "the row is read once after the wake and once on the signal, never on a timer")
}

func TestSettlerRecordsTheGapOfAnOwnerThatNeverAnswers(t *testing.T) {
	t.Parallel()
	attemptID := model.NewExamAttemptID()
	environments := newSettleStoreFake()
	environments.add(attemptID, settleRow{})
	silent, err := NewSettler(SettlerDependencies{Environments: environments, Signals: newSettles(t),
		Budget: 50 * time.Millisecond, NewEvidenceID: model.NewIntegrityEvidenceID})
	require.NoError(t, err)

	started := time.Now()
	silent.Settle(t.Context(), attemptID)
	require.GreaterOrEqual(t, time.Since(started), 50*time.Millisecond, "the budget is spent")
	gets, _, gaps := environments.counts()
	require.Equal(t, 1, gets, "the wait reads the row once, not on a timer")
	require.Len(t, gaps, 1)
	require.Equal(t, int64(1), gaps[0].Settle)
	require.Equal(t, attemptID, gaps[0].AttemptID)

	// An Attempt without an environment settles at once.
	silent.Settle(t.Context(), model.NewExamAttemptID())
	_, _, gaps = environments.counts()
	require.Len(t, gaps, 1)

	_, err = NewSettler(SettlerDependencies{Environments: environments, Signals: newSettles(t)})
	require.Error(t, err, "an evidence ID source is required")
	_, err = NewSettler(SettlerDependencies{Environments: environments, NewEvidenceID: model.NewIntegrityEvidenceID})
	require.Error(t, err, "the settle signals are required")
}

func TestSettlerAwaitsAPendingSettleWithoutAskingAnother(t *testing.T) {
	t.Parallel()
	environments, signals := newSettleStoreFake(), newSettles(t)
	settler, err := NewSettler(SettlerDependencies{Environments: environments, Signals: signals, Budget: time.Hour,
		NewEvidenceID: model.NewIntegrityEvidenceID})
	require.NoError(t, err)

	// Nothing pending returns at once.
	settled := model.NewExamAttemptID()
	environments.add(settled, settleRow{requested: 1, completed: 1, requestedAt: time.Now()})
	settler.AwaitPendingSettle(t.Context(), settled)
	settler.AwaitPendingSettle(t.Context(), model.NewExamAttemptID())

	// A pending settle the owner completes within its budget leaves no gap.
	pending := model.NewExamAttemptID()
	environments.add(pending, settleRow{requested: 1, requestedAt: time.Now()})
	environments.mu.Lock()
	environments.onGet = func(attemptID model.ExamAttemptID) {
		if attemptID == pending {
			go func() {
				environments.complete(pending)
				signals.Settled(pending)
			}()
		}
	}
	environments.mu.Unlock()
	settler.AwaitPendingSettle(t.Context(), pending)

	_, requests, gaps := environments.counts()
	require.Zero(t, requests, "the automatic seal asks for no settle")
	require.Empty(t, gaps)
}

func TestSettlerBoundsASittingCloseOfUnreachableHosts(t *testing.T) {
	t.Parallel()
	// Every environment of the Sitting was asked to settle when it began
	// closing, the sealing Job waited the budget, and no owner answered.
	// Each target finds its budget spent: the gap is recorded without a
	// wait, so the close takes no budget per Attempt.
	environments := newSettleStoreFake()
	asked := time.Now().Add(-2 * DefaultSettleBudget)
	targets := make([]model.ExamAttemptID, 200)
	for index := range targets {
		targets[index] = model.NewExamAttemptID()
		environments.add(targets[index], settleRow{requested: 1, requestedAt: asked})
	}
	settler, err := NewSettler(SettlerDependencies{Environments: environments, Signals: newSettles(t),
		NewEvidenceID: model.NewIntegrityEvidenceID})
	require.NoError(t, err)

	started := time.Now()
	for _, attemptID := range targets {
		settler.AwaitPendingSettle(t.Context(), attemptID)
	}
	require.Less(t, time.Since(started), DefaultSettleBudget, "the close is bounded by no budget per Attempt")
	_, requests, gaps := environments.counts()
	require.Zero(t, requests)
	require.Len(t, gaps, len(targets), "every unsettled environment has its gap")
	for index, gap := range gaps {
		require.Equal(t, targets[index], gap.AttemptID)
		require.Equal(t, int64(1), gap.Settle)
	}
}
