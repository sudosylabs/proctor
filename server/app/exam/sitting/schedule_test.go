// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package sitting

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/sudosylabs/proctor/server/app/apperror"
	"github.com/sudosylabs/proctor/server/app/realtime"
	"github.com/sudosylabs/proctor/server/model"
	"github.com/sudosylabs/proctor/server/store"
)

func TestScheduleCreatesAuthorizedSittingAndPublishesSafeEffect(t *testing.T) {
	t.Parallel()
	fixture := newFixture(t)
	start, end := testNow.Add(time.Hour), testNow.Add(3*time.Hour)
	command := ScheduleCommand{
		ExamID: fixture.examID, ExamRevisionID: fixture.revisionID, ClassID: fixture.classID,
		ScheduledStartAt: start, ScheduledEndAt: end, IdempotencyKey: "test-key",
	}

	got, err := fixture.service.ScheduleExamSitting(context.Background(), fixture.inv, command)
	require.NoError(t, err)
	require.NotNil(t, got.Sitting)
	require.Equal(t, fixture.sittingID, got.Sitting.ID)
	require.Equal(t, model.ExamSittingScheduled, got.Sitting.State)
	require.Equal(t, model.ActionExamSittingCreate, fixture.authorizer.action, "authorization")
	require.Equal(t, model.Resource{Type: model.ResourceExam, ID: fixture.examID.String()}, fixture.authorizer.resource, "authorization")
	require.NotNil(t, fixture.persistence.schedule, "store schedule")
	require.False(t, fixture.persistence.schedule.ManagerOverride, "store schedule")
	require.Equal(t, fixture.userID, fixture.persistence.schedule.ActorUserID, "store schedule")
	require.Equal(t, fixture.sittingID, fixture.persistence.schedule.Sitting.ID, "store schedule")
	require.Equal(t, start, fixture.persistence.schedule.Sitting.ScheduledStartAt, "store schedule")
	require.NotNil(t, fixture.persistence.command, "store schedule")
	require.Same(t, fixture.jobs.open, fixture.persistence.schedule.OpenJob, "store schedule")
	require.Same(t, fixture.jobs.deadline, fixture.persistence.schedule.DeadlineJob, "store schedule")
	require.Equal(t, int64(1), fixture.jobs.boundaryRevision, "store schedule")
	require.True(t, fixture.jobs.boundaryStart.Equal(start), "store schedule")
	require.True(t, fixture.jobs.boundaryEnd.Equal(end), "store schedule")
	require.Same(t, fixture.mail.result, fixture.persistence.schedule.Mail, "store schedule")
	require.Equal(t, store.ExamSittingMailScheduled, fixture.mail.request.ChangeKind, "store schedule")
	wantIdempotency, err := prepareScheduleIdempotency(fixture.inv, command)
	require.NoError(t, err)
	require.Equal(t, wantIdempotency, fixture.persistence.command)
	wantAudit := map[string]any{
		"exam_id": fixture.examID.String(), "exam_sitting_id": fixture.sittingID.String(),
		"exam_revision_id": fixture.revisionID.String(), "class_id": fixture.classID.String(),
	}
	require.Equal(t, model.ActionExamSittingCreate, fixture.auditor.action, "audit")
	require.Equal(t, "schedule", fixture.auditor.operation, "audit")
	require.Equal(t, wantAudit, fixture.auditor.value, "audit")
	// Contract: the scheduled fact reaches managers before the Sitting board is
	// invalidated; this Sitting has no candidate to invalidate after it.
	require.Equal(t, scheduleCalls(t, realtime.NewExamSittingScheduledEvent, fixture.examID, fixture.sittingID, model.ExamSittingScheduled, 1, testNow),
		fixture.publisher.Calls())
}

