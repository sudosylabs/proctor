// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package jobhandler

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/sudosylabs/proctor/server/app/invocation"
	"github.com/sudosylabs/proctor/server/app/job"
	"github.com/sudosylabs/proctor/server/model"
	"github.com/sudosylabs/proctor/server/store"
)

func TestExamSittingLifecycleJobFactoryFencesEachBoundaryByRevision(t *testing.T) {
	t.Parallel()
	at := time.Date(2026, time.August, 16, 9, 0, 0, 0, time.UTC)
	startAt, endAt := at.Add(time.Hour), at.Add(2*time.Hour)
	sittingID := model.NewExamSittingID()
	factory := NewExamSittingLifecycleJobFactory(func() time.Time { return at }, model.NewJobID)

	openJob, deadlineJob, err := factory.BoundaryJobs(sittingID, 4, startAt, endAt)
	require.NoError(t, err)
	assertExamSittingLifecycleJob(t, openJob, model.JobTypeExamSittingLifecycle, sittingID,
		model.ExamSittingLifecycleJobOpen, 4, at, startAt)
	assertExamSittingLifecycleJob(t, deadlineJob, model.JobTypeExamSittingLifecycle, sittingID,
		model.ExamSittingLifecycleJobDeadline, 4, at, endAt)

	nextDeadline := endAt.Add(time.Hour)
	extended, err := factory.DeadlineJob(sittingID, 5, nextDeadline)
	require.NoError(t, err)
	assertExamSittingLifecycleJob(t, extended, model.JobTypeExamSittingLifecycle, sittingID,
		model.ExamSittingLifecycleJobDeadline, 5, at, nextDeadline)

	finalize, err := factory.FinalizeJob(sittingID, 6, nextDeadline)
	require.NoError(t, err)
	assertExamSittingLifecycleJob(t, finalize, model.JobTypeExamSittingSealing, sittingID,
		model.ExamSittingLifecycleJobFinalize, 6, at, nextDeadline)
}

func TestExamSittingLifecycleHandlerReconcilesAndTreatsStaleWorkAsSuccess(t *testing.T) {
	t.Parallel()
	at := time.Date(2026, time.August, 16, 9, 0, 0, 0, time.UTC)
	sittingID := model.NewExamSittingID()
	record := mustExamSittingLifecycleJob(t, sittingID, at)
	attemptID := model.NewJobAttemptID()
	reconciler := &examSittingLifecycleFake{result: lifecycleJobResult(t, sittingID, false)}
	handler := examSittingLifecycleHandler{sittings: reconciler}

	outcome := handler.Run(context.Background(), job.NewExecution(record, &model.JobAttempt{ID: attemptID}, nil, nil))
	require.Equal(t, job.OutcomeSucceeded, outcome.Kind)
	require.NoError(t, outcome.Err)
	require.Equal(t, sittingID, reconciler.sittingID, "reconcile identity")
	require.Equal(t, invocation.Job(record.ID, attemptID), reconciler.system, "reconcile identity")
	var result ExamSittingLifecycleJobResult
	require.NoError(t, json.Unmarshal(outcome.Result, &result), "result = %#v", result)
	require.False(t, result.Changed, "result = %#v", result)
	require.Equal(t, model.ExamSittingScheduled, result.State, "result = %#v", result)
	require.Equal(t, int64(1), result.Revision, "result = %#v", result)
}

func TestExamSittingLifecycleHandlerClassifiesFailures(t *testing.T) {
	t.Parallel()
	at := time.Date(2026, time.August, 16, 9, 0, 0, 0, time.UTC)
	sittingID := model.NewExamSittingID()
	valid := mustExamSittingLifecycleJob(t, sittingID, at)
	attempt := &model.JobAttempt{ID: model.NewJobAttemptID()}

	tests := []struct {
		name string
		job  *model.Job
		err  error
		kind job.OutcomeKind
		code string
	}{
		{name: "invalid command", job: func() *model.Job {
			copy := *valid
			copy.Command = json.RawMessage(`{"exam_sitting_id":null}`)
			return &copy
		}(), kind: job.OutcomePermanentFailure, code: "job.command.invalid"},
		{name: "invalid durable request", job: valid, err: store.NewErrInvalidInput("exam_sitting", "advance_due", nil), kind: job.OutcomePermanentFailure, code: "job.invariant_failed"},
		{name: "database constraint rejection", job: valid, err: store.NewErrConstraintViolation("exam_sitting", "exam_sittings_lifecycle_check"), kind: job.OutcomeRetryableFailure, code: "dependency.unavailable"},
		{name: "missing durable target", job: valid, err: store.NewErrNotFound("exam_sitting", sittingID.String()), kind: job.OutcomePermanentFailure, code: "job.invariant_failed"},
		{name: "dependency failure", job: valid, err: errors.New("database unavailable"), kind: job.OutcomeRetryableFailure, code: "dependency.unavailable"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			reconciler := &examSittingLifecycleFake{err: tt.err}
			outcome := (examSittingLifecycleHandler{sittings: reconciler}).Run(context.Background(), job.NewExecution(tt.job, attempt, nil, nil))
			require.Equal(t, tt.kind, outcome.Kind)
			require.Equal(t, tt.code, outcome.PublicErrorCode)
		})
	}
}

func TestExamSittingLifecycleDescriptorIsBoundedAndNonCancelable(t *testing.T) {
	t.Parallel()
	descriptor := examSittingLifecycleDescriptor(examSittingLifecycleHandler{sittings: &examSittingLifecycleFake{}})
	require.Equal(t, model.JobTypeExamSittingLifecycle, descriptor.Type)
	require.False(t, descriptor.Cancelable)
	require.Equal(t, job.VisibilityOperator, descriptor.Visibility)
	require.Equal(t, 8, descriptor.MaximumAttempts)
	require.GreaterOrEqual(t, descriptor.Concurrency, 1)
}

