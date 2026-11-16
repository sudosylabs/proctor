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
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/sudosylabs/proctor/server/app/job"
	"github.com/sudosylabs/proctor/server/model"
	"github.com/sudosylabs/proctor/server/store"
)

func TestExamSittingLifecycleRecoveryDocumentsAreStrictAndBounded(t *testing.T) {
	t.Parallel()
	command, err := EncodeExamSittingLifecycleRecoveryCommand(ExamSittingLifecycleRecoveryCommand{WorkLimit: 12})
	require.NoError(t, err)
	decoded, err := DecodeExamSittingLifecycleRecoveryCommand(command)
	require.NoError(t, err, "command = %#v", decoded)
	require.Equal(t, 12, decoded.WorkLimit, "command = %#v", decoded)
	for _, malformed := range []json.RawMessage{json.RawMessage(`{}`), json.RawMessage(`{"work_limit":0}`), json.RawMessage(`{"work_limit":1001}`), json.RawMessage(`{"work_limit":1,"extra":true}`), json.RawMessage(`{"work_limit":1,"work_limit":2}`)} {
		_, err = DecodeExamSittingLifecycleRecoveryCommand(malformed)
		require.Error(t, err, "accepted %s", malformed)
	}

	at := time.Date(2026, time.August, 16, 8, 0, 0, 0, time.UTC)
	checkpoint := ExamSittingLifecycleRecoveryCheckpoint{AfterDueAt: at, AfterSittingID: model.NewExamSittingID(), Processed: 2, Changed: 1}
	document, err := EncodeExamSittingLifecycleRecoveryCheckpoint(checkpoint)
	require.NoError(t, err)
	decodedCheckpoint, err := DecodeExamSittingLifecycleRecoveryCheckpoint(document)
	require.NoError(t, err, "checkpoint")
	require.Equal(t, checkpoint, decodedCheckpoint, "checkpoint")
}

func TestExamSittingLifecycleRecoveryResumesCheckpointAndHonorsWorkLimit(t *testing.T) {
	t.Parallel()
	at := time.Date(2026, time.August, 16, 8, 0, 0, 0, time.UTC)
	firstID, secondID := model.NewExamSittingID(), model.NewExamSittingID()
	priorID := model.NewExamSittingID()
	checkpoint := ExamSittingLifecycleRecoveryCheckpoint{AfterDueAt: at.Add(-time.Minute), AfterSittingID: priorID, Processed: 1}
	checkpointDocument, err := EncodeExamSittingLifecycleRecoveryCheckpoint(checkpoint)
	require.NoError(t, err)
	record := mustRecoveryJob(t, at, 3)
	record.Checkpoint, record.WorkReserved = checkpointDocument, 1
	persistence := newSittingLifecycleStore(lifecycleJobResult(t, firstID, false), lifecycleJobResult(t, secondID, true))
	persistence.pages = [][]store.ExamSittingLifecycleDue{{
		{Value: lifecycleJobResult(t, firstID, false).Value, DueAt: at},
		{Value: lifecycleJobResult(t, secondID, false).Value, DueAt: at.Add(time.Second)},
	}}
	service, _ := newLifecycleSittings(t, persistence)
	var saved []job.CheckpointValue
	reserved := 0
	execution := job.NewExecution(record, &model.JobAttempt{ID: model.NewJobAttemptID()}, func(_ context.Context, value job.CheckpointValue) error {
		saved = append(saved, value)
		return nil
	}, func(_ context.Context, units, limit int) (bool, error) {
		if limit != 3 || reserved+units > 2 {
			return false, nil
		}
		reserved += units
		return true, nil
	})
	outcome := (examSittingLifecycleRecoveryHandler{sittings: service}).Run(context.Background(), execution)
	require.Equal(t, job.OutcomeSucceeded, outcome.Kind)
	require.NoError(t, outcome.Err)
	require.Len(t, persistence.listOptions, 1)
	require.Equal(t, checkpoint.AfterDueAt, persistence.listOptions[0].AfterDueAt)
	require.Equal(t, priorID, persistence.listOptions[0].AfterSittingID)
	require.Equal(t, 2, persistence.listOptions[0].Limit)
	require.Equal(t, []model.ExamSittingID{firstID, secondID}, persistence.advancedIDs(), "reconciled/checkpoints")
	require.Len(t, saved, 2, "reconciled/checkpoints")
	var result ExamSittingLifecycleRecoveryResult
	err = json.Unmarshal(outcome.Result, &result)
	require.NoError(t, err, "result = %#v", result)
	require.Equal(t, int64(3), result.Processed, "result = %#v", result)
	require.Equal(t, int64(1), result.Changed, "result = %#v", result)
	last, err := DecodeExamSittingLifecycleRecoveryCheckpoint(saved[len(saved)-1].Document)
	require.NoError(t, err, "last checkpoint = %#v", last)
	require.Equal(t, secondID, last.AfterSittingID, "last checkpoint = %#v", last)
	require.Equal(t, int64(3), last.Processed, "last checkpoint = %#v", last)
	require.Equal(t, int64(1), last.Changed, "last checkpoint = %#v", last)
}

