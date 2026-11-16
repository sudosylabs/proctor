// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package jobs

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/sudosylabs/proctor/server/app/job"
	"github.com/sudosylabs/proctor/server/model"
	"github.com/sudosylabs/proctor/server/store"
)

type commandOutcomeCleanerFake struct {
	store.CommandOutcomeStore
	limit   int
	deleted int64
	err     error
}

func (f *commandOutcomeCleanerFake) DeleteExpired(_ context.Context, limit int) (int64, error) {
	f.limit = limit
	return f.deleted, f.err
}

func TestCommandOutcomeCleanupDeletesOneBoundedPage(t *testing.T) {
	cleaner := &commandOutcomeCleanerFake{deleted: 17}
	command, _ := json.Marshal(CommandOutcomeCleanupCommand{BatchSize: 500})
	at := time.Now().UTC()
	record, err := model.NewJob(model.NewJobID(), model.JobTypeCommandOutcomeCleanup, command, "cleanup", at, at, 3)
	require.NoError(t, err)
	outcome := (commandOutcomeCleanupHandler{outcomes: cleaner}).Run(context.Background(), job.NewExecution(record, nil, nil, nil))
	require.Equal(t, job.OutcomeSucceeded, outcome.Kind, "outcome = %#v, limit", outcome)
	require.Equal(t, 500, cleaner.limit, "outcome = %#v, limit", outcome)
	var result CommandOutcomeCleanupResult
	err = json.Unmarshal(outcome.Result, &result)
	require.NoError(t, err, "result = %#v, err", result)
	require.Equal(t, int64(17), result.Deleted, "result = %#v, err", result)
}

func TestCommandOutcomeCleanupContinuesOnlyAfterAFullBatch(t *testing.T) {
	at := time.Date(2026, time.November, 14, 9, 0, 0, 0, time.UTC)
	command, _ := json.Marshal(CommandOutcomeCleanupCommand{BatchSize: 500})
	parent, err := model.NewJob(model.NewJobID(), model.JobTypeCommandOutcomeCleanup, command, "cleanup", at, at, 3)
	require.NoError(t, err)

	for _, tc := range []struct {
		name      string
		deleted   int64
		successor bool
	}{
		{name: "full batch", deleted: 500, successor: true},
		{name: "short batch", deleted: 499, successor: false},
		{name: "nothing left", deleted: 0, successor: false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			jobs := &deduplicatingJobEnqueuerFake{jobs: map[string]*model.Job{}}
			handler := commandOutcomeCleanupHandler{
				outcomes:      &commandOutcomeCleanerFake{deleted: tc.deleted},
				continuations: jobs,
				now:           func() time.Time { return at },
			}

			outcome := handler.Run(context.Background(), job.NewExecution(parent, nil, nil, nil))
			require.Equal(t, job.OutcomeSucceeded, outcome.Kind, "outcome = %#v", outcome)
			// A retry of the same parent must not fork the chain.
			require.Equal(t, job.OutcomeSucceeded, handler.Run(context.Background(), job.NewExecution(parent, nil, nil, nil)).Kind)

			if !tc.successor {
				require.Empty(t, jobs.jobs)
				return
			}
			require.Len(t, jobs.jobs, 1)
			for _, successor := range jobs.jobs {
				require.Equal(t, model.JobTypeCommandOutcomeCleanup, successor.Type)
				require.Equal(t, "command-outcome-cleanup:after:"+parent.ID.String(), successor.DedupeKey)
				require.Equal(t, model.JobDedupePermanent, successor.DedupePolicy)
				require.True(t, successor.AvailableAt.After(at), "the successor waits before it runs")
				require.JSONEq(t, string(command), string(successor.Command))
			}
		})
	}
}

func TestCommandOutcomeCleanupRetriesWhenItCannotEnqueueItsSuccessor(t *testing.T) {
	at := time.Date(2026, time.November, 14, 9, 0, 0, 0, time.UTC)
	command, _ := json.Marshal(CommandOutcomeCleanupCommand{BatchSize: 500})
	parent, err := model.NewJob(model.NewJobID(), model.JobTypeCommandOutcomeCleanup, command, "cleanup", at, at, 3)
	require.NoError(t, err)
	handler := commandOutcomeCleanupHandler{
		outcomes:      &commandOutcomeCleanerFake{deleted: 500},
		continuations: failingJobEnqueuer{},
		now:           func() time.Time { return at },
	}

	outcome := handler.Run(context.Background(), job.NewExecution(parent, nil, nil, nil))
	require.Equal(t, job.OutcomeRetryableFailure, outcome.Kind)
	require.Equal(t, "dependency.unavailable", outcome.PublicErrorCode)
}
