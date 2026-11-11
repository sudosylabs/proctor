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

	"github.com/sudosylabs/proctor/server/app/exam/attempt"
	"github.com/sudosylabs/proctor/server/app/exam/sitting"
	"github.com/sudosylabs/proctor/server/app/invocation"
	"github.com/sudosylabs/proctor/server/app/job"
	"github.com/sudosylabs/proctor/server/model"
	"github.com/sudosylabs/proctor/server/store"
)

const (
	examSittingSealingPageSize    = 100
	examSittingSealingMaximumWork = 1000
)

// ExamSittingSealingCheckpoint is the resumable checkpoint of the Exam Sitting sealing Job.
type ExamSittingSealingCheckpoint struct {
	AfterAttemptID model.ExamAttemptID `json:"after_attempt_id,omitempty"`
	Processed      int64               `json:"processed"`
	Sealed         int64               `json:"sealed"`
}

// ExamSittingSealingResult is the safe result of the Exam Sitting sealing Job.
type ExamSittingSealingResult struct {
	Processed int64                  `json:"processed"`
	Sealed    int64                  `json:"sealed"`
	Continued bool                   `json:"continued"`
	State     model.ExamSittingState `json:"state"`
	Revision  int64                  `json:"revision"`
}

// attemptSealing is the part of *attempt.Service the sealing Job drives. It
// is a seam because the handler tests fake it: sealing a real Attempt needs
// its Submission, audit and workspace machinery.
type attemptSealing interface {
	ListAutomaticSealTargets(
		context.Context,
		invocation.System,
		model.ExamSittingID,
		model.ExamAttemptID,
		int,
	) ([]store.ExamSubmissionAutomaticSealTarget, error)
	SealForSittingClose(context.Context, invocation.System, store.ExamSubmissionAutomaticSealTarget) (attempt.AutomaticSubmissionResult, error)
}

type examSittingSealingHandler struct {
	sittings sittingLifecycle
	attempts attemptSealing
	jobs     store.JobStore
	now      func() time.Time
	newID    func() model.JobID
}

func (h examSittingSealingHandler) Run(ctx context.Context, execution job.Execution) job.Outcome {
	if h.sittings == nil ||
		h.attempts == nil ||
		execution.Attempt == nil ||
		!execution.Job.ID.IsValid() ||
		!execution.Attempt.ID.IsValid() {
		return job.PermanentFailure("job.command.invalid", errors.New("Exam Sitting sealing execution is incomplete"))
	}

	command, err := model.DecodeExamSittingLifecycleCommand(execution.Job.Command)
	if err != nil {
		return job.PermanentFailure("job.command.invalid", err)
	}

	checkpoint, err := decodeExamSittingSealingCheckpoint(execution.Job.Checkpoint)
	if err != nil {
		return job.PermanentFailure("job.checkpoint.invalid", err)
	}

	if execution.Job.WorkReserved < 0 ||
		execution.Job.WorkReserved > examSittingSealingMaximumWork ||
		checkpoint.Processed > examSittingSealingMaximumWork ||
		execution.Job.WorkReserved < int(checkpoint.Processed) ||
		execution.Job.WorkReserved-int(checkpoint.Processed) > 1 {
		return job.PermanentFailure("job.checkpoint.invalid", errors.New("Exam Sitting sealing reservation and checkpoint disagree"))
	}

	// ReserveWork is durable and occurrence-wide, while a process can stop before
	// it records the corresponding per-Attempt checkpoint. Burn that one
	// uncertain unit before doing more work. This keeps the hard work cap true
	// whether the domain transaction committed or not; an unsealed Attempt will
	// simply consume a later unit (or a continuation Job) when it is seen again.
	if execution.Job.WorkReserved == int(checkpoint.Processed)+1 {
		checkpoint.Processed++
		if checkpointErr := checkpointExamSittingSealing(ctx, execution, checkpoint); checkpointErr != nil {
			return job.RetryableFailure("dependency.unavailable", checkpointErr)
		}
	}

	remaining := examSittingSealingMaximumWork - execution.Job.WorkReserved
	for remaining > 0 {
		limit := min(remaining, examSittingSealingPageSize)
		page, listErr := h.attempts.ListAutomaticSealTargets(
			ctx,
			invocation.Job(execution.Job.ID, execution.Attempt.ID),
			command.ExamSittingID,
			checkpoint.AfterAttemptID,
			limit,
		)
		if listErr != nil {
			return examSittingSealingFailure(listErr)
		}
		if len(page) > limit {
			return job.PermanentFailure("job.invariant_failed", errors.New("Exam Sitting sealing returned an oversized page"))
		}
		if len(page) == 0 {
			return h.finishPass(ctx, execution, command, checkpoint, false)
		}

		for _, target := range page {
			if target.SittingID != command.ExamSittingID ||
				(!checkpoint.AfterAttemptID.IsZero() &&
					target.AttemptID.String() <= checkpoint.AfterAttemptID.String()) {
				return job.PermanentFailure("job.invariant_failed", errors.New("Exam Sitting sealing page is not strictly ordered"))
			}

			reserved, reserveErr := execution.ReserveWork(ctx, 1, examSittingSealingMaximumWork)
			if reserveErr != nil {
				return job.RetryableFailure("dependency.unavailable", reserveErr)
			}
			if !reserved {
				return h.finishPass(ctx, execution, command, checkpoint, true)
			}

			sealed, sealErr := h.attempts.SealForSittingClose(
				ctx,
				invocation.Job(execution.Job.ID, execution.Attempt.ID),
				target,
			)
			if sealErr != nil {
				return examSittingSealingFailure(sealErr)
			}

			checkpoint.AfterAttemptID = target.AttemptID
			checkpoint.Processed++
			if !sealed.Replayed {
				checkpoint.Sealed++
			}
			if checkpointErr := checkpointExamSittingSealing(ctx, execution, checkpoint); checkpointErr != nil {
				return job.RetryableFailure("dependency.unavailable", checkpointErr)
			}

			remaining--
			if remaining == 0 {
				break
			}
		}

		if len(page) < limit {
			return h.finishPass(ctx, execution, command, checkpoint, false)
		}
	}

	return h.finishPass(ctx, execution, command, checkpoint, true)
}

