// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package attempt

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/sudosylabs/proctor/server/app/access/accesstest"
	"github.com/sudosylabs/proctor/server/app/apperror"
	"github.com/sudosylabs/proctor/server/app/exam/sitting"
	"github.com/sudosylabs/proctor/server/app/invocation"
	"github.com/sudosylabs/proctor/server/app/mail"
	"github.com/sudosylabs/proctor/server/app/realtime/realtimetest"
	"github.com/sudosylabs/proctor/server/model"
	"github.com/sudosylabs/proctor/server/store"
)

// sittingManagers is the world the Sitting area decides Exam Manager
// questions in: one Exam in one Academic Unit, its Sitting, the Users in an
// Exam Manager relationship with it and the Users currently members of its
// unit. service builds the real *sitting.Service over it.
type sittingManagers struct {
	exam     func() *model.Exam
	sitting  func() *model.ExamSitting
	managers map[model.UserID]bool
	members  map[model.UserID]bool
}

// managerGrant gives a User every Sitting and Submission permission the
// Attempt use cases ask, and their overrides, in one Academic Unit.
func managerGrant(userID model.UserID, unitID model.AcademicUnitID) accesstest.Grant {
	return accesstest.Grant{UserID: userID, ScopeType: model.RoleScopeAcademicUnit, ScopeID: unitID.String(), Actions: []model.Action{
		model.ActionExamSittingView, model.ActionExamSittingViewOverride,
		model.ActionExamSittingManage, model.ActionExamSittingManageOverride,
		model.ActionExamAttemptBrowserActivityView,
		model.ActionSubmissionView, model.ActionSubmissionViewOverride,
		model.ActionSubmissionReview, model.ActionSubmissionReviewOverride,
	}}
}

func (w *sittingManagers) exams() store.ExamAuthoringStore     { return managerExamStore{w: w} }
func (w *sittingManagers) sittings() store.ExamSittingStore    { return managerSittingStore{w: w} }
func (w *sittingManagers) memberships() managerMembershipStore { return managerMembershipStore{w: w} }

// service is the production Sitting service deciding with the fixture's
// real authorizer and auditor. Its lifecycle Jobs and schedule mail are
// never reached by a manager decision.
func (w *sittingManagers) service(t testing.TB, control *accesstest.Fixture, now func() time.Time) *sitting.Service {
	t.Helper()
	service, err := sitting.New(sitting.Dependencies{
		Persistence: w.sittings(), Access: w.exams(), Memberships: w.memberships(),
		Authorizer: control.Authorizer, Auditor: control.Auditor, Publisher: &realtimetest.Publisher{},
		Jobs: unusedLifecycleJobs{},
		Mail: sitting.ScheduleMail{Composer: &mail.SittingComposer{}, Revisions: unusedRevisions{}, Classes: unusedClasses{}},
		Now:  now, NewID: model.NewExamSittingID,
	})
	require.NoError(t, err)
	return service
}

type managerExamStore struct {
	store.ExamAuthoringStore
	w *sittingManagers
}

func (s managerExamStore) Access(_ context.Context, examID model.ExamID, userID model.UserID) (*store.ExamAccessSnapshot, error) {
	exam := s.w.exam()
	if exam.ID != examID {
		return nil, store.NewErrNotFound("exam", examID.String())
	}
	return &store.ExamAccessSnapshot{Exam: exam, ActorIsManager: s.w.managers[userID]}, nil
}

func (s managerExamStore) Resolve(_ context.Context, examID model.ExamID) (*model.Exam, error) {
	exam := s.w.exam()
	if exam.ID != examID {
		return nil, store.NewErrNotFound("exam", examID.String())
	}
	return exam, nil
}

type managerSittingStore struct {
	store.ExamSittingStore
	w *sittingManagers
}

func (s managerSittingStore) Resolve(_ context.Context, sittingID model.ExamSittingID) (*store.ExamSittingSnapshot, error) {
	value := s.w.sitting()
	if value == nil || value.ID != sittingID {
		return nil, store.NewErrNotFound("exam_sitting", sittingID.String())
	}
	return &store.ExamSittingSnapshot{Sitting: value}, nil
}

type managerMembershipStore struct {
	store.AcademicUnitMemberStore
	w *sittingManagers
}

func (s managerMembershipStore) ListActiveByUser(_ context.Context, userID model.UserID, _ time.Time) ([]*model.AcademicUnitMember, error) {
	if !s.w.members[userID] {
		return nil, nil
	}
	return []*model.AcademicUnitMember{{UserID: userID, AcademicUnitID: s.w.exam().AcademicUnitID}}, nil
}

