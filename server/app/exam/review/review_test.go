// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package review

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/sudosylabs/proctor/server/app/access/accesstest"
	"github.com/sudosylabs/proctor/server/app/exam/sitting"
	"github.com/sudosylabs/proctor/server/app/invocation"
	"github.com/sudosylabs/proctor/server/app/mail"
	"github.com/sudosylabs/proctor/server/app/mailtest"
	"github.com/sudosylabs/proctor/server/app/realtime"
	"github.com/sudosylabs/proctor/server/app/realtime/realtimetest"
	"github.com/sudosylabs/proctor/server/model"
	"github.com/sudosylabs/proctor/server/secretseal"
	"github.com/sudosylabs/proctor/server/store"
)

type reviewFixture struct {
	service     *Service
	inv         invocation.Invocation
	persistence *reviewStoreFake
	// control is the real authorizer and auditor. The caller manages the
	// Exam and holds the Submission actions at its Academic Unit, so the real
	// Sitting service decides for them as it does for a current manager.
	control   *accesstest.Fixture
	publisher *realtimetest.Publisher
	// renders records the result-release notices the real mail composer
	// rendered.
	renders *mailtest.Renders
	userID  model.UserID
	// candidateID made the Submission; the caller is another User, so the
	// real authorizer does not deny them as its candidate.
	candidateID  model.UserID
	submissionID model.SubmissionID
	reviewID     model.SubmissionReviewID
	flagID       model.IntegrityFlagID
	attemptID    model.ExamAttemptID
	examID       model.ExamID
	sittingID    model.ExamSittingID
	revisionID   model.ExamRevisionID
	unitID       model.AcademicUnitID
	at           time.Time
}

// reviewCandidateRevision is the candidate's User revision, which a prepared
// release notice is fenced on.
const reviewCandidateRevision = 2

func newReviewFixture(t *testing.T) *reviewFixture {
	t.Helper()
	f := &reviewFixture{userID: model.NewUserID(), candidateID: model.NewUserID(), submissionID: model.NewSubmissionID(), reviewID: model.NewSubmissionReviewID(),
		flagID: model.NewIntegrityFlagID(), attemptID: model.NewExamAttemptID(), at: time.Date(2026, 8, 15, 12, 0, 0, 0, time.UTC)}
	f.examID, f.sittingID, f.revisionID, f.unitID = model.NewExamID(), model.NewExamSittingID(), model.NewExamRevisionID(), model.NewAcademicUnitID()
	f.persistence = &reviewStoreFake{f: f}
	f.control = accesstest.New(t, accesstest.Options{
		Grants: []accesstest.Grant{{UserID: f.userID, ScopeType: model.RoleScopeAcademicUnit, ScopeID: f.unitID.String(),
			Actions: []model.Action{model.ActionSubmissionView, model.ActionSubmissionReview, model.ActionSubmissionRelease}}},
		Submissions: reviewSubmissionsFake{f: f},
		Now:         func() time.Time { return f.at },
	})
	f.publisher = &realtimetest.Publisher{}
	sittings := reviewSittingStoreFake{f: f}
	var renderer *mail.TemplateRenderer
	renderer, f.renders = mailtest.NewRecordingRenderer(t, nil)
	composer, err := mail.NewComposer(mail.ComposerDependencies{Renderer: renderer, Sender: &reviewMailSenderFake{enabled: true}, Sealer: reviewMailSealer(t)})
	require.NoError(t, err)
	scheduleComposer, err := mail.NewSittingComposer(mail.SittingComposerDependencies{Renderer: mailtest.NewRenderer(t),
		Sender: &reviewMailSenderFake{}, Now: func() time.Time { return f.at }})
	require.NoError(t, err)
	sittingService, err := sitting.New(sitting.Dependencies{
		Persistence: sittings, Access: reviewExamAccessFake{f: f}, Memberships: reviewMembershipsFake{f: f},
		Authorizer: f.control.Authorizer, Auditor: f.control.Auditor, Publisher: &realtimetest.Publisher{},
		Jobs: reviewLifecycleJobsFake{},
		Mail: sitting.ScheduleMail{Composer: scheduleComposer, Revisions: reviewRevisionsFake{f: f}, Classes: reviewClassesFake{}},
		Now:  func() time.Time { return f.at }, NewID: model.NewExamSittingID,
	})
	require.NoError(t, err)
	service, err := New(Dependencies{Persistence: f.persistence,
		Authorizer: SittingManagerAuthorizer{Reviews: f.persistence, Sittings: sittingService, Audit: f.control.Auditor},
		Auditor:    f.control.Auditor,
		Publisher:  f.publisher,
		Mail: ResultReleaseMail{Composer: composer, Users: reviewUsersFake{f: f}, Sittings: sittings,
			Revisions: reviewRevisionsFake{f: f}},
		Now:         func() time.Time { return f.at },
		NewReviewID: model.NewSubmissionReviewID, NewDecisionID: model.NewIntegrityReviewDecisionID})
	require.NoError(t, err)
	f.service = service
	f.inv = invocation.New(model.Principal{UserID: f.userID, SessionID: model.NewSessionID(), CredentialID: model.PrincipalCredentialID(model.NewID()),
		CredentialType: model.CredentialSessionAccess, AuthenticationMethod: "password", AuthenticationStrength: model.AuthenticationSingleFactor,
		ClientType: model.SessionClientWeb, AuthenticatedAt: f.at}, model.RequestMetadata{})
	return f
}

