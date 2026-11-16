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

// candidateWriteRate is sized so ordinary editing never meets it and a
// runaway writer is slowed instead of flooding storage. Each author kind has
// its own bucket, so a build in the environment writing hundreds of files a
// second never starves the candidate's own saves. Every byte burst exceeds
// one maximum batch request, so a single large file always fits from a full
// bucket.
var candidateWriteRate = workspaceWriteRate{
	mutationsPerSecond: 32,
	mutationBurst:      512,
	bytesPerSecond:     4 << 20,
	byteBurst:          128 << 20,
}

// The environment's write rate per CPU core, and the core count's clamp.
const (
	environmentMutationsPerCore = 128
	environmentMutationBurst    = 2048
	environmentBytesPerCore     = 16 << 20
	environmentByteBurstPerCore = 256 << 20
	environmentMinimumCores     = 1
	environmentMaximumCores     = 8
)

// environmentWriteRate scales with the profile's CPU, because a machine with
// more CPU runs bigger builds that write more. The byte burst never drops
// below one maximum batch request.
func environmentWriteRate(resources model.EnvironmentResources) workspaceWriteRate {
	cores := min(max(float64(resources.CPUMillis)/1000, environmentMinimumCores), environmentMaximumCores)
	return workspaceWriteRate{
		mutationsPerSecond: environmentMutationsPerCore * cores,
		mutationBurst:      environmentMutationBurst * cores,
		bytesPerSecond:     environmentBytesPerCore * cores,
		byteBurst:          max(environmentByteBurstPerCore*cores, float64(model.AttemptWorkspaceBatchMaximumRequestBytes)),
	}
}

// workspaceWriteRateFor reports false for an author kind without a rate,
// which is then refused.
func workspaceWriteRateFor(kind model.AttemptWorkspaceAuthorKind, resources model.EnvironmentResources) (workspaceWriteRate, bool) {
	switch kind {
	case model.AttemptWorkspaceAuthorCandidate:
		return candidateWriteRate, true
	case model.AttemptWorkspaceAuthorEnvironment:
		if !resources.Valid() {
			return workspaceWriteRate{}, false
		}
		return environmentWriteRate(resources), true
	default:
		return workspaceWriteRate{}, false
	}
}

// workspaceBudgetSweepSize is the tracked-bucket count past which full idle
// buckets are forgotten; a forgotten bucket is the same as a full one.
const workspaceBudgetSweepSize = 4096

type workspaceWriteRate struct {
	mutationsPerSecond float64
	mutationBurst      float64
	bytesPerSecond     float64
	byteBurst          float64
}

// workspaceWriteBudget holds one bucket per attempt and author kind on this
// node. Nodes do not share buckets: the limit protects each node's storage
// traffic and is not a cluster-wide quota.
type workspaceWriteBudget struct {
	rate    func(model.AttemptWorkspaceAuthorKind, model.EnvironmentResources) (workspaceWriteRate, bool)
	mu      sync.Mutex
	buckets map[workspaceBudgetKey]*workspaceWriteBucket
}

type workspaceBudgetKey struct {
	attemptID model.ExamAttemptID
	kind      model.AttemptWorkspaceAuthorKind
}

type workspaceWriteBucket struct {
	// rate is the rate the bucket was last spent at, which a sweep refills it
	// by.
	rate      workspaceWriteRate
	mutations float64
	bytes     float64
	at        time.Time
}

func newWorkspaceWriteBudget(
	rate func(model.AttemptWorkspaceAuthorKind, model.EnvironmentResources) (workspaceWriteRate, bool),
) *workspaceWriteBudget {
	return &workspaceWriteBudget{rate: rate, buckets: make(map[workspaceBudgetKey]*workspaceWriteBucket)}
}

// spend takes nothing and reports false when mutations or bytes would go
// below zero, or when the kind has no rate.
func (b *workspaceWriteBudget) spend(
	attemptID model.ExamAttemptID,
	kind model.AttemptWorkspaceAuthorKind,
	resources model.EnvironmentResources,
	mutations int,
	bytes int64,
	now time.Time,
) bool {
	rate, ok := b.rate(kind, resources)
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
	bucket.rate = rate
	elapsed := now.Sub(bucket.at).Seconds()
	if elapsed <= 0 {
		return
	}
	bucket.mutations = min(rate.mutationBurst, bucket.mutations+elapsed*rate.mutationsPerSecond)
	bucket.bytes = min(rate.byteBurst, bucket.bytes+elapsed*rate.bytesPerSecond)
	bucket.at = now
}

func (b *workspaceWriteBudget) sweep(now time.Time) {
	for key, bucket := range b.buckets {
		rate := bucket.rate
		refill(bucket, rate, now)
		if bucket.mutations >= rate.mutationBurst && bucket.bytes >= rate.byteBurst {
			delete(b.buckets, key)
		}
	}
}