// quietSubmissions lets the authorizer resolve a Submission's unit from the
// test's own Submission store without entering the call order, which
// records only what the Attempt use case itself reads.
type quietSubmissions struct {
	store.ExamSubmissionStore
	submissions *submissionStoreFake
}

func (q quietSubmissions) Resolve(context.Context, model.SubmissionID) (*store.ExamSubmissionAuthorization, error) {
	return q.submissions.authorization, q.submissions.err
}

type unusedLifecycleJobs struct{}

func (unusedLifecycleJobs) BoundaryJobs(model.ExamSittingID, int64, time.Time, time.Time) (*model.Job, *model.Job, error) {
	return nil, nil, errors.New("lifecycle Jobs are not part of a manager decision")
}

func (unusedLifecycleJobs) DeadlineJob(model.ExamSittingID, int64, time.Time) (*model.Job, error) {
	return nil, errors.New("lifecycle Jobs are not part of a manager decision")
}

func (unusedLifecycleJobs) FinalizeJob(model.ExamSittingID, int64, time.Time) (*model.Job, error) {
	return nil, errors.New("lifecycle Jobs are not part of a manager decision")
}

type unusedRevisions struct{ store.ExamRevisionStore }

type unusedClasses struct{ store.ClassStore }

// standaloneManagers is a sittingManagers world outside the Attempt
// fixture: managerID manages the Exam from its unit, reviewerID may only
// review its Submissions, and submissions answer both the authorizer and the
// adapter.
type standaloneManagers struct {
	world      *sittingManagers
	control    *accesstest.Fixture
	service    *sitting.Service
	exam       *model.Exam
	sitting    *model.ExamSitting
	managerID  model.UserID
	reviewerID model.UserID
}

func newStandaloneManagers(t *testing.T, submissions store.ExamSubmissionStore) *standaloneManagers {
	t.Helper()
	m := &standaloneManagers{managerID: model.NewUserID(), reviewerID: model.NewUserID()}
	m.exam = &model.Exam{ID: model.NewExamID(), AcademicUnitID: model.NewAcademicUnitID()}
	m.sitting = &model.ExamSitting{ID: model.NewExamSittingID(), ExamID: m.exam.ID, ClassID: model.NewClassID()}
	m.world = &sittingManagers{
		exam:     func() *model.Exam { return m.exam },
		sitting:  func() *model.ExamSitting { return m.sitting },
		managers: map[model.UserID]bool{m.managerID: true, m.reviewerID: true},
		members:  map[model.UserID]bool{m.managerID: true, m.reviewerID: true},
	}
	m.control = accesstest.New(t, accesstest.Options{
		Grants: []accesstest.Grant{
			managerGrant(m.managerID, m.exam.AcademicUnitID),
			{UserID: m.reviewerID, Actions: []model.Action{model.ActionSubmissionReview}},
		},
		Exams: m.world.exams(), Sittings: m.world.sittings(), Submissions: submissions,
	})
	m.service = m.world.service(t, m.control, time.Now)
	return m
}

// call is an invocation by userID.
func (m *standaloneManagers) call(userID model.UserID, requestID string) invocation.Invocation {
	principal := examAttemptPrincipal()
	principal.UserID = userID
	return invocation.New(principal, model.RequestMetadata{RequestID: requestID})
}

// decided reports the decisions recorded as "<action>:<status>:<resource ID>".
func (m *standaloneManagers) decided() []string {
	var decided []string
	for _, decision := range m.control.Audits.Decisions() {
		decided = append(decided, decision.Action+":"+string(decision.Status)+":"+decision.Resource.ID)
	}
	return decided
}

func TestExamAttemptSubmissionAuthorizationResolvesCanonicalExamBeforeCurrentManagerCheck(t *testing.T) {
	t.Parallel()

	submissionID := model.NewSubmissionID()
	submissions := &submissionAuthorizationStoreFake{}
	managers := newStandaloneManagers(t, submissions)
	submissions.authorization = &store.ExamSubmissionAuthorization{
		SubmissionID: submissionID, ExamID: managers.exam.ID, SittingID: managers.sitting.ID,
		AttemptID: model.NewExamAttemptID(), CandidateUserID: model.NewUserID(), AcademicUnitID: managers.exam.AcademicUnitID,
	}
	adapter := SittingManagerAuthorizer{Sittings: managers.service, Audit: managers.control.Auditor, Submissions: submissions}
	call := managers.call(managers.managerID, "submission-view")
	// The caller names only the Submission; the Sitting area decides against
	// the Exam the canonical projection names, which it reads the manager
	// relationship of.
	require.NoError(t, adapter.AuthorizeSubmissionView(context.Background(), call, submissionID))
	require.Equal(t, []string{string(model.ActionSubmissionView) + ":success:" + submissionID.String()}, managers.decided())
	require.Equal(t, managers.managerID, managers.control.Audits.Decisions()[0].ActorID, "the decision is the caller's")
}

