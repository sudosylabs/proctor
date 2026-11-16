// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package sitting

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/sudosylabs/proctor/server/app/access/accesstest"
	"github.com/sudosylabs/proctor/server/app/apperror"
	"github.com/sudosylabs/proctor/server/app/invocation"
	"github.com/sudosylabs/proctor/server/app/realtime"
	"github.com/sudosylabs/proctor/server/app/realtime/realtimetest"
	"github.com/sudosylabs/proctor/server/model"
	"github.com/sudosylabs/proctor/server/store"
)

var testNow = time.Date(2026, 8, 15, 9, 0, 0, 0, time.UTC)

type sittingFixture struct {
	service *Service
	inv     invocation.Invocation
	// stranger is a valid caller without any grant, whom the real authorizer
	// refuses.
	stranger    invocation.Invocation
	userID      model.UserID
	unitID      model.AcademicUnitID
	examID      model.ExamID
	revisionID  model.ExamRevisionID
	classID     model.ClassID
	sittingID   model.ExamSittingID
	persistence *sittingStoreFake
	access      *accessFake
	memberships *membershipsFake
	// control is the real authorizer and auditor. The caller holds every
	// ordinary and override action the Sitting use cases decide, in the
	// Exam's Academic Unit.
	control   *accesstest.Fixture
	publisher *realtimetest.Publisher
	jobs      *lifecycleJobFactoryFake
	mail      *scheduleMailPreparerFake
}

func (f sittingFixture) sitting(t *testing.T) *model.ExamSitting {
	t.Helper()
	sitting, err := model.NewExamSitting(f.sittingID, f.examID, f.revisionID, f.classID,
		testNow.Add(time.Hour), testNow.Add(3*time.Hour), testNow)
	require.NoError(t, err)
	return sitting
}

// sittingActions are the actions the fixture's caller is granted.
var sittingActions = []model.Action{
	model.ActionExamView, model.ActionExamViewOverride,
	model.ActionExamSittingView, model.ActionExamSittingViewOverride,
	model.ActionExamSittingCreate, model.ActionExamSittingCreateOverride,
	model.ActionExamSittingManage, model.ActionExamSittingManageOverride,
	model.ActionExamAttemptBrowserActivityView,
	model.ActionSubmissionView, model.ActionSubmissionViewOverride,
	model.ActionSubmissionReview, model.ActionSubmissionReviewOverride,
	model.ActionSubmissionRelease, model.ActionSubmissionReleaseOverride,
}

func newFixture(t *testing.T) sittingFixture {
	t.Helper()
	userID, unitID := model.NewUserID(), model.NewAcademicUnitID()
	examID, revisionID := model.NewExamID(), model.NewExamRevisionID()
	classID, sittingID := model.NewClassID(), model.NewExamSittingID()
	exam, err := model.NewExam(examID, unitID, userID, testNow.Add(-time.Hour))
	require.NoError(t, err)
	persistence := &sittingStoreFake{}
	accessStore := &accessFake{snapshot: &store.ExamAccessSnapshot{Exam: exam, ActorIsManager: true}}
	memberships := &membershipsFake{items: []*model.AcademicUnitMember{{AcademicUnitID: unitID}}}
	control := accesstest.New(t, accesstest.Options{
		Grants: []accesstest.Grant{{
			UserID: userID, Actions: sittingActions, ScopeType: model.RoleScopeAcademicUnit, ScopeID: unitID.String(),
		}},
		Exams:       accessStore,
		Sittings:    sittingOwnershipFake{sittingID: sittingID, examID: examID},
		Submissions: submissionOwnershipFake{examID: examID, sittingID: sittingID, unitID: unitID},
		Now:         func() time.Time { return testNow },
	})
	publisher := &realtimetest.Publisher{}
	jobs := &lifecycleJobFactoryFake{open: &model.Job{}, deadline: &model.Job{}, finalize: &model.Job{}}
	mailPreparer := &scheduleMailPreparerFake{result: &store.ExamSittingMailFanout{}}
	service, err := New(Dependencies{
		Persistence: persistence,
		Access:      accessStore,
		Memberships: memberships,
		Authorizer:  control.Authorizer,
		Auditor:     control.Auditor,
		Publisher:   publisher,
		Jobs:        jobs,
		Mail:        mailPreparer,
		Now:         func() time.Time { return testNow },
		NewID:       func() model.ExamSittingID { return sittingID },
	})
	require.NoError(t, err)
	return sittingFixture{service: service, inv: invocation.New(testPrincipal(userID), model.RequestMetadata{}),
		stranger: invocation.New(testPrincipal(model.NewUserID()), model.RequestMetadata{}), userID: userID,
		unitID: unitID, examID: examID, revisionID: revisionID, classID: classID, sittingID: sittingID,
		persistence: persistence, access: accessStore, memberships: memberships, control: control,
		publisher: publisher, jobs: jobs, mail: mailPreparer}
}

