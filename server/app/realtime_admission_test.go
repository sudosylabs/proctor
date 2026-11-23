// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package app

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/sudosylabs/proctor/server/app/access/accesstest"
	"github.com/sudosylabs/proctor/server/app/apperror"
	"github.com/sudosylabs/proctor/server/app/exam"
	"github.com/sudosylabs/proctor/server/app/exam/review"
	"github.com/sudosylabs/proctor/server/app/exam/sitting"
	"github.com/sudosylabs/proctor/server/app/invocation"
	"github.com/sudosylabs/proctor/server/app/jobs"
	"github.com/sudosylabs/proctor/server/app/mailtest"
	"github.com/sudosylabs/proctor/server/app/realtime/realtimetest"
	"github.com/sudosylabs/proctor/server/model"
	"github.com/sudosylabs/proctor/server/store"
)

func TestAuthorizeWebSocketExamSubscriptionUsesExamRelationshipGate(t *testing.T) {
	t.Parallel()
	gate := newRealtimeGateFixture(t)
	err := gate.admission.AuthorizeWebSocketSubscription(context.Background(),
		invocation.New(gate.manager, model.RequestMetadata{RequestID: "subscribe"}),
		model.ActionExamView, model.Resource{Type: model.ResourceExam, ID: gate.exam.ID.String()})
	require.NoError(t, err)
	// The Exam Manager's current relationship selects the ordinary action,
	// which the real authorizer decides and records for this caller and Exam.
	gate.requireOnlyDecision(t, model.ActionExamView, model.ResourceExam, gate.exam.ID.String(), model.AuditStatusSuccess)
	require.Equal(t, "subscribe", gate.access.Audits.Decisions()[0].RequestID)
}

func TestAuthorizeWebSocketExamSubscriptionConcealsMissingAndDeniedExams(t *testing.T) {
	t.Parallel()
	gate := newRealtimeGateFixture(t)
	missing := model.Resource{Type: model.ResourceExam, ID: model.NewExamID().String()}
	err := gate.admission.AuthorizeWebSocketSubscription(context.Background(), invocation.New(gate.manager, model.RequestMetadata{}), model.ActionExamView, missing)
	require.True(t, apperror.Is(err, "resource.not_found"), "missing Exam error = %v, want concealed resource.not_found", err)
	require.Empty(t, gate.access.Audits.Decisions(), "a missing Exam is refused before any decision")

	// Another User is no manager, so the gate asks for the override action
	// it does not hold.
	outsider := testExamPrincipal(model.NewUserID())
	err = gate.admission.AuthorizeWebSocketSubscription(context.Background(), invocation.New(outsider, model.RequestMetadata{}),
		model.ActionExamView, model.Resource{Type: model.ResourceExam, ID: gate.exam.ID.String()})
	require.True(t, apperror.Is(err, "resource.not_found"), "denied Exam error = %v, want concealed resource.not_found", err)
	gate.requireOnlyDecision(t, model.ActionExamViewOverride, model.ResourceExam, gate.exam.ID.String(), model.AuditStatusFail)
}

func TestAuthorizeWebSocketSittingSubscriptionUsesRelationshipGate(t *testing.T) {
	t.Parallel()
	gate := newRealtimeGateFixture(t)
	err := gate.admission.AuthorizeWebSocketSubscription(context.Background(),
		invocation.New(gate.manager, model.RequestMetadata{RequestID: "sitting-subscribe"}), model.ActionExamSittingView,
		model.Resource{Type: model.ResourceExamSitting, ID: gate.sitting.ID.String()})
	require.NoError(t, err)
	gate.requireOnlyDecision(t, model.ActionExamSittingView, model.ResourceExamSitting, gate.sitting.ID.String(), model.AuditStatusSuccess)
	require.Equal(t, "sitting-subscribe", gate.access.Audits.Decisions()[0].RequestID)
}

