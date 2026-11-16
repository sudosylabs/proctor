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
	"fmt"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type occurrenceProposerFake struct {
	calls chan time.Time
	mu    sync.Mutex
	fail  int
}

func (f *occurrenceProposerFake) Propose(_ context.Context, at time.Time) error {
	f.calls <- at
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.fail > 0 {
		f.fail--
		return errors.New("database unavailable")
	}
	return nil
}

type noOpProposer struct{}

func (noOpProposer) Propose(context.Context, time.Time) error { return nil }

func TestEngineValidatesRecurrencesAtConstruction(t *testing.T) {
	t.Parallel()
	descriptor := testDescriptor(handlerFunc(func(context.Context, Execution) Outcome { return succeededOutcome() }))
	config := Dependencies{
		Store: &jobRunnerStoreFake{}, Descriptors: []Descriptor{descriptor}, NodeID: "node-a",
		Diagnostics: newDiscardLogger(t), Policy: Policy{PollInterval: time.Second},
		Recurrences: []Recurrence{{Name: "daily", Proposer: noOpProposer{}}, {Name: "daily", Proposer: noOpProposer{}}},
	}
	_, err := New(config)
	require.Error(t, err, "New() accepted duplicate recurrence names")
	config.Recurrences = []Recurrence{{Name: "", Proposer: noOpProposer{}}}
	_, err = New(config)
	require.Error(t, err, "New() accepted an unnamed recurrence")
	config.Recurrences = []Recurrence{{Name: "unsafe\nname", Proposer: noOpProposer{}}}
	_, err = New(config)
	require.Error(t, err, "New() accepted an unsafe recurrence name")
	config.Recurrences = []Recurrence{{Name: "daily"}}
	_, err = New(config)
	require.Error(t, err, "New() accepted a recurrence without a proposer")
}

func TestEngineRetriesSameOccurrenceWakesLocallyAndStopsRecurrence(t *testing.T) {
	t.Parallel()
	synctest.Test(t, func(t *testing.T) {
		at := time.Date(2026, time.August, 10, 9, 30, 0, 0, time.UTC)
		proposer := &occurrenceProposerFake{calls: make(chan time.Time, 3), fail: 1}
		descriptor := testDescriptor(handlerFunc(func(context.Context, Execution) Outcome { return succeededOutcome() }))
		persistence := &jobRunnerStoreFake{claimRequests: make(chan struct{}, 8)}
		engine, err := New(Dependencies{
			Store: persistence, Descriptors: []Descriptor{descriptor}, NodeID: "node-a", Diagnostics: newDiscardLogger(t),
			Policy: Policy{PollInterval: time.Hour, ProposalRetryDelay: 10 * time.Second}, Now: bubbleClock(at),
			Recurrences: []Recurrence{{Name: "daily", Proposer: proposer}},
		})
		require.NoError(t, err)
		err = engine.Start(context.Background())
		require.NoError(t, err)
		first := nextProposal(t, proposer.calls)
		require.True(t, first.Equal(at), "first occurrence=%v", first)
		// The worker polls once on startup. A failed proposal must not add a wake.
		require.Len(t, persistence.claimRequests, 1, "worker initial claims")
		<-persistence.claimRequests
		noProposalAfter(t, proposer.calls, 10*time.Second-time.Nanosecond, "the failed proposal was retried before its delay")
		require.Empty(t, persistence.claimRequests, "failed proposal woke the local worker")
		time.Sleep(time.Nanosecond)
		second := nextProposal(t, proposer.calls)
		require.True(t, second.Equal(at), "retry occurrence=%v", second)
		require.Len(t, persistence.claimRequests, 1, "successful proposal did not wake the local worker")
		<-persistence.claimRequests
		noProposalAfter(t, proposer.calls, 14*time.Hour+29*time.Minute+50*time.Second-time.Nanosecond, "the next daily occurrence was proposed early")
		time.Sleep(time.Nanosecond)
		daily := nextProposal(t, proposer.calls)
		require.True(t, daily.Equal(nextDailyOccurrence(at)), "daily occurrence=%v", daily)
		err = engine.Close(context.Background())
		require.NoError(t, err)
		noProposalAfter(t, proposer.calls, 48*time.Hour, "proposal ran after engine shutdown")
	})
}
func TestEngineCopiesRecurrenceDefinitionsAtConstruction(t *testing.T) {
	t.Parallel()
	synctest.Test(t, func(t *testing.T) {
		at := time.Date(2026, time.August, 10, 9, 30, 0, 0, time.UTC)
		original := &occurrenceProposerFake{calls: make(chan time.Time, 1)}
		replacement := &occurrenceProposerFake{calls: make(chan time.Time, 1)}
		recurrences := []Recurrence{{Name: "daily", Proposer: original}}
		descriptor := testDescriptor(handlerFunc(func(context.Context, Execution) Outcome { return succeededOutcome() }))
		engine, err := New(Dependencies{
			Store: &jobRunnerStoreFake{}, Descriptors: []Descriptor{descriptor}, NodeID: "node-a", Diagnostics: newDiscardLogger(t),
			Policy: Policy{PollInterval: time.Hour}, Now: bubbleClock(at), Recurrences: recurrences,
		})
		require.NoError(t, err)
		recurrences[0] = Recurrence{Name: "replacement", Proposer: replacement}
		err = engine.Start(context.Background())
		require.NoError(t, err)
		nextProposal(t, original.calls)
		require.Empty(t, replacement.calls, "engine retained caller-owned recurrence slice")
		err = engine.Close(context.Background())
		require.NoError(t, err)
	})
}
func TestConcurrentEnginesAndRestartProposeTheSameOccurrence(t *testing.T) {
	t.Parallel()
	at := time.Date(2026, time.August, 10, 9, 30, 0, 0, time.UTC)
	proposer := &occurrenceProposerFake{calls: make(chan time.Time, 3)}
	descriptor := testDescriptor(handlerFunc(func(context.Context, Execution) Outcome { return succeededOutcome() }))
	newEngine := func(nodeID string) *Engine {
		t.Helper()
		engine, err := New(Dependencies{
			Store: &jobRunnerStoreFake{}, Descriptors: []Descriptor{descriptor}, NodeID: nodeID, Diagnostics: newDiscardLogger(t),
			Policy: Policy{PollInterval: time.Hour}, Now: func() time.Time { return at },
			Recurrences: []Recurrence{{Name: "daily", Proposer: proposer}},
		})
		require.NoError(t, err)
		return engine
	}

	first, second := newEngine("node-a"), newEngine("node-b")
	require.NoError(t, first.Start(context.Background()))
	require.NoError(t, second.Start(context.Background()))
	for range 2 {
		occurrence := <-proposer.calls
		require.True(t, occurrence.Equal(at), "concurrent occurrence=%v", occurrence)
	}
	require.NoError(t, first.Close(context.Background()))
	require.NoError(t, second.Close(context.Background()))

	restarted := newEngine("node-a-restarted")
	require.NoError(t, restarted.Start(context.Background()))
	occurrence := <-proposer.calls
	require.True(t, occurrence.Equal(at), "restart occurrence=%v", occurrence)
	require.NoError(t, restarted.Close(context.Background()))
}