type examSittingLifecycleFake struct {
	sittingLifecycle
	result    *store.ExamSittingLifecycleResult
	err       error
	sittingID model.ExamSittingID
	system    invocation.System
}

func (f *examSittingLifecycleFake) AdvanceDue(_ context.Context, system invocation.System, sittingID model.ExamSittingID) (store.ExamSittingLifecycleResult, error) {
	f.sittingID, f.system = sittingID, system
	if f.result == nil || f.err != nil {
		return store.ExamSittingLifecycleResult{}, f.err
	}
	return *f.result, nil
}

func mustExamSittingLifecycleJob(t *testing.T, sittingID model.ExamSittingID, at time.Time) *model.Job {
	t.Helper()
	command, err := model.EncodeExamSittingLifecycleCommand(model.ExamSittingLifecycleCommand{ExamSittingID: sittingID})
	require.NoError(t, err)
	job, err := model.NewJob(model.NewJobID(), model.JobTypeExamSittingLifecycle, command, "exam-sitting-test", at, at, 8)
	require.NoError(t, err)
	return job
}

func lifecycleJobResult(t *testing.T, sittingID model.ExamSittingID, changed bool) *store.ExamSittingLifecycleResult {
	t.Helper()
	at := time.Date(2026, time.August, 16, 9, 0, 0, 0, time.UTC)
	sitting, err := model.NewExamSitting(sittingID, model.NewExamID(), model.NewExamRevisionID(), model.NewClassID(), at.Add(time.Hour), at.Add(2*time.Hour), at)
	require.NoError(t, err)
	result := &store.ExamSittingLifecycleResult{Value: &store.ExamSittingSnapshot{Sitting: sitting}, Changed: changed}
	if changed {
		err = sitting.Open(at.Add(time.Hour))
		require.NoError(t, err)
		result.Transition = store.ExamSittingTransitionOpened
	}
	return result
}

func assertExamSittingLifecycleJob(t *testing.T, job *model.Job, wantType model.JobType,
	sittingID model.ExamSittingID, phase model.ExamSittingLifecycleJobPhase, revision int64,
	createdAt, availableAt time.Time,
) {
	t.Helper()
	require.NotNil(t, job)
	require.Equal(t, wantType, job.Type)
	require.Equal(t, model.JobDedupeActive, job.DedupePolicy)
	require.Equal(t, model.JobStatusQueued, job.Status)
	require.Equal(t, 8, job.MaximumAttempts)
	require.Equal(t, createdAt, job.CreatedAt)
	require.Equal(t, availableAt, job.AvailableAt)
	wantKey, err := model.ExamSittingLifecycleDedupeKey(sittingID, phase, revision)
	require.NoError(t, err)
	require.Equal(t, wantKey, job.DedupeKey, "dedupe key")
	command, err := model.DecodeExamSittingLifecycleCommand(job.Command)
	require.NoError(t, err, "command = %#v", command)
	require.Equal(t, sittingID, command.ExamSittingID, "command = %#v", command)
}

func TestExamSittingLifecycleJobReconcileLeavesClosingToDedicatedSealingJob(t *testing.T) {
	t.Parallel()
	at := time.Date(2026, time.August, 16, 9, 0, 0, 0, time.UTC)
	sitting, err := model.NewExamSitting(model.NewExamSittingID(), model.NewExamID(), model.NewExamRevisionID(), model.NewClassID(), at.Add(time.Hour), at.Add(2*time.Hour), at)
	require.NoError(t, err)
	err = sitting.Open(at.Add(time.Hour))
	require.NoError(t, err)
	err = sitting.EnterClosing(model.ExamSittingReasonScheduledEndReached, at.Add(2*time.Hour))
	require.NoError(t, err)
	closing := *sitting
	record := mustExamSittingLifecycleJob(t, sitting.ID, at)
	attemptID := model.NewJobAttemptID()
	fake := &examSittingLifecycleFake{
		result: &store.ExamSittingLifecycleResult{Value: &store.ExamSittingSnapshot{Sitting: &closing}, Changed: true, Transition: store.ExamSittingTransitionScheduledEndReached},
	}
	outcome := (examSittingLifecycleHandler{sittings: fake}).Run(context.Background(), job.NewExecution(record, &model.JobAttempt{ID: attemptID}, nil, nil))
	require.Equal(t, job.OutcomeSucceeded, outcome.Kind, "result/call")
	var result ExamSittingLifecycleJobResult
	require.NoError(t, json.Unmarshal(outcome.Result, &result), "result/call")
	require.Equal(t, model.ExamSittingClosing, result.State, "result/call")
	require.Equal(t, invocation.System{JobID: record.ID, AttemptID: attemptID}, fake.system, "result/call")

	// Another node may close before the lifecycle read. The dedicated sealing
	// handler still owns any closure work, so the handler reports AdvanceDue's
	// authoritative value and never invokes the legacy zero-Attempt closer.
	racing := &examSittingLifecycleFake{
		result: &store.ExamSittingLifecycleResult{Value: &store.ExamSittingSnapshot{Sitting: &closing}},
	}
	outcome = (examSittingLifecycleHandler{sittings: racing}).Run(context.Background(), job.NewExecution(record, &model.JobAttempt{ID: attemptID}, nil, nil))
	require.Equal(t, job.OutcomeSucceeded, outcome.Kind)
	require.NoError(t, json.Unmarshal(outcome.Result, &result))
	require.Equal(t, model.ExamSittingClosing, result.State)
}