func TestExamSittingLifecycleRecoveryClassifiesListAndCheckpointFailures(t *testing.T) {
	t.Parallel()
	at := time.Date(2026, time.August, 16, 8, 0, 0, 0, time.UTC)
	record := mustRecoveryJob(t, at, 1)
	attempt := &model.JobAttempt{ID: model.NewJobAttemptID()}
	failing := newSittingLifecycleStore()
	failing.err = errors.New("database unavailable")
	service, _ := newLifecycleSittings(t, failing)
	outcome := (examSittingLifecycleRecoveryHandler{sittings: service}).Run(context.Background(), job.NewExecution(record, attempt, nil, allowJobWorkReservation()))
	require.Equal(t, job.OutcomeRetryableFailure, outcome.Kind)
	require.Equal(t, "dependency.unavailable", outcome.PublicErrorCode)

	bad := *record
	bad.Command = json.RawMessage(`{"work_limit":0}`)
	outcome = (examSittingLifecycleRecoveryHandler{sittings: service}).Run(context.Background(), job.NewExecution(&bad, attempt, nil, nil))
	require.Equal(t, job.OutcomePermanentFailure, outcome.Kind)
	require.Equal(t, "job.command.invalid", outcome.PublicErrorCode)

	sittingID := model.NewExamSittingID()
	checkpointStore := newSittingLifecycleStore(lifecycleJobResult(t, sittingID, false))
	checkpointStore.pages = [][]store.ExamSittingLifecycleDue{{{Value: lifecycleJobResult(t, sittingID, false).Value, DueAt: at}}}
	checkpointService, _ := newLifecycleSittings(t, checkpointStore)
	outcome = (examSittingLifecycleRecoveryHandler{sittings: checkpointService}).Run(context.Background(), job.NewExecution(
		record, attempt,
		func(context.Context, job.CheckpointValue) error { return errors.New("checkpoint unavailable") },
		allowJobWorkReservation(),
	))
	require.Equal(t, job.OutcomeRetryableFailure, outcome.Kind)
	require.Equal(t, "dependency.unavailable", outcome.PublicErrorCode)
}

func TestExamSittingLifecycleRecoveryProposerUsesPermanentUTCDateOccurrence(t *testing.T) {
	t.Parallel()
	at := time.Date(2026, time.August, 16, 8, 0, 0, 0, time.UTC)
	jobs := &deduplicatingJobEnqueuerFake{jobs: map[string]*model.Job{}}
	proposer := examSittingLifecycleRecoveryProposer{jobs: jobs, now: func() time.Time { return at }}
	occurrence := time.Date(2026, time.August, 17, 1, 0, 0, 0, time.FixedZone("offset", 2*60*60))
	require.NoError(t, proposer.Propose(context.Background(), occurrence))
	var queued *model.Job
	for _, value := range jobs.jobs {
		queued = value
	}
	require.NotNil(t, queued)
	require.Equal(t, model.JobTypeExamSittingLifecycleRecovery, queued.Type)
	require.Equal(t, model.JobDedupePermanent, queued.DedupePolicy)
	require.Equal(t, "exam-sitting-lifecycle-recovery:2026-08-16", queued.DedupeKey)
}

func TestExamSittingLifecycleRecoveryDescriptorDeclaresCheckpointContract(t *testing.T) {
	t.Parallel()
	sittings, _ := newLifecycleSittings(t, newSittingLifecycleStore())
	descriptor := examSittingLifecycleRecoveryDescriptor(examSittingLifecycleRecoveryHandler{sittings: sittings})
	require.Equal(t, model.JobTypeExamSittingLifecycleRecovery, descriptor.Type)
	require.False(t, descriptor.Cancelable)
	require.Equal(t, job.VisibilityOperator, descriptor.Visibility)
	require.True(t, descriptor.Checkpoints)
	require.Len(t, descriptor.ProgressStages, 1)
	require.Equal(t, "reconciling", descriptor.ProgressStages[0])
}

func mustRecoveryJob(t *testing.T, at time.Time, limit int) *model.Job {
	t.Helper()
	command, err := EncodeExamSittingLifecycleRecoveryCommand(ExamSittingLifecycleRecoveryCommand{WorkLimit: limit})
	require.NoError(t, err)
	job, err := model.NewJobWithDedupePolicy(model.NewJobID(), model.JobTypeExamSittingLifecycleRecovery, command, "recovery-test", model.JobDedupePermanent, at, at, 5)
	require.NoError(t, err)
	return job
}
