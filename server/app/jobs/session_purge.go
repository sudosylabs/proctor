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

// maximumSessionPurgeBatches bounds the passes of each kind in one run; the
// next hourly occurrence continues any remainder.
const maximumSessionPurgeBatches = 40

// SessionPurgeCommand is the purge pass size.
type SessionPurgeCommand struct {
	BatchSize int `json:"batch_size"`
}

// SessionPurgeResult counts what one run deleted.
type SessionPurgeResult struct {
	Sessions    int64 `json:"sessions"`
	Credentials int64 `json:"credentials"`
}

type sessionPurgeHandler struct {
	sessions    store.SessionStore
	credentials store.SessionCredentialStore
	retention   time.Duration
}

func (h sessionPurgeHandler) Run(ctx context.Context, execution job.Execution) job.Outcome {
	var command SessionPurgeCommand
	if err := decodeStrictJobDocument(execution.Job.Command, &command); err != nil ||
		command.BatchSize < 1 ||
		command.BatchSize > store.MaximumSessionPurgeBatch {
		return job.PermanentFailure("job.command.invalid", errors.New("session purge command is invalid"))
	}

	if h.retention <= 0 {
		return job.PermanentFailure("job.command.invalid", errors.New("session retention is not configured"))
	}

	var result SessionPurgeResult
	for range maximumSessionPurgeBatches {
		deleted, err := h.credentials.PurgeRetired(ctx, &store.SessionCredentialPurge{Grace: h.retention, Limit: command.BatchSize})
		if err != nil {
			return job.RetryableFailure("dependency.unavailable", err)
		}

		result.Credentials += deleted
		if deleted < int64(command.BatchSize) {
			break
		}
	}

	var after store.SessionPurgePosition
	for range maximumSessionPurgeBatches {
		pass, err := h.sessions.PurgeRetired(ctx, &store.SessionPurge{Grace: h.retention, Limit: command.BatchSize, After: after})
		if err != nil {
			return job.RetryableFailure("dependency.unavailable", err)
		}

		result.Sessions += pass.Sessions
		result.Credentials += pass.Credentials
		if pass.Examined < command.BatchSize {
			break
		}

		after = pass.Next
	}

	document, err := json.Marshal(result)
	return job.Outcome{Kind: job.OutcomeSucceeded, Result: document, Err: err}
}

type sessionPurgeProposer struct {
	jobs store.JobStore
	now  func() time.Time
}

func (p sessionPurgeProposer) Propose(ctx context.Context, occurrence time.Time) error {
	command, err := json.Marshal(SessionPurgeCommand{BatchSize: store.MaximumSessionPurgeBatch})
	if err != nil {
		return err
	}

	at := model.TimeUTC(p.now())
	key := "session-purge-retired:" + model.TimeUTC(occurrence).Format("2006-01-02T15")
	job, err := model.NewJobWithDedupePolicy(
		model.NewJobID(),
		model.JobTypeSessionPurgeRetired,
		command,
		key,
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

func sessionPurgeDescriptor(handler job.Handler) job.Descriptor {
	descriptor := job.NewDescriptor(model.JobTypeSessionPurgeRetired, handler)
	descriptor.PublicErrorCodes = []string{"dependency.unavailable", "job.command.invalid"}
	descriptor.SuccessRetention = 7 * 24 * time.Hour
	return descriptor
}

// NewSessionPurge registers the purge of Sessions and credentials that have
// been retired for longer than retention, proposed hourly.
func NewSessionPurge(
	sessions store.SessionStore,
	credentials store.SessionCredentialStore,
	retention time.Duration,
	jobs store.JobStore,
	now func() time.Time,
) job.Registration {
	return job.Registration{
		Descriptor: sessionPurgeDescriptor(sessionPurgeHandler{sessions: sessions, credentials: credentials, retention: retention}),
		Recurrence: &job.Recurrence{Name: "session-purge-retired", Interval: time.Hour, Proposer: sessionPurgeProposer{jobs: jobs, now: now}},
	}
}
