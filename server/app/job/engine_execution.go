// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package job

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"hash/fnv"
	"time"

	"github.com/sudosylabs/proctor/server/logging"
	"github.com/sudosylabs/proctor/server/model"
	"github.com/sudosylabs/proctor/server/store"
)

func (r *Engine) executeClaim(parent context.Context, descriptor Descriptor, claim *store.JobClaim) {
	metricOutcome := ExecutionAbandoned
	parent, finishSpan := r.trace(parent, descriptor.Type)
	defer func() { finishSpan(metricOutcome) }()
	startedAt := time.Now()
	if r.deps.Recorder != nil {
		r.deps.Recorder.Started(descriptor.Type)
		defer func() {
			r.deps.Recorder.Finished(descriptor.Type, metricOutcome, time.Since(startedAt))
		}()
	}

	handlerCtx, cancelHandler := context.WithTimeout(parent, descriptor.Timeout)
	defer cancelHandler()
	heartbeatDone := make(chan struct{})
	heartbeatStopped := make(chan struct{})
	claimLost := make(chan struct{}, 1)
	go func() {
		defer close(heartbeatStopped)
		r.heartbeatClaim(claimHeartbeat{
			parent:        parent,
			handlerCtx:    handlerCtx,
			cancelHandler: cancelHandler,
			descriptor:    descriptor,
			claim:         claim,
			startedAt:     startedAt,
			done:          heartbeatDone,
			claimLost:     claimLost,
		})
	}()
	execution := r.newClaimExecution(descriptor, claim)
	outcome := runHandler(handlerCtx, descriptor.Handler, execution)
	close(heartbeatDone)
	<-heartbeatStopped
	select {
	case <-claimLost:
		r.record(Activity{Kind: "job", Name: string(descriptor.Type), Operation: "lease", Outcome: "lost"})
		metricOutcome = ExecutionClaimLost
		return
	default:
	}

	if errors.Is(parent.Err(), context.Canceled) {
		metricOutcome = ExecutionShutdown
		return
	}

	// A handler that returned success has committed its work, even when it
	// finished as its timeout elapsed; only an unfinished outcome is a timeout.
	if outcome.Kind != OutcomeSucceeded && errors.Is(handlerCtx.Err(), context.DeadlineExceeded) {
		outcome = RetryableFailure("job.timeout", handlerCtx.Err())
	}

	metricOutcome = executionOutcomeFromHandler(outcome.Kind)
	r.completeAttempt(parent, descriptor, claim, outcome)
}

// newClaimExecution builds the handler's Execution with work reservation and checkpoint hooks fenced by the claim.
func (r *Engine) newClaimExecution(descriptor Descriptor, claim *store.JobClaim) Execution {
	execution := NewExecution(claim.Job, claim.Attempt, nil, nil)
	execution.reserveWork = func(ctx context.Context, units, limit int) (bool, error) {
		started := time.Now()
		result, err := r.deps.Store.ReserveWork(ctx, &store.JobWorkReservation{
			AttemptID:  claim.Attempt.ID,
			ClaimToken: claim.Attempt.ClaimToken,
			Units:      units,
			Limit:      limit,
		})
		activityOutcome := "error"
		if err == nil && result.Reserved {
			activityOutcome = "reserved"
		} else if err == nil {
			activityOutcome = "limit"
		}

		r.record(Activity{
			Kind:      "job",
			Name:      string(descriptor.Type),
			Operation: "reserve_work",
			Outcome:   activityOutcome,
			Duration:  time.Since(started),
		})
		if err != nil {
			return false, err
		}

		return result.Reserved, nil
	}
	execution.checkpoint = func(ctx context.Context, value CheckpointValue) error {
		if !descriptor.Checkpoints ||
			len(value.Document) == 0 ||
			len(value.Document) > model.JobMaximumDocumentBytes ||
			!json.Valid(value.Document) {
			return errors.New("job type does not declare checkpoints")
		}

		if value.Progress != nil &&
			(value.Progress.Current < 0 ||
				value.Progress.Total <= 0 ||
				value.Progress.Current > value.Progress.Total ||
				!descriptor.SupportsProgressStage(value.Progress.Stage)) {
			return errors.New("job progress stage is not declared")
		}

		started := time.Now()
		_, err := r.deps.Store.Checkpoint(ctx, &store.JobCheckpoint{
			AttemptID:  claim.Attempt.ID,
			ClaimToken: claim.Attempt.ClaimToken,
			Progress:   value.Progress,
			Checkpoint: value.Document,
		})
		r.record(Activity{
			Kind:      "job",
			Name:      string(descriptor.Type),
			Operation: "checkpoint",
			Outcome:   simpleJobOutcome(err),
			Duration:  time.Since(started),
		})
		return err
	}
	return execution
}

