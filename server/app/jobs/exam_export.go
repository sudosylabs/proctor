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

	"github.com/sudosylabs/proctor/server/app/exam"
	"github.com/sudosylabs/proctor/server/app/invocation"
	"github.com/sudosylabs/proctor/server/app/job"
	"github.com/sudosylabs/proctor/server/model"
	"github.com/sudosylabs/proctor/server/store"
)

// examExportBuilder is the part of *exam.Exports the build Job drives. It is
// a seam because the handler test fakes it: the real service builds and
// publishes an archive.
type examExportBuilder interface {
	BuildFromJob(context.Context, invocation.System, store.ExamExportBuild) error
}

// ExamExportPurger removes the stored content of an expired Exam export,
// fenced by the Job Attempt that claimed the purge.
type ExamExportPurger interface {
	PurgeExamExport(context.Context, model.ExamExportID, model.JobAttemptID) error
}

type examExportBuildHandler struct{ service examExportBuilder }
type examExportBuildCommand struct {
	ExportID model.ExamExportID `json:"export_id"`
}

func (h examExportBuildHandler) Run(ctx context.Context, e job.Execution) job.Outcome {
	var command examExportBuildCommand
	if h.service == nil ||
		e.Attempt == nil ||
		!e.Job.ID.IsValid() ||
		!e.Attempt.ID.IsValid() ||
		!e.Attempt.ClaimToken.IsValid() ||
		e.Attempt.JobID != e.Job.ID ||
		job.DecodeStrictUniqueDocument(e.Job.Command, &command) != nil ||
		!command.ExportID.IsValid() ||
		e.Job.WorkReserved < 0 ||
		e.Job.WorkReserved > model.ExamExportMaximumAttempts {
		return job.PermanentFailure("job.command.invalid", errors.New("invalid Exam export build command"))
	}

	reserved, err := e.ReserveWork(ctx, 1, model.ExamExportMaximumAttempts)
	if err != nil {
		return job.RetryableFailure("exam.export.unavailable", errors.New("Exam export work reservation failed"))
	}
	if !reserved {
		return job.PermanentFailure("exam.export.unavailable", errors.New("Exam export construction budget exhausted"))
	}

	input := store.ExamExportBuild{
		ExamExportArtifact: store.ExamExportArtifact{ExportID: command.ExportID, AttemptID: e.Attempt.ID},
		JobID:              e.Job.ID,
		ClaimToken:         e.Attempt.ClaimToken,
	}
	if err = h.service.BuildFromJob(ctx, invocation.Job(e.Job.ID, e.Attempt.ID), input); err != nil {
		// SQL/provider details and archive content never enter ordinary Job logs.
		return job.RetryableFailure("exam.export.unavailable", errors.New("Exam export construction failed"))
	}

	return job.Outcome{Kind: job.OutcomeSucceeded, Result: json.RawMessage(`{"ready":true}`)}
}

func examExportBuildDescriptor(handler job.Handler) job.Descriptor {
	descriptor := job.NewDescriptor(model.JobTypeExamExportBuild, handler)
	descriptor.PublicErrorCodes = []string{"job.command.invalid", "exam.export.unavailable", "exam.export.expired"}
	descriptor.Timeout = 30 * time.Minute
	descriptor.BaseRetryDelay = time.Minute
	descriptor.MaximumRetryDelay = 5 * time.Minute
	descriptor.SuccessRetention = 7 * 24 * time.Hour
	descriptor.FailureRetention = 7 * 24 * time.Hour
	return descriptor
}

type examExportCleanupHandler struct {
	exports store.ExamExportStore
	content ExamExportPurger
}
type examExportCleanupCommand struct {
	BatchSize int `json:"batch_size"`
}