func TestAuthorizeWebSocketSittingSubscriptionConcealsMissingAndDeniedTargets(t *testing.T) {
	t.Parallel()
	gate := newRealtimeGateFixture(t)
	missing := model.Resource{Type: model.ResourceExamSitting, ID: model.NewExamSittingID().String()}
	err := gate.admission.AuthorizeWebSocketSubscription(context.Background(), invocation.New(gate.manager, model.RequestMetadata{}),
		model.ActionExamSittingView, missing)
	require.True(t, apperror.Is(err, "resource.not_found"), "missing Sitting error = %v, want concealed resource.not_found", err)
	require.Empty(t, gate.access.Audits.Decisions(), "a missing Sitting is refused before any decision")

	outsider := testExamPrincipal(model.NewUserID())
	err = gate.admission.AuthorizeWebSocketSubscription(context.Background(), invocation.New(outsider, model.RequestMetadata{}),
		model.ActionExamSittingView, model.Resource{Type: model.ResourceExamSitting, ID: gate.sitting.ID.String()})
	require.True(t, apperror.Is(err, "resource.not_found"), "denied Sitting error = %v, want concealed resource.not_found", err)
	gate.requireOnlyDecision(t, model.ActionExamSittingViewOverride, model.ResourceExamSitting, gate.sitting.ID.String(), model.AuditStatusFail)
}

// A manage subscription, which clip and live evidence hints need, asks for
// the manager relationship: a caller who holds only sitting view is refused
// and the refusal is concealed.
func TestAuthorizeWebSocketSittingManageSubscriptionUsesRelationshipGate(t *testing.T) {
	t.Parallel()
	gate := newRealtimeGateFixture(t)
	err := gate.admission.AuthorizeWebSocketSubscription(context.Background(),
		invocation.New(gate.manager, model.RequestMetadata{}), model.ActionExamSittingManage,
		model.Resource{Type: model.ResourceExamSitting, ID: gate.sitting.ID.String()})
	require.True(t, apperror.Is(err, "resource.not_found"), "a viewer who may not manage: error = %v", err)
	gate.requireOnlyDecision(t, model.ActionExamSittingManage, model.ResourceExamSitting, gate.sitting.ID.String(), model.AuditStatusFail)
}

func TestAuthorizeWebSocketSittingSubscriptionsMatchTheReads(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		action  model.Action
		grants  []model.Action
		allowed bool
	}{
		{name: "sitting viewers cannot subscribe to submissions", action: model.ActionSubmissionView},
		{name: "submission viewers can subscribe to submissions", action: model.ActionSubmissionView, grants: []model.Action{model.ActionSubmissionView}, allowed: true},
		{name: "sitting managers cannot subscribe to evidence without submission visibility", action: model.ActionExamSittingManage, grants: []model.Action{model.ActionExamSittingManage}},
		{name: "submission viewers cannot subscribe to evidence without sitting management", action: model.ActionExamSittingManage, grants: []model.Action{model.ActionSubmissionView}},
		{name: "users with both permissions can subscribe to evidence", action: model.ActionExamSittingManage, grants: []model.Action{model.ActionExamSittingManage, model.ActionSubmissionView}, allowed: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			gate := newRealtimeGateFixture(t, test.grants...)
			resource := model.Resource{Type: model.ResourceExamSitting, ID: gate.sitting.ID.String()}
			err := gate.admission.AuthorizeWebSocketSubscription(t.Context(), invocation.New(gate.manager, model.RequestMetadata{}), test.action, resource)
			if test.allowed {
				require.NoError(t, err)
				return
			}
			require.True(t, apperror.Is(err, "resource.not_found"), "denied subscription: %v", err)
		})
	}
}

func TestAuthorizeWebSocketSubmissionSubscriptionUsesReviewGate(t *testing.T) {
	t.Parallel()
	gate := newRealtimeGateFixture(t, model.ActionSubmissionView)
	resource := model.Resource{Type: model.ResourceSubmission, ID: gate.submission.SubmissionID.String()}
	inv := invocation.New(gate.manager, model.RequestMetadata{})
	subscribe := func() error {
		return gate.admission.AuthorizeWebSocketSubscription(t.Context(), inv, model.ActionSubmissionView, resource)
	}

	require.NoError(t, subscribe())

	gate.examStore.manager = model.NewUserID()
	err := subscribe()
	require.True(t, apperror.Is(err, "resource.not_found"), "unrelated submission viewer: %v", err)

	gate.examStore.manager = gate.manager.UserID
	gate.submission.CandidateUserID = gate.manager.UserID
	err = subscribe()
	require.True(t, apperror.Is(err, "resource.not_found"), "manager viewing own submission: %v", err)
}