// runHandler is the only place a handler is invoked. It fails the attempt
// without calling the handler when the Execution has no Job, and contains a
// handler panic as a retryable failure.
func runHandler(ctx context.Context, handler Handler, execution Execution) (outcome Outcome) {
	if execution.Job == nil {
		return PermanentFailure("job.command.invalid", errors.New("job is missing"))
	}

	defer func() {
		if recovered := recover(); recovered != nil {
			outcome = RetryableFailure("job.handler_panic", fmt.Errorf("job handler panic: %T", recovered))
		}
	}()
	return handler.Run(ctx, execution)
}

func (r *Engine) completeUnsupported(ctx context.Context, descriptor Descriptor, claim *store.JobClaim, resolutionErr error) {
	ctx, finishSpan := r.trace(ctx, descriptor.Type)
	defer finishSpan(ExecutionPermanentFailure)
	startedAt := time.Now()
	if r.deps.Recorder != nil {
		r.deps.Recorder.Started(descriptor.Type)
		defer func() {
			r.deps.Recorder.Finished(descriptor.Type, ExecutionPermanentFailure, time.Since(startedAt))
		}()
	}
	r.completeAttempt(ctx, descriptor, claim, PermanentFailure("job.command.unsupported", resolutionErr))
}

func (r *Engine) completeAttempt(ctx context.Context, descriptor Descriptor, claim *store.JobClaim, outcome Outcome) {
	failureWithoutCode := (outcome.Kind == OutcomeRetryableFailure ||
		outcome.Kind == OutcomeRelinquished ||
		outcome.Kind == OutcomePermanentFailure) &&
		outcome.PublicErrorCode == ""
	if failureWithoutCode || !descriptor.SupportsPublicErrorCode(outcome.PublicErrorCode) {
		outcome = PermanentFailure("job.outcome.invalid", errors.New("handler returned an undeclared public error code"))
	}

	completion := &store.JobCompletion{
		AttemptID:       claim.Attempt.ID,
		ClaimToken:      claim.Attempt.ClaimToken,
		Result:          outcome.Result,
		PublicErrorCode: outcome.PublicErrorCode,
	}
	switch outcome.Kind {
	case OutcomeSucceeded:
		if outcome.Err != nil || len(outcome.Result) == 0 {
			completion.Kind = store.JobCompletionPermanentFailure
			completion.PublicErrorCode = "job.result.invalid"
		} else {
			completion.Kind = store.JobCompletionSucceeded
		}
	case OutcomeRetryableFailure:
		completion.Kind = store.JobCompletionRetryableFailure
		completion.RetryDelay = retryDelay(descriptor, claim.Job.ID, claim.Attempt.Number)
	case OutcomeRelinquished:
		completion.Kind = store.JobCompletionRelinquished
		completion.RetryDelay = retryDelay(descriptor, claim.Job.ID, claim.Attempt.Number)
	case OutcomePermanentFailure:
		completion.Kind = store.JobCompletionPermanentFailure
	case OutcomeCanceled:
		completion.Kind = store.JobCompletionCanceled
	default:
		completion.Kind = store.JobCompletionPermanentFailure
		completion.PublicErrorCode = "job.outcome.invalid"
	}

	started := time.Now()
	_, err := r.deps.Store.Complete(ctx, completion)
	r.record(Activity{
		Kind:      "job",
		Name:      string(descriptor.Type),
		Operation: "complete",
		Outcome:   simpleJobOutcome(err),
		Duration:  time.Since(started),
	})
	if err != nil && !errors.Is(err, context.Canceled) {
		r.deps.Diagnostics.ErrorContext(ctx, "complete durable job", logging.Err(err))
	}
}

func retryDelay(descriptor Descriptor, jobID model.JobID, attempt int) time.Duration {
	delay := descriptor.BaseRetryDelay
	for index := 1; index < attempt && delay < descriptor.MaximumRetryDelay/2; index++ {
		delay *= 2
	}
	if delay > descriptor.MaximumRetryDelay {
		delay = descriptor.MaximumRetryDelay
	}
	hasher := fnv.New32a()
	_, _ = hasher.Write([]byte(jobID.String()))
	jitter := time.Duration(hasher.Sum32()%251) * delay / 1000
	return min(descriptor.MaximumRetryDelay, delay+jitter)
}
