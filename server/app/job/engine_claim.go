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
	"time"

	"github.com/sudosylabs/proctor/server/logging"
	"github.com/sudosylabs/proctor/server/model"
	"github.com/sudosylabs/proctor/server/store"
)

// claimLoop is one worker of a type. It waits until the dispatcher says the
// type has work, then claims and runs Jobs until none is left, so a backlog
// drains without the dispatcher. A failed claim backs off and tries again.
func (r *Engine) claimLoop(ctx context.Context, descriptor Descriptor, ready <-chan struct{}) {
	defer r.wg.Done()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ready:
		}

		failures := 0
		for ctx.Err() == nil {
			claimed, err := r.claimOnce(ctx, descriptor)
			if err == nil && !claimed {
				break
			}
			if err == nil {
				failures = 0
				continue
			}
			failures++
			if !sleepContext(ctx, backoff(failures)) {
				return
			}
		}
	}
}

// claimOnce claims and runs at most one Job. It reports false with no error
// when nothing of the type is due.
func (r *Engine) claimOnce(ctx context.Context, descriptor Descriptor) (bool, error) {
	token, err := model.NewJobClaimToken()
	if err != nil {
		r.deps.Diagnostics.ErrorContext(ctx, "generate job claim token", logging.Err(err))
		return false, err
	}

	select {
	case r.claims <- struct{}{}:
	case <-ctx.Done():
		return false, nil
	}
	startedClaim := time.Now()
	claim, err := r.deps.Store.ClaimNext(ctx, &store.JobClaimRequest{
		Types:         []model.JobType{descriptor.Type},
		NodeID:        r.deps.NodeID,
		ClaimToken:    token,
		LeaseDuration: descriptor.LeaseDuration,
	})
	claimDuration := time.Since(startedClaim)
	<-r.claims
	if err != nil {
		result := "error"
		if store.IsNotFound(err) {
			result = "empty"
		} else if errors.Is(err, context.Canceled) {
			result = "canceled"
		}

		r.record(Activity{Kind: "job", Name: string(descriptor.Type), Operation: "claim", Outcome: result, Duration: claimDuration})
		if result != "error" {
			return false, nil
		}
		r.deps.Diagnostics.ErrorContext(ctx, "claim durable job", logging.Err(err))
		return false, err
	}

	queueLatency := time.Since(claim.Job.AvailableAt)
	if queueLatency < 0 {
		queueLatency = 0
	}

	r.record(Activity{
		Kind:         "job",
		Name:         string(descriptor.Type),
		Operation:    "claim",
		Outcome:      "success",
		Duration:     claimDuration,
		QueueLatency: queueLatency,
	})
	r.announceStatus(ctx, claim.Job)
	resolved, err := r.registry.Descriptor(claim.Job.Type)
	if err != nil {
		r.completeUnsupported(ctx, descriptor, claim, err)
	} else {
		r.executeClaim(ctx, resolved, claim)
	}
	return true, nil
}

// sleepContext waits d, and reports false when ctx ends first.
func sleepContext(ctx context.Context, d time.Duration) bool {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}

func resetTimer(timer *time.Timer, delay time.Duration) {
	if !timer.Stop() {
		select {
		case <-timer.C:
		default:
		}
	}
	timer.Reset(delay)
}
