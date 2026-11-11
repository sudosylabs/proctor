// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package sitting

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/sudosylabs/proctor/server/app/access"
	"github.com/sudosylabs/proctor/server/app/apperror"
	"github.com/sudosylabs/proctor/server/app/invocation"
	"github.com/sudosylabs/proctor/server/app/realtime"
	"github.com/sudosylabs/proctor/server/app/realtime/realtimetest"
	"github.com/sudosylabs/proctor/server/model"
	"github.com/sudosylabs/proctor/server/store"
)

var testNow = time.Date(2026, 8, 15, 9, 0, 0, 0, time.UTC)

type sittingFixture struct {
	service     *Service
	invocation  invocation.Invocation
	userID      model.UserID
	unitID      model.AcademicUnitID
	examID      model.ExamID
	revisionID  model.ExamRevisionID
	classID     model.ClassID
	sittingID   model.ExamSittingID
	persistence *sittingStoreFake
	access      *accessFake
	memberships *membershipsFake
	authorizer  *authorizerFake
	auditor     *auditorFake
	systemAudit *systemAuditorFake
	publisher   *realtimetest.Publisher
	jobs        *lifecycleJobFactoryFake
	mail        *scheduleMailPreparerFake
}

func (f sittingFixture) sitting(t *testing.T) *model.ExamSitting {
	t.Helper()
	sitting, err := model.NewExamSitting(f.sittingID, f.examID, f.revisionID, f.classID,
		testNow.Add(time.Hour), testNow.Add(3*time.Hour), testNow)
	require.NoError(t, err)
	return sitting
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
	authorizer, auditor, systemAudit, publisher := &authorizerFake{}, &auditorFake{id: model.NewID()}, &systemAuditorFake{id: model.NewID()}, &realtimetest.Publisher{}
	jobs := &lifecycleJobFactoryFake{open: &model.Job{}, deadline: &model.Job{}, finalize: &model.Job{}}
	mailPreparer := &scheduleMailPreparerFake{result: &store.ExamSittingMailFanout{}}
	service, err := New(Dependencies{
		Persistence:   persistence,
		Access:        accessStore,
		Memberships:   memberships,
		Authorizer:    authorizer,
		Auditor:       auditor,
		SystemAuditor: systemAudit,
		Publisher:     publisher,
		Jobs:          jobs,
		Mail:          mailPreparer,
		Now:           func() time.Time { return testNow },
		NewID:         func() model.ExamSittingID { return sittingID },
	})
	require.NoError(t, err)
	return sittingFixture{service: service, invocation: invocation.New(testPrincipal(userID), model.RequestMetadata{}), userID: userID,
		unitID: unitID, examID: examID, revisionID: revisionID, classID: classID, sittingID: sittingID,
		persistence: persistence, access: accessStore, memberships: memberships, authorizer: authorizer, auditor: auditor,
		systemAudit: systemAudit, publisher: publisher, jobs: jobs, mail: mailPreparer}
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

func (f *accessFake) Access(context.Context, model.ExamID, model.UserID) (*store.ExamAccessSnapshot, error) {
	return f.snapshot, f.err
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

type authorizerFake struct {
	action   model.Action
	resource model.Resource
	err      error
	denied   bool
	denyErr  error
}

func (f *authorizerFake) Authorize(_ context.Context, _ invocation.Invocation, action model.Action, resource model.Resource) error {
	f.action, f.resource = action, resource
	return f.err
}

func (f *authorizerFake) RecordDenial(_ context.Context, _ invocation.Invocation, action model.Action, resource model.Resource, _ model.AcademicUnitID) error {
	f.action, f.resource, f.denied = action, resource, true
	return f.denyErr
}

type auditorFake struct {
	id        string
	action    model.Action
	resource  model.Resource
	operation string
	value     map[string]any
	failCode  string
	err       error
	failErr   error
}

func (f *auditorFake) Begin(_ context.Context, attempt access.Mutation) (model.AuditEventID, error) {
	f.action, f.resource, f.operation, f.value = attempt.Action, attempt.Resource, attempt.Operation, attempt.Value
	return model.AuditEventID(f.id), f.err
}

func (f *auditorFake) Fail(_ context.Context, _ model.AuditEventID, code string) error {
	f.failCode = code
	return f.failErr
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

type systemAuditorFake struct {
	id, operation, failCode string
	action                  model.Action
	resource                model.Resource
	scopeType               model.RoleScopeType
	scopeID                 string
	system                  invocation.System
	value                   map[string]any
	err, failErr            error
}

func (f *systemAuditorFake) BeginJob(_ context.Context, system invocation.System, action model.Action, resource model.Resource, scopeType model.RoleScopeType, scopeID, operation string, value map[string]any) (model.AuditEventID, error) {
	f.system, f.action, f.resource, f.operation, f.value = system, action, resource, operation, value
	f.scopeType, f.scopeID = scopeType, scopeID
	return model.AuditEventID(f.id), f.err
}

func (f *systemAuditorFake) Fail(_ context.Context, _ model.AuditEventID, code string) error {
	f.failCode = code
	return f.failErr
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