func TestExamAttemptSubmissionAuthorizationAuditsSelfDenialFailClosed(t *testing.T) {
	t.Parallel()
	principal := examAttemptPrincipal()
	submissionID, unitID := model.NewSubmissionID(), model.NewAcademicUnitID()
	authorization := &store.ExamSubmissionAuthorization{
		SubmissionID: submissionID, ExamID: model.NewExamID(), SittingID: model.NewExamSittingID(),
		AttemptID: model.NewExamAttemptID(), CandidateUserID: principal.UserID, AcademicUnitID: unitID,
	}
	metadata := model.RequestMetadata{RequestID: "self-submission-view"}
	t.Run("records denial before concealment", func(t *testing.T) {
		t.Parallel()
		submissions := &submissionAuthorizationStoreFake{authorization: authorization}
		managers := newStandaloneManagers(t, submissions)
		adapter := SittingManagerAuthorizer{Sittings: managers.service, Submissions: submissions, Audit: managers.control.Auditor}
		err := adapter.AuthorizeSubmissionView(context.Background(), invocation.New(principal, metadata), submissionID)
		var fault *apperror.Error
		require.ErrorAs(t, err, &fault, "AuthorizeSubmissionView() error")
		require.Equal(t, "exam.attempt.not_found", fault.Code(), "AuthorizeSubmissionView() error")
		decisions := managers.control.Audits.Decisions()
		require.Len(t, decisions, 1, "recorded decisions")
		require.Equal(t, principal.UserID, decisions[0].ActorID)
		require.Equal(t, string(model.ActionSubmissionView), decisions[0].Action)
		require.Equal(t, model.Resource{Type: model.ResourceSubmission, ID: submissionID.String()}, decisions[0].Resource)
		require.Equal(t, model.RoleScopeAcademicUnit, decisions[0].ScopeType)
		require.Equal(t, unitID.String(), decisions[0].ScopeID)
		require.Equal(t, metadata.RequestID, decisions[0].RequestID)
		require.Equal(t, model.AuditStatusFail, decisions[0].Status, "the decision is a denial")
	})
	t.Run("audit failure fails closed", func(t *testing.T) {
		t.Parallel()
		auditErr := errors.New("audit unavailable")
		submissions := &submissionAuthorizationStoreFake{authorization: authorization}
		managers := newStandaloneManagers(t, submissions)
		managers.control.Audits.FailDecisions(auditErr)
		adapter := SittingManagerAuthorizer{Sittings: managers.service, Submissions: submissions, Audit: managers.control.Auditor}
		require.ErrorIs(t, adapter.AuthorizeSubmissionView(context.Background(), invocation.New(principal, metadata), submissionID), auditErr, "AuthorizeSubmissionView() error")
	})
	t.Run("projection of another Submission is refused before self-denial", func(t *testing.T) {
		t.Parallel()
		other := *authorization
		other.SubmissionID = model.NewSubmissionID()
		submissions := &submissionAuthorizationStoreFake{authorization: &other}
		managers := newStandaloneManagers(t, submissions)
		adapter := SittingManagerAuthorizer{Sittings: managers.service, Submissions: submissions, Audit: managers.control.Auditor}
		err := adapter.AuthorizeSubmissionView(context.Background(), invocation.New(principal, metadata), submissionID)
		var fault *apperror.Error
		require.ErrorAs(t, err, &fault, "AuthorizeSubmissionView() error")
		require.Equal(t, "exam.attempt.unavailable", fault.Code(), "AuthorizeSubmissionView() error")
		// No self-denial and no Manager decision was recorded on another
		// Submission's projection.
		require.Empty(t, managers.control.Audits.Decisions(), "a decision was recorded on another Submission's projection")
	})
}

type submissionAuthorizationStoreFake struct {
	store.ExamSubmissionStore
	authorization *store.ExamSubmissionAuthorization
	err           error
}