// subscriptionUsers holds one active User, related to one unit.
type subscriptionUsers struct {
	store.UserStore
	user   *model.User
	unitID model.AcademicUnitID
}

func (s subscriptionUsers) Get(_ context.Context, id model.UserID) (*model.User, error) {
	if id != s.user.ID {
		return nil, store.NewErrNotFound("user", id.String())
	}
	copied := *s.user
	return &copied, nil
}

func (s subscriptionUsers) MatchVisibility(_ context.Context, id model.UserID, visibility store.UserVisibilityScope) (store.UserVisibilityMatch, error) {
	for _, root := range visibility.AcademicUnitRootIDs {
		if id == s.user.ID && root == s.unitID {
			return store.UserVisibilityMatch{ScopeType: model.RoleScopeAcademicUnit, ScopeID: root.String()}, nil
		}
	}
	return store.UserVisibilityMatch{}, nil
}

// TestAuthorizeWebSocketUserSubscriptionFollowsUserVisibility pins that a
// user.changed subscription is admitted through the caller's user
// visibility: a unit-scoped viewer may follow a User related to their unit,
// and no other.
func TestAuthorizeWebSocketUserSubscriptionFollowsUserVisibility(t *testing.T) {
	t.Parallel()
	related, unrelated := model.NewAcademicUnitID(), model.NewAcademicUnitID()
	subject := &model.User{ID: model.NewUserID(), Username: "student", Email: "student@example.edu", Revision: 1}
	resource := model.Resource{Type: model.ResourceUser, ID: subject.ID.String()}
	for name, test := range map[string]struct {
		unitID  model.AcademicUnitID
		allowed bool
	}{
		"related unit":   {unitID: related, allowed: true},
		"unrelated unit": {unitID: unrelated},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			caller := testExamPrincipal(model.NewUserID())
			fixture := accesstest.New(t, accesstest.Options{
				Grants: []accesstest.Grant{{UserID: caller.UserID, Actions: []model.Action{model.ActionUserView},
					ScopeType: model.RoleScopeAcademicUnit, ScopeID: test.unitID.String()}},
				Users: subscriptionUsers{user: subject, unitID: related},
			})
			admission := NewRealtimeAdmission(RealtimeAdmissionDependencies{Authorizer: fixture.Authorizer})
			err := admission.AuthorizeWebSocketSubscription(context.Background(), invocation.New(caller, model.RequestMetadata{}),
				model.ActionUserView, resource)
			if test.allowed {
				require.NoError(t, err)
				return
			}
			require.True(t, apperror.Is(err, "authorization.denied"), "unrelated viewer error = %v", err)
		})
	}
}

// A subscription may name an owning scope its action accepts, but no other
// Resource type; the shape is refused before any authorization decision.
func TestAuthorizeWebSocketSubscriptionRefusesAResourceTheActionDoesNotAccept(t *testing.T) {
	t.Parallel()
	admission := &RealtimeAdmission{}
	inv := invocation.New(testExamPrincipal(model.NewUserID()), model.RequestMetadata{})
	for _, refused := range []struct {
		action   model.Action
		resource model.Resource
	}{
		{model.ActionJobView, model.Resource{Type: model.ResourceClass, ID: model.NewClassID().String()}},
		{model.ActionOnboardingBatchView, model.Resource{Type: model.ResourceExam, ID: model.NewExamID().String()}},
	} {
		err := admission.AuthorizeWebSocketSubscription(context.Background(), inv, refused.action, refused.resource)
		require.True(t, apperror.Is(err, "websocket.request.invalid"), "%s on %s: error = %v", refused.action, refused.resource.Type, err)
	}
}