// requireDecision asserts the last authorization decision the real
// authorizer recorded.
func (f sittingFixture) requireDecision(t *testing.T, action model.Action, resource model.Resource, status model.AuditStatus) {
	t.Helper()
	decisions := f.control.Audits.Decisions()
	require.NotEmpty(t, decisions, "authorization decisions")
	last := decisions[len(decisions)-1]
	require.Equal(t, string(action), last.Action, "decision action")
	require.Equal(t, resource, last.Resource, "decision resource")
	require.Equal(t, status, last.Status, "decision status")
}

// recordedAttempt is the one mutation attempt the real auditor kept.
type recordedAttempt struct {
	event     *model.AuditEvent
	operation string
	value     json.RawMessage
}

func (f sittingFixture) attempt(t *testing.T) recordedAttempt {
	t.Helper()
	attempts := f.control.Audits.Attempts()
	require.Len(t, attempts, 1, "mutation attempts")
	var parameters struct {
		Operation string          `json:"operation"`
		Value     json.RawMessage `json:"value"`
	}
	require.NoError(t, json.Unmarshal(attempts[0].Parameters, &parameters))
	return recordedAttempt{event: attempts[0], operation: parameters.Operation, value: parameters.Value}
}

// requireValue asserts the audited value as it was stored.
func (a recordedAttempt) requireValue(t *testing.T, want map[string]any) {
	t.Helper()
	encoded, err := json.Marshal(want)
	require.NoError(t, err)
	require.JSONEq(t, string(encoded), string(a.value), "audited value")
}

func assertFaultCode(t *testing.T, err error, want string) {
	t.Helper()
	var fault *apperror.Error
	require.ErrorAs(t, err, &fault, "fault")
	require.Equal(t, want, fault.Code(), "fault")
}

func testPrincipal(userID model.UserID) model.Principal {
	return model.Principal{UserID: userID, SessionID: model.NewSessionID(), CredentialID: model.PrincipalCredentialID(model.NewID()),
		CredentialType: model.CredentialSessionAccess, AuthenticationMethod: "password", AuthenticationStrength: model.AuthenticationSingleFactor,
		ClientType: model.SessionClientWeb, AuthenticatedAt: testNow.Add(-time.Hour)}
}

type sittingStoreFake struct {
	store.ExamSittingStore
	candidateIDs  []model.UserID
	schedule      *store.ExamSittingSchedule
	command       *store.CommandIdempotency
	result        *store.ExamSittingCommandResult
	update        *store.ExamSittingScheduleUpdate
	cancel        *store.ExamSittingCancellation
	pause         *store.ExamSittingManagerTransition
	resume        *store.ExamSittingManagerTransition
	extend        *store.ExamSittingExtension
	earlyClose    *store.ExamSittingManagerTransition
	advance       *store.ExamSittingDueAdvance
	finish        *store.ExamSittingFinishSealing
	lifecycle     *store.ExamSittingLifecycleResult
	dueOptions    store.ExamSittingLifecycleDueOptions
	due           []store.ExamSittingLifecycleDue
	noShowOptions store.ExamSittingNoShowListOptions
	noShows       []store.ExamSittingNoShow
	snapshot      *store.ExamSittingSnapshot
	getExamID     model.ExamID
	getSittingID  model.ExamSittingID
	resolveID     model.ExamSittingID
	listOptions   store.ExamSittingListOptions
	items         []store.ExamSittingSnapshot
	err           error
	getErr        error
}

type scheduleMailPreparerFake struct {
	request ScheduleMailRequest
	result  *store.ExamSittingMailFanout
	err     error
}

func (f *scheduleMailPreparerFake) Prepare(_ context.Context, request ScheduleMailRequest) (*store.ExamSittingMailFanout, error) {
	f.request = request
	return f.result, f.err
}

func (f *sittingStoreFake) Resolve(_ context.Context, sittingID model.ExamSittingID) (*store.ExamSittingSnapshot, error) {
	f.resolveID = sittingID
	if f.getErr != nil {
		return nil, f.getErr
	}
	return f.snapshot, nil
}

