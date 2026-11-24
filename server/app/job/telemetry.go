// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package job

import (
	"context"
	"errors"
	"time"

	"github.com/sudosylabs/proctor/server/model"
	"github.com/sudosylabs/proctor/server/store"
)

// Recorder observes bounded runtime facts and never receives commands,
// results, errors, claims, identifiers or actor data.
type Recorder interface {
	Started(model.JobType)
	Finished(model.JobType, ExecutionOutcome, time.Duration)
	Record(Activity)
}

// QueueRecorder is an optional Recorder capability for the queue as the
// dispatcher and the lost-attempt sweep see it.
type QueueRecorder interface {
	Woke(WakeSource)
	// Queued receives every registered type, zero when nothing is queued.
	Queued(map[model.JobType]store.JobQueueDepth)
	LostAttemptsRecovered(int)
}

// WakeSource names what made the dispatcher look for work.
type WakeSource string

const (
	// WakeLocal is a commit on this node.
	WakeLocal WakeSource = "local"
	// WakeNotify is a commit on any node, heard through the listener.
	WakeNotify WakeSource = "notify"
	// WakeReconnect is the listener connecting after notifications may
	// have been missed.
	WakeReconnect WakeSource = "reconnect"
	// WakeDue is a scheduled Job reaching its time.
	WakeDue WakeSource = "due"
	// WakeBackstop is the periodic read that bounds a lost wake.
	WakeBackstop WakeSource = "backstop"
)

// Activity is a bounded runtime fact. Name is a registered job type,
// recurrence or periodic-task name; Operation and Outcome come only from this
// package.
type Activity struct {
	Kind         string
	Name         string
	Operation    string
	Outcome      string
	Duration     time.Duration
	QueueLatency time.Duration
}

// Tracer encloses one claimed execution in a span. It receives only the job
// type; the returned context carries the span to the handler, and finish
// receives the execution outcome.
type Tracer interface {
	StartExecution(ctx context.Context, jobType model.JobType) (spanCtx context.Context, finish func(ExecutionOutcome))
}

// ExecutionOutcome is the closed outcome vocabulary of one claimed execution,
// including runtime terminal states that are not handler outcomes.
type ExecutionOutcome string

// The execution outcomes a Recorder and a Tracer receive. The first five
// mirror the handler's Outcome; the rest end an execution without one.
const (
	ExecutionSucceeded        ExecutionOutcome = "succeeded"
	ExecutionRetryableFailure ExecutionOutcome = "retryable_failure"
	ExecutionPermanentFailure ExecutionOutcome = "permanent_failure"
	ExecutionCanceled         ExecutionOutcome = "canceled"
	ExecutionRelinquished     ExecutionOutcome = "relinquished"
	ExecutionAbandoned        ExecutionOutcome = "abandoned"
	ExecutionClaimLost        ExecutionOutcome = "claim_lost"
	ExecutionShutdown         ExecutionOutcome = "shutdown"
	ExecutionInvalid          ExecutionOutcome = "invalid"
)

func executionOutcomeFromHandler(outcome OutcomeKind) ExecutionOutcome {
	switch outcome {
	case OutcomeSucceeded:
		return ExecutionSucceeded
	case OutcomeRetryableFailure:
		return ExecutionRetryableFailure
	case OutcomePermanentFailure:
		return ExecutionPermanentFailure
	case OutcomeCanceled:
		return ExecutionCanceled
	case OutcomeRelinquished:
		return ExecutionRelinquished
	default:
		return ExecutionInvalid
	}
}

// trace returns a finish that must be called exactly once with the
// execution's final outcome.
func (r *Engine) trace(ctx context.Context, jobType model.JobType) (context.Context, func(ExecutionOutcome)) {
	if r.deps.Tracer == nil {
		return ctx, func(ExecutionOutcome) {}
	}
	return r.deps.Tracer.StartExecution(ctx, jobType)
}

func (r *Engine) record(activity Activity) {
	if r.deps.Recorder != nil {
		r.deps.Recorder.Record(activity)
	}
}

func simpleJobOutcome(err error) string {
	switch {
	case err == nil:
		return "success"
	case errors.Is(err, context.Canceled):
		return "canceled"
	case errors.Is(err, context.DeadlineExceeded):
		return "timeout"
	default:
		return "error"
	}
}
