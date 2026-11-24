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
				require.NoError(t, err)
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
	_, err = tx.Exec(ctx, `SET LOCAL enable_seqscan = off`)
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
