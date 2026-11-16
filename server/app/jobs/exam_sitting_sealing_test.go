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
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/sudosylabs/proctor/server/app/job"
	"github.com/sudosylabs/proctor/server/model"
	"github.com/sudosylabs/proctor/server/store"
)

func TestExamSittingSealingJobCheckpointsEachAttemptThenCloses(t *testing.T) {
	t.Parallel()
	at := time.Date(2026, time.August, 18, 9, 0, 0, 0, time.UTC)
	sitting := closingSittingForSealingTest(t, at)
	targets := automaticTargetsForSealingTest(sitting, 2)
	persistence := newSittingLifecycleStore(sealingLifecycleResult(sitting, true))
	sittings, _ := newLifecycleSittings(t, persistence)
	jobs := &deduplicatingJobEnqueuerFake{jobs: map[string]*model.Job{}}
	stores := &jobAttemptStores{sitting: sitting, targets: targets}
	attempts := newJobAttempts(t, stores, sittings)
	record := mustExamSittingSealingJob(t, sitting.ID, at)
	var checkpoints []job.CheckpointValue
	execution := job.NewExecution(record, &model.JobAttempt{ID: model.NewJobAttemptID()},
		func(_ context.Context, value job.CheckpointValue) error {
			checkpoints = append(checkpoints, value)
			return nil
		}, allowJobWorkReservation())
	outcome := (examSittingSealingHandler{sittings: sittings, attempts: attempts.service, jobs: jobs, now: sealingTestNow, newID: model.NewJobID}).Run(context.Background(), execution)
	require.Equal(t, job.OutcomeSucceeded, outcome.Kind, "outcome=%#v sealed=%d checkpoints=%d finish=%d continuations", outcome, len(stores.sealed()), len(checkpoints), len(persistence.finished))
	require.NoError(t, outcome.Err, "outcome=%#v sealed=%d checkpoints=%d finish=%d continuations", outcome, len(stores.sealed()), len(checkpoints), len(persistence.finished))
	require.Len(t, stores.sealed(), 2, "outcome=%#v sealed=%d checkpoints=%d finish=%d continuations", outcome, len(stores.sealed()), len(checkpoints), len(persistence.finished))
	require.Len(t, checkpoints, 2, "outcome=%#v sealed=%d checkpoints=%d finish=%d continuations", outcome, len(stores.sealed()), len(checkpoints), len(persistence.finished))
	require.Len(t, persistence.finished, 1, "outcome=%#v sealed=%d checkpoints=%d finish=%d continuations", outcome, len(stores.sealed()), len(checkpoints), len(persistence.finished))
	require.Empty(t, sealingContinuations(t, jobs), "outcome=%#v sealed=%d checkpoints=%d finish=%d continuations", outcome, len(stores.sealed()), len(checkpoints), len(persistence.finished))
	require.Equal(t, []model.ExamAttemptID{targets[0].AttemptID, targets[1].AttemptID}, stores.sealed(), "sealed in Attempt order")
	// Each seal commits under its own system audit attempt, which names the
	// sealing Job, and carries the candidate's frozen receipt.
	audits := attempts.access.Audits.Attempts()
	require.Len(t, audits, 2, "one system audit attempt per seal")
	for index, seal := range stores.seals {
		require.Equal(t, audits[index].ID, seal.AuditEventID, "the Store completes the attempt the service began")
		require.Empty(t, audits[index].ActorID, "the sealing Job acts as no User")
		require.Contains(t, string(audits[index].Parameters), record.ID.String(), "the attempt names the sealing Job")
		require.NotNil(t, seal.Notice, "a fresh seal carries the receipt")
		require.Equal(t, targets[index].CandidateUserID, seal.Notice.Delivery.TargetUserID, "the receipt goes to the candidate")
	}
	var result ExamSittingSealingResult
	err := json.Unmarshal(outcome.Result, &result)
	require.NoError(t, err, "result=%#v err", result)
	require.Equal(t, int64(2), result.Processed, "result=%#v err", result)
	require.Equal(t, int64(2), result.Sealed, "result=%#v err", result)
	require.False(t, result.Continued, "result=%#v err", result)
	require.Equal(t, model.ExamSittingClosed, result.State, "result=%#v err", result)
	checkpoint, err := decodeExamSittingSealingCheckpoint(checkpoints[1].Document)
	require.NoError(t, err, "checkpoint=%#v err", checkpoint)
	require.Equal(t, targets[1].AttemptID, checkpoint.AfterAttemptID, "checkpoint=%#v err", checkpoint)
	require.Equal(t, int64(2), checkpoint.Processed, "checkpoint=%#v err", checkpoint)
	require.Equal(t, int64(2), checkpoint.Sealed, "checkpoint=%#v err", checkpoint)
}