func TestScheduleUsesExplicitOverrideWithoutCurrentExactUnitMembership(t *testing.T) {
	t.Parallel()
	fixture := newFixture(t)
	fixture.memberships.items = nil
	start, end := testNow.Add(time.Hour), testNow.Add(3*time.Hour)

	_, err := fixture.service.ScheduleExamSitting(context.Background(), fixture.inv, ScheduleCommand{
		ExamID: fixture.examID, ExamRevisionID: fixture.revisionID, ClassID: fixture.classID,
		ScheduledStartAt: start, ScheduledEndAt: end, IdempotencyKey: "test-key",
	})
	require.NoError(t, err)
	require.Equal(t, model.ActionExamSittingCreateOverride, fixture.authorizer.action, "override authorization/store")
	require.NotNil(t, fixture.persistence.schedule, "override authorization/store")
	require.True(t, fixture.persistence.schedule.ManagerOverride, "override authorization/store")
}

func TestScheduleAuthorizationFailureStopsMutationAndAudit(t *testing.T) {
	t.Parallel()
	fixture := newFixture(t)
	fixture.authorizer.err = errors.New("denied")
	start, end := testNow.Add(time.Hour), testNow.Add(3*time.Hour)

	_, err := fixture.service.ScheduleExamSitting(context.Background(), fixture.inv, ScheduleCommand{
		ExamID: fixture.examID, ExamRevisionID: fixture.revisionID, ClassID: fixture.classID,
		ScheduledStartAt: start, ScheduledEndAt: end, IdempotencyKey: "test-key",
	})
	require.Error(t, err, "unauthorized mutation continued: err")
	require.Nil(t, fixture.persistence.schedule, "unauthorized mutation continued: err")
	require.Empty(t, fixture.auditor.operation, "unauthorized mutation continued: err")
}

func TestScheduleStopsWhenCurrentManagerMembershipCannotBeRead(t *testing.T) {
	t.Parallel()
	testFixture := newFixture(t)
	failure := errors.New("membership unavailable")
	testFixture.memberships.err = failure
	_, err := testFixture.service.ScheduleExamSitting(context.Background(), testFixture.inv, ScheduleCommand{
		ExamID: testFixture.examID, ExamRevisionID: testFixture.revisionID, ClassID: testFixture.classID,
		ScheduledStartAt: testNow.Add(time.Hour), ScheduledEndAt: testNow.Add(3 * time.Hour), IdempotencyKey: "test-key",
	})
	var fault *apperror.Error
	require.ErrorAs(t, err, &fault)
	require.Equal(t, "exam.sitting.unavailable", fault.Code())
	require.ErrorIs(t, err, failure)
	require.Empty(t, testFixture.authorizer.action, "membership failure continued: action")
	require.Nil(t, testFixture.persistence.schedule, "membership failure continued: action")
	require.Empty(t, testFixture.auditor.operation, "membership failure continued: action")
}

func TestReplayedCommandsDoNotPublishEffects(t *testing.T) {
	t.Parallel()
	fixture := newFixture(t)
	original := fixture.sitting(t)
	original.ID = model.NewExamSittingID()
	fixture.persistence.result = &store.ExamSittingCommandResult{
		Value: &store.ExamSittingSnapshot{Sitting: original}, Replayed: true,
	}
	start, end := testNow.Add(time.Hour), testNow.Add(3*time.Hour)

	got, err := fixture.service.ScheduleExamSitting(context.Background(), fixture.inv, ScheduleCommand{
		ExamID: fixture.examID, ExamRevisionID: fixture.revisionID, ClassID: fixture.classID,
		ScheduledStartAt: start, ScheduledEndAt: end, IdempotencyKey: "test-key",
	})
	require.NoError(t, err)
	require.NotNil(t, got.Sitting, "replayed Sitting = %#v, want original", got)
	require.Equal(t, original.ID, got.Sitting.ID, "replayed Sitting = %#v, want original", got)
	require.Empty(t, fixture.publisher.Calls())
}

