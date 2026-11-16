// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------
package attempt

import (
	"container/list"
	"context"
	"sync"
	"time"

	"github.com/sudosylabs/proctor/server/app/apperror"
	"github.com/sudosylabs/proctor/server/app/invocation"
	"github.com/sudosylabs/proctor/server/model"
)

const (
	controlWorkers = 32
	// controlUsers bounds the allowances one lane retains. The least recently
	// admitted user is evicted and restarts at a full burst, so eviction can
	// only favor an idle user, never a caller hot enough to stay tracked.
	controlUsers = 4096

	// Live renewal and security-coverage updates queue instead of being
	// refused: a refused renewal looks like a lost client, and enough of them
	// expire the lease and suspend a well-behaved candidate. A wait longer
	// than one renewal interval is superseded by the next renewal.
	liveControlWaiters = 4096
	liveControlWait    = model.AttemptParticipationRenewalInterval
)

type controlLane struct {
	workers int
	// waiters bounds requests queued for a busy worker; zero refuses at once.
	waiters int
	wait    time.Duration
	// interval is the sustained cost of one request: an allowance refills one
	// request per interval, up to burst requests.
	interval time.Duration
	burst    int64
}

var (
	// deliveryControlLane admits recovery and status work at 20 requests per
	// second with a burst of 40, refusing at once when its workers are busy.
	deliveryControlLane = controlLane{workers: controlWorkers, interval: 50 * time.Millisecond, burst: 40}
	// liveControlLane sustains two requests per renewal interval (one renewal
	// plus one security-coverage update or exact retry) with a burst of three
	// intervals, so a few callers cannot hold its workers while an honest
	// candidate never reaches the bound.
	liveControlLane = controlLane{
		workers:  controlWorkers,
		waiters:  liveControlWaiters,
		wait:     liveControlWait,
		interval: model.AttemptParticipationRenewalInterval / 2,
		burst:    6,
	}
)

// controlAllowance is one user's refill credit, in milliseconds of lane
// interval; one request costs one interval.
type controlAllowance struct {
	user   model.UserID
	credit int64
	at     time.Time
}

// controlAdmission bounds are per process: N nodes admit N times the
// concurrency, and a user's allowance is tracked separately on every node.
// Admission never grants authority. The zero value is the delivery lane.
type controlAdmission struct {
	lane    controlLane
	mu      sync.Mutex
	slots   chan struct{}
	waiting int
	// users indexes recency, most recently admitted at the front.
	users   map[model.UserID]*list.Element
	recency list.List
}

func (a *controlAdmission) enter(ctx context.Context, user model.UserID, now time.Time) (func(), error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if !user.IsValid() {
		return nil, apperror.New("authentication.token_invalid")
	}

	a.mu.Lock()
	lane := a.shape()
	if a.slots == nil {
		a.slots = make(chan struct{}, lane.workers)
	}

	slots := a.slots
	if lane.waiters == 0 && len(slots) >= lane.workers {
		a.mu.Unlock()
		return nil, apperror.New("exam.delivery.control_rate_limited")
	}

	if !a.spend(user, now, lane) {
		a.mu.Unlock()
		return nil, apperror.New("exam.delivery.control_rate_limited")
	}

	select {
	case slots <- struct{}{}:
		a.mu.Unlock()
		return releaseControl(slots), nil
	default:
	}

	if a.waiting >= lane.waiters {
		a.refund(user, lane)
		a.mu.Unlock()
		return nil, apperror.New("exam.delivery.control_rate_limited")
	}

	a.waiting++
	a.mu.Unlock()
	timer := time.NewTimer(lane.wait)
	defer timer.Stop()
	var err error
	select {
	case slots <- struct{}{}:
	case <-ctx.Done():
		err = ctx.Err()
	case <-timer.C:
		err = apperror.New("exam.delivery.control_rate_limited")
	}

	a.mu.Lock()
	a.waiting--
	if err != nil {
		a.refund(user, lane)
	}

	a.mu.Unlock()
	if err != nil {
		return nil, err
	}

	return releaseControl(slots), nil
}

// shape requires the caller to hold mu.
func (a *controlAdmission) shape() controlLane {
	if a.lane.workers < 1 {
		return deliveryControlLane
	}
	return a.lane
}

func releaseControl(slots chan struct{}) func() {
	var once sync.Once
	return func() { once.Do(func() { <-slots }) }
}

// spend requires the caller to hold mu.
func (a *controlAdmission) spend(user model.UserID, now time.Time, lane controlLane) bool {
	cost := lane.interval.Milliseconds()
	capacity := lane.burst * cost
	if a.users == nil {
		a.users = make(map[model.UserID]*list.Element)
	}
	element, exists := a.users[user]
	if !exists {
		if a.recency.Len() >= controlUsers {
			oldest := a.recency.Back()
			delete(a.users, oldest.Value.(*controlAllowance).user)
			a.recency.Remove(oldest)
		}
		element = a.recency.PushFront(&controlAllowance{user: user, credit: capacity, at: now})
		a.users[user] = element
	}
	a.recency.MoveToFront(element)
	value := element.Value.(*controlAllowance)
	// A clock rollback cannot mint credit or move the refill origin backwards.
	if now.After(value.at) {
		value.credit = min(capacity, value.credit+min(now.Sub(value.at).Milliseconds(), capacity))
		value.at = now
	}
	if value.credit < cost {
		return false
	}
	value.credit -= cost
	return true
}

// refund returns a request that never reached a worker, so a queue timeout or
// cancellation costs nothing. The caller holds mu.
func (a *controlAdmission) refund(user model.UserID, lane controlLane) {
	if element, exists := a.users[user]; exists {
		value := element.Value.(*controlAllowance)
		value.credit = min(lane.burst*lane.interval.Milliseconds(), value.credit+lane.interval.Milliseconds())
	}
}

func (s *Service) enterControl(ctx context.Context, inv invocation.Invocation, live bool) (func(), error) {
	budget := &s.deliveryControls
	if live {
		budget = &s.liveControls
	}
	return budget.enter(ctx, inv.Principal().UserID, s.now())
}
