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

// workspaceWriteRates bound how fast each kind of author may change one
// Attempt's Workspace. A bucket refills at the rate and holds at most the
// burst, so ordinary work never meets it and a runaway writer is slowed
// instead of flooding storage. Each kind has its own bucket: an editor's
// budget suits a person, while a build in the environment can write hundreds
// of files in a second without starving the candidate's own saves.
//
// Every byte burst exceeds one maximum batch request, so a single large file
// always fits from a full bucket.
var workspaceWriteRates = map[model.AttemptWorkspaceAuthorKind]workspaceWriteRate{
	model.AttemptWorkspaceAuthorCandidate: {
		mutationsPerSecond: 32,
		mutationBurst:      512,
		bytesPerSecond:     4 << 20,
		byteBurst:          128 << 20,
	},
	model.AttemptWorkspaceAuthorEnvironment: {
		mutationsPerSecond: 256,
		mutationBurst:      4096,
		bytesPerSecond:     32 << 20,
		byteBurst:          512 << 20,
	},
}

// workspaceBudgetSweepSize is the number of tracked buckets past which full,
// idle buckets are forgotten; a forgotten bucket is the same as a full one.
const workspaceBudgetSweepSize = 4096

type workspaceWriteRate struct {
	mutationsPerSecond float64
	mutationBurst      float64
	bytesPerSecond     float64
	byteBurst          float64
}

// workspaceWriteBudget holds one bucket per Attempt and author kind on this
// node. Nodes do not share buckets: the limit protects each node's storage
// traffic and is not an exact cluster-wide quota.
type workspaceWriteBudget struct {
	rates   map[model.AttemptWorkspaceAuthorKind]workspaceWriteRate
	mu      sync.Mutex
	buckets map[workspaceBudgetKey]*workspaceWriteBucket
}

type workspaceBudgetKey struct {
	attemptID model.ExamAttemptID
	kind      model.AttemptWorkspaceAuthorKind
}

type workspaceWriteBucket struct {
	mutations float64
	bytes     float64
	at        time.Time
}

func newWorkspaceWriteBudget(rates map[model.AttemptWorkspaceAuthorKind]workspaceWriteRate) *workspaceWriteBudget {
	return &workspaceWriteBudget{rates: rates, buckets: make(map[workspaceBudgetKey]*workspaceWriteBucket)}
}

// spend takes mutations and bytes from the bucket of the Attempt and author
// kind. It takes nothing and reports false when either would go below zero,
// or when the kind has no rate.
func (b *workspaceWriteBudget) spend(
	attemptID model.ExamAttemptID,
	kind model.AttemptWorkspaceAuthorKind,
	mutations int,
	bytes int64,
	now time.Time,
) bool {
	rate, ok := b.rates[kind]
	if !ok {
		return false
	}

	b.mu.Lock()
	defer b.mu.Unlock()

	key := workspaceBudgetKey{attemptID: attemptID, kind: kind}
	bucket := b.buckets[key]
	if bucket == nil {
		if len(b.buckets) >= workspaceBudgetSweepSize {
			b.sweep(now)
		}
		bucket = &workspaceWriteBucket{mutations: rate.mutationBurst, bytes: rate.byteBurst, at: now}
		b.buckets[key] = bucket
	}

	refill(bucket, rate, now)
	if bucket.mutations < float64(mutations) || bucket.bytes < float64(bytes) {
		return false
	}

	bucket.mutations -= float64(mutations)
	bucket.bytes -= float64(bytes)
	return true
}

func refill(bucket *workspaceWriteBucket, rate workspaceWriteRate, now time.Time) {
	elapsed := now.Sub(bucket.at).Seconds()
	if elapsed <= 0 {
		return
	}
	bucket.mutations = min(rate.mutationBurst, bucket.mutations+elapsed*rate.mutationsPerSecond)
	bucket.bytes = min(rate.byteBurst, bucket.bytes+elapsed*rate.bytesPerSecond)
	bucket.at = now
}

// sweep forgets every bucket that has refilled completely.
func (b *workspaceWriteBudget) sweep(now time.Time) {
	for key, bucket := range b.buckets {
		rate := b.rates[key.kind]
		refill(bucket, rate, now)
		if bucket.mutations >= rate.mutationBurst && bucket.bytes >= rate.byteBurst {
			delete(b.buckets, key)
		}
	}
}
