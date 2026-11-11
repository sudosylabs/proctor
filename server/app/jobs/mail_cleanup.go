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
	"github.com/sudosylabs/proctor/server/app/mail"
	"github.com/sudosylabs/proctor/server/model"
	"github.com/sudosylabs/proctor/server/store"
)

const (
	mailCleanupPageSize     = 500
	mailCleanupMaximumPages = 20
)

// MailCleanupPageSize is the largest page size a mail cleanup command may
// ask for.
const MailCleanupPageSize = mailCleanupPageSize

// MailCleanupMaximumPages is the most pages a mail cleanup command may ask
// for.
const MailCleanupMaximumPages = mailCleanupMaximumPages

// MailCleanupCommand is the immutable command of the mail cleanup Job.
type MailCleanupCommand struct {
	PageSize int `json:"page_size"`
	MaxPages int `json:"max_pages"`
}

// MailCleanupResult is the safe result of the mail cleanup Job.
type MailCleanupResult struct {
	FanoutsTerminalized        int `json:"fanouts_terminalized"`
	FanoutDeliveriesSuppressed int `json:"fanout_deliveries_suppressed"`
	Expired                    int `json:"expired"`
	Deleted                    int `json:"deleted"`
}

type mailCleanupHandler struct {
	deliveries store.MailStore
	sittings   store.ExamSittingStore
	recorder   mail.DeliveryRecorder
}

func (h mailCleanupHandler) Run(ctx context.Context, execution job.Execution) job.Outcome {
	var command MailCleanupCommand
	if decodeStrictJobDocument(execution.Job.Command, &command) != nil ||
		command.PageSize < 1 ||
		command.PageSize > mailCleanupPageSize ||
		command.MaxPages < 1 ||
		command.MaxPages > mailCleanupMaximumPages ||
		h.deliveries == nil ||
		h.sittings == nil {
		return job.PermanentFailure("job.command.invalid", errors.New("mail cleanup command is invalid"))
	}

	result := MailCleanupResult{}
	for page := 0; page < command.MaxPages; page++ {
		terminalized, err := h.sittings.MaintainMailExpansions(ctx, command.PageSize)
		if err != nil {
			return job.RetryableFailure("dependency.unavailable", err)
		}

		result.FanoutsTerminalized += terminalized.FanoutsTerminalized
		result.FanoutDeliveriesSuppressed += terminalized.DeliveriesSuppressed
		if !terminalized.More {
			break
		}
	}

	for page := 0; page < command.MaxPages; page++ {
		deleted, err := h.deliveries.CleanupTerminal(ctx, command.PageSize)
		if err != nil {
			return job.RetryableFailure("dependency.unavailable", err)
		}

		result.Deleted += deleted.Affected
		if !deleted.More {
			break
		}
	}

	for page := 0; page < command.MaxPages; page++ {
		expired, err := h.deliveries.SuppressExpired(ctx, command.PageSize)
		if err != nil {
			return job.RetryableFailure("dependency.unavailable", err)
		}

		result.Expired += expired.Affected
		mail.RecordMaintenanceDeliveries(ctx, h.recorder, expired)
		if !expired.More {
			break
		}
	}

	document, err := json.Marshal(result)
	return job.Outcome{Kind: job.OutcomeSucceeded, Result: document, Err: err}
}

type mailCleanupProposer struct {
	jobs store.JobStore
	now  func() time.Time
}

func (p mailCleanupProposer) Propose(ctx context.Context, occurrence time.Time) error {
	if p.jobs == nil || p.now == nil {
		return errors.New("mail cleanup proposer dependencies are unavailable")
	}

	command, err := json.Marshal(MailCleanupCommand{PageSize: mailCleanupPageSize, MaxPages: mailCleanupMaximumPages})
	if err != nil {
		return err
	}

	at := model.TimeUTC(p.now())
	job, err := model.NewJobWithDedupePolicy(
		model.NewJobID(),
		model.JobTypeMailCleanup,
		command,
		"mail-cleanup:"+model.TimeUTC(occurrence).Format("2006-01-02"),
		model.JobDedupePermanent,
		at,
		at,
		5,
	)
	if err != nil {
		return err
	}

	_, _, err = p.jobs.Enqueue(ctx, &store.JobEnqueue{Job: job})
	return err
}

func mailCleanupDescriptor(handler job.Handler) job.Descriptor {
	descriptor := job.NewDescriptor(model.JobTypeMailCleanup, handler)
	descriptor.PublicErrorCodes = []string{"dependency.unavailable", "job.command.invalid"}
	descriptor.Timeout = 5 * time.Minute
	descriptor.BaseRetryDelay = time.Minute
	descriptor.MaximumRetryDelay = 30 * time.Minute
	return descriptor
}

// NewMailCleanup registers the bounded cleanup of terminal mail deliveries
// and Sitting mail maintenance, proposed daily.
func NewMailCleanup(
	deliveries store.MailStore,
	sittings store.ExamSittingStore,
	recorder mail.DeliveryRecorder,
	jobs store.JobStore,
	now func() time.Time,
) job.Registration {
	return job.Registration{
		Descriptor: mailCleanupDescriptor(mailCleanupHandler{deliveries: deliveries, sittings: sittings, recorder: recorder}),
		Recurrence: &job.Recurrence{Name: "mail-cleanup", Proposer: mailCleanupProposer{jobs: jobs, now: now}},
	}
}