func (h examSittingSealingHandler) finishPass(
	ctx context.Context,
	execution job.Execution,
	command model.ExamSittingLifecycleCommand,
	checkpoint ExamSittingSealingCheckpoint,
	continueIfClosing bool,
) job.Outcome {
	finished := h.finish(ctx, execution, command.ExamSittingID, checkpoint)
	if finished.Kind != job.OutcomeSucceeded || len(finished.Result) == 0 {
		return finished
	}

	var result ExamSittingSealingResult
	if err := json.Unmarshal(finished.Result, &result); err != nil {
		return job.PermanentFailure("job.invariant_failed", err)
	}
	if result.State == model.ExamSittingClosed {
		return finished
	}
	if result.State != model.ExamSittingClosing {
		return job.PermanentFailure("job.invariant_failed", errors.New("Exam Sitting sealing finished in an invalid state"))
	}

	if !continueIfClosing {
		return job.RetryableFailure("dependency.unavailable",
			errors.New("Exam Sitting sealing has unfinished state without a sealable Attempt"))
	}

	return h.continuePass(ctx, execution, command, result)
}

func decodeExamSittingSealingCheckpoint(document json.RawMessage) (ExamSittingSealingCheckpoint, error) {
	var value ExamSittingSealingCheckpoint
	if len(document) == 0 {
		return value, nil
	}
	if err := job.DecodeStrictUniqueDocument(document, &value); err != nil {
		return value, err
	}

	if (!value.AfterAttemptID.IsZero() && !value.AfterAttemptID.IsValid()) ||
		value.Processed < 0 ||
		value.Sealed < 0 ||
		value.Sealed > value.Processed ||
		(value.AfterAttemptID.IsZero() && value.Sealed != 0) ||
		(!value.AfterAttemptID.IsZero() && value.Processed == 0) {
		return value, errors.New("Exam Sitting sealing checkpoint is invalid")
	}

	return value, nil
}

func checkpointExamSittingSealing(
	ctx context.Context,
	execution job.Execution,
	checkpoint ExamSittingSealingCheckpoint,
) error {
	document, err := json.Marshal(checkpoint)
	if err != nil {
		return err
	}

	return execution.Checkpoint(
		ctx,
		job.CheckpointValue{Progress: &model.JobProgress{Current: checkpoint.Processed, Total: examSittingSealingMaximumWork, Stage: "sealing"},
			Document: document},
	)
}

