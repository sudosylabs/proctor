// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package review

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/sudosylabs/proctor/server/app/access"
	"github.com/sudosylabs/proctor/server/app/invocation"
	"github.com/sudosylabs/proctor/server/app/realtime"
	"github.com/sudosylabs/proctor/server/app/realtime/realtimetest"
	"github.com/sudosylabs/proctor/server/model"
	"github.com/sudosylabs/proctor/server/store"
)

type reviewFixture struct {
	service      *Service
	invocation   invocation.Invocation
	persistence  *reviewStoreFake
	authorizer   *reviewAuthorizerFake
	auditor      *reviewAuditorFake
	publisher    *realtimetest.Publisher
	mail         *reviewMailFake
	userID       model.UserID
	submissionID model.SubmissionID
	reviewID     model.SubmissionReviewID
	flagID       model.IntegrityFlagID
	attemptID    model.ExamAttemptID
	at           time.Time
}

func newReviewFixture(t *testing.T) *reviewFixture {
	t.Helper()
	f := &reviewFixture{userID: model.NewUserID(), submissionID: model.NewSubmissionID(), reviewID: model.NewSubmissionReviewID(),
		flagID: model.NewIntegrityFlagID(), attemptID: model.NewExamAttemptID(), at: time.Date(2026, 8, 15, 12, 0, 0, 0, time.UTC)}
	f.persistence = &reviewStoreFake{f: f}
	f.authorizer = &reviewAuthorizerFake{}
	f.auditor = &reviewAuditorFake{}
	f.publisher = &realtimetest.Publisher{}
	f.mail = &reviewMailFake{f: f}
	service, err := New(Dependencies{Persistence: f.persistence, Authorizer: f.authorizer, Auditor: f.auditor,
		Publisher: f.publisher, Mail: f.mail, Now: func() time.Time { return f.at },
		NewReviewID: model.NewSubmissionReviewID, NewDecisionID: model.NewIntegrityReviewDecisionID})
	require.NoError(t, err)
	f.service = service
	f.invocation = invocation.New(model.Principal{UserID: f.userID, SessionID: model.NewSessionID(), CredentialID: model.PrincipalCredentialID(model.NewID()),
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
	return &store.ExamIntegrityReviewAuthorization{SubmissionID: f.f.submissionID, ExamID: model.NewExamID(), SittingID: model.NewExamSittingID(), AttemptID: f.f.attemptID, CandidateUserID: f.f.userID, AcademicUnitID: model.NewAcademicUnitID()}, nil
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

type reviewAuthorizerFake struct {
	reviewSubmission model.SubmissionID
	reviewCalls      int
	releaseCalls     int
}

func (*reviewAuthorizerFake) AuthorizeSubmissionView(context.Context, invocation.Invocation, model.SubmissionID) error {
	return nil
}
func (f *reviewAuthorizerFake) AuthorizeSubmissionReview(_ context.Context, _ invocation.Invocation, id model.SubmissionID) (access.ManagerAuthorization, error) {
	f.reviewSubmission, f.reviewCalls = id, f.reviewCalls+1
	return access.ManagerAuthorization{}, nil
}
func (f *reviewAuthorizerFake) AuthorizeSubmissionRelease(_ context.Context, _ invocation.Invocation, _ model.SubmissionID) (access.ManagerAuthorization, error) {
	f.releaseCalls++
	return access.ManagerAuthorization{}, nil
}

type reviewAuditorFake struct {
	value      map[string]any
	begun      int
	failedCode string
}

func (f *reviewAuditorFake) Begin(_ context.Context, attempt access.Mutation) (model.AuditEventID, error) {
	f.value = attempt.Value
	f.begun++
	return model.NewAuditEventID(), nil
}
func (f *reviewAuditorFake) Fail(_ context.Context, _ model.AuditEventID, code string) error {
	f.failedCode = code
	return nil
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

type reviewMailFake struct {
	f       *reviewFixture
	request ResultReleaseMailPreparation
	calls   int
}

func (f *reviewMailFake) PrepareResultRelease(_ context.Context,
	request ResultReleaseMailPreparation,
) (*PreparedResultReleaseMail, error) {
	f.calls++
	f.request = request
	return &PreparedResultReleaseMail{Notice: &store.PreparedMail{Occurrence: &model.MailOccurrence{},
		Delivery: &model.MailDelivery{}, Job: &model.Job{}}, ExpectedRecipientRevision: 2}, nil
}

func (f *reviewStoreFake) ListNativeConditions(context.Context, store.NativeConditionListOptions) (*store.NativeConditionPage, error) {
	return &store.NativeConditionPage{Items: []model.NativeConditionEvidence{}}, nil
}