type reviewStoreFake struct {
	f           *reviewFixture
	decision    *store.ExamIntegrityReviewDecisionMutation
	replayed    bool
	mode        string
	student     *model.StudentResult
	release     *store.ExamIntegrityReviewRelease
	reopen      *store.ExamIntegrityReviewReopen
	reopenErr   error
	draft       *store.ExamIntegrityReviewDraftMutation
	idempotency *store.CommandIdempotency
	prepareErr  error
}

func (f *reviewStoreFake) PrepareRelease(ctx context.Context, input *store.ExamIntegrityReviewReleasePrepare) (*store.ExamIntegrityReviewReleasePreparation, error) {
	if f.prepareErr != nil {
		return nil, f.prepareErr
	}
	return &store.ExamIntegrityReviewReleasePreparation{Replayed: f.replayed, ReleaseAt: f.f.at.Add(time.Minute)}, nil
}

func (f *reviewStoreFake) Resolve(context.Context, model.SubmissionID) (*store.ExamIntegrityReviewAuthorization, error) {
	return &store.ExamIntegrityReviewAuthorization{SubmissionID: f.f.submissionID, ExamID: f.f.examID, SittingID: f.f.sittingID, AttemptID: f.f.attemptID, CandidateUserID: f.f.candidateID, AcademicUnitID: f.f.unitID}, nil
}
func (f *reviewStoreFake) Get(context.Context, model.SubmissionID) (*store.ExamSubmissionReviewSnapshot, error) {
	return nil, errors.New("unused")
}
func (f *reviewStoreFake) ListFlags(context.Context, store.ExamIntegrityFlagListOptions) (*store.ExamIntegrityFlagPage, error) {
	return nil, errors.New("unused")
}
func (f *reviewStoreFake) ListEvidence(context.Context, store.ExamIntegrityEvidenceListOptions) (*store.ExamIntegrityEvidencePage, error) {
	return nil, errors.New("unused")
}
func (f *reviewStoreFake) ListDiscrepancies(context.Context, store.ExamIntegrityDiscrepancyListOptions) (*store.ExamIntegrityDiscrepancyPage, error) {
	return nil, errors.New("unused")
}
func (f *reviewStoreFake) SaveDecision(_ context.Context, input *store.ExamIntegrityReviewDecisionMutation, command *store.CommandIdempotency) (*store.ExamIntegrityReviewMutationResult, error) {
	f.decision, f.idempotency = input, command
	review, _ := model.NewSubmissionReview(input.ReviewID, f.f.submissionID, f.f.userID, f.f.at)
	decision, _ := model.NewIntegrityReviewDecision(input.DecisionID, review.ID, f.f.flagID,
		model.IntegrityReviewConfirmed, f.f.userID, "The retained continuity evidence was verified.", f.f.at)
	return &store.ExamIntegrityReviewMutationResult{Authorization: *mustReviewAuthorization(f), Review: review, Decision: decision, Replayed: f.replayed}, nil
}
func (f *reviewStoreFake) UpdateDraft(_ context.Context, input *store.ExamIntegrityReviewDraftMutation, command *store.CommandIdempotency) (*store.ExamIntegrityReviewMutationResult, error) {
	f.draft, f.idempotency = input, command
	review, _ := model.NewSubmissionReview(f.f.reviewID, f.f.submissionID, f.f.userID, f.f.at.Add(-time.Minute))
	_ = review.UpdateDraft(input.ExpectedRevision, input.ManagerNotes, input.StudentRemarksMarkdown, f.f.at)
	return &store.ExamIntegrityReviewMutationResult{Authorization: *mustReviewAuthorization(f), Review: review}, nil
}
func (f *reviewStoreFake) Finalize(_ context.Context, _ *store.ExamIntegrityReviewFinalize, command *store.CommandIdempotency) (*store.ExamIntegrityReviewMutationResult, error) {
	if command != nil {
		f.idempotency = command
	}
	review, _ := model.NewSubmissionReview(f.f.reviewID, f.f.submissionID, f.f.userID, f.f.at.Add(-time.Minute))
	_ = review.UpdateDraft(1, "", "", f.f.at.Add(-30*time.Second))
	_ = review.Finalize(2, f.f.userID, 0, 0, 0, "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", f.f.at)
	return &store.ExamIntegrityReviewMutationResult{Authorization: *mustReviewAuthorization(f), Review: review}, nil
}
func (f *reviewStoreFake) Release(_ context.Context, input *store.ExamIntegrityReviewRelease, command *store.CommandIdempotency) (*store.ExamIntegrityReviewMutationResult, error) {
	f.release, f.idempotency = input, command
	result, _ := f.Finalize(context.Background(), nil, nil)
	_ = result.Review.Release(3, f.f.userID, f.f.at.Add(time.Minute))
	result.Replayed = f.replayed
	return result, nil
}
func (f *reviewStoreFake) GetReleasedStudentResult(_ context.Context, attemptID model.ExamAttemptID, candidateID model.UserID) (*model.StudentResult, error) {
	if f.student == nil || f.student.AttemptID != attemptID || f.student.CandidateUserID != candidateID {
		return nil, store.NewErrNotFound("student_result", attemptID.String())
	}
	clone := *f.student
	return &clone, nil
}