func TestExamSittingSealingJobBoundsPassAndDurablyContinues(t *testing.T) {
	t.Parallel()
	at := time.Date(2026, time.August, 18, 9, 0, 0, 0, time.UTC)
	sitting := closingSittingForSealingTest(t, at)
	persistence := newSittingLifecycleStore(sealingLifecycleResult(sitting, false))
	sittings, _ := newLifecycleSittings(t, persistence)
	jobs := &deduplicatingJobEnqueuerFake{jobs: map[string]*model.Job{}}
	stores := &jobAttemptStores{sitting: sitting, targets: automaticTargetsForSealingTest(sitting, examSittingSealingMaximumWork+1)}
	attempts := newJobAttempts(t, stores, sittings)
	record := mustExamSittingSealingJob(t, sitting.ID, at)
	outcome := (examSittingSealingHandler{sittings: sittings, attempts: attempts.service, jobs: jobs, now: sealingTestNow, newID: model.NewJobID}).Run(
		context.Background(), job.NewExecution(record, &model.JobAttempt{ID: model.NewJobAttemptID()},
			func(context.Context, job.CheckpointValue) error { return nil }, allowJobWorkReservation()))
	require.Equal(t, job.OutcomeSucceeded, outcome.Kind, "outcome=%#v sealed=%d finish=%d continuations", outcome, len(stores.sealed()), len(persistence.finished))
	require.Len(t, stores.sealed(), examSittingSealingMaximumWork, "outcome=%#v sealed=%d finish=%d continuations", outcome, len(stores.sealed()), len(persistence.finished))
	require.Len(t, persistence.finished, 1, "outcome=%#v sealed=%d finish=%d continuations", outcome, len(stores.sealed()), len(persistence.finished))
	queued := sealingContinuations(t, jobs)
	require.Len(t, queued, 1, "outcome=%#v sealed=%d finish=%d continuations", outcome, len(stores.sealed()), len(persistence.finished))
	continuation, err := model.DecodeExamSittingLifecycleCommand(queued[0].Command)
	require.NoError(t, err, "outcome=%#v sealed=%d finish=%d continuations", outcome, len(stores.sealed()), len(persistence.finished))
	require.Equal(t, sitting.ID, continuation.ExamSittingID, "outcome=%#v sealed=%d finish=%d continuations", outcome, len(stores.sealed()), len(persistence.finished))
	require.Equal(t, "exam-sitting-sealing-continuation:"+record.ID.String(), queued[0].DedupeKey, "outcome=%#v sealed=%d finish=%d continuations", outcome, len(stores.sealed()), len(persistence.finished))
	require.Equal(t, model.JobDedupePermanent, queued[0].DedupePolicy, "outcome=%#v sealed=%d finish=%d continuations", outcome, len(stores.sealed()), len(persistence.finished))
	require.Equal(t, model.JobTypeExamSittingSealing, queued[0].Type)
	require.Equal(t, 8, queued[0].MaximumAttempts)
	require.True(t, queued[0].CreatedAt.Equal(model.TimeUTC(sealingTestNow())), "created_at=%s", queued[0].CreatedAt)
	require.True(t, queued[0].AvailableAt.Equal(model.TimeUTC(sealingTestNow())), "available_at=%s", queued[0].AvailableAt)
	var result ExamSittingSealingResult
	err = json.Unmarshal(outcome.Result, &result)
	require.NoError(t, err, "result=%#v err", result)
	require.True(t, result.Continued, "result=%#v err", result)
	require.Equal(t, model.ExamSittingClosing, result.State, "result=%#v err", result)
	require.Equal(t, sitting.Revision, result.Revision, "result=%#v err", result)
	require.Equal(t, int64(examSittingSealingMaximumWork), result.Processed, "result=%#v err", result)
	require.Equal(t, int64(examSittingSealingMaximumWork), result.Sealed, "result=%#v err", result)
}

func TestExamSittingSealingDescriptorIsBoundedCheckpointedAndNonCancelable(t *testing.T) {
	t.Parallel()
	descriptor := examSittingSealingDescriptor(examSittingSealingHandler{})
	require.Equal(t, model.JobTypeExamSittingSealing, descriptor.Type)
	require.False(t, descriptor.Cancelable)
	require.Equal(t, job.VisibilityOperator, descriptor.Visibility)
	require.True(t, descriptor.Checkpoints)
	require.Len(t, descriptor.ProgressStages, 1)
	require.Equal(t, "sealing", descriptor.ProgressStages[0])
}