func (h examExportCleanupHandler) Run(ctx context.Context, e job.Execution) job.Outcome {
	var command examExportCleanupCommand
	if h.exports == nil ||
		h.content == nil ||
		job.DecodeStrictUniqueDocument(e.Job.Command, &command) != nil ||
		command.BatchSize < 1 ||
		command.BatchSize > 100 ||
		e.Job.WorkReserved < 0 ||
		e.Job.WorkReserved > 3 {
		return job.PermanentFailure("job.command.invalid", errors.New("invalid Exam export cleanup command"))
	}

	reserved, err := e.ReserveWork(ctx, 1, 3)
	if err != nil {
		return job.RetryableFailure("exam.export.unavailable", errors.New("Exam export cleanup reservation failed"))
	}
	if !reserved {
		return job.PermanentFailure("exam.export.unavailable", errors.New("Exam export cleanup budget exhausted"))
	}

	reconciled, err := h.exports.Reconcile(ctx, command.BatchSize)
	if err != nil || reconciled < 0 || reconciled > command.BatchSize {
		return job.RetryableFailure("exam.export.unavailable", errors.New("Exam export expiry reconciliation failed"))
	}

	artifacts, err := h.exports.BeginPurgeBatch(ctx, command.BatchSize)
	if err != nil || len(artifacts) > command.BatchSize {
		return job.RetryableFailure("exam.export.unavailable", errors.New("Exam export cleanup inventory failed"))
	}

	purged, failed := 0, false
	for _, a := range artifacts {
		if err = ctx.Err(); err != nil {
			return job.RetryableFailure("exam.export.unavailable", errors.New("Exam export cleanup interrupted"))
		}

		if !a.ExportID.IsValid() || !a.AttemptID.IsValid() {
			failed = true
			continue
		}

		if err = h.content.PurgeExamExport(ctx, a.ExportID, a.AttemptID); err != nil {
			failed = true
			continue
		}

		if err = h.exports.CompletePurge(ctx, a); err != nil {
			failed = true
			continue
		}

		purged++
	}

	if failed {
		return job.RetryableFailure("exam.export.unavailable", errors.New("Exam export cleanup remains incomplete"))
	}

	result, _ := json.Marshal(struct {
		Reconciled     int `json:"reconciled"`
		ObservedAbsent int `json:"observed_absent"`
	}{reconciled, purged})
	return job.Outcome{Kind: job.OutcomeSucceeded, Result: result}
}

func examExportCleanupDescriptor(handler job.Handler) job.Descriptor {
	descriptor := job.NewDescriptor(model.JobTypeExamExportCleanup, handler)
	descriptor.PublicErrorCodes = []string{"job.command.invalid", "exam.export.unavailable"}
	descriptor.BaseRetryDelay = time.Minute
	descriptor.MaximumRetryDelay = 5 * time.Minute
	descriptor.SuccessRetention = 7 * 24 * time.Hour
	descriptor.FailureRetention = 7 * 24 * time.Hour
	return descriptor
}

type examExportCleanupProposer struct {
	jobs store.JobStore
	now  func() time.Time
}

func (p examExportCleanupProposer) Propose(ctx context.Context, occurrence time.Time) error {
	at := model.TimeUTC(p.now())
	command, _ := json.Marshal(examExportCleanupCommand{BatchSize: 100})
	job, err := model.NewJobWithDedupePolicy(
		model.NewJobID(),
		model.JobTypeExamExportCleanup,
		command,
		"exam-export-cleanup:"+model.TimeUTC(occurrence).Format("2006-01-02T15"),
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

// NewExamExportBuild registers the build of a requested Exam export.
func NewExamExportBuild(exports *exam.Exports) job.Registration {
	return job.Registration{Descriptor: examExportBuildDescriptor(examExportBuildHandler{service: exports})}
}

// NewExamExportCleanup registers the removal of expired Exam exports and
// their content, proposed hourly.
func NewExamExportCleanup(exports store.ExamExportStore, content ExamExportPurger, jobs store.JobStore, now func() time.Time) job.Registration {
	return job.Registration{
		Descriptor: examExportCleanupDescriptor(examExportCleanupHandler{exports: exports, content: content}),
		Recurrence: &job.Recurrence{Name: "exam-export-cleanup", Interval: time.Hour, Proposer: examExportCleanupProposer{jobs: jobs, now: now}},
	}
}