func (h examSittingSealingHandler) finish(
	ctx context.Context,
	execution job.Execution,
	sittingID model.ExamSittingID,
	checkpoint ExamSittingSealingCheckpoint,
) job.Outcome {
	finished, err := h.sittings.FinishSealing(ctx, invocation.Job(execution.Job.ID, execution.Attempt.ID), sittingID)
	if err != nil {
		return examSittingSealingFailure(err)
	}

	result := &finished
	if err = validateExamSittingLifecycleJobResult(sittingID, result); err != nil {
		return job.PermanentFailure("job.invariant_failed", err)
	}

	document, err := json.Marshal(ExamSittingSealingResult{
		Processed: checkpoint.Processed,
		Sealed:    checkpoint.Sealed,
		State:     result.Value.Sitting.State,
		Revision:  result.Value.Sitting.Revision,
	})
	if err != nil {
		return job.PermanentFailure("job.invariant_failed", err)
	}

	return job.Outcome{Kind: job.OutcomeSucceeded, Result: document}
}

func (h examSittingSealingHandler) continuePass(
	ctx context.Context,
	execution job.Execution,
	command model.ExamSittingLifecycleCommand,
	result ExamSittingSealingResult,
) job.Outcome {
	if err := h.enqueueContinuation(ctx, command.ExamSittingID, execution.Job.ID); err != nil {
		return examSittingSealingFailure(err)
	}

	result.Continued = true
	resultDocument, err := json.Marshal(result)
	if err != nil {
		return job.PermanentFailure("job.invariant_failed", err)
	}

	return job.Outcome{Kind: job.OutcomeSucceeded, Result: resultDocument}
}

// enqueueContinuation queues the next sealing pass for a Sitting whose
// Attempts the bounded pass did not finish.
func (h examSittingSealingHandler) enqueueContinuation(
	ctx context.Context,
	sittingID model.ExamSittingID,
	parentJobID model.JobID,
) error {
	if h.jobs == nil || h.now == nil || h.newID == nil || !sittingID.IsValid() || !parentJobID.IsValid() {
		return store.NewErrInvalidInput("exam_sitting_sealing", "continuation", nil)
	}

	document, err := model.EncodeExamSittingLifecycleCommand(model.ExamSittingLifecycleCommand{ExamSittingID: sittingID})
	if err != nil {
		return store.NewErrInvalidInput("exam_sitting_sealing", "continuation", err)
	}

	at := model.TimeUTC(h.now())
	record, err := model.NewJobWithDedupePolicy(
		h.newID(),
		model.JobTypeExamSittingSealing,
		document,
		"exam-sitting-sealing-continuation:"+parentJobID.String(),
		model.JobDedupePermanent,
		at,
		at,
		8,
	)
	if err != nil {
		return store.NewErrInvalidInput("exam_sitting_sealing", "continuation", err)
	}

	_, _, err = h.jobs.Enqueue(ctx, &store.JobEnqueue{Job: record})
	return err
}

// examSittingSealingFailure fails permanently only for input the Store refused. A
// database constraint rejection can come from a concurrent transition or clock
// skew, so it stays retryable within the attempt budget.
func examSittingSealingFailure(err error) job.Outcome {
	var invalid *store.ErrInvalidInput
	if (errors.As(err, &invalid) && !store.IsConstraintViolation(err)) || store.IsNotFound(err) {
		return job.PermanentFailure("job.invariant_failed", err)
	}
	return job.RetryableFailure("dependency.unavailable", err)
}

func examSittingSealingDescriptor(handler job.Handler) job.Descriptor {
	descriptor := job.NewDescriptor(model.JobTypeExamSittingSealing, handler)
	descriptor.Checkpoints = true
	descriptor.ProgressStages = []string{"sealing"}
	descriptor.PublicErrorCodes = []string{"dependency.unavailable", "job.checkpoint.invalid", "job.command.invalid", "job.invariant_failed"}
	descriptor.Concurrency = 4
	descriptor.MaximumAttempts = 8
	descriptor.ExplicitRetryStatuses = []model.JobStatus{model.JobStatusFailed}
	return descriptor
}

// NewExamSittingSealing registers the automatic sealing of Exam
// Attempts when a Sitting closes.
func NewExamSittingSealing(
	sittings *sitting.Service,
	attempts *attempt.Service,
	jobs store.JobStore,
	now func() time.Time,
	newID func() model.JobID,
) job.Registration {
	handler := examSittingSealingHandler{sittings: sittings, attempts: attempts, jobs: jobs, now: now, newID: newID}
	return job.Registration{Descriptor: examSittingSealingDescriptor(handler)}
}