func TestNextDailyOccurrenceUsesUTCDayBoundary(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, time.August, 10, 23, 59, 0, 0, time.FixedZone("offset", 2*60*60))
	want := time.Date(2026, time.August, 11, 0, 0, 0, 0, time.UTC)
	got := nextDailyOccurrence(now)
	require.True(t, got.Equal(want), "next occurrence = %v", got)
}

func TestRecurrenceIntervalRetriesSameBucketThenAdvancesAtUTCBoundary(t *testing.T) {
	t.Parallel()
	synctest.Test(t, func(t *testing.T) {
		at := time.Date(2026, time.September, 7, 13, 57, 0, 0, time.FixedZone("offset", 2*60*60))
		proposer := &occurrenceProposerFake{calls: make(chan time.Time, 4), fail: 1}
		ctx, cancel := context.WithCancel(context.Background())
		done := make(chan struct{})
		logger := newDiscardLogger(t)
		go func() {
			defer close(done)
			runRecurrenceProposal(ctx, Recurrence{Name: "hourly", Interval: time.Hour, Proposer: proposer}, logger, bubbleClock(at), 5*time.Minute, nil, nil)
		}()
		defer func() { cancel(); <-done }()
		require.True(t, nextProposal(t, proposer.calls).Equal(at))
		noProposalAfter(t, proposer.calls, 5*time.Minute-time.Nanosecond, "the retry ran before its delay")
		// A transient failure crossing noon must first finish the old occurrence.
		time.Sleep(time.Nanosecond)
		require.True(t, nextProposal(t, proposer.calls).Equal(at))
		require.True(t, nextProposal(t, proposer.calls).Equal(at.Add(5*time.Minute)))
		noProposalAfter(t, proposer.calls, 58*time.Minute-time.Nanosecond, "the next bucket was proposed early")
		time.Sleep(time.Nanosecond)
		got := nextProposal(t, proposer.calls)
		require.Equal(t, 13, got.Hour())
		require.Same(t, time.UTC, got.Location())
	})
}

// nextProposal returns the proposal made by the time every goroutine in the
// bubble is waiting again.
func nextProposal(t *testing.T, calls <-chan time.Time) time.Time {
	t.Helper()
	synctest.Wait()
	select {
	case occurrence := <-calls:
		return occurrence
	default:
		require.FailNow(t, "no occurrence was proposed")
		return time.Time{}
	}
}

// noProposalAfter lets wait pass in the bubble and requires that nothing
// was proposed meanwhile.
func noProposalAfter(t *testing.T, calls <-chan time.Time, wait time.Duration, message string) {
	t.Helper()
	time.Sleep(wait)
	synctest.Wait()
	select {
	case occurrence := <-calls:
		require.FailNow(t, fmt.Sprintf("%s: %v", message, occurrence))
	default:
	}
}
func TestRecurrenceIntervalsAreBoundedAndAlignAcrossNodes(t *testing.T) {
	t.Parallel()
	for _, interval := range []time.Duration{-time.Hour, time.Second, 7 * time.Minute, 25 * time.Hour} {
		_, err := cloneRecurrences([]Recurrence{{Name: "invalid", Interval: interval, Proposer: noOpProposer{}}})
		assert.Error(t, err, "accepted interval %s", interval)
	}
	for _, interval := range []time.Duration{0, time.Minute, 10 * time.Minute, time.Hour, 24 * time.Hour} {
		_, err := cloneRecurrences([]Recurrence{{Name: "valid", Interval: interval, Proposer: noOpProposer{}}})
		require.NoError(t, err)
	}
	first := time.Date(2026, time.September, 7, 12, 31, 0, 0, time.UTC)
	second := first.Add(4 * time.Minute).In(time.FixedZone("offset", -5*60*60))
	want := time.Date(2026, time.September, 7, 12, 40, 0, 0, time.UTC)
	for _, now := range []time.Time{first, second} {
		got := nextRecurrenceOccurrence(now, 10*time.Minute)
		require.True(t, got.Equal(want), "boundary = %v", got)
	}
}
