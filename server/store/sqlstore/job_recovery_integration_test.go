//go:build integration

// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package sqlstore

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/lib/pq"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/sudosylabs/proctor/server/model"
	"github.com/sudosylabs/proctor/server/store"
)

func enqueueRecoveryJob(t *testing.T, ctx context.Context, persistence *SQLStore, jobType model.JobType, at time.Time) *model.Job {
	t.Helper()
	job, err := model.NewJob(model.NewJobID(), jobType,
		json.RawMessage(`{"user_id":"`+model.NewUserID().String()+`"}`), "recovery:"+model.NewID(), at, at, 3)
	require.NoError(t, err)
	_, inserted, err := persistence.Job().Enqueue(ctx, &store.JobEnqueue{Job: job})
	require.NoError(t, err)
	require.True(t, inserted)
	return job
}

func newRecoveryClaimToken(t *testing.T) model.JobClaimToken {
	t.Helper()
	token, err := model.NewJobClaimToken()
	require.NoError(t, err)
	return token
}

// TestJobClaimWithNothingDueWritesNothing pins that a claim finding nothing
// due leaves a lost attempt alone: recovery is the sweep's job alone.
func TestJobClaimWithNothingDueWritesNothing(t *testing.T) {
	ctx := context.Background()
	persistence := openTestStore(t)
	resetPristineTestStore(t, persistence)

	lost := enqueueRecoveryJob(t, ctx, persistence, model.JobTypeProfilePictureGenerateDefault, model.NowUTC().Add(-time.Minute))
	_, err := persistence.Job().ClaimNext(ctx, &store.JobClaimRequest{Types: []model.JobType{lost.Type}, NodeID: "dead-node",
		ClaimToken: newRecoveryClaimToken(t), LeaseDuration: 20 * time.Millisecond})
	require.NoError(t, err)
	time.Sleep(40 * time.Millisecond)

	_, err = persistence.Job().ClaimNext(ctx, &store.JobClaimRequest{Types: []model.JobType{lost.Type}, NodeID: "live-node",
		ClaimToken: newRecoveryClaimToken(t), LeaseDuration: time.Minute})
	require.True(t, store.IsNotFound(err), "nothing is due: %v", err)

	var status string
	require.NoError(t, persistence.GetMaster().Get(ctx, &status,
		`SELECT status FROM job_attempts WHERE job_id = ?`, lost.ID.String()))
	require.Equal(t, "running", status, "a claim never ends a lost attempt")
}

// TestJobLostAttemptsAreRecoveredOnceAcrossSweepers runs three sweeps at
// once, as three nodes would: every lost attempt is ended exactly once, and
// no sweep waits on another's locks.
func TestJobLostAttemptsAreRecoveredOnceAcrossSweepers(t *testing.T) {
	ctx := context.Background()
	persistence := openTestStore(t)
	resetPristineTestStore(t, persistence)

	const lostJobs = 30
	for range lostJobs {
		job := enqueueRecoveryJob(t, ctx, persistence, model.JobTypeProfilePictureGenerateDefault, model.NowUTC().Add(-time.Minute))
		_, err := persistence.Job().ClaimNext(ctx, &store.JobClaimRequest{Types: []model.JobType{job.Type}, NodeID: "dead-node",
			ClaimToken: newRecoveryClaimToken(t), LeaseDuration: 20 * time.Millisecond})
		require.NoError(t, err)
	}
	time.Sleep(40 * time.Millisecond)

	var (
		wait      sync.WaitGroup
		mu        sync.Mutex
		recovered int
	)
	for range 3 {
		wait.Add(1)
		go func() {
			defer wait.Done()
			for {
				count, err := persistence.Job().RecoverLostAttempts(ctx, 7)
				if !assert.NoError(t, err) {
					return
				}
				mu.Lock()
				recovered += count
				done := recovered >= lostJobs
				mu.Unlock()
				if done || count == 0 {
					return
				}
			}
		}()
	}
	wait.Wait()
	// A sweeper that lost the lock recovers nothing and stops; whatever is
	// left is swept by the next round, as the periodic task would.
	for {
		count, err := persistence.Job().RecoverLostAttempts(ctx, 100)
		require.NoError(t, err)
		recovered += count
		if count == 0 {
			break
		}
	}
	require.Equal(t, lostJobs, recovered, "each lost attempt is recovered exactly once")

	var expired int
	require.NoError(t, persistence.GetMaster().Get(ctx, &expired,
		`SELECT count(*) FROM job_attempts WHERE status = 'lease_expired'`))
	require.Equal(t, lostJobs, expired)
	var queued int
	require.NoError(t, persistence.GetMaster().Get(ctx, &queued,
		`SELECT count(*) FROM jobs WHERE status = 'queued' AND available_at > updated_at`))
	require.Equal(t, lostJobs, queued, "every recovered Job waits out its backoff")
}