func mustReviewAuthorization(fake *reviewStoreFake) *store.ExamIntegrityReviewAuthorization {
	authorization, _ := fake.Resolve(context.Background(), fake.f.submissionID)
	return authorization
}

// reviewSubmissionsFake resolves the fixture's Submission to its Exam,
// Sitting and Academic Unit for the real authorizer.
type reviewSubmissionsFake struct {
	store.ExamSubmissionStore
	f *reviewFixture
}

func (s reviewSubmissionsFake) Resolve(_ context.Context, id model.SubmissionID) (*store.ExamSubmissionAuthorization, error) {
	if id != s.f.submissionID {
		return nil, store.NewErrNotFound("submission", id.String())
	}
	return &store.ExamSubmissionAuthorization{SubmissionID: id, ExamID: s.f.examID, SittingID: s.f.sittingID,
		AttemptID: s.f.attemptID, CandidateUserID: s.f.candidateID, AcademicUnitID: s.f.unitID}, nil
}

// reviewExamAccessFake reports the fixture's Exam in its Academic Unit, which
// the caller manages and nobody else does.
type reviewExamAccessFake struct {
	store.ExamAuthoringStore
	f *reviewFixture
}

func (s reviewExamAccessFake) Access(_ context.Context, examID model.ExamID, userID model.UserID) (*store.ExamAccessSnapshot, error) {
	if examID != s.f.examID {
		return nil, store.NewErrNotFound("exam", examID.String())
	}
	return &store.ExamAccessSnapshot{Exam: &model.Exam{ID: examID, AcademicUnitID: s.f.unitID}, ActorIsManager: userID == s.f.userID}, nil
}

// reviewMembershipsFake makes the caller a current member of the Exam's
// Academic Unit, so a manager decision rests on the ordinary Action.
type reviewMembershipsFake struct {
	store.AcademicUnitMemberStore
	f *reviewFixture
}

func (s reviewMembershipsFake) ListActiveByUser(_ context.Context, userID model.UserID, _ time.Time) ([]*model.AcademicUnitMember, error) {
	if userID != s.f.userID {
		return nil, nil
	}
	return []*model.AcademicUnitMember{{AcademicUnitID: s.f.unitID, UserID: userID}}, nil
}

// reviewSittingStoreFake is the fixture's Sitting, read by the result-release
// notice for its published revision.
type reviewSittingStoreFake struct {
	store.ExamSittingStore
	f *reviewFixture
}

func (s reviewSittingStoreFake) Get(_ context.Context, examID model.ExamID, sittingID model.ExamSittingID) (*store.ExamSittingSnapshot, error) {
	if examID != s.f.examID || sittingID != s.f.sittingID {
		return nil, store.NewErrNotFound("exam_sitting", sittingID.String())
	}
	return &store.ExamSittingSnapshot{Sitting: &model.ExamSitting{ID: sittingID, ExamID: examID, ExamRevisionID: s.f.revisionID},
		AcademicUnitID: s.f.unitID}, nil
}

// reviewRevisionsFake publishes the fixture's Exam revision under
// reviewExamTitle.
type reviewRevisionsFake struct {
	store.ExamRevisionStore
	f *reviewFixture
}

const reviewExamTitle = "Algorithms final"

