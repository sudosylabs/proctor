// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package academic

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/sudosylabs/proctor/server/app/access/accesstest"
	"github.com/sudosylabs/proctor/server/app/apperror"
	"github.com/sudosylabs/proctor/server/app/internal/batchitem"
	"github.com/sudosylabs/proctor/server/app/invocation"
	"github.com/sudosylabs/proctor/server/app/realtime"
	"github.com/sudosylabs/proctor/server/app/realtime/realtimetest"
	"github.com/sudosylabs/proctor/server/model"
	"github.com/sudosylabs/proctor/server/store"
)

type classMemberStoreFake struct {
	store.ClassMemberStore
	pageOptions  store.ClassMemberPageOptions
	countOptions store.ClassMemberPageOptions
	pageResult   *store.ClassMemberPage
	events       *[]string
	current      *model.ClassMember
	enrollInput  *store.ClassMemberEnrollment
	endInput     *store.ClassMemberEnd
	active       []*model.ClassMember
	replayed     bool
	noOp         bool
}

func (s *classMemberStoreFake) ListActiveByUser(context.Context, model.UserID, time.Time) ([]*model.ClassMember, error) {
	*s.events = append(*s.events, "list-active")
	return s.active, nil
}

func (s *classMemberStoreFake) Get(context.Context, model.ClassMemberID) (*model.ClassMember, error) {
	*s.events = append(*s.events, "get-member")
	return s.current, nil
}
func (s *classMemberStoreFake) Enroll(_ context.Context, input *store.ClassMemberEnrollment) (*store.ClassEnrollmentResult, error) {
	*s.events = append(*s.events, "store-enroll")
	s.enrollInput = input
	input.Replayed = s.replayed
	input.NoOp = s.noOp
	return &store.ClassEnrollmentResult{Membership: input.Member}, nil
}
func (s *classMemberStoreFake) End(_ context.Context, input *store.ClassMemberEnd) (*model.ClassMember, error) {
	*s.events = append(*s.events, "store-end")
	s.endInput = input
	ended := *s.current
	ended.EndsAt = model.OptionalTimeFrom(input.EndAt)
	ended.Revision = input.ExpectedRevision + 1
	return &ended, nil
}

// classMemberClassStoreFake serves the service and the authorizer, which
// resolves each Class to the unit it belongs to.
type classMemberClassStoreFake struct {
	store.ClassStore
	events *[]string
	unitID model.AcademicUnitID
	value  *model.Class
	values map[string]*model.Class
}

func (s *classMemberClassStoreFake) Get(_ context.Context, id model.ClassID) (*model.Class, error) {
	*s.events = append(*s.events, "get-class")
	if s.values != nil {
		return s.values[id.String()], nil
	}
	return s.value, nil
}

func (s *classMemberClassStoreFake) GetAcademicUnitID(context.Context, model.ClassID) (model.AcademicUnitID, error) {
	return s.unitID, nil
}

type classMemberUserStoreFake struct {
	store.UserStore
	events *[]string
	value  *model.User
}

func (s *classMemberUserStoreFake) Get(context.Context, model.UserID) (*model.User, error) {
	*s.events = append(*s.events, "get-user")
	return s.value, nil
}

// classMemberAccess grants the caller the actions in the unit and resolves
// the Classes the store holds through the world.
func classMemberAccess(t *testing.T, events *[]string, world classWorld, classes *classMemberClassStoreFake, unitID model.AcademicUnitID, actions ...model.Action) (*accesstest.Fixture, invocation.Invocation) {
	t.Helper()
	principal, caller := newCaller()
	return world.access(t, events, classes, unitGrant(principal.UserID, unitID, actions...)), caller
}