// TestJobClaimReadsOnlyItsOwnType pins the type-first claim index: a backlog
// of one type is not scanned by another type's claim.
func TestJobClaimReadsOnlyItsOwnType(t *testing.T) {
	ctx := context.Background()
	persistence := openTestStore(t)
	resetPristineTestStore(t, persistence)

	tx, err := persistence.GetMaster().Begin(ctx)
	require.NoError(t, err)
	defer func() { _ = tx.Rollback() }()
	// The planner chooses by statistics, so give it a queue shaped like a busy
	// one: many due Jobs spread over every type. On a near-empty table either
	// index costs the same and the choice is arbitrary.
	types := make([]string, 0, len(model.JobTypes()))
	for _, jobType := range model.JobTypes() {
		types = append(types, string(jobType))
	}
	_, err = tx.Exec(ctx, `INSERT INTO jobs (id, type, status, created_at, updated_at, available_at, command, dedupe_key, maximum_attempts)
		SELECT translate(lpad(n::text, 26, '0'), '0123456789', 'ybndrfg8ej'), (?::text[])[1 + n % cardinality(?::text[])], 'queued',
			now() - interval '1 hour', now() - interval '1 hour', now() - interval '1 hour', '{}', 'plan-' || n, 3
		FROM generate_series(1, 20000) AS n`, pq.Array(types), pq.Array(types))
	require.NoError(t, err)
	_, err = tx.Exec(ctx, `ANALYZE jobs`)
	require.NoError(t, err)
	var plan []string
	require.NoError(t, tx.Select(ctx, &plan, `EXPLAIN SELECT id FROM jobs
		WHERE status = 'queued'
			AND available_at <= CURRENT_TIMESTAMP
			AND attempt_count < maximum_attempts
			AND `+claimTypeCondition([]string{string(model.JobTypeMailDeliver)})+`
		ORDER BY available_at, created_at, id
		FOR UPDATE SKIP LOCKED
		LIMIT 1`, string(model.JobTypeMailDeliver)))
	joined := strings.Join(plan, "\n")
	require.Contains(t, joined, "jobs_claim_type_idx", "plan:\n%s", joined)
}

// TestJobQueueDepthSplitsDueFromScheduled pins the metrics count: only queued
// Jobs of the listed types count, split at the database's now.
func TestJobQueueDepthSplitsDueFromScheduled(t *testing.T) {
	ctx := context.Background()
	persistence := openTestStore(t)
	resetPristineTestStore(t, persistence)

	now := model.NowUTC()
	enqueueRecoveryJob(t, ctx, persistence, model.JobTypeProfilePictureGenerateDefault, now.Add(-time.Minute))
	enqueueRecoveryJob(t, ctx, persistence, model.JobTypeProfilePictureGenerateDefault, now.Add(-time.Second))
	enqueueRecoveryJob(t, ctx, persistence, model.JobTypeProfilePictureGenerateDefault, now.Add(time.Hour))
	enqueueRecoveryJob(t, ctx, persistence, model.JobTypeMailDeliver, now.Add(-time.Minute))
	claimed := enqueueRecoveryJob(t, ctx, persistence, model.JobTypeCleanup, now.Add(-time.Minute))
	_, err := persistence.Job().ClaimNext(ctx, &store.JobClaimRequest{Types: []model.JobType{claimed.Type}, NodeID: "node-a",
		ClaimToken: newRecoveryClaimToken(t), LeaseDuration: time.Minute})
	require.NoError(t, err)

	depth, err := persistence.Job().QueueDepth(ctx, []model.JobType{model.JobTypeProfilePictureGenerateDefault, model.JobTypeCleanup})
	require.NoError(t, err)
	require.Equal(t, map[model.JobType]store.JobQueueDepth{
		model.JobTypeProfilePictureGenerateDefault: {Due: 2, Scheduled: 1},
	}, depth, "a running Job and an unlisted type do not count")
}
