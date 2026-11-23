// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package realtime

import (
	"context"
	"errors"
	"sync"
	"time"
)

// throttledKeySweep is how many keys may be remembered before idle ones are
// forgotten.
const throttledKeySweep = 4096

// ThrottledPublisher publishes at most one event per key per window. The
// first event of a quiet key goes at once; later ones inside the window
// collapse into one trailing event, the newest, sent when the window ends, so
// the last change is never lost. Each node throttles on its own: a hint sent
// twice across a cluster is harmless.
type ThrottledPublisher struct {
	publisher Publisher
	window    time.Duration
	now       func() time.Time
	afterFunc func(time.Duration, func()) func()

	mu       sync.Mutex
	keys     map[string]*throttledKey
	closed   bool
	active   int
	done     chan struct{}
	lifetime context.Context
	cancel   context.CancelFunc
}

type throttledKey struct {
	last    time.Time
	pending *Event
	ctx     context.Context
	cancel  func()
}

// NewThrottledPublisher returns a throttle over publisher. now and afterFunc
// default to the wall clock and time.AfterFunc.
func NewThrottledPublisher(
	publisher Publisher,
	window time.Duration,
	now func() time.Time,
	afterFunc func(time.Duration, func()) func(),
) (*ThrottledPublisher, error) {
	if publisher == nil || window <= 0 {
		return nil, errors.New("throttled publisher requires a publisher and a positive window")
	}
	if now == nil {
		now = time.Now
	}
	if afterFunc == nil {
		afterFunc = func(delay time.Duration, run func()) func() {
			timer := time.AfterFunc(delay, run)
			return func() { timer.Stop() }
		}
	}
	lifetime, cancel := context.WithCancel(context.Background())
	return &ThrottledPublisher{publisher: publisher, window: window, now: now, afterFunc: afterFunc,
		keys: map[string]*throttledKey{}, done: make(chan struct{}), lifetime: lifetime, cancel: cancel}, nil
}

// Publish sends event now or folds it into the key's trailing event. A
// failed trailing publication is reported under operation, since no caller
// waits for it.
func (t *ThrottledPublisher) Publish(ctx context.Context, key, operation string, event Event) error {
	if err := event.ValidateForPublish(); err != nil {
		return err
	}
	now := t.now()
	t.mu.Lock()
	if t.closed {
		t.mu.Unlock()
		return nil
	}
	slot := t.keys[key]
	if slot == nil || now.Sub(slot.last) >= t.window && slot.pending == nil {
		if len(t.keys) >= throttledKeySweep {
			t.forgetIdle(now)
		}
		t.keys[key] = &throttledKey{last: now}
		t.active++
		t.mu.Unlock()
		defer t.finish()
		return t.publisher.Publish(ctx, event)
	}
	if slot.pending == nil {
		delay := t.window - now.Sub(slot.last)
		slot.cancel = t.afterFunc(max(delay, 0), func() { t.flush(key, operation) })
	}
	slot.pending, slot.ctx = &event, context.WithoutCancel(ctx)
	t.mu.Unlock()
	return nil
}

func (t *ThrottledPublisher) flush(key, operation string) {
	t.mu.Lock()
	slot := t.keys[key]
	if slot == nil || slot.pending == nil {
		t.mu.Unlock()
		return
	}
	event, ctx := *slot.pending, slot.ctx
	slot.pending, slot.ctx, slot.cancel, slot.last = nil, nil, nil, t.now()
	t.active++
	t.mu.Unlock()
	defer t.finish()
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	stop := context.AfterFunc(t.lifetime, cancel)
	defer stop()
	if t.lifetime.Err() != nil {
		return
	}
	if err := t.publisher.Publish(ctx, event); err != nil {
		t.publisher.Report(ctx, operation, err)
	}
}

// Close drops pending hints and waits within the shutdown budget for sends
// already in progress.
func (t *ThrottledPublisher) Close(ctx context.Context) error {
	if t == nil {
		return nil
	}
	t.mu.Lock()
	if !t.closed {
		t.closed = true
		t.cancel()
		for _, slot := range t.keys {
			if slot.cancel != nil {
				slot.cancel()
			}
		}
		t.keys = nil
		if t.active == 0 {
			close(t.done)
		}
	}
	t.mu.Unlock()

	select {
	case <-t.done:
		return nil
	default:
	}
	select {
	case <-t.done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (t *ThrottledPublisher) finish() {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.active--
	if t.closed && t.active == 0 {
		close(t.done)
	}
}

// forgetIdle drops keys whose window has passed with nothing pending. The
// caller holds mu.
func (t *ThrottledPublisher) forgetIdle(now time.Time) {
	for key, slot := range t.keys {
		if slot.pending == nil && now.Sub(slot.last) >= t.window {
			delete(t.keys, key)
		}
	}
}