func (s reviewRevisionsFake) GetSummary(_ context.Context, examID model.ExamID, revisionID model.ExamRevisionID) (*store.ExamRevisionSummary, error) {
	if examID != s.f.examID || revisionID != s.f.revisionID {
		return nil, store.NewErrNotFound("exam_revision", revisionID.String())
	}
	return &store.ExamRevisionSummary{ID: revisionID, ExamID: examID, Title: reviewExamTitle}, nil
}

// reviewUsersFake holds the candidate the release notice is addressed to.
type reviewUsersFake struct {
	store.UserStore
	f *reviewFixture
}

func (s reviewUsersFake) Get(_ context.Context, id model.UserID) (*model.User, error) {
	if id != s.f.candidateID {
		return nil, store.NewErrNotFound("user", id.String())
	}
	user := &model.User{Username: "candidate", Email: "candidate@example.edu", DisplayName: "Candidate", EmailVerified: true, Locale: "en", Timezone: "UTC"}
	user.PrepareCreate(id, time.UnixMilli(1))
	user.Revision = reviewCandidateRevision
	return user, nil
}

// reviewClassesFake and reviewLifecycleJobsFake complete the Sitting service;
// no Review use case schedules a Sitting.
type reviewClassesFake struct{ store.ClassStore }

type reviewLifecycleJobsFake struct{ sitting.LifecycleJobFactory }

// reviewMailSenderFake is the mail transport port.
type reviewMailSenderFake struct{ enabled bool }

func (s *reviewMailSenderFake) Enabled() bool { return s.enabled }
func (*reviewMailSenderFake) From() mail.Address {
	return mail.Address{Name: "Proctor", Address: "no-reply@example.test"}
}
func (*reviewMailSenderFake) Send(context.Context, mail.Outbound) (mail.TransportOutcome, error) {
	return mail.TransportUnknown, errors.New("review tests send no mail")
}

func reviewMailSealer(t *testing.T) *secretseal.Sealer {
	t.Helper()
	key := base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{7}, 32))
	sealer, err := secretseal.New(secretseal.Settings{EncryptionKey: key, MaximumPlaintext: secretseal.MaximumPlaintextBytes})
	require.NoError(t, err)
	return sealer
}

// decisions are the recorded authorization decisions for action, as
// "<status>" in order.
func (f *reviewFixture) decisions(action model.Action) []string {
	var statuses []string
	for _, decision := range f.control.Audits.Decisions() {
		if decision.Action == string(action) {
			statuses = append(statuses, string(decision.Status))
		}
	}
	return statuses
}

// auditedValue is the value of the one mutation attempt the real auditor
// kept, as it was stored.
func (f *reviewFixture) auditedValue(t *testing.T) map[string]any {
	t.Helper()
	attempts := f.control.Audits.Attempts()
	require.Len(t, attempts, 1, "mutation attempts")
	var parameters struct {
		Value map[string]any `json:"value"`
	}
	require.NoError(t, json.Unmarshal(attempts[0].Parameters, &parameters))
	return parameters.Value
}

// reviewFact is the bounded fact a Review event carries for result.
func reviewFact(result Result) realtime.ExamIntegrityReviewEventFact {
	return realtime.ExamIntegrityReviewEventFact{
		SubmissionID: result.Authorization.SubmissionID,
		AttemptID:    result.Authorization.AttemptID,
		CandidateID:  result.Authorization.CandidateUserID,
		ReviewID:     result.Review.ID,
		State:        result.Review.State,
		Revision:     result.Review.Revision,
		ReleaseState: result.Review.ReleaseState,
		ChangedAt:    result.Review.UpdatedAt,
	}
}

// reviewCalls are the calls of a Review change that is announced to managers
// with one event built by newEvent, followed by the Sitting board
// invalidation.
func reviewCalls(
	t *testing.T,
	newEvent func(realtime.ExamIntegrityReviewEventFact) (realtime.Event, error),
	result Result,
) []realtimetest.Call {
	t.Helper()
	event, err := newEvent(reviewFact(result))
	require.NoError(t, err)
	board, err := realtime.NewManagerSittingBoardChangedEvent(result.Authorization.ExamID, result.Authorization.SittingID)
	require.NoError(t, err)
	return []realtimetest.Call{
		{Method: realtimetest.Publish, Event: event},
		{Method: realtimetest.Publish, Event: board},
	}
}

func (f *reviewStoreFake) ListNativeConditions(context.Context, store.NativeConditionListOptions) (*store.NativeConditionPage, error) {
	return &store.NativeConditionPage{Items: []model.NativeConditionEvidence{}}, nil
}

func (f *reviewStoreFake) ListAttemptEvidence(context.Context, store.AttemptEvidenceListOptions) (*store.AttemptEvidencePage, error) {
	return &store.AttemptEvidencePage{Items: []model.AttemptEvidence{}}, nil
}
