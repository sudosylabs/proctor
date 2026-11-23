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
	"errors"
	"time"

	"github.com/sudosylabs/proctor/server/app/job"
	"github.com/sudosylabs/proctor/server/model"
	"github.com/sudosylabs/proctor/server/store"
)

// exportSweep is one kind of export's cleanup: expire what is due, then remove
// the stored files no published export points at. A is the kind's artifact
// identity.
type exportSweep[A any] struct {
	// label names the kind in Job errors, and code is its public error code.
	label string
	code  string
	// reconcile expires or fails up to n exports and reports how many.
	reconcile func(ctx context.Context, n int) (int, error)
	// begin selects and defers up to n artifacts due for removal.
	begin func(ctx context.Context, n int) ([]A, error)
	valid func(A) bool
	// purge removes the artifact's file; complete records its absence.
	purge    func(context.Context, A) error
	complete func(context.Context, A) error
}

type exportCleanupCommand struct {
	BatchSize int `json:"batch_size"`
}

// exportCleanupHandler runs one sweep. Every removal is checked: a file is
// forgotten only after its absence is observed.
type exportCleanupHandler[A any] struct{ sweep *exportSweep[A] }

func (h exportCleanupHandler[A]) Run(ctx context.Context, e job.Execution) job.Outcome {
	var command exportCleanupCommand
	s := h.sweep
	if s == nil ||
		s.reconcile == nil ||
		s.begin == nil ||
		s.purge == nil ||
		s.complete == nil ||
		model.DecodeStrictJobDocument(e.Job.Command, &command) != nil ||
		command.BatchSize < 1 ||
		command.BatchSize > 100 ||
		e.Job.WorkReserved < 0 ||
		e.Job.WorkReserved > 3 {
		return job.PermanentFailure("job.command.invalid", errors.New("invalid "+h.label()+" cleanup command"))
	}
	if s.valid == nil {
		return job.PermanentFailure("job.command.invalid", errors.New("invalid "+h.label()+" cleanup command"))
	}

	reserved, err := e.ReserveWork(ctx, 1, 3)
	if err != nil {
		return job.RetryableFailure(s.code, errors.New(s.label+" cleanup reservation failed"))
	}
	if !reserved {
		return job.PermanentFailure(s.code, errors.New(s.label+" cleanup budget exhausted"))
	}

	reconciled, err := s.reconcile(ctx, command.BatchSize)
	if err != nil || reconciled < 0 || reconciled > command.BatchSize {
		return job.RetryableFailure(s.code, errors.New(s.label+" expiry reconciliation failed"))
	}

	artifacts, err := s.begin(ctx, command.BatchSize)
	if err != nil || len(artifacts) > command.BatchSize {
		return job.RetryableFailure(s.code, errors.New(s.label+" cleanup inventory failed"))
	}

	purged, failed := 0, false
	for _, a := range artifacts {
		if err = ctx.Err(); err != nil {
			return job.RetryableFailure(s.code, errors.New(s.label+" cleanup interrupted"))
		}

		if !s.valid(a) {
			failed = true
			continue
		}

		if err = s.purge(ctx, a); err != nil {
			failed = true
			continue
		}

		if err = s.complete(ctx, a); err != nil {
			failed = true
			continue
		}

		purged++
	}

	if failed {
		return job.RetryableFailure(s.code, errors.New(s.label+" cleanup remains incomplete"))
	}

	result, _ := json.Marshal(struct {
		Reconciled     int `json:"reconciled"`
		ObservedAbsent int `json:"observed_absent"`
	}{reconciled, purged})
	return job.Outcome{Kind: job.OutcomeSucceeded, Result: result}
}

func (h exportCleanupHandler[A]) label() string {
	if h.sweep == nil {
		return "export"
	}
	return h.sweep.label
}

func exportCleanupDescriptor(jobType model.JobType, code string, handler job.Handler) job.Descriptor {
	descriptor := job.NewDescriptor(jobType, handler)
	descriptor.PublicErrorCodes = []string{"job.command.invalid", code}
	descriptor.BaseRetryDelay = time.Minute
	descriptor.MaximumRetryDelay = 5 * time.Minute
	descriptor.SuccessRetention = 7 * 24 * time.Hour
	descriptor.FailureRetention = 7 * 24 * time.Hour
	return descriptor
}

// exportCleanupProposer enqueues one cleanup Job per hour, deduplicated by
// the hour, for one kind of export.
type exportCleanupProposer struct {
	jobs    store.JobStore
	now     func() time.Time
	jobType model.JobType
	dedupe  string
}

func (p exportCleanupProposer) Propose(ctx context.Context, occurrence time.Time) error {
	at := model.TimeUTC(p.now())
	command, _ := json.Marshal(exportCleanupCommand{BatchSize: 100})
	job, err := model.NewJobWithDedupePolicy(
		model.NewJobID(),
		p.jobType,
		command,
		p.dedupe+":"+model.TimeUTC(occurrence).Format("2006-01-02T15"),
		model.JobDedupePermanent,
		at,
		at,
		3,
	)
	if err != nil {
		return err
	}

	_, _, err = p.jobs.Enqueue(ctx, &store.JobEnqueue{Job: job})
	return err
}
