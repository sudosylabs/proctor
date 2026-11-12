// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package storetest

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/sudosylabs/proctor/server/model"
	"github.com/sudosylabs/proctor/server/store"
)

// TestDomainEventStore verifies the outbox read and retire contract. Rows
// exist only through the aggregate that commits their fact, so the suite
// seals a voluntary Submission to create one.
func TestDomainEventStore(t *testing.T, ss store.Store) {
	t.Helper()
	ctx := context.Background()
	events := ss.DomainEvent()

	t.Run("InvalidIdentifier", func(t *testing.T) {
		var invalid *store.ErrInvalidInput
		_, err := events.Get(ctx, "bad")
		require.ErrorAs(t, err, &invalid)
		require.ErrorAs(t, events.Delete(ctx, "bad"), &invalid)
	})
	t.Run("Absent", func(t *testing.T) {
		missing := model.NewDomainEventID()
		event, err := events.Get(ctx, missing)
		require.True(t, store.IsNotFound(err), "Get(absent event) err=%v", err)
		require.Nil(t, event)
		require.NoError(t, events.Delete(ctx, missing), "deleting an absent event is already done")
	})
	t.Run("SealedSubmission", func(t *testing.T) {
		fixture := newExamAttemptFixture(t, ctx, ss)
		connected, focusAccess := connectFocusLossFixture(t, ctx, ss, fixture, "domain-event-connect")
		access := submissionAccess(focusAccess, fixture.sitting.ExamRevisionID, connected.Workspace.Cursor, 0)
		input := &store.ExamSubmissionSeal{SubmissionID: model.NewSubmissionID(), ReceiptCode: model.NewSubmissionReceiptCode(),
			Access: access, AuditEventID: saveExamAttemptAudit(t, ctx, ss, fixture).ID, AuditAt: model.NowUTC()}
		attachSubmissionReceipt(t, ctx, ss, fixture.candidate, input)
		sealed, err := ss.ExamSubmission().Seal(ctx, input, examCommand(fixture.candidate.ID, store.ExamSubmissionSealOperation,
			"domain-event-seal", "domain-event-seal"))
		require.NoError(t, err)
		requireExamAttemptSubmittedEvent(t, ctx, ss, input.DomainEventID, input.DomainEventJobID, examAttemptSubmittedFact{
			SubmissionID: sealed.Receipt.SubmissionID, AttemptID: connected.Attempt.ID, SittingID: fixture.sitting.ID,
			ExamID: fixture.examID, SubmittedAt: sealed.Receipt.SubmittedAt,
		})

		require.NoError(t, events.Delete(ctx, input.DomainEventID))
		event, err := events.Get(ctx, input.DomainEventID)
		require.True(t, store.IsNotFound(err), "Get(delivered event) err=%v", err)
		require.Nil(t, event)
		require.NoError(t, events.Delete(ctx, input.DomainEventID), "a repeated delivery deletes nothing")
	})
}

// examAttemptSubmittedFact is what an exam.attempt.submitted event must name.
type examAttemptSubmittedFact struct {
	SubmissionID model.SubmissionID
	AttemptID    model.ExamAttemptID
	SittingID    model.ExamSittingID
	ExamID       model.ExamID
	SubmittedAt  time.Time
}

// requireExamAttemptSubmittedEvent asserts that a fresh seal committed exactly
// the proposed outbox row, naming only the sealed Submission's identifiers, and
// its queued dispatch Job.
func requireExamAttemptSubmittedEvent(t *testing.T, ctx context.Context, ss store.Store, eventID model.DomainEventID,
	jobID model.JobID, fact examAttemptSubmittedFact,
) {
	t.Helper()
	event, err := ss.DomainEvent().Get(ctx, eventID)
	require.NoError(t, err)
	require.Equal(t, eventID, event.ID)
	require.Equal(t, model.DomainEventTopicExamAttemptSubmitted, event.Topic)
	require.False(t, event.OccurredAt.Before(fact.SubmittedAt), "event occurred_at=%s precedes submitted_at=%s", event.OccurredAt, fact.SubmittedAt)
	var data map[string]string
	require.NoError(t, json.Unmarshal(event.Data, &data))
	require.Equal(t, map[string]string{
		"submission_id": fact.SubmissionID.String(), "attempt_id": fact.AttemptID.String(),
		"sitting_id": fact.SittingID.String(), "exam_id": fact.ExamID.String(),
	}, data)

	job, err := ss.Job().Get(ctx, jobID)
	require.NoError(t, err)
	require.Equal(t, model.JobTypeDomainEventDispatch, job.Type)
	require.Equal(t, model.JobStatusQueued, job.Status)
	require.Equal(t, eventID.String(), job.DedupeKey)
	require.Equal(t, model.DomainEventDispatchMaximumAttempts, job.MaximumAttempts)
	require.True(t, job.CreatedAt.Equal(event.OccurredAt), "dispatch Job created_at=%s event occurred_at=%s", job.CreatedAt, event.OccurredAt)
	command, err := model.DecodeDomainEventDispatchCommand(job.Command)
	require.NoError(t, err)
	require.Equal(t, eventID, command.EventID)
}

