// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package storetest

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/sudosylabs/proctor/server/model"
	"github.com/sudosylabs/proctor/server/store"
)

// TestExamSittingJoinWindow schedules a Sitting whose join window opens
// before its start and closes before its end. In the early-join phase the
// candidate can read the readiness overview but cannot connect, so no Attempt
// or workspace exists; the Sitting still opens at its scheduled start, admits
// a first entry inside the window, refuses one after it, and lets a started
// candidate reconnect.
func TestExamSittingJoinWindow(t *testing.T, ss store.Store) {
	ctx := context.Background()
	fixture := newExamAttemptFixture(t, ctx, ss)

	var sitting *model.ExamSitting
	for scheduleAttempt := 1; ; scheduleAttempt++ {
		opensAt := model.NowUTC().Add(2 * time.Second)
		start, end := opensAt.Add(3*time.Second), opensAt.Add(time.Hour)
		window := model.ExamSittingJoinWindow{OpensAt: model.OptionalTimeFrom(opensAt), ClosesAt: model.OptionalTimeFrom(start.Add(2 * time.Second))}
		candidate, err := model.NewExamSitting(model.NewExamSittingID(), fixture.examID, fixture.revisionID, fixture.class.ID, start, end, window, model.NowUTC())
		require.NoError(t, err)

		schedule := func(openAt time.Time, key string) error {
			openJob, deadlineJob := newExamSittingLifecycleJobs(t, candidate.ID, 1, openAt, end)
			_, scheduleErr := ss.ExamSitting().Schedule(ctx, &store.ExamSittingSchedule{Sitting: candidate, OpenJob: openJob, DeadlineJob: deadlineJob,
				ActorUserID:  fixture.manager.ID,
				AuditEventID: saveExamSittingAudit(t, ctx, ss, fixture.manager.ID, fixture.examID, fixture.unitID).ID, AuditAt: model.NowUTC(),
				Mail: newExamSittingMailFanout(t, fixture.manager.ID, store.ExamSittingMailScheduled, model.MailTemplateExamSittingScheduled)},
				examCommand(fixture.manager.ID, "exam.sitting.schedule", key, key))
			return scheduleErr
		}
		var invalid *store.ErrInvalidInput
		require.ErrorAs(t, schedule(opensAt, "join-window-early-job"), &invalid, "the sitting still opens at its scheduled start")

		err = schedule(start, "join-window-"+string(rune('0'+scheduleAttempt)))
		var conflict *store.ErrConflict
		if scheduleAttempt < 5 && errors.As(err, &conflict) && conflict.Constraint == "exam_sitting_not_future" {
			continue
		}
		require.NoError(t, err)
		sitting = candidate
		break
	}

	stored, err := ss.ExamSitting().Get(ctx, fixture.examID, sitting.ID)
	require.NoError(t, err)
	require.True(t, stored.Sitting.JoinWindow.Equal(sitting.JoinWindow), "the join window was not stored")

	joined := fixture
	joined.sitting = sitting
	overview := func() error {
		connection := &store.ExamAttemptConnect{CandidateUserID: fixture.candidate.ID, SessionID: fixture.session.ID}
		prepareExamAttemptConnect(t, ctx, ss, connection)
		build := connection.DesktopBuild
		build.NativeAgreement = syntheticNativeAgreement(t, build)
		_, readErr := ss.SecurityPreflight().ReadSecurityPolicyOverview(ctx, store.SecurityPreflightAccess{SittingID: sitting.ID,
			CandidateUserID: fixture.candidate.ID, SessionID: fixture.session.ID, DesktopRegistrationID: fixture.session.DesktopRegistrationID,
			DPoPKeyThumbprint: fixture.session.DPoPKeyThumbprint, DesktopBuild: build, DesktopCompatibilityPolicyRevision: connection.DesktopCompatibilityPolicyRevision})
		return readErr
	}
	connect := func(fixture examAttemptFixture, key string) (*store.ExamAttemptConnect, *store.ExamAttemptConnectResult, error) {
		input := &store.ExamAttemptConnect{SittingID: fixture.sitting.ID, CandidateUserID: fixture.candidate.ID,
			SessionID: fixture.session.ID, DesktopRegistrationID: fixture.session.DesktopRegistrationID,
			DPoPKeyThumbprint: fixture.session.DPoPKeyThumbprint, AttemptID: model.NewExamAttemptID(), WorkspaceID: model.NewExamAttemptWorkspaceID(),
			ParticipationID: model.NewAttemptParticipationID(), ConnectionID: model.NewAttemptConnectionID(),
			ContinuityCredentialHash: model.HashToken(model.NewCredentialToken())}
		input.AuditEventID, input.AuditAt = saveExamAttemptAudit(t, ctx, ss, fixture).ID, model.NowUTC()
		prepareExamAttemptConnect(t, ctx, ss, input)
		result, connectErr := ss.ExamAttempt().Connect(ctx, input, examCommand(fixture.candidate.ID, store.ExamAttemptConnectOperation, key, key))
		return input, result, connectErr
	}
	activityItem := func(candidate *model.User, session *model.Session) store.CandidateExamActivityItem {
		activity, listErr := ss.ExamAttempt().ListCandidateActivity(ctx, store.CandidateExamActivityListOptions{
			CandidateUserID: candidate.ID, DesktopSessionID: session.ID, DesktopRegistrationID: session.DesktopRegistrationID,
			DPoPKeyThumbprint: session.DPoPKeyThumbprint, Limit: 50,
		})
		require.NoError(t, listErr)
		for _, item := range activity.Items {
			if item.SittingID == sitting.ID {
				return item
			}
		}
		require.FailNow(t, "the activity does not list the sitting")
		return store.CandidateExamActivityItem{}
	}

	requireConflictConstraint(t, overview(), "exam_sitting_state")
	require.Equal(t, store.CandidateExamAccessNotOpen, activityItem(fixture.candidate, fixture.session).AccessState, "before the window the sitting is not open")

	late := saveUser(t, ctx, ss)
	_, err = ss.Affiliation().Save(ctx, &model.Affiliation{UserID: late.ID, Kind: model.AffiliationStudent, StartsAt: model.NowUTC().Add(-time.Hour)})
	require.NoError(t, err)
	_, err = ss.ClassMember().SeedEnroll(ctx, &model.ClassMember{ClassID: fixture.class.ID, UserID: late.ID, StartsAt: model.NowUTC().Add(-time.Minute)})
	require.NoError(t, err)
	lateSession := saveRegisteredDesktopSession(t, ctx, ss, late.ID, fixture.institutionID)

	time.Sleep(time.Until(sitting.JoinWindow.OpensAt.Time) + 100*time.Millisecond)
	require.NoError(t, overview(), "a candidate who joined early can run the readiness checks")
	waiting := activityItem(fixture.candidate, fixture.session)
	require.Equal(t, store.CandidateExamAccessWaiting, waiting.AccessState, "the candidate is told they are waiting for the start")
	require.Empty(t, waiting.AllowedActions, "nobody can enter before the start")
	earlyInput, _, err := connect(joined, "join-window-early")
	requireConflictConstraint(t, err, "exam_sitting_state")
	_, err = ss.ExamAttempt().Get(ctx, fixture.examID, earlyInput.AttemptID)
	require.True(t, store.IsNotFound(err), "an early join created an Attempt, and so a workspace")
	stillScheduled, err := ss.ExamSitting().Get(ctx, fixture.examID, sitting.ID)
	require.NoError(t, err)
	require.Equal(t, model.ExamSittingScheduled, stillScheduled.Sitting.State, "the sitting does not open early")

	time.Sleep(time.Until(sitting.ScheduledStartAt) + 100*time.Millisecond)
	opened, err := ss.ExamSitting().AdvanceDue(ctx, &store.ExamSittingDueAdvance{SittingID: sitting.ID,
		AuditEventID: saveExamSittingSystemAudit(t, ctx, ss, sitting.ID, fixture.unitID).ID, AuditAt: model.NowUTC()})
	require.NoError(t, err)
	require.Equal(t, model.ExamSittingOpen, opened.Value.Sitting.State, "the sitting opens at its scheduled start")
	require.False(t, opened.Value.Sitting.OpenedAt.Time.Before(sitting.ScheduledStartAt), "the sitting opened before its scheduled start")
	joined.sitting = opened.Value.Sitting

	firstInput, first, err := connect(joined, "join-window-first")
	require.NoError(t, err, "a first entry inside the window is admitted at the start")
	require.True(t, first.FirstAdmission)
	require.False(t, first.Attempt.CreatedAt.Before(sitting.ScheduledStartAt), "the Attempt starts no earlier than the scheduled start")

	time.Sleep(time.Until(sitting.JoinWindow.ClosesAt.Time) + 100*time.Millisecond)
	lateFixture := joined
	lateFixture.candidate, lateFixture.session = late, lateSession
	lateInput, _, err := connect(lateFixture, "join-window-late")
	requireConflictConstraint(t, err, "exam_sitting_join_closed")
	_, err = ss.ExamAttempt().Get(ctx, fixture.examID, lateInput.AttemptID)
	require.True(t, store.IsNotFound(err), "a refused first entry created an Attempt")

	resume := *firstInput
	resume.Security = model.ConnectSecurity{Kind: "resume", ParticipationID: first.Participation.ID, Generation: first.Participation.Generation, PolicyDigest: first.Security.Policy.Digest}
	resume.AttemptID, resume.WorkspaceID = model.NewExamAttemptID(), model.NewExamAttemptWorkspaceID()
	resume.ParticipationID, resume.ConnectionID = model.NewAttemptParticipationID(), model.NewAttemptConnectionID()
	resume.AuditEventID, resume.AuditAt = saveExamAttemptAudit(t, ctx, ss, joined).ID, model.NowUTC()
	resumed, err := ss.ExamAttempt().Connect(ctx, &resume,
		examCommand(fixture.candidate.ID, store.ExamAttemptConnectOperation, "join-window-resume", "join-window-resume"))
	require.NoError(t, err, "a started candidate reconnects after the window closes")
	require.Equal(t, first.Attempt.ID, resumed.Attempt.ID)

	closed := activityItem(late, lateSession)
	require.True(t, closed.JoinWindow.Equal(sitting.JoinWindow), "the candidate sees the join window")
	require.Equal(t, store.CandidateExamAccessJoinClosed, closed.AccessState)
	require.Empty(t, closed.AllowedActions, "a closed join window offers no entry")
}