// realtimeGateFixture is a RealtimeAdmission over the real Exam Authoring
// and Sitting services and the real authorizer. One Exam in one Academic
// Unit has one Sitting; manager is its Exam Manager, a current member of
// that unit, granted exam.view and exam_sitting.view there.
type realtimeGateFixture struct {
	access     *accesstest.Fixture
	admission  *RealtimeAdmission
	manager    model.Principal
	exam       *model.Exam
	sitting    *model.ExamSitting
	examStore  *realtimeExamStoreFake
	submission *store.ExamIntegrityReviewAuthorization
}

func newRealtimeGateFixture(t *testing.T, extraActions ...model.Action) realtimeGateFixture {
	t.Helper()
	unitID := model.NewAcademicUnitID()
	manager := testExamPrincipal(model.NewUserID())
	examRecord := &model.Exam{ID: model.NewExamID(), AcademicUnitID: unitID}
	sittingRecord := &model.ExamSitting{ID: model.NewExamSittingID(), ExamID: examRecord.ID}
	exams := &realtimeExamStoreFake{exam: examRecord, manager: manager.UserID}
	sittings := &realtimeSittingStoreFake{snapshot: &store.ExamSittingSnapshot{Sitting: sittingRecord, AcademicUnitID: unitID}}
	submission := &store.ExamIntegrityReviewAuthorization{
		SubmissionID: model.NewSubmissionID(), ExamID: examRecord.ID,
		SittingID: sittingRecord.ID, AttemptID: model.NewExamAttemptID(),
		CandidateUserID: model.NewUserID(), AcademicUnitID: unitID,
	}
	memberships := realtimeMembershipStoreFake{unitID: unitID, userID: manager.UserID}
	fixture := accesstest.New(t, accesstest.Options{
		Grants: []accesstest.Grant{{
			UserID: manager.UserID, Actions: append([]model.Action{model.ActionExamView, model.ActionExamSittingView}, extraActions...),
			ScopeType: model.RoleScopeAcademicUnit, ScopeID: unitID.String(),
		}},
		Exams: exams, Sittings: sittings, Submissions: realtimeSubmissionStoreFake{submission: submission},
	})
	publisher := &realtimetest.Publisher{}
	authoring, err := exam.NewAuthoring(exam.AuthoringDependencies{
		Persistence: exams, Memberships: memberships, Users: realtimeNoUsers{}, Sittings: sittings,
		Mail: realtimeUnusedManagerMail{}, Authorizer: fixture.Authorizer, Auditor: fixture.Auditor,
		Publisher: publisher, Now: time.Now, NewID: model.NewExamID,
	})
	require.NoError(t, err)
	// A subscription never schedules, so the schedule notices have nothing
	// to read; the composer is the production one over a disabled sender.
	sittingMail, err := newSittingMailPreparer(mailtest.NewRenderer(t), &mailSenderFake{}, nil, time.Now, nil)
	require.NoError(t, err)
	sittingService, err := sitting.New(sitting.Dependencies{
		Persistence: sittings, Access: exams, Memberships: memberships,
		Authorizer: fixture.Authorizer, Auditor: fixture.Auditor, Publisher: publisher,
		Jobs: jobs.NewExamSittingLifecycleJobFactory(time.Now, model.NewJobID),
		Mail: sitting.ScheduleMail{Composer: sittingMail, Revisions: realtimeNoRevisions{}, Classes: realtimeNoClasses{}}, Now: time.Now, NewID: model.NewExamSittingID,
	})
	require.NoError(t, err)
	return realtimeGateFixture{
		access: fixture,
		admission: NewRealtimeAdmission(RealtimeAdmissionDependencies{
			Exams: authoring, Sittings: sittingService, Authorizer: fixture.Authorizer,
			Reviews: review.SittingManagerAuthorizer{
				Reviews: realtimeReviewStoreFake{submission: submission}, Sittings: sittingService, Audit: fixture.Auditor,
			},
		}),
		manager:    manager,
		exam:       examRecord,
		sitting:    sittingRecord,
		examStore:  exams,
		submission: submission,
	}
}

// requireOnlyDecision requires that the one recorded decision is the given
// action on the resource.
func (f realtimeGateFixture) requireOnlyDecision(t *testing.T, action model.Action, resourceType model.ResourceType, id string, status model.AuditStatus) {
	t.Helper()
	decisions := f.access.Audits.Decisions()
	require.Len(t, decisions, 1, "recorded decisions")
	require.Equal(t, string(action), decisions[0].Action)
	require.Equal(t, model.Resource{Type: resourceType, ID: id}, decisions[0].Resource)
	require.Equal(t, status, decisions[0].Status)
}

