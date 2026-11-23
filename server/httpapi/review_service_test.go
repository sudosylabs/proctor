// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package httpapi

import (
	"context"
	"net/http"
	"net/http/httptest"
	"slices"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/sudosylabs/proctor/server/app/exam/review"
	"github.com/sudosylabs/proctor/server/httpapi/route"
	"github.com/sudosylabs/proctor/server/httpapi/route/routetest"
	"github.com/sudosylabs/proctor/server/model"
	"github.com/sudosylabs/proctor/server/store"
)

// reviewFixture is the real review.Service the Integrity Review handlers
// call, over the Sitting fixture's stores plus a Review Store holding one
// Submission of the seeded Sitting with its draft Review, one Flag, its
// Evidence and one late Discrepancy. The caller manages the Exam and may
// review and release its Submissions; the candidate who submitted it signs
// in with the bearer token "candidate". A handler test asserts what the
// request turned into at the Store.
type reviewFixture struct {
	*sittingFixture
	candidate model.Principal

	attemptID   model.ExamAttemptID
	submission  *model.ExamSubmission
	review      *model.SubmissionReview
	decision    *model.IntegrityReviewDecision
	flag        *model.IntegrityFlag
	evidence    *model.IntegrityEvidence
	discrepancy *model.IntegrityDiscrepancy

	reviewStore *reviewLedger
	service     *review.Service
}

// reviewActions are sittingActions plus the Submission review and release
// actions the Review mutations ask about.
var reviewActions = append(slices.Clone(sittingActions),
	model.ActionSubmissionReview, model.ActionSubmissionReviewOverride,
	model.ActionSubmissionRelease, model.ActionSubmissionReleaseOverride,
)

// reviewFixtureTime is when the seeded Submission was submitted.
var reviewFixtureTime = time.Date(2026, 8, 15, 18, 0, 0, 0, time.UTC)

func newReviewFixture(t *testing.T) *reviewFixture {
	t.Helper()
	f := &reviewFixture{sittingFixture: newSittingFixtureWith(t, testExamHTTPPrincipal(), reviewActions...)}
	at := reviewFixtureTime
	f.candidate = testExamHTTPPrincipal()
	f.candidate.UserID = f.submissions.candidateID
	f.users.add(activeExamUser(f.candidate.UserID, "candidate"))
	f.attemptID = model.NewExamAttemptID()

	manifest, err := model.NewExamSubmissionManifest(0, nil)
	require.NoError(t, err)
	f.submission, err = model.NewExamSubmission(model.ExamSubmissionSpecification{ID: model.NewSubmissionID(), ReceiptCode: model.NewSubmissionReceiptCode(),
		AttemptID: f.attemptID, ExamRevisionID: f.sitting.ExamRevisionID, WorkspaceID: model.NewExamAttemptWorkspaceID(), Manifest: manifest,
		BrowserActivity: model.BrowserSubmissionSettlement{State: "not_applicable", InventoryRevision: 1},
		Provenance:      model.ExamSubmissionCandidateSubmitted, SubmittedAt: at})
	require.NoError(t, err)
	f.review, err = model.NewSubmissionReview(model.NewSubmissionReviewID(), f.submission.ID, f.caller.UserID, at)
	require.NoError(t, err)
	require.NoError(t, f.review.UpdateDraft(1, "Manager-only note.", "Approved **remarks**", at.Add(time.Second)))
	f.flag, err = model.NewIntegrityFlag(model.NewIntegrityFlagID(), f.attemptID, 1, model.IntegrityPolicyConnectionLoss, at)
	require.NoError(t, err)
	f.decision, err = model.NewIntegrityReviewDecision(model.NewIntegrityReviewDecisionID(), f.review.ID, f.flag.ID,
		model.IntegrityReviewInconclusive, f.caller.UserID, "Manager-only rationale.", at.Add(time.Second))
	require.NoError(t, err)
	f.evidence, err = model.NewConnectionLossEvidence(model.NewIntegrityEvidenceID(), f.attemptID,
		model.NewAttemptParticipationID(), f.flag.ID, 1, at.Add(-time.Second), at)
	require.NoError(t, err)
	f.discrepancy, err = model.NewIntegrityDiscrepancy(model.IntegrityDiscrepancySpecification{
		ID: model.NewIntegrityDiscrepancyID(), SubmissionID: f.submission.ID, AttemptID: f.attemptID,
		ParticipationID: model.NewAttemptParticipationID(), Generation: 1,
		Kind:     model.IntegrityDiscrepancyLateFocusLoss,
		SignalID: model.NewFocusLossSignalID(), Sequence: 2, DurationMilliseconds: 900,
		Source: model.FocusLossSourceWindowBlur, ReceivedAt: at.Add(time.Second)})
	require.NoError(t, err)
	f.reviewStore = &reviewLedger{f: f, evidencePage: store.ExamIntegrityEvidencePage{Items: []model.IntegrityEvidence{*f.evidence}},
		attemptEvidence: &store.AttemptEvidencePage{Items: []model.AttemptEvidence{}}}

	f.service, err = review.New(review.Dependencies{
		Persistence:  f.reviewStore,
		Environments: &reviewPreviewLedger{review: f.reviewStore},
		Authorizer:   review.SittingManagerAuthorizer{Reviews: f.reviewStore, Sittings: f.sittingService, Audit: f.access.Auditor},
		Auditor:      f.access.Auditor, Publisher: f.publisher,
		Mail: review.ResultReleaseMail{Composer: administrationComposer(t, &identityMailSender{}), Users: f.users,
			Sittings: f.sittingStore, Revisions: sittingMailRevisions{}},
		Now: time.Now, NewReviewID: model.NewSubmissionReviewID, NewDecisionID: model.NewIntegrityReviewDecisionID,
	})
	require.NoError(t, err)
	return f
}