// TestExamSittingJoinWindowUpdate sets, keeps and clears the join window of a
// scheduled Sitting, and refuses one that does not fit the schedule.
func TestExamSittingJoinWindowUpdate(t *testing.T, ss store.Store) {
	ctx := context.Background()
	fixture := newExamSittingFixture(t, ctx, ss)
	start := fixture.period.StartsAt.Add(2 * time.Hour)
	end := start.Add(2 * time.Hour)
	sitting, err := model.NewExamSitting(model.NewExamSittingID(), fixture.examID, fixture.revisionID, fixture.class.ID, start, end, model.ExamSittingJoinWindow{}, model.NowUTC())
	require.NoError(t, err)
	candidateInput := newUser()
	candidateInput.EmailVerified = true
	candidate, err := createUser(t, ctx, ss, candidateInput)
	require.NoError(t, err)
	_, err = ss.Affiliation().Save(ctx, &model.Affiliation{UserID: candidate.ID, Kind: model.AffiliationStudent, StartsAt: fixture.period.StartsAt})
	require.NoError(t, err)
	member := &model.ClassMember{ClassID: fixture.class.ID, UserID: candidate.ID, AcademicPeriodID: fixture.period.ID, StartsAt: fixture.period.StartsAt}
	member.PrepareCreate(model.NewClassMemberID(), model.NowUTC())
	_, err = ss.ClassMember().Enroll(ctx, &store.ClassMemberEnrollment{Member: member, Recipient: candidate,
		Notice:       classMemberPreparedMail(t, member, model.MailTemplateAcademicClassEnrolled, member.CreatedAt),
		AuditEventID: saveClassMemberAuditAttempt(t, ctx, ss, fixture.class.ID.String()).ID, AuditAt: model.NowUTC()})
	require.NoError(t, err)
	scheduleMail := newExamSittingMailFanout(t, fixture.actor.ID, store.ExamSittingMailScheduled, model.MailTemplateExamSittingScheduled)

	openJob, deadlineJob := newExamSittingLifecycleJobs(t, sitting.ID, 1, start, end)
	_, err = ss.ExamSitting().Schedule(ctx, &store.ExamSittingSchedule{Sitting: sitting, OpenJob: openJob, DeadlineJob: deadlineJob, ActorUserID: fixture.actor.ID,
		AuditEventID: saveExamSittingAudit(t, ctx, ss, fixture.actor.ID, fixture.examID, fixture.unitID).ID, AuditAt: model.NowUTC(),
		Mail: scheduleMail},
		examCommand(fixture.actor.ID, "exam.sitting.schedule", "join-update-schedule", "join-update-schedule"))
	require.NoError(t, err)

	update := func(revision int64, window model.ExamSittingJoinWindow, key string) (*store.ExamSittingCommandResult, error) {
		openJob, deadlineJob := newExamSittingLifecycleJobs(t, sitting.ID, revision+1, start, end)
		return ss.ExamSitting().UpdateSchedule(ctx, &store.ExamSittingScheduleUpdate{
			ExamID: fixture.examID, SittingID: sitting.ID, ActorUserID: fixture.actor.ID, ExpectedRevision: revision,
			ExamRevisionID: fixture.revisionID, ClassID: fixture.class.ID, ScheduledStartAt: start, ScheduledEndAt: end,
			JoinWindow: window, OpenJob: openJob, DeadlineJob: deadlineJob,
			AuditEventID: saveExamSittingAudit(t, ctx, ss, fixture.actor.ID, fixture.examID, fixture.unitID).ID, AuditAt: model.NowUTC(),
		}, examCommand(fixture.actor.ID, "exam.sitting.schedule.update", key, key))
	}

	window := model.ExamSittingJoinWindow{OpensAt: model.OptionalTimeFrom(start.Add(-15 * time.Minute)), ClosesAt: model.OptionalTimeFrom(start.Add(20 * time.Minute))}
	set, err := update(1, window, "join-update-set")
	require.NoError(t, err)
	require.True(t, set.Value.Sitting.JoinWindow.Equal(window))
	require.Equal(t, int64(2), set.Value.Sitting.Revision)
	mail, err := ss.ExamSitting().ListMailRecipients(ctx, store.ExamSittingMailRecipientPageRequest{
		OccurrenceID: scheduleMail.Occurrence.ID, Limit: model.SittingMailExpansionPageSize,
	})
	require.NoError(t, err)
	require.Len(t, mail.Recipients, 1)
	require.Equal(t, candidate.ID, mail.Recipients[0].User.ID)
	require.Equal(t, model.MailTemplateExamSittingScheduled, mail.Recipients[0].TemplateKey,
		"a join-window edit preserves mail for the schedule still in force")

	_, err = update(2, model.ExamSittingJoinWindow{ClosesAt: model.OptionalTimeFrom(end.Add(time.Minute))}, "join-update-invalid")
	var invalid *store.ErrInvalidInput
	require.ErrorAs(t, err, &invalid, "a window that does not fit is refused")

	cleared, err := update(2, model.ExamSittingJoinWindow{}, "join-update-clear")
	require.NoError(t, err)
	reloaded, err := ss.ExamSitting().Get(ctx, fixture.examID, sitting.ID)
	require.NoError(t, err)
	require.Equal(t, model.ExamSittingJoinWindow{}, reloaded.Sitting.JoinWindow, "the cleared window was not stored")
	require.Equal(t, cleared.Value.Sitting.Revision, reloaded.Sitting.Revision)
}