func (f *sittingStoreFake) Schedule(_ context.Context, input *store.ExamSittingSchedule, command *store.CommandIdempotency) (*store.ExamSittingCommandResult, error) {
	f.schedule, f.command = input, command
	if f.err != nil {
		return nil, f.err
	}
	if f.result != nil {
		return f.result, nil
	}
	return &store.ExamSittingCommandResult{Value: &store.ExamSittingSnapshot{Sitting: input.Sitting}}, nil
}

func (f *sittingStoreFake) Get(_ context.Context, examID model.ExamID, sittingID model.ExamSittingID) (*store.ExamSittingSnapshot, error) {
	f.getExamID, f.getSittingID = examID, sittingID
	return f.snapshot, f.getErr
}

func (f *sittingStoreFake) List(_ context.Context, options store.ExamSittingListOptions) ([]store.ExamSittingSnapshot, error) {
	f.listOptions = options
	return f.items, f.err
}

func (f *sittingStoreFake) UpdateSchedule(_ context.Context, input *store.ExamSittingScheduleUpdate, command *store.CommandIdempotency) (*store.ExamSittingCommandResult, error) {
	f.update, f.command = input, command
	return f.result, f.err
}

func (f *sittingStoreFake) Cancel(_ context.Context, input *store.ExamSittingCancellation, command *store.CommandIdempotency) (*store.ExamSittingCommandResult, error) {
	f.cancel, f.command = input, command
	return f.result, f.err
}

func (f *sittingStoreFake) Pause(_ context.Context, input *store.ExamSittingManagerTransition, command *store.CommandIdempotency) (*store.ExamSittingLifecycleResult, error) {
	f.pause, f.command = input, command
	return f.lifecycle, f.err
}

func (f *sittingStoreFake) Resume(_ context.Context, input *store.ExamSittingManagerTransition, command *store.CommandIdempotency) (*store.ExamSittingLifecycleResult, error) {
	f.resume, f.command = input, command
	return f.lifecycle, f.err
}

func (f *sittingStoreFake) Extend(_ context.Context, input *store.ExamSittingExtension, command *store.CommandIdempotency) (*store.ExamSittingLifecycleResult, error) {
	f.extend, f.command = input, command
	return f.lifecycle, f.err
}

func (f *sittingStoreFake) EarlyClose(_ context.Context, input *store.ExamSittingManagerTransition, command *store.CommandIdempotency) (*store.ExamSittingLifecycleResult, error) {
	f.earlyClose, f.command = input, command
	return f.lifecycle, f.err
}

func (f *sittingStoreFake) AdvanceDue(_ context.Context, input *store.ExamSittingDueAdvance) (*store.ExamSittingLifecycleResult, error) {
	f.advance = input
	return f.lifecycle, f.err
}

func (f *sittingStoreFake) FinishSealing(_ context.Context, input *store.ExamSittingFinishSealing) (*store.ExamSittingLifecycleResult, error) {
	f.finish = input
	return f.lifecycle, f.err
}

func (f *sittingStoreFake) ListLifecycleDue(_ context.Context, options store.ExamSittingLifecycleDueOptions) ([]store.ExamSittingLifecycleDue, error) {
	f.dueOptions = options
	return f.due, f.err
}

func (f *sittingStoreFake) ListNoShows(_ context.Context, options store.ExamSittingNoShowListOptions) ([]store.ExamSittingNoShow, error) {
	f.noShowOptions = options
	return f.noShows, f.err
}

type accessFake struct {
	store.ExamAuthoringStore
	snapshot *store.ExamAccessSnapshot
	err      error
}

// Resolve gives the authorizer the Exam's owning Academic Unit.
func (f *accessFake) Resolve(context.Context, model.ExamID) (*model.Exam, error) {
	return f.snapshot.Exam, nil
}

func (f *accessFake) Access(context.Context, model.ExamID, model.UserID) (*store.ExamAccessSnapshot, error) {
	return f.snapshot, f.err
}

// sittingOwnershipFake resolves the fixture's Sitting to its Exam for the
// authorizer, apart from the snapshot a test gives the service.
type sittingOwnershipFake struct {
	store.ExamSittingStore
	sittingID model.ExamSittingID
	examID    model.ExamID
}

func (f sittingOwnershipFake) Resolve(_ context.Context, id model.ExamSittingID) (*store.ExamSittingSnapshot, error) {
	if id != f.sittingID {
		return nil, store.NewErrNotFound("exam_sitting", id.String())
	}
	return &store.ExamSittingSnapshot{Sitting: &model.ExamSitting{ID: id, ExamID: f.examID}}, nil
}