func TestClassMemberEnrollUsesDestinationPeriodAndAtomicStore(t *testing.T) {
	t.Parallel()
	events := []string{}
	world := newClassWorld(1)
	classID, periodID, userID := model.NewID(), world.period.ID.String(), model.NewID()
	persistence := &classMemberStoreFake{events: &events}
	classes := &classMemberClassStoreFake{events: &events, unitID: world.unitID, value: &model.Class{ID: model.ClassID(classID), AcademicPeriodID: model.AcademicPeriodID(periodID), DisplayName: "Class A"}}
	access, caller := classMemberAccess(t, &events, world, classes, world.unitID, model.ActionClassMembersManage)
	user := mailTestUser(model.Principal{UserID: model.UserID(userID)}, time.UnixMilli(500))
	users := &classMemberUserStoreFake{events: &events, value: user}
	composer, rendered := newMail(t, &events)
	publisher := &realtimetest.Publisher{}
	sitting := store.ExamSittingInvalidationTarget{ExamID: model.NewExamID(), SittingID: model.NewExamSittingID()}
	sittings := &sittingTargetsFake{targets: map[model.ClassID][]store.ExamSittingInvalidationTarget{model.ClassID(classID): {sitting}}}
	clockCalls := 0
	service := mustNew(Dependencies{Store: &catalogFake{classMember: persistence, class: classes, user: users, examSitting: sittings}, Authorizer: access.Authorizer, Auditor: access.Auditor, Mail: composer, Now: func() time.Time {
		clockCalls++
		return time.UnixMilli(500)
	}, NewID: model.NewID, Publisher: publisher})
	enrollment, err := service.EnrollClassMember(context.Background(), caller, EnrollClassMemberCommand{ClassID: model.ClassID(classID), UserID: model.UserID(userID), StartAt: 100})
	require.NoError(t, err)
	require.Equal(t, periodID, enrollment.Membership.AcademicPeriodID.String(), "enrollment/input = %#v / %#v", enrollment, persistence.enrollInput)
	require.Equal(t, userID, persistence.enrollInput.Member.UserID.String(), "enrollment/input = %#v / %#v", enrollment, persistence.enrollInput)
	require.Equal(t, user.Revision, persistence.enrollInput.ExpectedRecipientRevision, "expected recipient revision")
	require.Equal(t, 1, clockCalls, "clock calls/creation/audit time")
	require.True(t, model.TimeUTC(enrollment.Membership.CreatedAt).Equal(persistence.enrollInput.AuditAt), "clock calls/creation/audit time")
	require.NotNil(t, persistence.enrollInput.Notice, "enrollment mail/input = %#v", persistence.enrollInput)
	require.Equal(t, model.MailTemplateAcademicClassEnrolled, persistence.enrollInput.Notice.Occurrence.TemplateKey, "enrollment mail/input = %#v", persistence.enrollInput)
	require.Equal(t, classes.value.DisplayName, rendered.classTransition(t).ClassDisplayName, "enrollment mail/input = %#v", persistence.enrollInput)
	require.False(t, persistence.enrollInput.ExpectedPreviousID.IsValid(), "enrollment mail/input = %#v", persistence.enrollInput)
	// Contract (guardrail 8): authorization precedes the audit, and the audit
	// begins before the durable write. The first get-class resolves the
	// Class's scope for the decision.
	require.Equal(t, []string{"get-class", "grants", "decision:class.members.manage:success", "get-class", "list-active", "get-user", "render-mail", "audit-begin:class.members.manage", "store-enroll"}, events)
	candidateEvent, err := realtime.NewCandidateExamActivityChangedEvent(model.UserID(userID))
	require.NoError(t, err)
	boardEvent, err := realtime.NewManagerSittingBoardChangedEvent(sitting.ExamID, sitting.SittingID)
	require.NoError(t, err)
	// Contract: the enrolled candidate is told first that their Exam activity
	// and current-user context changed, then the managers of each Sitting of
	// the Class are told to refresh their board; nothing is reported.
	require.Equal(t, []realtimetest.Call{
		{Method: realtimetest.Publish, Event: candidateEvent},
		{Method: realtimetest.InvalidateCurrentUserContext, UserID: model.UserID(userID)},
		{Method: realtimetest.Publish, Event: boardEvent},
	}, publisher.Calls())
	require.Equal(t, []model.ClassID{model.ClassID(classID)}, sittings.listed)
}

func TestClassMemberEnrollReplayDoesNotPublishMembershipChange(t *testing.T) {
	t.Parallel()
	events := []string{}
	world := newClassWorld(1)
	classID, periodID, userID := model.NewClassID(), world.period.ID, model.NewUserID()
	persistence := &classMemberStoreFake{events: &events, replayed: true}
	publisher := &realtimetest.Publisher{}
	classes := &classMemberClassStoreFake{events: &events, unitID: world.unitID, value: &model.Class{
		ID: classID, AcademicPeriodID: periodID, DisplayName: "Class A",
	}}
	access, caller := classMemberAccess(t, &events, world, classes, world.unitID, model.ActionClassMembersManage)
	composer, _ := newMail(t, &events)
	service := mustNew(Dependencies{Store: &catalogFake{classMember: persistence, class: classes, user: &classMemberUserStoreFake{events: &events, value: mailTestUser(model.Principal{UserID: userID}, time.UnixMilli(500))}}, Authorizer: access.Authorizer, Auditor: access.Auditor, Mail: composer, Now: func() time.Time { return time.UnixMilli(500) }, NewID: model.NewID, Publisher: publisher})
	result, err := service.EnrollClassMember(
		context.Background(),
		caller,
		EnrollClassMemberCommand{
			ClassID: classID, UserID: userID, StartAt: 100, IdempotencyKey: "replay",
		},
	)
	require.NoError(t, err, "Enroll()")
	require.NotNil(t, result, "Enroll()")
	require.Empty(t, publisher.Calls(), "replayed enrollment published")
}

