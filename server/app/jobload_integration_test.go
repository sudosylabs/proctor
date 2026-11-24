//go:build integration && jobload

// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package app

// The job load harness measures what the job engine costs PostgreSQL. It is
// not part of CI: run it by hand against a database you can reset, for
// example
//
//	PROCTOR_TEST_DATABASE_URL=postgres://proctor:proctor@127.0.0.1:25432/proctor?sslmode=disable \
//	  go test -tags 'integration jobload' -run TestJobLoad -v -timeout 30m ./app/
//
// PROCTOR_JOBLOAD_IDLE sets how long the idle scenario runs (default 1m).

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"slices"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/sudosylabs/proctor/server/app/job"
	"github.com/sudosylabs/proctor/server/app/jobs"
	"github.com/sudosylabs/proctor/server/model"
	"github.com/sudosylabs/proctor/server/store"
	"github.com/sudosylabs/proctor/server/store/sqlstore"
)

const jobLoadNodes = 3

// jobLoadCounters is one snapshot of the counters the harness compares.
// sessions counts every backend PostgreSQL started, including a container
// health check's; opened counts only the nodes' own pool connections.
type jobLoadCounters struct {
	commits, rollbacks, sessions, jobUpdates int64
	opened                                   uint64
}

func readJobLoadCounters(t *testing.T, persistence *sqlstore.SQLStore, nodes []*sqlstore.SQLStore) jobLoadCounters {
	t.Helper()
	// An idle backend flushes its statistics up to ten seconds late, so
	// wait them out before reading.
	time.Sleep(11 * time.Second)
	var counters jobLoadCounters
	for _, node := range nodes {
		for _, pool := range node.PoolStats() {
			counters.opened += pool.Opened
		}
	}
	row := persistence.GetMaster().DB().QueryRowContext(context.Background(), `SELECT
			d.xact_commit, d.xact_rollback, d.sessions,
			(SELECT n_tup_upd FROM pg_stat_user_tables WHERE relname = 'jobs')
		FROM pg_stat_database d WHERE d.datname = current_database()`)
	require.NoError(t, row.Scan(&counters.commits, &counters.rollbacks, &counters.sessions, &counters.jobUpdates))
	return counters
}

func (c jobLoadCounters) report(t *testing.T, scenario string, since jobLoadCounters, elapsed time.Duration) jobLoadCounters {
	t.Helper()
	delta := jobLoadCounters{
		commits: c.commits - since.commits, rollbacks: c.rollbacks - since.rollbacks,
		sessions: c.sessions - since.sessions, jobUpdates: c.jobUpdates - since.jobUpdates,
		opened: c.opened - since.opened,
	}
	seconds := elapsed.Seconds()
	t.Logf("%s over %s: %d commits (%.1f/s), %d rollbacks (%.1f/s), %d new backends, %d pool connections opened, %d job row updates",
		scenario, elapsed.Round(time.Second), delta.commits, float64(delta.commits)/seconds,
		delta.rollbacks, float64(delta.rollbacks)/seconds, delta.sessions, delta.opened, delta.jobUpdates)
	return delta
}

// jobLoadHandler succeeds after an optional pause and counts its runs.
type jobLoadHandler struct {
	work time.Duration
	runs *atomic.Int64
}

func (h jobLoadHandler) Run(ctx context.Context, _ job.Execution) job.Outcome {
	if h.work > 0 {
		select {
		case <-time.After(h.work):
		case <-ctx.Done():
		}
	}
	h.runs.Add(1)
	return job.Outcome{Kind: job.OutcomeSucceeded, Result: json.RawMessage(`{}`)}
}

// jobLoadDescriptors registers every production job type with two workers
// each, about the claim loops a production node runs.
func jobLoadDescriptors(runs *atomic.Int64) []job.Descriptor {
	types := model.JobTypes()
	descriptors := make([]job.Descriptor, 0, len(types))
	for _, jobType := range types {
		descriptors = append(descriptors, job.Descriptor{
			Type: jobType, PublicErrorCodes: []string{"dependency.unavailable"}, Timeout: time.Minute, Concurrency: 2,
			LeaseDuration: 30 * time.Second, HeartbeatInterval: 10 * time.Second,
			BaseRetryDelay: time.Second, MaximumRetryDelay: time.Minute,
			Visibility: job.VisibilityOperator, SuccessRetention: 24 * time.Hour, FailureRetention: 48 * time.Hour,
			Handler: jobLoadHandler{runs: runs},
		})
	}
	return descriptors
}

// startJobLoadEngine runs one node's engine as production configures it.
func startJobLoadEngine(t *testing.T, persistence *sqlstore.SQLStore, nodeID string, runs *atomic.Int64) {
	t.Helper()
	engine, err := job.New(job.Dependencies{
		Store: persistence.Job(), Descriptors: jobLoadDescriptors(runs), NodeID: nodeID,
		Diagnostics: newDiscardLogger(t), Policy: job.Policy{BackstopInterval: 30 * time.Second},
		Notifications: persistence,
		PeriodicTasks: []job.PeriodicTask{jobs.NewLostAttemptSweepTask(persistence.Job(), nil)},
	})
	require.NoError(t, err)
	require.NoError(t, engine.Start(context.Background()))
	t.Cleanup(func() { require.NoError(t, engine.Close(context.Background())) })
}

