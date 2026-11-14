// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package attempt

import (
	"sync"
	"time"

	"github.com/sudosylabs/proctor/server/model"
)

// workspaceWriteRates bound how fast one Attempt may change its Workspace,
// whoever writes: the candidate's editor or the execution environment. A
// bucket refills at the rate and holds at most the burst, so ordinary saving
// never meets it and a runaway writer is slowed instead of flooding storage.
//
// The byte burst exceeds one maximum batch request, so a single large file
// always fits from a full bucket.
var workspaceWriteRates = workspaceWriteRate{
	mutationsPerSecond: 32,
	mutationBurst:      512,
	bytesPerSecond:     4 << 20,
	byteBurst:          128 << 20,
}

// workspaceBudgetSweepSize is the number of tracked Attempts past which full,
// idle buckets are forgotten; a forgotten bucket is the same as a full one.
const workspaceBudgetSweepSize = 4096

type workspaceWriteRate struct {
	mutationsPerSecond float64
	mutationBurst      float64
	bytesPerSecond     float64
	byteBurst          float64
}

// workspaceWriteBudget holds one bucket per Attempt on this node. Nodes do not
// share buckets: the limit protects each node's storage traffic and is not an
// exact cluster-wide quota.
type workspaceWriteBudget struct {
	rate     workspaceWriteRate
	mu       sync.Mutex
	attempts map[model.ExamAttemptID]*workspaceWriteBucket
}

type workspaceWriteBucket struct {
	mutations float64
	bytes     float64
	at        time.Time
}

func newWorkspaceWriteBudget(rate workspaceWriteRate) *workspaceWriteBudget {
	return &workspaceWriteBudget{rate: rate, attempts: make(map[model.ExamAttemptID]*workspaceWriteBucket)}
}

// spend takes mutations and bytes from the Attempt's bucket. It takes nothing
// and reports false when either would go below zero.
func (b *workspaceWriteBudget) spend(attemptID model.ExamAttemptID, mutations int, bytes int64, now time.Time) bool {
	b.mu.Lock()
	defer b.mu.Unlock()

	bucket := b.attempts[attemptID]
	if bucket == nil {
		if len(b.attempts) >= workspaceBudgetSweepSize {
			b.sweep(now)
		}
		bucket = &workspaceWriteBucket{mutations: b.rate.mutationBurst, bytes: b.rate.byteBurst, at: now}
		b.attempts[attemptID] = bucket
	}

	b.refill(bucket, now)
	if bucket.mutations < float64(mutations) || bucket.bytes < float64(bytes) {
		return false
	}

	bucket.mutations -= float64(mutations)
	bucket.bytes -= float64(bytes)
	return true
}

func (b *workspaceWriteBudget) refill(bucket *workspaceWriteBucket, now time.Time) {
	elapsed := now.Sub(bucket.at).Seconds()
	if elapsed <= 0 {
		return
	}
	bucket.mutations = min(b.rate.mutationBurst, bucket.mutations+elapsed*b.rate.mutationsPerSecond)
	bucket.bytes = min(b.rate.byteBurst, bucket.bytes+elapsed*b.rate.bytesPerSecond)
	bucket.at = now
}

// sweep forgets every bucket that has refilled completely.
func (b *workspaceWriteBudget) sweep(now time.Time) {
	for id, bucket := range b.attempts {
		b.refill(bucket, now)
		if bucket.mutations >= b.rate.mutationBurst && bucket.bytes >= b.rate.byteBurst {
			delete(b.attempts, id)
		}
	}
}
