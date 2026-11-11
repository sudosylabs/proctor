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

	"github.com/sudosylabs/proctor/server/app/exam/sitting"
	"github.com/sudosylabs/proctor/server/app/invocation"
	"github.com/sudosylabs/proctor/server/app/job"
	"github.com/sudosylabs/proctor/server/model"
	"github.com/sudosylabs/proctor/server/store"
)

const (
	maximumExamSittingLifecycleRecoveryWork = 1000
	examSittingLifecycleRecoveryPageSize    = 100
)

// ExamSittingLifecycleRecoveryCommand is the immutable command of the Exam Sitting lifecycle recovery Job.
type ExamSittingLifecycleRecoveryCommand struct {
	WorkLimit int `json:"work_limit"`
}

// ExamSittingLifecycleRecoveryCheckpoint is the resumable checkpoint of the Exam Sitting lifecycle recovery Job.
type ExamSittingLifecycleRecoveryCheckpoint struct {
	AfterDueAt     time.Time           `json:"after_due_at,omitempty"`
	AfterSittingID model.ExamSittingID `json:"after_sitting_id,omitempty"`
	Processed      int64               `json:"processed"`
	Changed        int64               `json:"changed"`
}

// ExamSittingLifecycleRecoveryResult is the safe result of the Exam Sitting lifecycle recovery Job.
type ExamSittingLifecycleRecoveryResult struct {
	Processed int64 `json:"processed"`
	Changed   int64 `json:"changed"`
}

// EncodeExamSittingLifecycleRecoveryCommand validates and encodes an Exam Sitting lifecycle recovery Job command.
func EncodeExamSittingLifecycleRecoveryCommand(value ExamSittingLifecycleRecoveryCommand) (json.RawMessage, error) {
	if value.WorkLimit < 1 || value.WorkLimit > maximumExamSittingLifecycleRecoveryWork {
		return nil, errors.New("Exam Sitting lifecycle recovery work limit is invalid")
	}
	return json.Marshal(value)
}

// DecodeExamSittingLifecycleRecoveryCommand strictly decodes and validates an Exam Sitting lifecycle recovery Job command.
func DecodeExamSittingLifecycleRecoveryCommand(document json.RawMessage) (ExamSittingLifecycleRecoveryCommand, error) {
	var value ExamSittingLifecycleRecoveryCommand
	if err := model.DecodeStrictJobDocument(document, &value); err != nil {
		return value, err
	}
	if value.WorkLimit < 1 || value.WorkLimit > maximumExamSittingLifecycleRecoveryWork {
		return value, errors.New("Exam Sitting lifecycle recovery work limit is invalid")
	}
	return value, nil
}

// EncodeExamSittingLifecycleRecoveryCheckpoint validates and encodes an Exam Sitting lifecycle recovery Job checkpoint.
func EncodeExamSittingLifecycleRecoveryCheckpoint(value ExamSittingLifecycleRecoveryCheckpoint) (json.RawMessage, error) {
	if err := validateExamSittingLifecycleRecoveryCheckpoint(value); err != nil {
		return nil, err
	}
	return json.Marshal(value)
}

// DecodeExamSittingLifecycleRecoveryCheckpoint strictly decodes and validates an Exam Sitting lifecycle recovery Job checkpoint.
func DecodeExamSittingLifecycleRecoveryCheckpoint(document json.RawMessage) (ExamSittingLifecycleRecoveryCheckpoint, error) {
	var value ExamSittingLifecycleRecoveryCheckpoint
	if err := model.DecodeStrictJobDocument(document, &value); err != nil {
		return value, err
	}
	return value, validateExamSittingLifecycleRecoveryCheckpoint(value)
}

func validateExamSittingLifecycleRecoveryCheckpoint(value ExamSittingLifecycleRecoveryCheckpoint) error {
	hasTime, hasID := !value.AfterDueAt.IsZero(), !value.AfterSittingID.IsZero()
	if hasTime != hasID ||
		(hasTime && (value.AfterDueAt.Location() != time.UTC || !value.AfterSittingID.IsValid())) ||
		value.Processed < 0 ||
		value.Changed < 0 ||
		value.Changed > value.Processed {
		return errors.New("Exam Sitting lifecycle recovery checkpoint is invalid")
	}

	return nil
}