func (f *submissionAuthorizationStoreFake) ResolveAttempt(context.Context, model.ExamAttemptID) (*store.ExamSubmissionAuthorization, error) {
	return f.authorization, f.err
}

func (f *submissionAuthorizationStoreFake) Resolve(context.Context, model.SubmissionID) (*store.ExamSubmissionAuthorization, error) {
	return f.authorization, nil
}

func examAttemptPrincipal() model.Principal {
	return model.Principal{UserID: model.NewUserID(), SessionID: model.NewSessionID(),
		CredentialID: model.PrincipalCredentialID(model.NewID()), CredentialType: model.CredentialSessionAccess,
		AuthenticationMethod: "password", AuthenticationStrength: model.AuthenticationSingleFactor,
		ClientType: model.SessionClientDesktop, AuthenticatedAt: time.Date(2026, time.August, 17, 9, 0, 0, 0, time.UTC)}
}

func TestScreenClipsAreForTheSittingsManagersAndTheSubmissionsReviewers(t *testing.T) {
	t.Parallel()
	attemptID := model.NewExamAttemptID()
	submissions := &submissionAuthorizationStoreFake{}
	managers := newStandaloneManagers(t, submissions)
	sittingID, unitID := managers.sitting.ID, managers.exam.AcademicUnitID
	submission := &store.ExamSubmissionAuthorization{SubmissionID: model.NewSubmissionID(), ExamID: managers.exam.ID,
		SittingID: sittingID, AttemptID: attemptID, CandidateUserID: model.NewUserID(), AcademicUnitID: unitID}
	submissions.authorization = submission
	adapter := SittingManagerAuthorizer{Sittings: managers.service, Submissions: submissions}

	viewer, err := adapter.AuthorizeScreenClipView(context.Background(), managers.call(managers.managerID, "clip-view"), sittingID, attemptID)
	require.NoError(t, err)
	require.Equal(t, model.ActionExamSittingManage, viewer.Action)
	require.Equal(t, model.RoleScopeClass, viewer.ScopeType)

	before := len(managers.control.Audits.Decisions())
	viewer, err = adapter.AuthorizeScreenClipView(context.Background(), managers.call(managers.reviewerID, "clip-view"), sittingID, attemptID)
	require.NoError(t, err, "a reviewer of the Submission sees its clips")
	require.Equal(t, model.ActionSubmissionReview, viewer.Action)
	require.Equal(t, model.Resource{Type: model.ResourceSubmission, ID: submission.SubmissionID.String()}, viewer.Resource)
	require.Equal(t, unitID.String(), viewer.ScopeID)
	require.Equal(t, []string{
		string(model.ActionExamSittingManage) + ":fail:" + sittingID.String(),
		string(model.ActionSubmissionReview) + ":success:" + submission.SubmissionID.String(),
	}, managers.decided()[before:], "the reviewer is refused the Sitting, then allowed the Submission")

	outsider := model.NewUserID()
	for name, test := range map[string]struct {
		caller        model.UserID
		authorization *store.ExamSubmissionAuthorization
		err           error
	}{
		"not submitted":  {caller: managers.reviewerID, err: store.NewErrNotFound("exam_submission", attemptID.String())},
		"not a reviewer": {caller: outsider, authorization: submission},
		"another Sitting's Submission": {caller: managers.reviewerID, authorization: &store.ExamSubmissionAuthorization{
			SubmissionID: submission.SubmissionID, SittingID: model.NewExamSittingID(), AttemptID: attemptID}},
	} {
		submissions.authorization, submissions.err = test.authorization, test.err
		_, err = adapter.AuthorizeScreenClipView(context.Background(), managers.call(test.caller, "clip-view"), sittingID, attemptID)
		require.True(t, apperror.Is(err, "authorization.denied"), "%s: error = %v", name, err)
	}

	own := *submission
	own.CandidateUserID = managers.reviewerID
	submissions.authorization, submissions.err = &own, nil
	before = len(managers.control.Audits.Decisions())
	_, err = adapter.AuthorizeScreenClipView(context.Background(), managers.call(managers.reviewerID, "clip-view"), sittingID, attemptID)
	require.True(t, apperror.Is(err, "authorization.denied"), "the candidate never sees their own clips: %v", err)
	require.Equal(t, []string{string(model.ActionExamSittingManage) + ":fail:" + sittingID.String()}, managers.decided()[before:],
		"the candidate is refused before any review decision")
}