func TestClassMemberRetainedEnrollmentBypassesChangedMembershipAndMail(t *testing.T) {
	t.Parallel()
	events := []string{}
	world := newClassWorld(1)
	persistence := &classMemberStoreFake{events: &events, active: []*model.ClassMember{{ID: model.NewClassMemberID(), ClassID: model.NewClassID()}}}
	classes := &classMemberClassStoreFake{events: &events, unitID: world.unitID, value: world.class()}
	access, caller := classMemberAccess(t, &events, world, classes, world.unitID, model.ActionClassMembersManage)
	composer, rendered := newMail(t, &events)
	service := mustNew(Dependencies{Store: &catalogFake{classMember: persistence, class: classes, user: &classMemberUserStoreFake{events: &events}}, Authorizer: access.Authorizer, Auditor: access.Auditor, Mail: composer, Now: func() time.Time { return time.UnixMilli(500) }, NewID: func() string { return model.NewClassMemberID().String() }, Publisher: &realtimetest.Publisher{}})
	result, err := service.EnrollClassMember(context.Background(), caller, EnrollClassMemberCommand{ClassID: classes.value.ID,
		UserID: model.NewUserID(), StartAt: 100, IdempotencyKey: "row", Batch: batchitem.New(batchitem.Fields{RetainedOutcome: true})})
	require.NoError(t, err, "retained enrollment")
	require.NotNil(t, result, "retained enrollment")
	require.Empty(t, rendered.requests, "retained enrollment")
	// The only Class read resolves the Class's scope for the decision.
	require.Equal(t, []string{"get-class", "grants", "decision:class.members.manage:success", "audit-begin:class.members.manage", "store-enroll"}, events)
	require.NotNil(t, persistence.enrollInput, "retained enrollment")
	require.Nil(t, persistence.enrollInput.Notice, "retained enrollment")
}

func TestClassMemberAlreadySatisfiedEnrollmentPreparesNoticeForAuthoritativeRace(t *testing.T) {
	t.Parallel()
	events := []string{}
	world := newClassWorld(1)
	classID, periodID, userID := model.NewClassID(), world.period.ID, model.NewUserID()
	current := &model.ClassMember{ID: model.NewClassMemberID(), ClassID: classID, AcademicPeriodID: periodID, UserID: userID, StartsAt: model.TimeFromMillis(100)}
	persistence := &classMemberStoreFake{events: &events, active: []*model.ClassMember{current}}
	class := &model.Class{ID: classID, AcademicPeriodID: periodID, DisplayName: "Class", Revision: 1}
	classes := &classMemberClassStoreFake{events: &events, unitID: world.unitID, value: class}
	access, caller := classMemberAccess(t, &events, world, classes, world.unitID, model.ActionClassMembersManage)
	composer, rendered := newMail(t, &events)
	service := mustNew(Dependencies{Store: &catalogFake{classMember: persistence, class: classes, user: &classMemberUserStoreFake{events: &events, value: mailTestUser(model.Principal{UserID: userID}, time.UnixMilli(500))}}, Authorizer: access.Authorizer, Auditor: access.Auditor, Mail: composer, Now: func() time.Time { return time.UnixMilli(500) }, NewID: func() string { return model.NewClassMemberID().String() }, Publisher: &realtimetest.Publisher{}})
	result, err := service.EnrollClassMember(context.Background(), caller,
		EnrollClassMemberCommand{ClassID: classID, UserID: userID, StartAt: 100, IdempotencyKey: "row"})
	require.NoError(t, err, "already-satisfied enrollment")
	require.NotNil(t, result, "already-satisfied enrollment")
	require.NotNil(t, persistence.enrollInput, "already-satisfied enrollment")
	require.NotNil(t, persistence.enrollInput.Notice, "already-satisfied enrollment")
	require.Len(t, rendered.requests, 1, "already-satisfied enrollment")
}

