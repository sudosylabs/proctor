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

type leaseStoreFake struct {
	mu        sync.Mutex
	available []model.ExamAttemptID
	fence     int64
	held      map[model.ExamAttemptID]store.AttemptRuntimeLease
	released  []store.AttemptRuntimeLease
	lose      map[model.ExamAttemptID]bool
	renewals  int
}

func newLeaseStoreFake(available ...model.ExamAttemptID) *leaseStoreFake {
	return &leaseStoreFake{available: available, held: map[model.ExamAttemptID]store.AttemptRuntimeLease{},
		lose: map[model.ExamAttemptID]bool{}}
}

func (f *leaseStoreFake) claim(attemptID model.ExamAttemptID, nodeID string, lifetime time.Duration) *store.AttemptRuntimeLease {
	if _, ok := f.held[attemptID]; ok {
		return nil
	}
	f.fence++
	lease := store.AttemptRuntimeLease{AttemptID: attemptID, NodeID: nodeID, Fence: f.fence, ExpiresAt: time.Now().Add(lifetime)}
	f.held[attemptID] = lease
	return &lease
}

func (f *leaseStoreFake) Claim(_ context.Context, attemptID model.ExamAttemptID, nodeID string, lifetime time.Duration) (*store.AttemptRuntimeLease, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.claim(attemptID, nodeID, lifetime), nil
}

func (f *leaseStoreFake) ClaimAvailable(_ context.Context, nodeID string, lifetime time.Duration, limit int) ([]store.AttemptRuntimeLease, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var leases []store.AttemptRuntimeLease
	for _, attemptID := range f.available {
		if len(leases) == limit {
			break
		}
		if lease := f.claim(attemptID, nodeID, lifetime); lease != nil {
			leases = append(leases, *lease)
		}
	}
	return leases, nil
}

func (f *leaseStoreFake) Renew(_ context.Context, lease store.AttemptRuntimeLease, lifetime time.Duration) (*store.AttemptRuntimeLease, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.renewals++
	if f.lose[lease.AttemptID] {
		return nil, store.ErrAttemptRuntimeLeaseLost
	}
	lease.ExpiresAt = time.Now().Add(lifetime)
	return &lease, nil
}

func (f *leaseStoreFake) Release(_ context.Context, lease store.AttemptRuntimeLease) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.held, lease.AttemptID)
	f.released = append(f.released, lease)
	return nil
}

// ownerFake runs until its context ends or it is told to finish, counting
// the wakes it sees.
type ownerFake struct {
	mu     sync.Mutex
	runs   map[model.ExamAttemptID]int
	wakes  map[model.ExamAttemptID]int
	finish map[model.ExamAttemptID]chan struct{}
}

func newOwnerFake() *ownerFake {
	return &ownerFake{runs: map[model.ExamAttemptID]int{}, wakes: map[model.ExamAttemptID]int{},
		finish: map[model.ExamAttemptID]chan struct{}{}}
}

func (o *ownerFake) factory(attemptID model.ExamAttemptID) Owner {
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.finish[attemptID] == nil {
		o.finish[attemptID] = make(chan struct{})
	}
	return ownerFunc(func(ctx context.Context, _ store.AttemptRuntimeLease, wake <-chan struct{}) error {
		o.mu.Lock()
		o.runs[attemptID]++
		finish := o.finish[attemptID]
		o.mu.Unlock()
		for {
			select {
			case <-ctx.Done():
				return nil
			case <-finish:
				return nil
			case <-wake:
				o.mu.Lock()
				o.wakes[attemptID]++
				o.mu.Unlock()
			}
		}
	})
}

func (o *ownerFake) count(m map[model.ExamAttemptID]int, attemptID model.ExamAttemptID) int {
	o.mu.Lock()
	defer o.mu.Unlock()
	return m[attemptID]
}

type ownerFunc func(context.Context, store.AttemptRuntimeLease, <-chan struct{}) error

func (f ownerFunc) Run(ctx context.Context, lease store.AttemptRuntimeLease, wake <-chan struct{}) error {
	return f(ctx, lease, wake)
}

