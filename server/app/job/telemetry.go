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
)

// Recorder observes bounded Job runtime facts and never receives commands,
// results, errors, claims, identifiers, or actor data.
type Recorder interface {
	Started(model.JobType)
	Finished(model.JobType, ExecutionOutcome, time.Duration)
	Record(Activity)
}

// Activity is a bounded operational fact emitted by the Job runtime. Name is
// either a registered Job type, recurrence, or periodic-task name; Operation
// and Outcome are selected only by this package.
type Activity struct {
	Kind         string
	Name         string
	Operation    string
	Outcome      string
	Duration     time.Duration
	QueueLatency time.Duration
}

// Tracer encloses one claimed Job execution in a span. It receives only the
// registered Job type; the returned context carries the span to the handler
// and its store calls, and finish receives the closed execution outcome.
type Tracer interface {
	StartExecution(ctx context.Context, jobType model.JobType) (spanCtx context.Context, finish func(ExecutionOutcome))
}

// ExecutionOutcome is the closed telemetry vocabulary for one claimed Job
// execution. It includes runtime terminal states that are not handler outcomes.
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

// trace starts the execution span when a Tracer is configured. The returned
// finish must be called exactly once with the execution's final outcome.
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
