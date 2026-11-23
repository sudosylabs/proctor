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

	"github.com/sudosylabs/proctor/server/app/invocation"
	"github.com/sudosylabs/proctor/server/app/job"
	"github.com/sudosylabs/proctor/server/model"
	"github.com/sudosylabs/proctor/server/store"
)

// AuditExportBuilder builds one audit export for the Job that claimed it.
type AuditExportBuilder interface {
	BuildAuditExportFromJob(context.Context, invocation.System, store.AuditExportBuild) error
}

// AuditExportPurger removes an audit export's stored file.
type AuditExportPurger interface {
	PurgeAuditExport(context.Context, model.AuditExportID, model.JobAttemptID) error
}

type auditExportBuildHandler struct{ builder AuditExportBuilder }

type auditExportBuildCommand struct {
	ExportID model.AuditExportID `json:"export_id"`
}

func (h auditExportBuildHandler) Run(ctx context.Context, e job.Execution) job.Outcome {
	var command auditExportBuildCommand
	if h.builder == nil ||
		e.Attempt == nil ||
		!e.Job.ID.IsValid() ||
		!e.Attempt.ID.IsValid() ||
		!e.Attempt.ClaimToken.IsValid() ||
		e.Attempt.JobID != e.Job.ID ||
		model.DecodeStrictJobDocument(e.Job.Command, &command) != nil ||
		!command.ExportID.IsValid() ||
		e.Job.WorkReserved < 0 ||
		e.Job.WorkReserved > model.AuditExportMaximumAttempts {
		return job.PermanentFailure("job.command.invalid", errors.New("invalid audit export build command"))
	}

	reserved, err := e.ReserveWork(ctx, 1, model.AuditExportMaximumAttempts)
	if err != nil {
		return job.RetryableFailure("audit.export.unavailable", errors.New("audit export work reservation failed"))
	}
	if !reserved {
		return job.PermanentFailure("audit.export.unavailable", errors.New("audit export build budget exhausted"))
	}

	input := store.AuditExportBuild{
		AuditExportArtifact: store.AuditExportArtifact{ExportID: command.ExportID, AttemptID: e.Attempt.ID},
		JobID:               e.Job.ID,
		ClaimToken:          e.Attempt.ClaimToken,
	}
	if err = h.builder.BuildAuditExportFromJob(ctx, invocation.Job(e.Job.ID, e.Attempt.ID), input); err != nil {
		// SQL and provider details and event content never enter job logs.
		return job.RetryableFailure("audit.export.unavailable", errors.New("audit export build failed"))
	}
	return job.Outcome{Kind: job.OutcomeSucceeded, Result: json.RawMessage(`{"ready":true}`)}
}

func auditExportBuildDescriptor(handler job.Handler) job.Descriptor {
	descriptor := job.NewDescriptor(model.JobTypeAuditExportBuild, handler)
	descriptor.PublicErrorCodes = []string{"job.command.invalid", "audit.export.unavailable", "audit.export.expired"}
	descriptor.Timeout = 30 * time.Minute
	descriptor.BaseRetryDelay = time.Minute
	descriptor.MaximumRetryDelay = 5 * time.Minute
	descriptor.SuccessRetention = 7 * 24 * time.Hour
	descriptor.FailureRetention = 7 * 24 * time.Hour
	return descriptor
}

func auditExportSweep(exports store.AuditExportStore, content AuditExportPurger) *exportSweep[store.AuditExportArtifact] {
	sweep := &exportSweep[store.AuditExportArtifact]{
		label: "Audit export",
		code:  "audit.export.unavailable",
		valid: func(a store.AuditExportArtifact) bool { return a.ExportID.IsValid() && a.AttemptID.IsValid() },
	}
	if exports == nil || content == nil {
		return sweep
	}
	sweep.reconcile = exports.Reconcile
	sweep.begin = exports.BeginPurgeBatch
	sweep.complete = exports.CompletePurge
	sweep.purge = func(ctx context.Context, a store.AuditExportArtifact) error {
		return content.PurgeAuditExport(ctx, a.ExportID, a.AttemptID)
	}
	return sweep
}

// NewAuditExportBuild registers the build of a requested audit export.
func NewAuditExportBuild(builder AuditExportBuilder) job.Registration {
	return job.Registration{Descriptor: auditExportBuildDescriptor(auditExportBuildHandler{builder: builder})}
}

// NewAuditExportCleanup registers the expiry of audit exports and the removal
// of their files, proposed hourly.
func NewAuditExportCleanup(exports store.AuditExportStore, content AuditExportPurger, jobs store.JobStore, now func() time.Time) job.Registration {
	handler := exportCleanupHandler[store.AuditExportArtifact]{sweep: auditExportSweep(exports, content)}
	return job.Registration{
		Descriptor: exportCleanupDescriptor(model.JobTypeAuditExportCleanup, "audit.export.unavailable", handler),
		Recurrence: &job.Recurrence{
			Name:     "audit-export-cleanup",
			Interval: time.Hour,
			Proposer: exportCleanupProposer{jobs: jobs, now: now, jobType: model.JobTypeAuditExportCleanup, dedupe: "audit-export-cleanup"},
		},
	}
}