func TestClassMemberEnrollCommitsTerminalNoticeForInactiveRecipient(t *testing.T) {
	t.Parallel()
	events := []string{}
	world := newClassWorld(1)
	classID, periodID, userID := model.NewClassID(), world.period.ID, model.NewUserID()
	persistence := &classMemberStoreFake{events: &events}
	classes := &classMemberClassStoreFake{events: &events, unitID: world.unitID, value: &model.Class{
		ID: classID, AcademicPeriodID: periodID, DisplayName: "Class A"}}
	access, caller := classMemberAccess(t, &events, world, classes, world.unitID, model.ActionClassMembersManage)
	user := mailTestUser(model.Principal{UserID: userID}, time.UnixMilli(500))
	user.DisabledAt = model.OptionalTimeFrom(time.UnixMilli(450))
	users := &classMemberUserStoreFake{events: &events, value: user}
	composer, rendered := newMail(t, &events)
	service := mustNew(Dependencies{Store: &catalogFake{classMember: persistence, class: classes, user: users}, Authorizer: access.Authorizer, Auditor: access.Auditor, Mail: composer, Now: func() time.Time { return time.UnixMilli(500) }, NewID: model.NewID, Publisher: &realtimetest.Publisher{}})
	_, err := service.EnrollClassMember(context.Background(), caller,
		EnrollClassMemberCommand{ClassID: classID, UserID: userID, StartAt: 100})
	require.NoError(t, err)
	require.NotNil(t, persistence.enrollInput)
	require.NotNil(t, persistence.enrollInput.Notice)
	require.Equal(t, user.Revision, persistence.enrollInput.ExpectedRecipientRevision)
	require.Equal(t, model.MailDeliverySuppressed, persistence.enrollInput.Notice.Delivery.State)
	require.Equal(t, model.MailDeliveryRecipientIneligibleCode, persistence.enrollInput.Notice.Delivery.PublicFailureCode)
	require.Equal(t, model.JobStatusCanceled, persistence.enrollInput.Notice.Job.Status)
	require.Empty(t, rendered.requests, "an inactive recipient's notice is never rendered")
}

