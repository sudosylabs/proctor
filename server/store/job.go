// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package store

import (
	"context"
	"encoding/json"
	"time"

	"github.com/sudosylabs/proctor/server/model"
)

type JobEnqueue struct {
	Job *model.Job
}

type JobClaimRequest struct {
	Types         []model.JobType
	NodeID        string
	ClaimToken    model.JobClaimToken
	LeaseDuration time.Duration
}

type JobClaim struct {
	Job     *model.Job
	Attempt *model.JobAttempt
}

type JobHeartbeat struct {
	AttemptID     model.JobAttemptID
	ClaimToken    model.JobClaimToken
	LeaseDuration time.Duration
}

type JobCheckpoint struct {
	AttemptID  model.JobAttemptID
	ClaimToken model.JobClaimToken
	Progress   *model.JobProgress
	Checkpoint json.RawMessage
}

type JobWorkReservation struct {
	AttemptID  model.JobAttemptID
	ClaimToken model.JobClaimToken
	Units      int
	Limit      int
}

type JobWorkReservationResult struct {
	Reserved bool
	Consumed int
}

type JobCompletionKind string

const (
	JobCompletionSucceeded        JobCompletionKind = "succeeded"
	JobCompletionRetryableFailure JobCompletionKind = "retryable_failure"
	JobCompletionRelinquished     JobCompletionKind = "relinquished"
	JobCompletionPermanentFailure JobCompletionKind = "permanent_failure"
	JobCompletionCanceled         JobCompletionKind = "canceled"
)

type JobCompletion struct {
	AttemptID       model.JobAttemptID
	ClaimToken      model.JobClaimToken
	Kind            JobCompletionKind
	RetryDelay      time.Duration
	Result          json.RawMessage
	PublicErrorCode string
}

type JobListOptions struct {
	Types    []model.JobType
	Statuses []model.JobStatus
	// UpdatedWithin, when positive, keeps Jobs updated less than that long
	// before the database clock.
	UpdatedWithin time.Duration
	// Ascending reads oldest first; the default is newest first.
	Ascending bool
	// BeforeCreatedAt and BeforeID are the previous page's last key.
	BeforeCreatedAt time.Time
	BeforeID        model.JobID
	Limit           int
}

type JobAttemptListOptions struct {
	JobID        model.JobID
	BeforeNumber int
	Limit        int
}

type JobMutation struct {
	ID               model.JobID
	ExpectedRevision int64
	AuditEventID     model.AuditEventID
	AuditAt          time.Time
}

type JobRetentionPolicy struct {
	Type                 model.JobType
	SucceededCanceledAge time.Duration
	FailedAge            time.Duration
}

type JobHistoryCleanup struct {
	ExcludeJobID     model.JobID
	Policies         []JobRetentionPolicy
	AfterCompletedAt time.Time
	AfterJobID       model.JobID
	Limit            int
}

type JobHistoryCleanupResult struct {
	Deleted         int64
	LastCompletedAt time.Time
	LastJobID       model.JobID
	Done            bool
}

// JobStore claims Jobs at least once and fences each attempt by its claim
// token. Every time it compares is on the primary database clock.
// JobQueueDepth is one type's queued Jobs at one instant.
type JobQueueDepth struct {
	Due       int
	Scheduled int
}

type JobStore interface {
	// Enqueue deduplicates by type, key and the Job's dedupe policy. Active
	// dedupe admits a new Job once the old one is terminal; permanent dedupe
	// holds the key even after retention deletes the history. The boolean
	// reports whether the given Job was inserted; otherwise the existing one
	// is returned when it still exists.
	Enqueue(context.Context, *JobEnqueue) (*model.Job, bool, error)
	// ClaimNext claims one eligible Job of the requested types with SKIP
	// LOCKED and appends its next attempt. It writes nothing when no Job is
	// due; lost attempts are recovered by RecoverLostAttempts, never here.
	ClaimNext(context.Context, *JobClaimRequest) (*JobClaim, error)
	// RecoverLostAttempts ends up to limit attempts whose lease expired and
	// requeues their Jobs with growing backoff, or fails a Job whose attempts
	// are exhausted. One node sweeps at a time; the others recover nothing.
	RecoverLostAttempts(ctx context.Context, limit int) (int, error)
	// NextDue reports, for each listed type with a claimable queued Job, the
	// earliest instant one becomes available. A type with nothing queued is
	// absent.
	NextDue(ctx context.Context, types []model.JobType) (map[model.JobType]time.Time, error)
	// QueueDepth counts each listed type's queued Jobs, split into those due
	// now and those scheduled later. A type with nothing queued is absent.
	QueueDepth(ctx context.Context, types []model.JobType) (map[model.JobType]JobQueueDepth, error)
	// Heartbeat extends only the live attempt holding the claim token.
	Heartbeat(context.Context, *JobHeartbeat) (*model.JobAttempt, error)
	// Checkpoint saves progress only for the live token before its lease
	// expires.
	Checkpoint(context.Context, *JobCheckpoint) (*model.Job, error)
	// ReserveWork consumes units from a budget shared by every attempt of the
	// Job. Units stay consumed across retries, so a crashed worker may do less
	// work than reserved but never more than the limit.
	ReserveWork(context.Context, *JobWorkReservation) (*JobWorkReservationResult, error)
	// Complete closes the attempt and moves the Job to a terminal state or a
	// queued retry. An expired token cannot complete.
	Complete(context.Context, *JobCompletion) (*model.Job, error)
	Get(context.Context, model.JobID) (*model.Job, error)
	List(context.Context, JobListOptions) ([]*model.Job, error)
	// Count counts what List would return across every page, ignoring the
	// keyset and limit, capped at maximum (see MaximumListCount).
	Count(context.Context, JobListOptions, int) (int, error)
	ListAttemptsPage(context.Context, JobAttemptListOptions) ([]model.JobAttempt, error)
	// CancellationRequested fails unless the attempt still holds the live
	// fence.
	CancellationRequested(context.Context, model.JobAttemptID, model.JobClaimToken) (bool, error)
	// Cancel and Retry complete the recorded operator audit attempt in the
	// same transaction.
	Cancel(context.Context, *JobMutation) (*model.Job, error)
	Retry(context.Context, *JobMutation) (*model.Job, error)
	// DeleteTerminalHistory removes one oldest-first page of terminal Jobs past
	// their retention, with their attempts. It never touches active work and
	// always skips the caller's own cleanup Job.
	DeleteTerminalHistory(context.Context, *JobHistoryCleanup) (*JobHistoryCleanupResult, error)
}
