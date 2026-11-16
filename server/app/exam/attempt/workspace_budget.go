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

// candidateWriteRate bounds how fast a candidate's editor may change one
// Attempt's Workspace. A bucket refills at the rate and holds at most the
// burst, so ordinary work never meets it and a runaway writer is slowed
// instead of flooding storage. Each author kind has its own bucket: an
// editor's budget suits a person, while a build in the environment can write
// hundreds of files in a second without starving the candidate's own saves.
//
// Every byte burst exceeds one maximum batch request, so a single large file
// always fits from a full bucket.
var candidateWriteRate = workspaceWriteRate{
	mutationsPerSecond: 32,
	mutationBurst:      512,
	bytesPerSecond:     4 << 20,
	byteBurst:          128 << 20,
}

// The environment's write rate per CPU core of its profile, and the cores
// it counts.
const (
	environmentMutationsPerCore = 128
	environmentMutationBurst    = 2048
	environmentBytesPerCore     = 16 << 20
	environmentByteBurstPerCore = 256 << 20
	environmentMinimumCores     = 1
	environmentMaximumCores     = 8
)

// environmentWriteRate sizes an environment's budget by its profile: a
// machine with more CPU runs bigger builds that write more. With cores the
// profile's CPU in whole cores, held from 1 to 8, it allows 128 mutations
// and 16 MiB a second per core, with bursts of 2,048 mutations and 256 MiB
// per core, and never a byte burst below one maximum batch request. A
// two-core profile gets 256 mutations and 32 MiB a second.
func environmentWriteRate(resources model.EnvironmentResources) workspaceWriteRate {
	cores := min(max(float64(resources.CPUMillis)/1000, environmentMinimumCores), environmentMaximumCores)
	return workspaceWriteRate{
		mutationsPerSecond: environmentMutationsPerCore * cores,
		mutationBurst:      environmentMutationBurst * cores,
		bytesPerSecond:     environmentBytesPerCore * cores,
		byteBurst:          max(environmentByteBurstPerCore*cores, float64(model.AttemptWorkspaceBatchMaximumRequestBytes)),
	}
}

// workspaceWriteRateFor is the rate of an author kind; the environment's
// comes from its profile's resources. A kind without one is refused.
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
	rate    func(model.AttemptWorkspaceAuthorKind, model.EnvironmentResources) (workspaceWriteRate, bool)
	mu      sync.Mutex
	buckets map[workspaceBudgetKey]*workspaceWriteBucket
}

type workspaceBudgetKey struct {
	attemptID model.ExamAttemptID
	kind      model.AttemptWorkspaceAuthorKind
}

type workspaceWriteBucket struct {
	// rate is the rate the bucket was last spent at, which a sweep refills
	// it by.
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

// spend takes mutations and bytes from the bucket of the Attempt and author
// kind, at the kind's rate for resources (the environment's profile; zero
// for other kinds). It takes nothing and reports false when either would go
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

// sweep forgets every bucket that has refilled completely.
func (b *workspaceWriteBudget) sweep(now time.Time) {
	for key, bucket := range b.buckets {
		rate := bucket.rate
		refill(bucket, rate, now)
		if bucket.mutations >= rate.mutationBurst && bucket.bytes >= rate.byteBurst {
			delete(b.buckets, key)
		}
	}
}