func TestClassMemberTransferAuthorizesSourceAndCreatesOneTransferNotice(t *testing.T) {
	t.Parallel()
	events := []string{}
	world := newClassWorld(1)
	periodID, userID := world.period.ID, model.NewUserID()
	source := &model.Class{ID: model.NewClassID(), AcademicPeriodID: periodID, DisplayName: "Source Class"}
	destination := &model.Class{ID: model.NewClassID(), AcademicPeriodID: periodID, DisplayName: "Destination Class"}
	previous := &model.ClassMember{ID: model.NewClassMemberID(), ClassID: source.ID, AcademicPeriodID: periodID,
		UserID: userID, StartsAt: model.TimeFromMillis(100), Revision: 1}
	persistence := &classMemberStoreFake{events: &events, active: []*model.ClassMember{previous}}
	classes := &classMemberClassStoreFake{events: &events, unitID: world.unitID, values: map[string]*model.Class{source.ID.String(): source, destination.ID.String(): destination}}
	principal, caller := newCaller()
	access := world.access(t, &events, classes, unitGrant(principal.UserID, world.unitID, model.ActionClassMembersManage))
	users := &classMemberUserStoreFake{events: &events, value: mailTestUser(model.Principal{UserID: userID}, time.UnixMilli(500))}
	composer, rendered := newMail(t, &events)
	service := mustNew(Dependencies{Store: &catalogFake{classMember: persistence, class: classes, user: users}, Authorizer: access.Authorizer, Auditor: access.Auditor, Mail: composer, Now: func() time.Time { return time.UnixMilli(500) }, NewID: model.NewID, Publisher: &realtimetest.Publisher{}})
	authority := &store.CommandAuthorization{Principal: principal, ScopeType: model.RoleScopeClass, ScopeID: destination.ID.String(),
		Actions: []model.Action{model.ActionOnboardingBatchManage, model.ActionClassMembersManage}}
	_, err := service.EnrollClassMember(context.Background(), caller,
		EnrollClassMemberCommand{ClassID: destination.ID, UserID: userID, StartAt: 200,
			ExpectedPreviousID: previous.ID, RequireTransfer: true, IdempotencyKey: "row", Batch: batchitem.New(batchitem.Fields{Authorization: authority,
				Metadata: &store.CommandBatch{GroupDigest: [32]byte{1}}})})
	require.NoError(t, err)
	require.Equal(t, model.MailTemplateAcademicClassTransferred, persistence.enrollInput.Notice.Occurrence.TemplateKey, "transfer mail/input = %#v", persistence.enrollInput)
	details := rendered.classTransition(t)
	require.Equal(t, source.DisplayName, details.PreviousClassDisplayName, "transfer mail/input = %#v", persistence.enrollInput)
	require.Equal(t, destination.DisplayName, details.ClassDisplayName, "transfer mail/input = %#v", persistence.enrollInput)
	require.Equal(t, previous.ID, persistence.enrollInput.ExpectedPreviousID, "transfer mail/input = %#v", persistence.enrollInput)
	require.NotNil(t, persistence.enrollInput.Notice, "transfer mail/input = %#v", persistence.enrollInput)
	require.Equal(t, users.value.Revision, persistence.enrollInput.ExpectedRecipientRevision, "transfer mail/input = %#v", persistence.enrollInput)
	attempts := access.Audits.Attempts()
	require.Len(t, attempts, 2, "transfer audits")
	require.Equal(t, attempts[0].ID, persistence.enrollInput.AuditEventID, "the destination audit begins first")
	require.Equal(t, attempts[1].ID, persistence.enrollInput.PreviousAuditEventID, "the source audit begins second")
	wantAuditResources := []model.Resource{{Type: model.ResourceClass, ID: destination.ID.String()}, {Type: model.ResourceClass, ID: source.ID.String()}}
	require.Equal(t, wantAuditResources, []model.Resource{attempts[0].Resource, attempts[1].Resource}, "transfer audit resources")
	require.Equal(t, []model.UserID{userID, userID}, []model.UserID{attempts[0].SubjectUserID, attempts[1].SubjectUserID}, "both transfer audits name the member as their subject")
	require.NotNil(t, persistence.enrollInput.Command)
	require.Equal(t, previous.ID, persistence.enrollInput.Command.Authorization.ClassMemberID)
	require.Equal(t, []string{
		"class.members.manage class:" + destination.ID.String() + " success",
		"class.members.manage class:" + source.ID.String() + " success",
	}, decisions(access), "the destination and the source are authorized")
	// Contract (guardrail 8): authorization precedes the audit, and the audit
	// begins before the durable write. Each decision first resolves its Class.
	require.Equal(t, []string{
		"get-class", "grants", "decision:class.members.manage:success", "get-class", "list-active",
		"get-class", "grants", "decision:class.members.manage:success", "get-class", "get-user", "render-mail",
		"audit-begin:class.members.manage", "audit-begin:class.members.manage", "store-enroll",
	}, events)
}

func TestClassMemberEndCarriesRevision(t *testing.T) {
	t.Parallel()
	events := []string{}
	world := newClassWorld(1)
	current := &model.ClassMember{
		ID: model.NewClassMemberID(), CreatedAt: model.TimeFromMillis(100), UpdatedAt: model.TimeFromMillis(100),
		Revision: 4, ClassID: model.ClassID(model.NewID()), AcademicPeriodID: world.period.ID,
		UserID: model.UserID(model.NewID()), StartsAt: model.TimeFromMillis(100),
	}
	persistence := &classMemberStoreFake{events: &events, current: current}
	class := &model.Class{ID: current.ClassID, AcademicPeriodID: current.AcademicPeriodID, DisplayName: "Ended Class"}
	classes := &classMemberClassStoreFake{events: &events, unitID: world.unitID, value: class}
	access, caller := classMemberAccess(t, &events, world, classes, world.unitID, model.ActionClassMembersManage)
	users := &classMemberUserStoreFake{events: &events, value: mailTestUser(model.Principal{UserID: current.UserID}, time.UnixMilli(500))}
	composer, rendered := newMail(t, &events)
	service := mustNew(Dependencies{Store: &catalogFake{classMember: persistence, class: classes, user: users}, Authorizer: access.Authorizer, Auditor: access.Auditor, Mail: composer, Now: func() time.Time { return time.UnixMilli(500) }, NewID: model.NewID, Publisher: &realtimetest.Publisher{}})
	ended, err := service.EndClassMember(context.Background(), caller, EndClassMemberCommand{ID: current.ID})
	require.NoError(t, err)
	require.Equal(t, int64(4), persistence.endInput.ExpectedRevision, "end input/result = %#v / %#v", persistence.endInput, ended)
	require.Equal(t, int64(5), ended.Revision, "end input/result = %#v / %#v", persistence.endInput, ended)
	require.Equal(t, users.value.Revision, persistence.endInput.ExpectedRecipientRevision, "end expected recipient revision")
	require.Len(t, rendered.requests, 1, "end mail/input = %#v", persistence.endInput)
	require.Equal(t, model.MailTemplateAcademicClassEnrollmentEnded, persistence.endInput.Notice.Occurrence.TemplateKey, "end mail/input = %#v", persistence.endInput)
	require.NotNil(t, persistence.endInput.Notice, "end mail/input = %#v", persistence.endInput)
	// Contract (guardrail 8): authorization precedes the audit, and the audit
	// begins before the durable write. The first grants read is the preflight,
	// which records no decision when the caller holds the action somewhere.
	require.Equal(t, []string{"grants", "get-member", "get-class", "grants", "decision:class.members.manage:success", "get-class", "get-user", "render-mail", "audit-begin:class.members.manage", "store-end"}, events)
}