func TestExamSittingSealingJobLeavesUnfinishedClosingStateRetryable(t *testing.T) {
	t.Parallel()
	at := time.Date(2026, time.August, 18, 9, 0, 0, 0, time.UTC)
	sitting := closingSittingForSealingTest(t, at)
	persistence := newSittingLifecycleStore(sealingLifecycleResult(sitting, false))
	sittings, _ := newLifecycleSittings(t, persistence)
	jobs := &deduplicatingJobEnqueuerFake{jobs: map[string]*model.Job{}}
	attempts := newJobAttempts(t, &jobAttemptStores{sitting: sitting}, sittings)
	record := mustExamSittingSealingJob(t, sitting.ID, at)
	outcome := (examSittingSealingHandler{sittings: sittings, attempts: attempts.service, jobs: jobs, now: sealingTestNow, newID: model.NewJobID}).Run(context.Background(),
		job.NewExecution(record, &model.JobAttempt{ID: model.NewJobAttemptID()}, nil, allowJobWorkReservation()))
	require.Equal(t, job.OutcomeRetryableFailure, outcome.Kind, "outcome=%#v finish", outcome)
	require.Equal(t, "dependency.unavailable", outcome.PublicErrorCode, "outcome=%#v finish", outcome)
	require.Len(t, persistence.finished, 1, "outcome=%#v finish", outcome)
}

func TestExamSittingSealingJobRetriesDatabaseConstraintRejection(t *testing.T) {
	t.Parallel()
	at := time.Date(2026, time.August, 18, 9, 0, 0, 0, time.UTC)
	sitting := closingSittingForSealingTest(t, at)
	record := mustExamSittingSealingJob(t, sitting.ID, at)
	for _, test := range []struct {
		name string
		err  error
		kind job.OutcomeKind
		code string
	}{
		{"database constraint rejection", store.NewErrConstraintViolation("exam_sitting", "exam_sittings_lifecycle_check"), job.OutcomeRetryableFailure, "dependency.unavailable"},
		{"refused input", store.NewErrInvalidInput("exam_sitting", "finish", nil), job.OutcomePermanentFailure, "job.invariant_failed"},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			persistence := newSittingLifecycleStore(sealingLifecycleResult(sitting, false))
			persistence.err = test.err
			sittings, _ := newLifecycleSittings(t, persistence)
			jobs := &deduplicatingJobEnqueuerFake{jobs: map[string]*model.Job{}}
			attempts := newJobAttempts(t, &jobAttemptStores{sitting: sitting}, sittings)
			outcome := (examSittingSealingHandler{sittings: sittings, attempts: attempts.service, jobs: jobs, now: sealingTestNow, newID: model.NewJobID}).Run(context.Background(),
				job.NewExecution(record, &model.JobAttempt{ID: model.NewJobAttemptID()}, nil, allowJobWorkReservation()))
			require.Equal(t, test.kind, outcome.Kind, "outcome=%#v", outcome)
			require.Equal(t, test.code, outcome.PublicErrorCode, "outcome=%#v", outcome)
		})
	}
}

func TestExamSittingSealingJobRecoversReservationAheadOfCheckpoint(t *testing.T) {
	t.Parallel()
	at := time.Date(2026, time.August, 18, 9, 0, 0, 0, time.UTC)
	sitting := closingSittingForSealingTest(t, at)
	targets := automaticTargetsForSealingTest(sitting, 1)
	persistence := newSittingLifecycleStore(sealingLifecycleResult(sitting, true))
	sittings, _ := newLifecycleSittings(t, persistence)
	jobs := &deduplicatingJobEnqueuerFake{jobs: map[string]*model.Job{}}
	stores := &jobAttemptStores{sitting: sitting, targets: targets}
	attempts := newJobAttempts(t, stores, sittings)
	record := mustExamSittingSealingJob(t, sitting.ID, at)
	record.WorkReserved = 1
	var checkpoints []ExamSittingSealingCheckpoint
	reserved := 0
	execution := job.NewExecution(record, &model.JobAttempt{ID: model.NewJobAttemptID()},
		func(_ context.Context, value job.CheckpointValue) error {
			checkpoint, err := decodeExamSittingSealingCheckpoint(value.Document)
			require.NoError(t, err)
			checkpoints = append(checkpoints, checkpoint)
			return nil
		}, func(_ context.Context, units, limit int) (bool, error) {
			require.Equal(t, 1, units, "reservation units")
			require.Equal(t, examSittingSealingMaximumWork, limit, "reservation units")
			reserved += units
			return true, nil
		})
	outcome := (examSittingSealingHandler{sittings: sittings, attempts: attempts.service, jobs: jobs, now: sealingTestNow, newID: model.NewJobID}).Run(context.Background(), execution)
	require.Equal(t, job.OutcomeSucceeded, outcome.Kind, "outcome=%#v reserved=%d sealed=%d checkpoints", outcome, reserved, len(stores.sealed()))
	require.Equal(t, 1, reserved, "outcome=%#v reserved=%d sealed=%d checkpoints", outcome, reserved, len(stores.sealed()))
	require.Len(t, stores.sealed(), 1, "outcome=%#v reserved=%d sealed=%d checkpoints", outcome, reserved, len(stores.sealed()))
	require.Len(t, checkpoints, 2, "outcome=%#v reserved=%d sealed=%d checkpoints", outcome, reserved, len(stores.sealed()))
	require.Equal(t, int64(1), checkpoints[0].Processed)
	require.Equal(t, int64(0), checkpoints[0].Sealed)
	require.True(t, checkpoints[0].AfterAttemptID.IsZero())
	require.Equal(t, int64(2), checkpoints[1].Processed)
	require.Equal(t, int64(1), checkpoints[1].Sealed)
	require.Equal(t, targets[0].AttemptID, checkpoints[1].AfterAttemptID)
	var result ExamSittingSealingResult
	err := json.Unmarshal(outcome.Result, &result)
	require.NoError(t, err, "result=%#v err", result)
	require.Equal(t, int64(2), result.Processed, "result=%#v err", result)
	require.Equal(t, int64(1), result.Sealed, "result=%#v err", result)
	require.Equal(t, model.ExamSittingClosed, result.State, "result=%#v err", result)
}

