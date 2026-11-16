// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package retrylayer

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/sudosylabs/proctor/server/store"
)

const maxAllowedAttempts = 10

// Policy bounds retries. IsTransient comes from the database adapter;
// MaxAttempts is 1..10.
type Policy struct {
	MaxAttempts    int
	InitialBackoff time.Duration
	MaxBackoff     time.Duration
	IsTransient    func(error) bool
}

// Recorder counts retry decisions ("retry" or "exhausted") without store
// arguments, errors or context.
type Recorder interface {
	ObserveStoreRetry(outcome string)
}

// DefaultPolicy makes three attempts with backoff from 10ms to 100ms.
func DefaultPolicy(classifier func(error) bool) Policy {
	return Policy{
		MaxAttempts:    3,
		InitialBackoff: 10 * time.Millisecond,
		MaxBackoff:     100 * time.Millisecond,
		IsTransient:    classifier,
	}
}

// Layer retries only the methods listed in allowlist.txt, whose overrides
// are generated into forwarding_gen.go; embedded methods are forwarded
// exactly once.
type Layer struct {
	store.Store
	policy   Policy
	recorder Recorder
	stores   retryStores
}

// NewWithRecorder wraps next. A nil recorder records nothing.
func NewWithRecorder(next store.Store, policy Policy, recorder Recorder) (*Layer, error) {
	return newLayer(next, policy, recorder)
}

func newLayer(next store.Store, policy Policy, recorder Recorder) (*Layer, error) {
	if next == nil {
		return nil, errors.New("retry store is nil")
	}
	if policy.MaxAttempts < 1 || policy.MaxAttempts > maxAllowedAttempts {
		return nil, fmt.Errorf("retry max attempts must be between 1 and %d", maxAllowedAttempts)
	}
	if policy.InitialBackoff <= 0 {
		return nil, errors.New("retry initial backoff must be positive")
	}
	if policy.MaxBackoff < policy.InitialBackoff {
		return nil, errors.New("retry max backoff must not be less than initial backoff")
	}
	if policy.IsTransient == nil {
		return nil, errors.New("retry transient classifier is nil")
	}
	return &Layer{Store: next, policy: policy, recorder: recorder}, nil
}

func retryCall1[T any](ctx context.Context, layer *Layer, call func() (T, error)) (T, error) {
	var zero T
	delay := layer.policy.InitialBackoff
	for attempt := 1; ; attempt++ {
		result, err := call()
		transient := err != nil && layer.policy.IsTransient(err)
		if err == nil || attempt >= layer.policy.MaxAttempts || !transient {
			if transient && attempt >= layer.policy.MaxAttempts && layer.recorder != nil {
				layer.recorder.ObserveStoreRetry("exhausted")
			}
			return result, err
		}
		if layer.recorder != nil {
			layer.recorder.ObserveStoreRetry("retry")
		}
		if err := wait(ctx, delay); err != nil {
			return zero, err
		}
		delay = nextBackoff(delay, layer.policy.MaxBackoff)
	}
}

func retryCall0(ctx context.Context, layer *Layer, call func() error) error {
	_, err := retryCall1(ctx, layer, func() (struct{}, error) {
		return struct{}{}, call()
	})
	return err
}

func retryCall2[T, U any](
	ctx context.Context,
	layer *Layer,
	call func() (T, U, error),
) (T, U, error) {
	type pair struct {
		first  T
		second U
	}
	result, err := retryCall1(ctx, layer, func() (pair, error) {
		first, second, err := call()
		return pair{first: first, second: second}, err
	})
	return result.first, result.second, err
}

func wait(ctx context.Context, delay time.Duration) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-timer.C:
		return ctx.Err()
	case <-ctx.Done():
		return ctx.Err()
	}
}

func nextBackoff(current, maximum time.Duration) time.Duration {
	if current >= maximum || current > maximum/2 {
		return maximum
	}
	return current * 2
}