func TestRuntimeClaimsWakesRenewsAndReleases(t *testing.T) {
	t.Parallel()
	first, second, third := model.NewExamAttemptID(), model.NewExamAttemptID(), model.NewExamAttemptID()
	leases := newLeaseStoreFake(first, second, third)
	owners := newOwnerFake()
	runtime, err := NewRuntime(RuntimeConfig{Leases: leases, NodeID: "node-a", NewOwner: owners.factory, Owners: 2,
		LeaseLifetime: 30 * time.Millisecond * 3, Backstop: time.Hour})
	require.NoError(t, err)
	require.NoError(t, runtime.Start(t.Context()))
	t.Cleanup(func() { _ = runtime.Close(context.Background()) })

	require.Eventually(t, func() bool { return runtime.Holds(first) && runtime.Holds(second) }, time.Second, 5*time.Millisecond)
	require.False(t, runtime.Holds(third), "the owner bound leaves the rest to other nodes")

	runtime.Wake(first)
	require.Eventually(t, func() bool { return owners.count(owners.wakes, first) == 1 }, time.Second, 5*time.Millisecond,
		"a held environment's owner is woken")
	require.Eventually(t, func() bool {
		leases.mu.Lock()
		defer leases.mu.Unlock()
		return leases.renewals > 0
	}, time.Second, 5*time.Millisecond, "held leases are renewed")

	// A lost lease stops its owner and frees room for another environment.
	leases.mu.Lock()
	leases.lose[second] = true
	leases.mu.Unlock()
	require.Eventually(t, func() bool { return !runtime.Holds(second) }, time.Second, 5*time.Millisecond)
	runtime.Wake(third)
	require.Eventually(t, func() bool { return runtime.Holds(third) }, time.Second, 5*time.Millisecond,
		"a wake claims an environment nobody holds")

	// A finished owner releases its lease.
	owners.mu.Lock()
	close(owners.finish[first])
	owners.mu.Unlock()
	require.Eventually(t, func() bool { return !runtime.Holds(first) }, time.Second, 5*time.Millisecond)

	require.NoError(t, runtime.Close(context.Background()))
	leases.mu.Lock()
	defer leases.mu.Unlock()
	require.Empty(t, leases.held, "closing releases every lease")
}

func TestNewRuntimeRefusesAnIncompleteConfiguration(t *testing.T) {
	t.Parallel()
	_, err := NewRuntime(RuntimeConfig{Leases: newLeaseStoreFake(), NodeID: "node", Owners: 1})
	require.Error(t, err, "an owner factory is required")
	_, err = NewRuntime(RuntimeConfig{Leases: newLeaseStoreFake(), NewOwner: newOwnerFake().factory, Owners: 1})
	require.Error(t, err, "a node ID is required")
}

type environmentStoreFake struct {
	store.AttemptEnvironmentStore
	facts  []model.AttemptEnvironmentFact
	result *store.AttemptEnvironmentFactResult
}

func (s *environmentStoreFake) ApplyFact(_ context.Context, fact model.AttemptEnvironmentFact) (*store.AttemptEnvironmentFactResult, error) {
	s.facts = append(s.facts, fact)
	return s.result, nil
}

type wakerFake struct{ woken []model.ExamAttemptID }

func (w *wakerFake) Wake(attemptID model.ExamAttemptID) { w.woken = append(w.woken, attemptID) }

func TestFactsApplyEachEventAndWakeWhatChanged(t *testing.T) {
	t.Parallel()
	attemptID := model.NewExamAttemptID()
	environments := &environmentStoreFake{result: &store.AttemptEnvironmentFactResult{Changed: []model.ExamAttemptID{attemptID}}}
	waker := &wakerFake{}
	facts, err := NewFacts(environments, waker)
	require.NoError(t, err)
	event, err := model.NewExamAttemptSubmittedEvent(model.ExamAttemptSubmittedEvent{ID: model.NewDomainEventID(), OccurredAt: time.Now(),
		SubmissionID: model.NewSubmissionID(), AttemptID: attemptID, SittingID: model.NewExamSittingID(), ExamID: model.NewExamID()})
	require.NoError(t, err)
	require.NoError(t, facts.HandleEvent(t.Context(), *event))
	require.Len(t, environments.facts, 1)
	require.Equal(t, model.AttemptEnvironmentFactSubmitted, environments.facts[0].Kind)
	require.Equal(t, []model.ExamAttemptID{attemptID}, waker.woken)
	require.Len(t, Topics(), 6)
}