func (f *reviewFixture) AuthenticateAccess(_ context.Context, token string) (*model.Principal, error) {
	principal := f.caller
	if token == "candidate" {
		principal = f.candidate
	}
	return &principal, nil
}

func (f *reviewFixture) AuthenticateBearer(ctx context.Context, token string) (*model.Principal, error) {
	return f.AuthenticateAccess(ctx, token)
}

func (f *reviewFixture) AuthenticateDPoP(ctx context.Context, token, _, _, _ string) (*model.Principal, error) {
	return f.AuthenticateAccess(ctx, token)
}

// api serves the Integrity Review resource over the fixture's service.
func (f *reviewFixture) api(t *testing.T) *route.API {
	t.Helper()
	logger, _ := routetest.NewLogger(t)
	resolver := testPeople(f.users, f.access.Authorizer)
	return newFocusedResourceAPI(t, logger, f, examIntegrityReviewResource(f.service, resolver),
		examLiveEvidenceResource(f.service, resolver))
}

// request is an authenticated request; the bearer token chooses the caller.
func (f *reviewFixture) request(t *testing.T, method, path, token string) *http.Request {
	request := httptest.NewRequestWithContext(t.Context(), method, path, nil)
	request.Header.Set("Authorization", "Bearer "+token)
	return request
}

// authorization is the Review authorization projection of the seeded
// Submission.
func (f *reviewFixture) authorization() store.ExamIntegrityReviewAuthorization {
	return store.ExamIntegrityReviewAuthorization{SubmissionID: f.submission.ID, ExamID: f.examID,
		SittingID: f.sitting.ID, AttemptID: f.attemptID, CandidateUserID: f.candidate.UserID, AcademicUnitID: f.unitID}
}