var sealingTestNow = func() time.Time { return time.Date(2026, time.August, 18, 9, 30, 0, 0, time.UTC) }

// sealingContinuations returns the sealing continuation Jobs the handler queued.
func sealingContinuations(t *testing.T, jobs *deduplicatingJobEnqueuerFake) []*model.Job {
	t.Helper()
	var queued []*model.Job
	for _, record := range jobs.jobs {
		require.Equal(t, model.JobTypeExamSittingSealing, record.Type)
		queued = append(queued, record)
	}
	return queued
}

func mustExamSittingSealingJob(t *testing.T, sittingID model.ExamSittingID, at time.Time) *model.Job {
	t.Helper()
	command, err := model.EncodeExamSittingLifecycleCommand(model.ExamSittingLifecycleCommand{ExamSittingID: sittingID})
	require.NoError(t, err)
	job, err := model.NewJob(model.NewJobID(), model.JobTypeExamSittingSealing, command, "sealing-test", at, at, 8)
	require.NoError(t, err)
	return job
}

func closingSittingForSealingTest(t *testing.T, at time.Time) *model.ExamSitting {
	t.Helper()
	sitting, err := model.NewExamSitting(model.NewExamSittingID(), model.NewExamID(), model.NewExamRevisionID(),
		model.NewClassID(), at.Add(-2*time.Hour), at.Add(-time.Hour), at.Add(-3*time.Hour))
	require.NoError(t, err)
	err = sitting.Open(at.Add(-2 * time.Hour))
	require.NoError(t, err)
	err = sitting.EnterClosing(model.ExamSittingReasonScheduledEndReached, at.Add(-time.Hour))
	require.NoError(t, err)
	return sitting
}

func sealingLifecycleResult(sitting *model.ExamSitting, closed bool) *store.ExamSittingLifecycleResult {
	copy := *sitting
	result := &store.ExamSittingLifecycleResult{Value: &store.ExamSittingSnapshot{Sitting: &copy}}
	if closed {
		_ = copy.Close(copy.ClosingAt.Time.Add(time.Second))
		result.Changed, result.Transition = true, store.ExamSittingTransitionSealingCompleted
	}
	return result
}

func automaticTargetsForSealingTest(sitting *model.ExamSitting, count int) []store.ExamSubmissionAutomaticSealTarget {
	items := make([]store.ExamSubmissionAutomaticSealTarget, count)
	for index := range items {
		items[index] = store.ExamSubmissionAutomaticSealTarget{ExamID: sitting.ExamID, SittingID: sitting.ID,
			ClassID: sitting.ClassID, AcademicUnitID: model.NewAcademicUnitID(), CandidateUserID: model.NewUserID(),
			AttemptID: model.NewExamAttemptID(), CurrentRevisionID: sitting.ExamRevisionID, WorkspaceID: model.NewExamAttemptWorkspaceID(),
			ParticipationID: model.NewAttemptParticipationID(), Generation: 1, ConnectionID: model.NewAttemptConnectionID()}
	}
	slices.SortFunc(items, func(left, right store.ExamSubmissionAutomaticSealTarget) int {
		return strings.Compare(left.AttemptID.String(), right.AttemptID.String())
	})
	return items
}