func TestUpdateScheduleUsesSittingManagementAndOptimisticFence(t *testing.T) {
	t.Parallel()
	fixture := newFixture(t)
	updated := fixture.sitting(t)
	current := fixture.sitting(t)
	changedAt := testNow
	changedStart, changedEnd := testNow.Add(2*time.Hour), testNow.Add(5*time.Hour)
	changed, err := updated.ApplySchedule(fixture.revisionID, fixture.classID, changedStart, changedEnd, changedAt)
	require.NoError(t, err, "ApplySchedule()")
	require.True(t, changed, "ApplySchedule()")
	fixture.persistence.snapshot = &store.ExamSittingSnapshot{Sitting: current}
	fixture.persistence.result = &store.ExamSittingCommandResult{Value: &store.ExamSittingSnapshot{Sitting: updated}}

	command := UpdateScheduleCommand{
		ExamID: fixture.examID, SittingID: fixture.sittingID, ExpectedRevision: 1,
		ScheduledStartAt: &changedStart, ScheduledEndAt: &changedEnd, IdempotencyKey: "test-key",
	}
	got, err := fixture.service.UpdateExamSittingSchedule(context.Background(), fixture.inv, command)
	require.NoError(t, err)
	require.NotNil(t, got.Sitting)
	require.Equal(t, int64(2), got.Sitting.Revision)
	require.Equal(t, model.ActionExamSittingManage, fixture.authorizer.action, "authorization = %q %#v", fixture.authorizer.action, fixture.authorizer.resource)
	require.Equal(t, model.ResourceExamSitting, fixture.authorizer.resource.Type, "authorization = %q %#v", fixture.authorizer.action, fixture.authorizer.resource)
	input := fixture.persistence.update
	require.NotNil(t, input, "store update")
	require.Equal(t, int64(1), input.ExpectedRevision, "store update")
	require.False(t, input.ManagerOverride, "store update")
	require.NotNil(t, fixture.persistence.command, "store update")
	wantIdempotency, err := prepareScheduleUpdateIdempotency(fixture.inv, command)
	require.NoError(t, err)
	require.Equal(t, wantIdempotency, fixture.persistence.command)
	require.Same(t, fixture.jobs.open, input.OpenJob, "boundary Jobs = %#v %#v, factory revision/times = %d %v %v", input.OpenJob, input.DeadlineJob, fixture.jobs.boundaryRevision, fixture.jobs.boundaryStart, fixture.jobs.boundaryEnd)
	require.Same(t, fixture.jobs.deadline, input.DeadlineJob, "boundary Jobs = %#v %#v, factory revision/times = %d %v %v", input.OpenJob, input.DeadlineJob, fixture.jobs.boundaryRevision, fixture.jobs.boundaryStart, fixture.jobs.boundaryEnd)
	require.Equal(t, int64(2), fixture.jobs.boundaryRevision, "boundary Jobs = %#v %#v, factory revision/times = %d %v %v", input.OpenJob, input.DeadlineJob, fixture.jobs.boundaryRevision, fixture.jobs.boundaryStart, fixture.jobs.boundaryEnd)
	require.True(t, fixture.jobs.boundaryStart.Equal(changedStart), "boundary Jobs = %#v %#v, factory revision/times = %d %v %v", input.OpenJob, input.DeadlineJob, fixture.jobs.boundaryRevision, fixture.jobs.boundaryStart, fixture.jobs.boundaryEnd)
	require.True(t, fixture.jobs.boundaryEnd.Equal(changedEnd), "boundary Jobs = %#v %#v, factory revision/times = %d %v %v", input.OpenJob, input.DeadlineJob, fixture.jobs.boundaryRevision, fixture.jobs.boundaryStart, fixture.jobs.boundaryEnd)
	require.Same(t, fixture.mail.result, input.Mail, "mail preparation = %#v, store", fixture.mail.request)
	require.Equal(t, store.ExamSittingMailRescheduled, fixture.mail.request.ChangeKind, "mail preparation = %#v, store", fixture.mail.request)
	require.Equal(t, fixture.classID, fixture.mail.request.PriorClassID, "mail preparation = %#v, store", fixture.mail.request)
	require.Equal(t, int64(2), fixture.mail.request.SittingRevision, "mail preparation = %#v, store", fixture.mail.request)
	require.Equal(t, fixture.revisionID, input.ExamRevisionID)
	require.Equal(t, fixture.classID, input.ClassID)
	require.True(t, input.ScheduledStartAt.Equal(changedStart))
	require.True(t, input.ScheduledEndAt.Equal(changedEnd))
	wantAudit := map[string]any{"exam_id": fixture.examID.String(), "exam_sitting_id": fixture.sittingID.String(),
		"exam_revision_id": fixture.revisionID.String(), "class_id": fixture.classID.String(), "expected_sitting_revision": int64(1)}
	require.Equal(t, "update_schedule", fixture.auditor.operation, "audit")
	require.Equal(t, wantAudit, fixture.auditor.value, "audit")
	// Contract: the updated schedule reaches managers at the committed
	// revision before the Sitting board is invalidated.
	require.Equal(t, scheduleCalls(t, realtime.NewExamSittingScheduleUpdatedEvent, fixture.examID, fixture.sittingID, got.Sitting.State, 2, testNow),
		fixture.publisher.Calls())
}

