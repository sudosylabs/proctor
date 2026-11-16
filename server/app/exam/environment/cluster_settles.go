// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package environment

import (
	"fmt"
	"sync"

	"github.com/sudosylabs/proctor/server/cluster"
	"github.com/sudosylabs/proctor/server/model"
)

// settledEvent tells every node that the owner of an Attempt's environment
// completed a settle, so a seal waiting on another node stops waiting. It
// carries only the Attempt ID; a lost message costs the waiter its budget,
// and the Store records no gap for a settle that completed.
const settledEvent cluster.Event = "exam.attempt.environment.settled"

// ClusterSettles carries completed settles across the cluster: the owner's
// node signals its own waiters and broadcasts to its peers, which signal
// theirs. It never blocks the owner and never fails it. A waiter holds at
// most one signal, and a lost one costs the waiter its budget, never a false
// gap, because the gap is recorded only for a settle the row still has
// pending.
type ClusterSettles struct {
	*attemptBroadcast

	mu      sync.Mutex
	waiters map[model.ExamAttemptID]map[chan struct{}]struct{}
}

func newClusterSettles(transport cluster.Transport, report func(error)) (*ClusterSettles, error) {
	s := &ClusterSettles{waiters: map[model.ExamAttemptID]map[chan struct{}]struct{}{}}
	broadcast, err := newAttemptBroadcast(transport, settledEvent, report, s.signal)
	if err != nil {
		return nil, fmt.Errorf("construct attempt environment settle signals: %w", err)
	}
	s.attemptBroadcast = broadcast
	return s, nil
}

// Settled signals attemptID's waiters here and on every peer.
func (s *ClusterSettles) Settled(attemptID model.ExamAttemptID) {
	if !attemptID.IsValid() {
		return
	}
	s.signal(attemptID)
	s.enqueue(attemptID)
}

// SubscribeSettled signals the returned channel after every settle of
// attemptID completed from now on, until cancel.
func (s *ClusterSettles) SubscribeSettled(attemptID model.ExamAttemptID) (<-chan struct{}, func()) {
	signal := make(chan struct{}, 1)
	s.mu.Lock()
	waiters := s.waiters[attemptID]
	if waiters == nil {
		waiters = map[chan struct{}]struct{}{}
		s.waiters[attemptID] = waiters
	}
	waiters[signal] = struct{}{}
	s.mu.Unlock()
	var once sync.Once
	return signal, func() {
		once.Do(func() {
			s.mu.Lock()
			defer s.mu.Unlock()
			delete(s.waiters[attemptID], signal)
			if len(s.waiters[attemptID]) == 0 {
				delete(s.waiters, attemptID)
			}
		})
	}
}

func (s *ClusterSettles) signal(attemptID model.ExamAttemptID) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for waiter := range s.waiters[attemptID] {
		select {
		case waiter <- struct{}{}:
		default:
		}
	}
}