type examSittingLifecycleRecoveryHandler struct {
	sittings sittingLifecycle
}

func (h examSittingLifecycleRecoveryHandler) Run(ctx context.Context, execution job.Execution) job.Outcome {
	if h.sittings == nil ||
		execution.Attempt == nil ||
		!execution.Job.ID.IsValid() ||
		!execution.Attempt.ID.IsValid() {
		return job.PermanentFailure("job.command.invalid", errors.New("Exam Sitting lifecycle recovery execution is incomplete"))
	}

	command, err := DecodeExamSittingLifecycleRecoveryCommand(execution.Job.Command)
	if err != nil {
		return job.PermanentFailure("job.command.invalid", err)
	}

	checkpoint := ExamSittingLifecycleRecoveryCheckpoint{}
	if len(execution.Job.Checkpoint) != 0 {
		checkpoint, err = DecodeExamSittingLifecycleRecoveryCheckpoint(execution.Job.Checkpoint)
		if err != nil {
			return job.PermanentFailure("job.checkpoint.invalid", err)
		}
	}

	if checkpoint.Processed > int64(command.WorkLimit) ||
		execution.Job.WorkReserved < int(checkpoint.Processed) ||
		execution.Job.WorkReserved > command.WorkLimit {
		return job.PermanentFailure("job.checkpoint.invalid", errors.New("Exam Sitting lifecycle recovery reservation and checkpoint disagree"))
	}

	remaining := command.WorkLimit - execution.Job.WorkReserved
	for remaining > 0 {
		limit := min(remaining, examSittingLifecycleRecoveryPageSize)
		page, listErr := h.sittings.ListLifecycleDue(ctx, invocation.Job(
			execution.Job.ID,
			execution.Attempt.ID,
		), store.ExamSittingLifecycleDueOptions{
			AfterDueAt:     checkpoint.AfterDueAt,
			AfterSittingID: checkpoint.AfterSittingID,
			Limit:          limit,
		})
		if listErr != nil {
			return examSittingLifecycleFailure(listErr)
		}
		if len(page) > limit {
			return job.PermanentFailure("job.invariant_failed", errors.New("Exam Sitting lifecycle recovery returned an oversized page"))
		}
		if len(page) == 0 {
			break
		}

		for _, due := range page {
			if err = validateExamSittingLifecycleDue(checkpoint, due); err != nil {
				return job.PermanentFailure("job.invariant_failed", err)
			}

			reserved, reserveErr := execution.ReserveWork(ctx, 1, command.WorkLimit)
			if reserveErr != nil {
				return job.RetryableFailure("dependency.unavailable", reserveErr)
			}
			if !reserved {
				return examSittingLifecycleRecoverySucceeded(checkpoint)
			}

			advanced, reconcileErr := h.sittings.AdvanceDue(
				ctx,
				invocation.Job(execution.Job.ID, execution.Attempt.ID),
				due.Value.Sitting.ID,
			)
			if reconcileErr != nil {
				return examSittingLifecycleFailure(reconcileErr)
			}

			result := &advanced
			if err = validateExamSittingLifecycleJobResult(due.Value.Sitting.ID, result); err != nil {
				return job.PermanentFailure("job.invariant_failed", err)
			}

			checkpoint.AfterDueAt, checkpoint.AfterSittingID = due.DueAt, due.Value.Sitting.ID
			checkpoint.Processed++
			if result.Changed {
				checkpoint.Changed++
			}

			document, encodeErr := EncodeExamSittingLifecycleRecoveryCheckpoint(checkpoint)
			if encodeErr != nil {
				return job.PermanentFailure("job.invariant_failed", encodeErr)
			}

			if checkpointErr := execution.Checkpoint(ctx, job.CheckpointValue{
				Progress: &model.JobProgress{Current: checkpoint.Processed, Total: int64(command.WorkLimit), Stage: "reconciling"},
				Document: document,
			}); checkpointErr != nil {
				return job.RetryableFailure("dependency.unavailable", checkpointErr)
			}

			remaining--
			if remaining == 0 {
				break
			}
		}

		if len(page) < limit {
			break
		}
	}

	return examSittingLifecycleRecoverySucceeded(checkpoint)
}