func TestUpdateScheduleReturnsCurrentNoChangesOnlyForCurrentActiveScheduledState(t *testing.T) {
	t.Parallel()
	fixture := newFixture(t)
	current := fixture.sitting(t)
	fixture.persistence.snapshot = &store.ExamSittingSnapshot{Sitting: current}
	revisionID := fixture.revisionID

	_, err := fixture.service.UpdateExamSittingSchedule(context.Background(), fixture.inv, UpdateScheduleCommand{
		ExamID: fixture.examID, SittingID: fixture.sittingID, ExpectedRevision: 1,
		ExamRevisionID: &revisionID, IdempotencyKey: "test-key",
	})
	assertFaultCode(t, err, "exam.sitting.no_changes")
	require.Nil(t, fixture.persistence.update, "no-op reached mutation boundary: update")
	require.Empty(t, fixture.auditor.operation, "no-op reached mutation boundary: update")
}

func TestUpdateScheduleSendsStaleNoOpToStore(t *testing.T) {
	t.Parallel()
	fixture := newFixture(t)
	current := fixture.sitting(t)
	current.Revision = 2
	fixture.persistence.snapshot = &store.ExamSittingSnapshot{Sitting: current}
	fixture.persistence.err = store.NewErrConflict("exam_sitting", "exam_sitting_revision", nil)
	revisionID := fixture.revisionID

	_, err := fixture.service.UpdateExamSittingSchedule(context.Background(), fixture.inv, UpdateScheduleCommand{
		ExamID: fixture.examID, SittingID: fixture.sittingID, ExpectedRevision: 1,
		ExamRevisionID: &revisionID, IdempotencyKey: "test-key",
	})
	assertFaultCode(t, err, "exam.sitting.revision_conflict")
	require.NotNil(t, fixture.persistence.update, "stale no-op did not reach mutation boundary: update")
	require.Equal(t, "update_schedule", fixture.auditor.operation, "stale no-op did not reach mutation boundary: update")
}

func TestUpdateScheduleSendsArchivedAndNonScheduledNoOpsToStore(t *testing.T) {
	t.Parallel()
	t.Run("archived exam", func(t *testing.T) {
		t.Parallel()
		fixture := newFixture(t)
		fixture.persistence.snapshot = &store.ExamSittingSnapshot{Sitting: fixture.sitting(t)}
		require.NoError(t, fixture.access.snapshot.Exam.Archive(testNow))
		fixture.persistence.err = store.NewErrConflict("exam_sitting", "exam_archived", nil)
		revisionID := fixture.revisionID

		_, err := fixture.service.UpdateExamSittingSchedule(context.Background(), fixture.inv, UpdateScheduleCommand{
			ExamID: fixture.examID, SittingID: fixture.sittingID, ExpectedRevision: 1,
			ExamRevisionID: &revisionID, IdempotencyKey: "test-key",
		})
		assertFaultCode(t, err, "exam.archived")
		require.NotNil(t, fixture.persistence.update, "archived no-op did not reach Store")
	})

	t.Run("non-scheduled sitting", func(t *testing.T) {
		t.Parallel()
		fixture := newFixture(t)
		current := fixture.sitting(t)
		require.NoError(t, current.Cancel(model.ExamSittingReasonManagerCanceled, testNow))
		fixture.persistence.snapshot = &store.ExamSittingSnapshot{Sitting: current}
		fixture.persistence.err = store.NewErrConflict("exam_sitting", "exam_sitting_state", nil)
		revisionID := fixture.revisionID

		_, err := fixture.service.UpdateExamSittingSchedule(context.Background(), fixture.inv, UpdateScheduleCommand{
			ExamID: fixture.examID, SittingID: fixture.sittingID, ExpectedRevision: current.Revision,
			ExamRevisionID: &revisionID, IdempotencyKey: "test-key",
		})
		assertFaultCode(t, err, "exam.sitting.state_conflict")
		require.NotNil(t, fixture.persistence.update, "state-conflicting no-op did not reach Store")
	})
}