// realtimeExamStoreFake holds one Exam; manager is its only Exam Manager.
type realtimeExamStoreFake struct {
	store.ExamAuthoringStore
	exam    *model.Exam
	manager model.UserID
}

func (s *realtimeExamStoreFake) Resolve(_ context.Context, id model.ExamID) (*model.Exam, error) {
	if id != s.exam.ID {
		return nil, store.NewErrNotFound("exam", id.String())
	}
	return s.exam, nil
}

func (s *realtimeExamStoreFake) Access(_ context.Context, id model.ExamID, userID model.UserID) (*store.ExamAccessSnapshot, error) {
	if id != s.exam.ID {
		return nil, store.NewErrNotFound("exam", id.String())
	}
	return &store.ExamAccessSnapshot{Exam: s.exam, ActorIsManager: userID == s.manager}, nil
}

type realtimeReviewStoreFake struct {
	store.ExamIntegrityReviewStore
	submission *store.ExamIntegrityReviewAuthorization
}

func (s realtimeReviewStoreFake) Resolve(_ context.Context, id model.SubmissionID) (*store.ExamIntegrityReviewAuthorization, error) {
	if id != s.submission.SubmissionID {
		return nil, store.NewErrNotFound("submission", id.String())
	}
	return s.submission, nil
}

type realtimeSubmissionStoreFake struct {
	store.ExamSubmissionStore
	submission *store.ExamIntegrityReviewAuthorization
}

func (s realtimeSubmissionStoreFake) Resolve(_ context.Context, id model.SubmissionID) (*store.ExamSubmissionAuthorization, error) {
	if id != s.submission.SubmissionID {
		return nil, store.NewErrNotFound("submission", id.String())
	}
	projection := store.ExamSubmissionAuthorization(*s.submission)
	return &projection, nil
}

type realtimeSittingStoreFake struct {
	store.ExamSittingStore
	snapshot *store.ExamSittingSnapshot
}

func (s *realtimeSittingStoreFake) Resolve(_ context.Context, id model.ExamSittingID) (*store.ExamSittingSnapshot, error) {
	if id != s.snapshot.Sitting.ID {
		return nil, store.NewErrNotFound("exam_sitting", id.String())
	}
	copied := *s.snapshot
	return &copied, nil
}

// realtimeMembershipStoreFake makes userID a current member of unitID.
type realtimeMembershipStoreFake struct {
	store.AcademicUnitMemberStore
	unitID model.AcademicUnitID
	userID model.UserID
}

func (s realtimeMembershipStoreFake) ListActiveByUser(_ context.Context, userID model.UserID, _ time.Time) ([]*model.AcademicUnitMember, error) {
	if userID != s.userID {
		return nil, nil
	}
	return []*model.AcademicUnitMember{{AcademicUnitID: s.unitID, UserID: userID}}, nil
}

type (
	realtimeNoUsers     struct{ store.UserStore }
	realtimeNoRevisions struct{ store.ExamRevisionStore }
	realtimeNoClasses   struct{ store.ClassStore }
)

// realtimeUnusedManagerMail is the manager mail port a subscription never
// reaches; it refuses so a test would notice if one did.
type realtimeUnusedManagerMail struct{}

func (realtimeUnusedManagerMail) PrepareManagerMail(exam.ManagerMailPreparation) (*store.ExamManagerMail, error) {
	return nil, errors.New("a realtime subscription prepares no manager mail")
}

func testExamPrincipal(userID model.UserID) model.Principal {
	return model.Principal{
		UserID: userID, SessionID: model.NewSessionID(), CredentialID: model.PrincipalCredentialID(model.NewID()),
		CredentialType: model.CredentialSessionAccess, AuthenticationMethod: "password",
		AuthenticationStrength: model.AuthenticationSingleFactor, ClientType: model.SessionClientWeb,
		AuthenticatedAt: time.Now().UTC(),
	}
}