func validateExamSittingLifecycleDue(after ExamSittingLifecycleRecoveryCheckpoint, due store.ExamSittingLifecycleDue) error {
	if due.Value == nil ||
		due.Value.Sitting == nil ||
		due.DueAt.IsZero() ||
		due.DueAt.Location() != time.UTC ||
		due.Value.Sitting.Validate() != nil {
		return errors.New("Exam Sitting lifecycle recovery returned an invalid due Sitting")
	}

	if !after.AfterDueAt.IsZero() &&
		(due.DueAt.Before(after.AfterDueAt) ||
			(due.DueAt.Equal(after.AfterDueAt) && due.Value.Sitting.ID.String() <= after.AfterSittingID.String())) {
		return errors.New("Exam Sitting lifecycle recovery page is not strictly ordered")
	}

	return nil
}

func examSittingLifecycleRecoverySucceeded(checkpoint ExamSittingLifecycleRecoveryCheckpoint) job.Outcome {
	document, err := json.Marshal(ExamSittingLifecycleRecoveryResult{Processed: checkpoint.Processed, Changed: checkpoint.Changed})
	return job.Outcome{Kind: job.OutcomeSucceeded, Result: document, Err: err}
}

type examSittingLifecycleRecoveryProposer struct {
	jobs store.JobStore
	now  func() time.Time
}

func (p examSittingLifecycleRecoveryProposer) Propose(ctx context.Context, occurrence time.Time) error {
	if p.jobs == nil || p.now == nil {
		return errors.New("Exam Sitting lifecycle recovery proposer dependencies are required")
	}

	command, err := EncodeExamSittingLifecycleRecoveryCommand(
		ExamSittingLifecycleRecoveryCommand{WorkLimit: maximumExamSittingLifecycleRecoveryWork},
	)
	if err != nil {
		return err
	}

	at := model.TimeUTC(p.now())
	key := "exam-sitting-lifecycle-recovery:" + model.TimeUTC(occurrence).Format("2006-01-02")
	record, err := model.NewJobWithDedupePolicy(
		model.NewJobID(),
		model.JobTypeExamSittingLifecycleRecovery,
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

	_, _, err = p.jobs.Enqueue(ctx, &store.JobEnqueue{Job: record})
	return err
}

func examSittingLifecycleRecoveryDescriptor(handler job.Handler) job.Descriptor {
	descriptor := job.NewDescriptor(model.JobTypeExamSittingLifecycleRecovery, handler)
	descriptor.Checkpoints = true
	descriptor.ProgressStages = []string{"reconciling"}
	descriptor.PublicErrorCodes = []string{"dependency.unavailable", "job.checkpoint.invalid", "job.command.invalid", "job.invariant_failed"}
	descriptor.ExplicitRetryStatuses = []model.JobStatus{model.JobStatusFailed}
	return descriptor
}

// NewExamSittingLifecycleRecovery registers the reconciliation of Exam
// Sittings whose lifecycle Jobs are overdue, proposed daily.
func NewExamSittingLifecycleRecovery(sittings *sitting.Service, jobs store.JobStore, now func() time.Time) job.Registration {
	return job.Registration{
		Descriptor: examSittingLifecycleRecoveryDescriptor(examSittingLifecycleRecoveryHandler{sittings: sittings}),
		Recurrence: &job.Recurrence{Name: "exam-sitting-lifecycle-recovery", Proposer: examSittingLifecycleRecoveryProposer{jobs: jobs, now: now}},
	}
}