// reviewLedger holds the seeded Submission's Review and records every
// request that reached it.
type reviewLedger struct {
	store.ExamIntegrityReviewStore
	f *reviewFixture

	evidencePage    store.ExamIntegrityEvidencePage
	attemptEvidence *store.AttemptEvidencePage
	flagSummaries   []store.ExamIntegrityFlagSummary
	submissionLists []store.SubmissionListOptions
	liveTimelines   []store.LiveTimelineListOptions

	flagLists        []store.ExamIntegrityFlagListOptions
	evidenceLists    []store.ExamIntegrityEvidenceListOptions
	discrepancyLists []store.ExamIntegrityDiscrepancyListOptions
	attemptLists     []store.AttemptEvidenceListOptions
	decisions        []*store.ExamIntegrityReviewDecisionMutation
	commands         []*store.CommandIdempotency
	resultReads      []model.ExamAttemptID
	previewReads     []model.SubmissionID
	previewUsage     []model.PreviewUsage
}

func (s *reviewLedger) ResolveLiveEvidence(_ context.Context, attemptID model.ExamAttemptID) (*store.LiveEvidenceScope, error) {
	if attemptID != s.f.attemptID {
		return nil, store.NewErrNotFound("exam_attempt", attemptID.String())
	}
	return &store.LiveEvidenceScope{AttemptID: attemptID, ExamID: s.f.examID, SittingID: s.f.sitting.ID,
		CandidateUserID: s.f.candidate.UserID, AsOf: reviewFixtureTime}, nil
}

// ListLiveTimeline lists the seeded Flag as the timeline's one entry.
func (s *reviewLedger) ListLiveTimeline(_ context.Context, options store.LiveTimelineListOptions) (*store.LiveTimelinePage, error) {
	s.liveTimelines = append(s.liveTimelines, options)
	return &store.LiveTimelinePage{Items: []store.LiveTimelineItem{{Kind: store.LiveTimelineFlag, At: s.f.flag.CreatedAt,
		Flag: &store.ExamIntegrityFlagSummary{Flag: *s.f.flag, EvidenceCount: 1}}}}, nil
}

// ListSubmissions lists the seeded Submission with its draft review.
func (s *reviewLedger) ListSubmissions(_ context.Context, options store.SubmissionListOptions) (*store.SubmissionListPage, error) {
	s.submissionLists = append(s.submissionLists, options)
	authorization := s.f.authorization()
	item := store.SubmissionListItem{SubmissionID: s.f.submission.ID, ExamID: authorization.ExamID, ExamTitle: "Algorithms",
		SittingID: authorization.SittingID, AttemptID: s.f.attemptID,
		Candidate:   store.SittingCandidateIdentity{UserID: s.f.candidate.UserID, Username: "candidate", DisplayName: "Candidate"},
		SubmittedAt: s.f.submission.SubmittedAt, ReceiptCode: s.f.submission.ReceiptCode, FlagCount: 1,
		IntegrityState: model.SubmissionIntegritySettled, ReviewVisible: options.ReviewVisible || options.ReviewVisibility != nil}
	if item.ReviewVisible {
		item.Review = &store.SubmissionListReview{ID: s.f.review.ID, State: s.f.review.State,
			ReleaseState: s.f.review.ReleaseState, Revision: s.f.review.Revision}
	}
	page := &store.SubmissionListPage{Items: []store.SubmissionListItem{item}}
	if options.IncludeCounts {
		reviewed := int64(0)
		page.Counts = &store.SubmissionListCounts{Total: 1, Reviewed: &reviewed, Released: &reviewed, FlaggedSubmissions: 1, Flags: 1}
	}
	return page, nil
}

func (s *reviewLedger) Resolve(_ context.Context, id model.SubmissionID) (*store.ExamIntegrityReviewAuthorization, error) {
	if id != s.f.submission.ID {
		return nil, store.NewErrNotFound("submission", id.String())
	}
	authorization := s.f.authorization()
	return &authorization, nil
}

func (s *reviewLedger) Get(_ context.Context, id model.SubmissionID) (*store.ExamSubmissionReviewSnapshot, error) {
	if id != s.f.submission.ID {
		return nil, store.NewErrNotFound("submission", id.String())
	}
	return &store.ExamSubmissionReviewSnapshot{NativeConditions: model.NewNativeConditionInventory(),
		AttemptEvidence: model.NewAttemptEvidenceInventory(), Authorization: s.f.authorization(), Submission: s.f.submission,
		Review: s.f.review, Decisions: []model.IntegrityReviewDecision{*s.f.decision}}, nil
}

