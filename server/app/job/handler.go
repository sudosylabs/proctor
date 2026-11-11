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

	"github.com/sudosylabs/proctor/server/model"
)

// Handler is what a Job type implements. Run does one attempt's work and must
// be idempotent, because a Job can run more than once. Its context ends at
// the descriptor's Timeout, when the engine closes, when the claim is lost
// and, for a cancelable type, when cancellation is requested.
type Handler interface {
	Run(context.Context, Execution) Outcome
}

// Execution is what a handler runs with: the engine never runs a handler
// without a Job, so handlers read Execution.Job without checking it.
type Execution struct {
	Job         *model.Job
	Attempt     *model.JobAttempt
	checkpoint  func(context.Context, CheckpointValue) error
	reserveWork func(context.Context, int, int) (bool, error)
}

// NewExecution builds the Execution a handler runs with and validates nothing.
// checkpoint and reserveWork back Execution.Checkpoint and
// Execution.ReserveWork; when one is nil, that method returns an error instead
// of acting.
func NewExecution(
	record *model.Job,
	attempt *model.JobAttempt,
	checkpoint func(context.Context, CheckpointValue) error,
	reserveWork func(context.Context, int, int) (bool, error),
) Execution {
	return Execution{Job: record, Attempt: attempt, checkpoint: checkpoint, reserveWork: reserveWork}
}

// ReserveWork consumes units of the Job's work budget and reports false when
// that would pass limit. The budget is counted across every attempt of the
// Job, so a retry cannot spend again what a lost attempt already reserved.
func (e Execution) ReserveWork(ctx context.Context, units, limit int) (bool, error) {
	if e.reserveWork == nil {
		return false, errors.New("job execution does not support work reservation")
	}
	return e.reserveWork(ctx, units, limit)
}

// CheckpointValue is what a handler saves so a later attempt can resume:
// Document is the handler's own cursor, and Progress, when set, is the
// position operators see.
type CheckpointValue struct {
	Progress *model.JobProgress
	Document json.RawMessage
}

// Checkpoint saves value under the attempt's claim. The engine refuses it
// unless the descriptor declares Checkpoints, Document is valid JSON of at
// most model.JobMaximumDocumentBytes, and any Progress lies within its total
// at a stage the descriptor declares.
func (e Execution) Checkpoint(ctx context.Context, value CheckpointValue) error {
	if e.checkpoint == nil {
		return errors.New("job execution does not support checkpointing")
	}
	return e.checkpoint(ctx, value)
}

// Outcome is how a handler ends an attempt. A success must carry Result, a
// JSON document, and no Err. A failure must carry a PublicErrorCode the
// descriptor declares; an Outcome that breaks either rule is recorded as a
// permanent failure. Err is never stored.
type Outcome struct {
	Kind            OutcomeKind
	Result          json.RawMessage
	PublicErrorCode string
	Err             error
}

// OutcomeKind is the closed set of ways a handler can end an attempt. A kind
// outside it is recorded as a permanent failure.
type OutcomeKind string

// The handler outcomes. The constructors below build every kind but
// OutcomeSucceeded, which a handler builds itself with its Result.
const (
	OutcomeSucceeded        OutcomeKind = "succeeded"
	OutcomeRetryableFailure OutcomeKind = "retryable_failure"
	OutcomeRelinquished     OutcomeKind = "relinquished"
	OutcomePermanentFailure OutcomeKind = "permanent_failure"
	OutcomeCanceled         OutcomeKind = "canceled"
)

// RetryableFailure reports a failure another attempt may overcome. The Job is
// requeued after the descriptor's backoff while it has attempts left.
func RetryableFailure(code string, err error) Outcome {
	return Outcome{Kind: OutcomeRetryableFailure, PublicErrorCode: code, Err: err}
}

// Relinquished reports that the claiming node cannot execute the immutable
// command, while another compatible node may. The attempt remains visible but
// does not consume the Job's failure-attempt budget.
func Relinquished(code string, err error) Outcome {
	return Outcome{Kind: OutcomeRelinquished, PublicErrorCode: code, Err: err}
}

// PermanentFailure reports a failure no retry can overcome, such as a command
// that does not decode. The Job fails without another attempt.
func PermanentFailure(code string, err error) Outcome {
	return Outcome{Kind: OutcomePermanentFailure, PublicErrorCode: code, Err: err}
}

// Canceled reports that the handler stopped because the Job was canceled.
func Canceled(code string) Outcome {
	return Outcome{Kind: OutcomeCanceled, PublicErrorCode: code}
}
