// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package jobs

import (
	"context"
	"time"

	"github.com/sudosylabs/proctor/server/app/job"
	"github.com/sudosylabs/proctor/server/store"
)

const (
	// LostAttemptSweepTaskName names the task that recovers attempts whose
	// node stopped heartbeating.
	LostAttemptSweepTaskName = "job-lost-attempt-sweep"
	// LostAttemptSweepInterval is how often a node offers to sweep. A lost
	// attempt waits at most its lease plus this before it is requeued.
	LostAttemptSweepInterval = 10 * time.Second
	lostAttemptSweepBatch    = 100
	// lostAttemptSweepBatches bounds one run, so a large recovery after an
	// outage spreads over several runs instead of holding one transaction.
	lostAttemptSweepBatches = 10
)

// LostAttemptRecovery is the store operation the sweep needs.
type LostAttemptRecovery interface {
	RecoverLostAttempts(ctx context.Context, limit int) (int, error)
}

// LostAttemptSweep recovers lost attempts in bounded batches. Only one node
// sweeps at a time; the store reports nothing recovered on the others.
type LostAttemptSweep struct {
	Jobs LostAttemptRecovery
}

// Run sweeps until a batch comes back short or the run's bound is reached.
func (r LostAttemptSweep) Run(ctx context.Context) error {
	for range lostAttemptSweepBatches {
		recovered, err := r.Jobs.RecoverLostAttempts(ctx, lostAttemptSweepBatch)
		if err != nil || recovered < lostAttemptSweepBatch {
			return err
		}
	}
	return nil
}

// NewLostAttemptSweepTask returns the periodic sweep over jobs.
func NewLostAttemptSweepTask(jobs store.JobStore) job.PeriodicTask {
	return job.PeriodicTask{
		Name:     LostAttemptSweepTaskName,
		Interval: LostAttemptSweepInterval,
		Runner:   LostAttemptSweep{Jobs: jobs},
	}
}