func enqueueJobLoad(t *testing.T, persistence *sqlstore.SQLStore, jobType model.JobType, count int, at time.Time) {
	t.Helper()
	var wait sync.WaitGroup
	errs := make(chan error, 8)
	for worker := range 8 {
		wait.Go(func() {
			for index := worker; index < count; index += 8 {
				record, err := model.NewJob(model.NewJobID(), jobType,
					json.RawMessage(`{"user_id":"`+model.NewUserID().String()+`"}`), "load:"+model.NewID(), at, at, 3)
				if err == nil {
					_, _, err = persistence.Job().Enqueue(context.Background(), &store.JobEnqueue{Job: record})
				}
				if err != nil {
					errs <- err
					return
				}
			}
		})
	}
	wait.Wait()
	close(errs)
	for err := range errs {
		require.NoError(t, err)
	}
}

func jobLoadIdleDuration(t *testing.T) time.Duration {
	t.Helper()
	value := os.Getenv("PROCTOR_JOBLOAD_IDLE")
	if value == "" {
		return time.Minute
	}
	duration, err := time.ParseDuration(value)
	require.NoError(t, err)
	return duration
}

// measureLeaseRenewals renews a serving lease on the control pool twice a
// second until stop closes and returns each renewal's duration. A failed
// renewal counts as the whole five-second budget.
func measureLeaseRenewals(t *testing.T, persistence *sqlstore.SQLStore, nodeID string, stop <-chan struct{}) <-chan []time.Duration {
	t.Helper()
	result := make(chan []time.Duration, 1)
	claim := store.ServingNodeLeaseClaim{NodeID: nodeID, LeaseID: model.NewID(), Lifetime: 30 * time.Second}
	go func() {
		var durations []time.Duration
		ticker := time.NewTicker(500 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-stop:
				result <- durations
				return
			case <-ticker.C:
			}
			startedAt := time.Now()
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			_, err := persistence.ServingNodeLease().Upsert(ctx, &claim)
			cancel()
			if err != nil {
				durations = append(durations, 5*time.Second)
				continue
			}
			durations = append(durations, time.Since(startedAt))
		}
	}()
	return result
}

func percentile(durations []time.Duration, fraction float64) time.Duration {
	if len(durations) == 0 {
		return 0
	}
	sorted := slices.Clone(durations)
	slices.Sort(sorted)
	return sorted[min(int(float64(len(sorted))*fraction), len(sorted)-1)]
}

// TestJobLoad runs the idle and burst scenarios against three nodes sharing
// one database, and reports what each cost PostgreSQL.
func TestJobLoad(t *testing.T) {
	observer := openClusteredJobIntegrationStore(t)
	var runs atomic.Int64
	nodes := make([]*sqlstore.SQLStore, jobLoadNodes)
	for index := range nodes {
		nodes[index] = openClusteredJobIntegrationStore(t)
	}
	for index, node := range nodes {
		startJobLoadEngine(t, node, fmt.Sprintf("load-node-%d", index), &runs)
	}
	// Thirty Jobs due next month keep every type's queue non-empty but idle.
	for _, jobType := range model.JobTypes() {
		enqueueJobLoad(t, observer, jobType, 1, model.NowUTC().AddDate(0, 1, 0))
	}
	// Let the nodes start and read the queue once.
	time.Sleep(5 * time.Second)

	idle := jobLoadIdleDuration(t)
	stopProbe := make(chan struct{})
	probe := measureLeaseRenewals(t, observer, "idle-probe", stopProbe)
	before := readJobLoadCounters(t, observer, nodes)
	time.Sleep(idle)
	idleDelta := readJobLoadCounters(t, observer, nodes).report(t, "idle", before, idle+11*time.Second)
	close(stopProbe)
	idleLease := <-probe
	t.Logf("idle lease renewal p50 %s, p99 %s", percentile(idleLease, 0.5), percentile(idleLease, 0.99))

	stopProbe = make(chan struct{})
	probe = measureLeaseRenewals(t, observer, "burst-probe", stopProbe)
	before = readJobLoadCounters(t, observer, nodes)
	startedAt := time.Now()
	enqueueJobLoad(t, observer, model.JobTypeMailDeliver, 10000, model.NowUTC())
	enqueueJobLoad(t, observer, model.JobTypeCleanup, 500, model.NowUTC())
	require.Eventually(t, func() bool { return runs.Load() >= 10500 }, 15*time.Minute, 100*time.Millisecond, "burst drained")
	drained := time.Since(startedAt)
	readJobLoadCounters(t, observer, nodes).report(t, "burst", before, drained+11*time.Second)
	close(stopProbe)
	burstLease := <-probe
	t.Logf("burst of 10500 drained in %s; lease renewal p50 %s, p99 %s",
		drained.Round(time.Millisecond), percentile(burstLease, 0.5), percentile(burstLease, 0.99))

	// Idle nodes claim nothing; the only rollbacks are the sweeps that find
	// another node holding the sweep lock.
	require.Less(t, float64(idleDelta.rollbacks)/idle.Seconds(), 1.0, "idle rollbacks per second")
	require.Zero(t, idleDelta.opened, "idle nodes opened no new pool connections")
	// Half the renewal budget, the ProctorServingLeaseRenewalSlow threshold.
	// A shared laptop's scheduling noise alone reaches about a second.
	require.Less(t, percentile(burstLease, 0.99), 2500*time.Millisecond, "lease renewal p99 under burst")
}