func (s *reviewLedger) ListFlags(_ context.Context, options store.ExamIntegrityFlagListOptions) (*store.ExamIntegrityFlagPage, error) {
	s.flagLists = append(s.flagLists, options)
	if s.flagSummaries != nil {
		return &store.ExamIntegrityFlagPage{Items: append([]store.ExamIntegrityFlagSummary(nil), s.flagSummaries...)}, nil
	}
	return &store.ExamIntegrityFlagPage{Items: []store.ExamIntegrityFlagSummary{{Flag: *s.f.flag, EvidenceCount: 1}}}, nil
}

func (s *reviewLedger) ListEvidence(_ context.Context, options store.ExamIntegrityEvidenceListOptions) (*store.ExamIntegrityEvidencePage, error) {
	s.evidenceLists = append(s.evidenceLists, options)
	page := s.evidencePage
	return &page, nil
}

func (s *reviewLedger) ListDiscrepancies(_ context.Context, options store.ExamIntegrityDiscrepancyListOptions) (*store.ExamIntegrityDiscrepancyPage, error) {
	s.discrepancyLists = append(s.discrepancyLists, options)
	return &store.ExamIntegrityDiscrepancyPage{Items: []model.IntegrityDiscrepancy{*s.f.discrepancy}}, nil
}

func (s *reviewLedger) ListNativeConditions(context.Context, store.NativeConditionListOptions) (*store.NativeConditionPage, error) {
	return &store.NativeConditionPage{Items: []model.NativeConditionEvidence{}}, nil
}

func (s *reviewLedger) ListAttemptEvidence(_ context.Context, options store.AttemptEvidenceListOptions) (*store.AttemptEvidencePage, error) {
	s.attemptLists = append(s.attemptLists, options)
	return s.attemptEvidence, nil
}

// SaveDecision commits the decision against the seeded Review, which the
// request must name at its current revision.
func (s *reviewLedger) SaveDecision(
	_ context.Context,
	input *store.ExamIntegrityReviewDecisionMutation,
	command *store.CommandIdempotency,
) (*store.ExamIntegrityReviewMutationResult, error) {
	s.decisions = append(s.decisions, input)
	s.commands = append(s.commands, command)
	if input.ReviewID != s.f.review.ID || input.ExpectedReviewRevision != s.f.review.Revision {
		return nil, store.NewErrConflict("submission_review", "revision", nil)
	}
	decision := *s.f.decision
	decision.ID, decision.Outcome, decision.PrivateRationale = input.DecisionID, input.Outcome, input.PrivateRationale
	return &store.ExamIntegrityReviewMutationResult{Authorization: s.f.authorization(), Review: s.f.review, Decision: &decision}, nil
}

// GetReleasedStudentResult answers the candidate's released result for the
// seeded Attempt.
func (s *reviewLedger) GetReleasedStudentResult(_ context.Context, attemptID model.ExamAttemptID, userID model.UserID) (*model.StudentResult, error) {
	s.resultReads = append(s.resultReads, attemptID)
	if attemptID != s.f.attemptID || userID != s.f.candidate.UserID {
		return nil, store.NewErrNotFound("student_result", attemptID.String())
	}
	return &model.StudentResult{ReviewID: s.f.review.ID, SubmissionID: s.f.submission.ID, ReceiptCode: s.f.submission.ReceiptCode, AttemptID: s.f.attemptID,
		CandidateUserID: s.f.candidate.UserID, StudentRemarksMarkdown: "Approved **remarks**", ReleasedAt: s.f.review.UpdatedAt}, nil
}

type reviewPreviewLedger struct {
	store.AttemptEnvironmentStore
	review *reviewLedger
}

func (s *reviewPreviewLedger) ListPreviewUsage(_ context.Context, id model.SubmissionID) ([]model.PreviewUsage, error) {
	s.review.previewReads = append(s.review.previewReads, id)
	return s.review.previewUsage, nil
}
