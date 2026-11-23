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

// ExamExportPurger removes an expired export's stored content, fenced by the
// job attempt that claimed the purge.
type ExamExportPurger interface {
	PurgeExamExport(context.Context, model.ExamExportID, model.JobAttemptID) error
}

type examExportBuildHandler struct{ service *exam.Exports }
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
		model.DecodeStrictJobDocument(e.Job.Command, &command) != nil ||
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
		// SQL and provider details and archive content never enter job logs.
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

func (h examExportCleanupHandler) Run(ctx context.Context, e job.Execution) job.Outcome {
	return exportCleanupHandler[store.ExamExportArtifact]{sweep: h.sweep()}.Run(ctx, e)
}

// sweep is the exam export's cleanup on the shared export layer. A missing
// collaborator leaves its steps nil, which the shared handler refuses.
func (h examExportCleanupHandler) sweep() *exportSweep[store.ExamExportArtifact] {
	sweep := &exportSweep[store.ExamExportArtifact]{
		label: "Exam export",
		code:  "exam.export.unavailable",
		valid: func(a store.ExamExportArtifact) bool { return a.ExportID.IsValid() && a.AttemptID.IsValid() },
	}
	if h.exports == nil || h.content == nil {
		return sweep
	}
	sweep.reconcile = h.exports.Reconcile
	sweep.begin = h.exports.BeginPurgeBatch
	sweep.complete = h.exports.CompletePurge
	sweep.purge = func(ctx context.Context, a store.ExamExportArtifact) error {
		return h.content.PurgeExamExport(ctx, a.ExportID, a.AttemptID)
	}
	return sweep
}

func examExportCleanupDescriptor(handler job.Handler) job.Descriptor {
	return exportCleanupDescriptor(model.JobTypeExamExportCleanup, "exam.export.unavailable", handler)
}

type examExportCleanupProposer struct {
	jobs store.JobStore
	now  func() time.Time
}

func (p examExportCleanupProposer) Propose(ctx context.Context, occurrence time.Time) error {
	return exportCleanupProposer{jobs: p.jobs, now: p.now, jobType: model.JobTypeExamExportCleanup, dedupe: "exam-export-cleanup"}.Propose(ctx, occurrence)
}

// NewExamExportBuild registers the build of a requested exam export.
func NewExamExportBuild(exports *exam.Exports) job.Registration {
	return job.Registration{Descriptor: examExportBuildDescriptor(examExportBuildHandler{service: exports})}
}

// NewExamExportCleanup registers the removal of expired exam exports and
// their content, proposed hourly.
func NewExamExportCleanup(exports store.ExamExportStore, content ExamExportPurger, jobs store.JobStore, now func() time.Time) job.Registration {
	return job.Registration{
		Descriptor: examExportCleanupDescriptor(examExportCleanupHandler{exports: exports, content: content}),
		Recurrence: &job.Recurrence{Name: "exam-export-cleanup", Interval: time.Hour, Proposer: examExportCleanupProposer{jobs: jobs, now: now}},
	}
}
