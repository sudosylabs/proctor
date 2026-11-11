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

// claimLoop claims and runs Jobs of descriptor's type one at a time until ctx
// ends, waiting for the poll interval or a Wake after a claim that found
// nothing.
func (r *Engine) claimLoop(ctx context.Context, descriptor Descriptor) {
	defer r.wg.Done()
	timer := time.NewTimer(0)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-r.wake:
		case <-timer.C:
		}

		token, err := model.NewJobClaimToken()
		if err != nil {
			r.deps.Diagnostics.ErrorContext(ctx, "generate job claim token", logging.Err(err))
			resetTimer(timer, r.deps.Policy.PollInterval)
			continue
		}

		startedClaim := time.Now()
		claim, err := r.deps.Store.ClaimNext(ctx, &store.JobClaimRequest{
			Types:         []model.JobType{descriptor.Type},
			NodeID:        r.deps.NodeID,
			ClaimToken:    token,
			LeaseDuration: descriptor.LeaseDuration,
		})
		claimDuration := time.Since(startedClaim)
		if err != nil {
			result := "error"
			if store.IsNotFound(err) {
				result = "empty"
			} else if errors.Is(err, context.Canceled) {
				result = "canceled"
			}

			r.record(Activity{Kind: "job", Name: string(descriptor.Type), Operation: "claim", Outcome: result, Duration: claimDuration})
			if !store.IsNotFound(err) && !errors.Is(err, context.Canceled) {
				r.deps.Diagnostics.ErrorContext(ctx, "claim durable job", logging.Err(err))
			}

			resetTimer(timer, r.deps.Policy.PollInterval)
			continue
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
		resolved, err := r.registry.Descriptor(claim.Job.Type)
		if err != nil {
			r.completeUnsupported(ctx, descriptor, claim, err)
		} else {
			r.executeClaim(ctx, resolved, claim)
		}

		resetTimer(timer, 0)
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