func TestUpdateRejectsMismatchedStoreOutcome(t *testing.T) {
	t.Parallel()
	fixture := newFixture(t)
	fixture.persistence.snapshot = &store.ExamSittingSnapshot{Sitting: fixture.sitting(t)}
	foreign := fixture.sitting(t)
	foreign.ID = model.NewExamSittingID()
	start := testNow.Add(2 * time.Hour)
	fixture.persistence.result = &store.ExamSittingCommandResult{Value: &store.ExamSittingSnapshot{Sitting: foreign}}

	_, err := fixture.service.UpdateExamSittingSchedule(context.Background(), fixture.inv, UpdateScheduleCommand{
		ExamID: fixture.examID, SittingID: fixture.sittingID, ExpectedRevision: 1,
		ScheduledStartAt: &start, IdempotencyKey: "test-key",
	})
	assertFaultCode(t, err, "exam.sitting.unavailable")
	require.Empty(t, fixture.publisher.Calls())
}

func TestReplayedUpdateAndCancellationDoNotPublishEffects(t *testing.T) {
	t.Parallel()
	t.Run("update", func(t *testing.T) {
		t.Parallel()
		fixture := newFixture(t)
		fixture.persistence.snapshot = &store.ExamSittingSnapshot{Sitting: fixture.sitting(t)}
		updated := fixture.sitting(t)
		start, end := testNow.Add(2*time.Hour), testNow.Add(5*time.Hour)
		_, err := updated.ApplySchedule(fixture.revisionID, fixture.classID, start, end, testNow)
		require.NoError(t, err)
		fixture.persistence.result = &store.ExamSittingCommandResult{Value: &store.ExamSittingSnapshot{Sitting: updated}, Replayed: true}

		_, err = fixture.service.UpdateExamSittingSchedule(context.Background(), fixture.inv, UpdateScheduleCommand{
			ExamID: fixture.examID, SittingID: fixture.sittingID, ExpectedRevision: 1,
			ScheduledStartAt: &start, ScheduledEndAt: &end, IdempotencyKey: "test-key",
		})
		require.NoError(t, err)
		require.Empty(t, fixture.publisher.Calls())
	})

	t.Run("cancel", func(t *testing.T) {
		t.Parallel()
		fixture := newFixture(t)
		fixture.persistence.snapshot = &store.ExamSittingSnapshot{Sitting: fixture.sitting(t)}
		canceled := fixture.sitting(t)
		require.NoError(t, canceled.Cancel(model.ExamSittingReasonManagerCanceled, testNow))
		fixture.persistence.result = &store.ExamSittingCommandResult{Value: &store.ExamSittingSnapshot{Sitting: canceled}, Replayed: true}

		_, err := fixture.service.CancelExamSitting(context.Background(), fixture.inv, CancelCommand{
			ExamID: fixture.examID, SittingID: fixture.sittingID, ExpectedRevision: 1,
			PrivateReason: "Manager canceled", IdempotencyKey: "test-key",
		})
		require.NoError(t, err)
		require.Empty(t, fixture.publisher.Calls())
	})
}
