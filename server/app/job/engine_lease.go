// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package job

import (
	"context"
	"time"

	"github.com/sudosylabs/proctor/server/logging"
	"github.com/sudosylabs/proctor/server/store"
)

// claimHeartbeat carries the state one claimed attempt's heartbeat loop shares with its execution.
type claimHeartbeat struct {
	parent        context.Context
	handlerCtx    context.Context
	cancelHandler context.CancelFunc
	descriptor    Descriptor
	claim         *store.JobClaim
	startedAt     time.Time
	done          <-chan struct{}
	claimLost     chan<- struct{}
}

// heartbeatClaim renews the attempt's lease and, for cancelable types, observes cancellation on every heartbeat
// tick until the handler finishes, abandoning the attempt when the claim is lost.
func (r *Engine) heartbeatClaim(h claimHeartbeat) {
	descriptor, claim := h.descriptor, h.claim
	ticker := time.NewTicker(descriptor.HeartbeatInterval)
	defer ticker.Stop()
	// The lease runs from the last renewal the store accepted. A failed
	// heartbeat abandons the attempt only when the store reports the claim
	// lost or when the next heartbeat could no longer land inside the lease.
	renewedAt := h.startedAt
	for {
		select {
		case <-h.done:
			return
		case <-h.handlerCtx.Done():
			return
		case <-ticker.C:
			heartbeatStarted := time.Now()
			if _, err := r.deps.Store.Heartbeat(h.handlerCtx, &store.JobHeartbeat{
				AttemptID:     claim.Attempt.ID,
				ClaimToken:    claim.Attempt.ClaimToken,
				LeaseDuration: descriptor.LeaseDuration,
			}); err != nil {
				r.record(Activity{
					Kind:      "job",
					Name:      string(descriptor.Type),
					Operation: "heartbeat",
					Outcome:   "error",
					Duration:  time.Since(heartbeatStarted),
				})
				r.deps.Diagnostics.ErrorContext(h.parent, "heartbeat durable job", logging.Err(err))
				if !heartbeatClaimLost(err) && time.Since(renewedAt)+descriptor.HeartbeatInterval < descriptor.LeaseDuration {
					continue
				}

				select {
				case h.claimLost <- struct{}{}:
				default:
				}

				h.cancelHandler()
				return
			}

			renewedAt = heartbeatStarted
			r.record(Activity{
				Kind:      "job",
				Name:      string(descriptor.Type),
				Operation: "heartbeat",
				Outcome:   "success",
				Duration:  time.Since(heartbeatStarted),
			})
			if descriptor.Cancelable {
				cancellationStarted := time.Now()
				requested, err := r.deps.Store.CancellationRequested(h.handlerCtx, claim.Attempt.ID, claim.Attempt.ClaimToken)
				if err != nil {
					r.record(Activity{
						Kind:      "job",
						Name:      string(descriptor.Type),
						Operation: "cancellation_check",
						Outcome:   "error",
						Duration:  time.Since(cancellationStarted),
					})
					r.deps.Diagnostics.ErrorContext(h.parent, "observe durable job cancellation", logging.Err(err))
					if !heartbeatClaimLost(err) && time.Since(renewedAt)+descriptor.HeartbeatInterval < descriptor.LeaseDuration {
						continue
					}

					select {
					case h.claimLost <- struct{}{}:
					default:
					}

					h.cancelHandler()
					return
				}

				if requested {
					r.record(Activity{
						Kind:      "job",
						Name:      string(descriptor.Type),
						Operation: "cancellation_check",
						Outcome:   "requested",
						Duration:  time.Since(cancellationStarted),
					})
					h.cancelHandler()
					return
				}

				r.record(Activity{
					Kind:      "job",
					Name:      string(descriptor.Type),
					Operation: "cancellation_check",
					Outcome:   "clear",
					Duration:  time.Since(cancellationStarted),
				})
			}
		}
	}
}

// heartbeatClaimLost reports a heartbeat the store refused because another
// owner holds the attempt or its lease already expired. Any other failure is
// an unknown outcome that later heartbeats may still repair.
func heartbeatClaimLost(err error) bool {
	return store.IsConflict(err) || store.IsNotFound(err)
}