// requireNoDomainEvent asserts that a replayed or failed seal committed
// neither the proposed outbox row nor its dispatch Job.
func requireNoDomainEvent(t *testing.T, ctx context.Context, ss store.Store, eventID model.DomainEventID, jobID model.JobID) {
	t.Helper()
	event, err := ss.DomainEvent().Get(ctx, eventID)
	require.True(t, store.IsNotFound(err), "Get(uncommitted event) err=%v", err)
	require.Nil(t, event)
	job, err := ss.Job().Get(ctx, jobID)
	require.True(t, store.IsNotFound(err), "Get(uncommitted dispatch Job) err=%v", err)
	require.Nil(t, job)
}

// DomainEventCounter returns the number of outbox rows and of dispatch Jobs.
// Only the SQL adapter can count rows the public contract never lists.
type DomainEventCounter func(*testing.T, context.Context) (events, dispatchJobs int)

// TestExamSubmissionDomainEvents verifies that each seal path (voluntary,
// manager-ended and automatic) commits exactly
// one exam.attempt.submitted event and one dispatch Job with a new
// Submission, and that a replayed or failed seal commits neither.
func TestExamSubmissionDomainEvents(t *testing.T, ss store.Store, count DomainEventCounter) {
	t.Helper()
	ctx := context.Background()
	submissions := ss.ExamSubmission()
	requireDelta := func(t *testing.T, beforeEvents, beforeJobs, wantEvents, wantJobs int, message string) {
		t.Helper()
		events, jobs := count(t, ctx)
		require.Equal(t, wantEvents, events-beforeEvents, "%s: domain events", message)
		require.Equal(t, wantJobs, jobs-beforeJobs, "%s: dispatch Jobs", message)
	}

	t.Run("Voluntary", func(t *testing.T) {
		fixture := newExamAttemptFixture(t, ctx, ss)
		connected, focusAccess := connectFocusLossFixture(t, ctx, ss, fixture, "domain-event-voluntary-connect")
		access := submissionAccess(focusAccess, fixture.sitting.ExamRevisionID, connected.Workspace.Cursor, 0)

		failed := &store.ExamSubmissionSeal{SubmissionID: model.NewSubmissionID(), ReceiptCode: model.NewSubmissionReceiptCode(),
			Access: access, AuditEventID: model.NewAuditEventID(), AuditAt: model.NowUTC()}
		attachSubmissionReceipt(t, ctx, ss, fixture.candidate, failed)
		beforeEvents, beforeJobs := count(t, ctx)
		_, err := submissions.Seal(ctx, failed, examCommand(fixture.candidate.ID, store.ExamSubmissionSealOperation,
			"domain-event-voluntary-failed", "domain-event-voluntary-failed"))
		require.Error(t, err, "Seal(with missing audit) succeeded")
		requireDelta(t, beforeEvents, beforeJobs, 0, 0, "failed seal")
		requireNoDomainEvent(t, ctx, ss, failed.DomainEventID, failed.DomainEventJobID)

		input := &store.ExamSubmissionSeal{SubmissionID: model.NewSubmissionID(), ReceiptCode: model.NewSubmissionReceiptCode(),
			Access: access, AuditEventID: saveExamAttemptAudit(t, ctx, ss, fixture).ID, AuditAt: model.NowUTC()}
		attachSubmissionReceipt(t, ctx, ss, fixture.candidate, input)
		command := examCommand(fixture.candidate.ID, store.ExamSubmissionSealOperation, "domain-event-voluntary", "domain-event-voluntary")
		beforeEvents, beforeJobs = count(t, ctx)
		sealed, err := submissions.Seal(ctx, input, command)
		require.NoError(t, err)
		require.False(t, sealed.Replayed)
		requireDelta(t, beforeEvents, beforeJobs, 1, 1, "fresh voluntary seal")
		requireExamAttemptSubmittedEvent(t, ctx, ss, input.DomainEventID, input.DomainEventJobID, examAttemptSubmittedFact{
			SubmissionID: sealed.Receipt.SubmissionID, AttemptID: connected.Attempt.ID, SittingID: fixture.sitting.ID,
			ExamID: fixture.examID, SubmittedAt: sealed.Receipt.SubmittedAt,
		})

		replay := *input
		replay.SubmissionID, replay.ReceiptCode = model.NewSubmissionID(), model.NewSubmissionReceiptCode()
		replay.AuditEventID = saveExamAttemptAudit(t, ctx, ss, fixture).ID
		replay.DomainEventID, replay.DomainEventJobID = model.NewDomainEventID(), model.NewJobID()
		beforeEvents, beforeJobs = count(t, ctx)
		replayed, err := submissions.Seal(ctx, &replay, command)
		require.NoError(t, err)
		require.True(t, replayed.Replayed)
		requireDelta(t, beforeEvents, beforeJobs, 0, 0, "replayed voluntary seal")
		requireNoDomainEvent(t, ctx, ss, replay.DomainEventID, replay.DomainEventJobID)
	})

	t.Run("ManagerEnd", func(t *testing.T) {
		fixture := newExamAttemptFixture(t, ctx, ss)
		connected, _ := connectFocusLossFixture(t, ctx, ss, fixture, "domain-event-manager-connect")
		request := store.ExamSubmissionManagerEndRequest{ExamID: fixture.examID, SittingID: fixture.sitting.ID,
			AttemptID: connected.Attempt.ID, ActorUserID: fixture.manager.ID,
			ExpectedAttemptRevision: connected.Attempt.Revision, PrivateReason: "end the Attempt for the domain event"}
		end := func(t *testing.T, auditID model.AuditEventID, command *store.CommandIdempotency) (*store.ExamSubmissionManagerEnd, *store.ExamSubmissionManagerEndResult, error) {
			t.Helper()
			preparation, err := submissions.PrepareManagerEnd(ctx, request)
			require.NoError(t, err)
			input := &store.ExamSubmissionManagerEnd{Request: request, Target: preparation.Target, SubmissionID: model.NewSubmissionID(),
				ReceiptCode: model.NewSubmissionReceiptCode(), AuditEventID: auditID, AuditAt: preparation.SealAt}
			attachManagerEndedSubmissionReceipt(t, fixture.candidate, input)
			result, err := submissions.EndByManager(ctx, input, command)
			return input, result, err
		}

		beforeEvents, beforeJobs := count(t, ctx)
		failed, _, err := end(t, model.NewAuditEventID(), examCommand(fixture.manager.ID, store.ExamSubmissionManagerEndOperation,
			"domain-event-manager-failed", "domain-event-manager-failed"))
		require.Error(t, err, "EndByManager(with missing audit) succeeded")
		requireDelta(t, beforeEvents, beforeJobs, 0, 0, "failed manager seal")
		requireNoDomainEvent(t, ctx, ss, failed.DomainEventID, failed.DomainEventJobID)

		command := examCommand(fixture.manager.ID, store.ExamSubmissionManagerEndOperation, "domain-event-manager", "domain-event-manager")
		beforeEvents, beforeJobs = count(t, ctx)
		input, ended, err := end(t, saveExamSittingAudit(t, ctx, ss, fixture.manager.ID, fixture.examID, fixture.unitID).ID, command)
		require.NoError(t, err)
		require.False(t, ended.Replayed)
		requireDelta(t, beforeEvents, beforeJobs, 1, 1, "fresh manager seal")
		requireExamAttemptSubmittedEvent(t, ctx, ss, input.DomainEventID, input.DomainEventJobID, examAttemptSubmittedFact{
			SubmissionID: ended.Receipt.SubmissionID, AttemptID: connected.Attempt.ID, SittingID: fixture.sitting.ID,
			ExamID: fixture.examID, SubmittedAt: ended.Receipt.SubmittedAt,
		})

		beforeEvents, beforeJobs = count(t, ctx)
		replay, replayed, err := end(t, saveExamSittingAudit(t, ctx, ss, fixture.manager.ID, fixture.examID, fixture.unitID).ID, command)
		require.NoError(t, err)
		require.True(t, replayed.Replayed)
		require.Equal(t, ended.Receipt.SubmissionID, replayed.Receipt.SubmissionID)
		requireDelta(t, beforeEvents, beforeJobs, 0, 0, "replayed manager seal")
		requireNoDomainEvent(t, ctx, ss, replay.DomainEventID, replay.DomainEventJobID)
	})

	t.Run("Automatic", func(t *testing.T) {
		fixture := newExamAttemptFixture(t, ctx, ss)
		connected, _ := connectFocusLossFixture(t, ctx, ss, fixture, "domain-event-automatic-connect")
		closeAt := model.NowUTC()
		closing, err := ss.ExamSitting().EarlyClose(ctx, &store.ExamSittingManagerTransition{ExamID: fixture.examID,
			SittingID: fixture.sitting.ID, ActorUserID: fixture.manager.ID, ExpectedRevision: fixture.sitting.Revision,
			FinalizeJob:   newExamSittingFinalizeJob(t, fixture.sitting.ID, fixture.sitting.Revision+1, closeAt),
			PrivateReason: "seal the remaining Attempt", ChangedAt: closeAt,
			AuditEventID: saveExamSittingAudit(t, ctx, ss, fixture.manager.ID, fixture.examID, fixture.unitID).ID,
			AuditAt:      model.NowUTC()},
			examCommand(fixture.manager.ID, "exam.sitting.close", "domain-event-close", "domain-event-close"))
		require.NoError(t, err)
		require.Equal(t, model.ExamSittingClosing, closing.Value.Sitting.State)
		targets, err := submissions.ListAutomaticSealTargets(ctx, store.ExamSubmissionAutomaticSealListOptions{
			SittingID: fixture.sitting.ID, Limit: 2})
		require.NoError(t, err)
		require.Len(t, targets, 1)
		target := targets[0]
		require.Equal(t, connected.Attempt.ID, target.AttemptID)

		seal := func(t *testing.T) (*store.ExamSubmissionAutomaticSeal, *store.ExamSubmissionAutomaticSealResult) {
			t.Helper()
			preparation, err := submissions.PrepareAutomaticSeal(ctx, target)
			require.NoError(t, err)
			input := &store.ExamSubmissionAutomaticSeal{Target: target, SubmissionID: model.NewSubmissionID(),
				ReceiptCode: model.NewSubmissionReceiptCode(), AuditAt: preparation.SealAt,
				AuditEventID: saveExamSittingSystemAudit(t, ctx, ss, fixture.sitting.ID, fixture.unitID).ID}
			attachAutomaticSubmissionReceipt(t, fixture.candidate, input)
			result, err := submissions.SealForSittingClose(ctx, input)
			require.NoError(t, err)
			return input, result
		}
		beforeEvents, beforeJobs := count(t, ctx)
		input, sealed := seal(t)
		require.False(t, sealed.Replayed)
		requireDelta(t, beforeEvents, beforeJobs, 1, 1, "fresh automatic seal")
		requireExamAttemptSubmittedEvent(t, ctx, ss, input.DomainEventID, input.DomainEventJobID, examAttemptSubmittedFact{
			SubmissionID: sealed.Receipt.SubmissionID, AttemptID: target.AttemptID, SittingID: target.SittingID,
			ExamID: target.ExamID, SubmittedAt: sealed.Receipt.SubmittedAt,
		})

		beforeEvents, beforeJobs = count(t, ctx)
		replay, replayed := seal(t)
		require.True(t, replayed.Replayed)
		require.Equal(t, sealed.Receipt.SubmissionID, replayed.Receipt.SubmissionID)
		requireDelta(t, beforeEvents, beforeJobs, 0, 0, "replayed automatic seal")
		requireNoDomainEvent(t, ctx, ss, replay.DomainEventID, replay.DomainEventJobID)
	})
}