// submissionOwnershipFake places every Submission in the fixture's Exam.
type submissionOwnershipFake struct {
	store.ExamSubmissionStore
	examID    model.ExamID
	sittingID model.ExamSittingID
	unitID    model.AcademicUnitID
}

func (f submissionOwnershipFake) Resolve(_ context.Context, id model.SubmissionID) (*store.ExamSubmissionAuthorization, error) {
	return &store.ExamSubmissionAuthorization{SubmissionID: id, ExamID: f.examID, SittingID: f.sittingID,
		AttemptID: model.NewExamAttemptID(), CandidateUserID: model.NewUserID(), AcademicUnitID: f.unitID}, nil
}

type membershipsFake struct {
	store.AcademicUnitMemberStore
	items []*model.AcademicUnitMember
	err   error
	at    time.Time
}

func (f *membershipsFake) ListActiveByUser(_ context.Context, _ model.UserID, at time.Time) ([]*model.AcademicUnitMember, error) {
	f.at = at
	return f.items, f.err
}

// scheduleCalls are the calls of a schedule change to a Sitting without
// candidates: the fact newEvent builds, then the Sitting board invalidation.
func scheduleCalls(
	t *testing.T,
	newEvent func(model.ExamID, model.ExamSittingID, model.ExamSittingState, int64, time.Time) (realtime.Event, error),
	examID model.ExamID,
	sittingID model.ExamSittingID,
	state model.ExamSittingState,
	revision int64,
	at time.Time,
) []realtimetest.Call {
	t.Helper()
	event, err := newEvent(examID, sittingID, state, revision, at)
	require.NoError(t, err)
	board, err := realtime.NewManagerSittingBoardChangedEvent(examID, sittingID)
	require.NoError(t, err)
	return []realtimetest.Call{
		{Method: realtimetest.Publish, Event: event},
		{Method: realtimetest.Publish, Event: board},
	}
}

// lifecycleCalls are the calls of a lifecycle transition of a Sitting without
// candidates: the transition to managers, the same fact to candidates, then
// the Sitting board invalidation.
func lifecycleCalls(
	t *testing.T,
	sitting *model.ExamSitting,
	transition store.ExamSittingLifecycleTransitionCode,
) []realtimetest.Call {
	t.Helper()
	managerEvent, err := realtime.NewExamSittingLifecycleChangedEvent(sitting.ExamID, sitting.ID, sitting.State, sitting.Revision,
		string(transition), sitting.ScheduledEndAt, sitting.UpdatedAt)
	require.NoError(t, err)
	candidateEvent, err := realtime.NewCandidateExamSittingLifecycleChangedEvent(sitting.ExamID, sitting.ID, sitting.State, sitting.Revision,
		string(transition), sitting.ScheduledEndAt, sitting.UpdatedAt)
	require.NoError(t, err)
	board, err := realtime.NewManagerSittingBoardChangedEvent(sitting.ExamID, sitting.ID)
	require.NoError(t, err)
	return []realtimetest.Call{
		{Method: realtimetest.Publish, Event: managerEvent},
		{Method: realtimetest.Publish, Event: candidateEvent},
		{Method: realtimetest.Publish, Event: board},
	}
}

// ListCandidateInvalidationTargetsBySitting answers the candidates whose Exam
// activity a Sitting change invalidates.
func (f *sittingStoreFake) ListCandidateInvalidationTargetsBySitting(
	context.Context,
	model.ExamSittingID,
	model.UserID,
	int,
) ([]model.UserID, error) {
	return f.candidateIDs, nil
}

type lifecycleJobFactoryFake struct {
	open, deadline, finalize                                    *model.Job
	boundaryRevision, deadlineRevision, finalizeRevision        int64
	boundaryStart, boundaryEnd, deadlineAt, finalizeAvailableAt time.Time
	err                                                         error
}

func (f *lifecycleJobFactoryFake) BoundaryJobs(_ model.ExamSittingID, revision int64, startAt, endAt time.Time) (*model.Job, *model.Job, error) {
	f.boundaryRevision, f.boundaryStart, f.boundaryEnd = revision, startAt, endAt
	return f.open, f.deadline, f.err
}

func (f *lifecycleJobFactoryFake) DeadlineJob(_ model.ExamSittingID, revision int64, deadline time.Time) (*model.Job, error) {
	f.deadlineRevision, f.deadlineAt = revision, deadline
	return f.deadline, f.err
}

func (f *lifecycleJobFactoryFake) FinalizeJob(_ model.ExamSittingID, revision int64, availableAt time.Time) (*model.Job, error) {
	f.finalizeRevision = revision
	f.finalizeAvailableAt = availableAt
	return f.finalize, f.err
}