func TestClassMemberEndDenialDoesNotInspectOpaqueMemberID(t *testing.T) {
	t.Parallel()
	events := []string{}
	world := newClassWorld(1)
	_, caller := newCaller()
	classes := &classMemberClassStoreFake{events: &events, unitID: world.unitID}
	access := world.access(t, &events, classes)
	composer, _ := newMail(t, &events)
	service := mustNew(Dependencies{Store: &catalogFake{classMember: &classMemberStoreFake{events: &events}, class: classes, user: &classMemberUserStoreFake{events: &events}}, Authorizer: access.Authorizer, Auditor: access.Auditor, Mail: composer, Now: time.Now, NewID: model.NewID, Publisher: &realtimetest.Publisher{}})
	_, err := service.EndClassMember(context.Background(), caller, EndClassMemberCommand{ID: model.NewClassMemberID()})
	require.True(t, apperror.Is(err, "authorization.denied"), "End() error = %v, want authorization.denied", err)
	require.Equal(t, []string{"grants", "decision:class.members.manage:fail"}, events, "events")
}

func TestClassMemberEndConcealsCrossScopeTarget(t *testing.T) {
	t.Parallel()
	events := []string{}
	world := newClassWorld(1)
	current := &model.ClassMember{
		ID: model.NewClassMemberID(), ClassID: model.NewClassID(), AcademicPeriodID: world.period.ID, UserID: model.NewUserID(),
		CreatedAt: model.NowUTC(), UpdatedAt: model.NowUTC(), StartsAt: model.NowUTC(), Revision: 1,
	}
	classes := &classMemberClassStoreFake{events: &events, unitID: world.unitID, value: &model.Class{ID: current.ClassID, AcademicPeriodID: world.period.ID, DisplayName: "Class"}}
	// The caller manages the members of another unit's Classes only.
	access, caller := classMemberAccess(t, &events, world, classes, model.NewAcademicUnitID(), model.ActionClassMembersManage)
	composer, _ := newMail(t, &events)
	service := mustNew(Dependencies{Store: &catalogFake{classMember: &classMemberStoreFake{events: &events, current: current}, class: classes, user: &classMemberUserStoreFake{events: &events}}, Authorizer: access.Authorizer, Auditor: access.Auditor, Mail: composer, Now: time.Now, NewID: model.NewID, Publisher: &realtimetest.Publisher{}})
	_, err := service.EndClassMember(context.Background(), caller, EndClassMemberCommand{ID: current.ID})
	require.True(t, apperror.Is(err, "resource.not_found"), "End() error = %v, want concealed resource.not_found", err)
}

func (s *classMemberStoreFake) ListPageByClass(_ context.Context, options store.ClassMemberPageOptions) (*store.ClassMemberPage, error) {
	*s.events = append(*s.events, "list-page")
	s.pageOptions = options
	if s.pageResult != nil {
		return s.pageResult, nil
	}
	return &store.ClassMemberPage{}, nil
}
func (s *classMemberStoreFake) CountPageByClass(_ context.Context, options store.ClassMemberPageOptions, maximum int) (int, error) {
	*s.events = append(*s.events, "count-page")
	s.countOptions = options
	if s.pageResult == nil {
		return 0, nil
	}
	return min(len(s.pageResult.Members), maximum), nil
}
