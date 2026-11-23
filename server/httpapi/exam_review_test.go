// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package httpapi

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/sudosylabs/proctor/server/model"
	"github.com/sudosylabs/proctor/server/store"
)

func TestIntegrityReviewHTTPMapsStrictDecisionAndPrivateManagerProjection(t *testing.T) {
	t.Parallel()
	f := newReviewFixture(t)
	httpAPI := f.api(t)
	path := "/api/v1/submissions/" + f.submission.ID.String() + "/review/decisions/" + f.flag.ID.String()
	body := `{"submission_review_id":"` + f.review.ID.String() + `","expected_review_revision":2,"expected_revision":1,"outcome":"inconclusive","private_rationale":"Manager-only rationale."}`
	request := httptest.NewRequestWithContext(t.Context(), http.MethodPut, path, strings.NewReader(body))
	request.Header.Set("Authorization", "Bearer credential")
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Idempotency-Key", "decision-once")
	response := httptest.NewRecorder()
	httpAPI.ServeHTTP(response, request)
	require.Equal(t, http.StatusOK, response.Code, "response=%d headers=%v body=%s", response.Code, response.Header(), response.Body.String())
	require.Equal(t, "no-store", response.Header().Get("Cache-Control"), "response=%d headers=%v body=%s", response.Code, response.Header(), response.Body.String())
	require.Len(t, f.reviewStore.decisions, 1, "the decision reached the Store once")
	decision := f.reviewStore.decisions[0]
	require.Equal(t, f.submission.ID, decision.SubmissionID, "decision=%#v", decision)
	require.Equal(t, f.review.ID, decision.ReviewID, "decision=%#v", decision)
	require.Equal(t, f.flag.ID, decision.FlagID, "decision=%#v", decision)
	require.Equal(t, int64(2), decision.ExpectedReviewRevision, "decision=%#v", decision)
	require.Equal(t, int64(1), decision.ExpectedRevision, "decision=%#v", decision)
	require.Equal(t, model.IntegrityReviewInconclusive, decision.Outcome, "decision=%#v", decision)
	require.Equal(t, "Manager-only rationale.", decision.PrivateRationale, "decision=%#v", decision)
	require.Equal(t, f.caller.UserID, decision.ActorUserID, "decision=%#v", decision)
	require.Equal(t, sha256.Sum256([]byte("decision-once")), f.reviewStore.commands[0].KeyDigest, "the idempotency key reached the Store")
	attempts := f.access.Audits.Attempts()
	require.Len(t, attempts, 1, "the decision was audited")
	require.Equal(t, attempts[0].ID, decision.AuditEventID, "the Store completes the decision's audit")
	require.Contains(t, response.Body.String(), `"private_rationale":"Manager-only rationale."`)

	duplicate := httptest.NewRequestWithContext(t.Context(), http.MethodPut, path,
		strings.NewReader(`{"expected_review_revision":2,"expected_review_revision":3,"expected_revision":1,"outcome":"confirmed","private_rationale":"x"}`))
	duplicate.Header.Set("Authorization", "Bearer credential")
	duplicate.Header.Set("Content-Type", "application/json")
	duplicate.Header.Set("Idempotency-Key", "duplicate")
	response = httptest.NewRecorder()
	httpAPI.ServeHTTP(response, duplicate)
	require.Equal(t, http.StatusBadRequest, response.Code, "duplicate JSON response=%d body=%s", response.Code, response.Body.String())
	require.Len(t, f.reviewStore.decisions, 1, "duplicate JSON reaches no Store write")
}

func TestIntegrityReviewHTTPUsesBoundedOpaqueEvidenceCursor(t *testing.T) {
	t.Parallel()
	f := newReviewFixture(t)
	f.reviewStore.evidencePage.HasMore = true
	httpAPI := f.api(t)
	path := "/api/v1/submissions/" + f.submission.ID.String() + "/integrity-flags/" + f.flag.ID.String() + "/evidence?limit=1"
	response := httptest.NewRecorder()
	httpAPI.ServeHTTP(response, f.request(t, http.MethodGet, path, "credential"))
	require.Equal(t, http.StatusOK, response.Code, "response=%d body=%s", response.Code, response.Body.String())
	require.Len(t, f.reviewStore.evidenceLists, 1, "the listing reached the Store")
	listed := f.reviewStore.evidenceLists[0]
	require.Equal(t, 1, listed.Limit, "listed=%#v", listed)
	require.Equal(t, f.flag.ID, listed.FlagID, "listed=%#v", listed)
	require.Equal(t, f.submission.ID, listed.SubmissionID, "listed=%#v", listed)
	var page examIntegrityEvidenceListResponse
	require.NoError(t, json.Unmarshal(response.Body.Bytes(), &page), "page=%#v error", page)
	require.Len(t, page.Items, 1, "page=%#v error", page)
	require.NotEmpty(t, page.NextCursor, "page=%#v error", page)
	id, err := decodeExamIntegrityReviewCursor(page.NextCursor, "evidence")
	require.NoError(t, err, "cursor=%q id=%q error", page.NextCursor, id)
	require.Equal(t, f.evidence.ID.String(), id, "cursor=%q id=%q error", page.NextCursor, id)
	unknown := base64.RawURLEncoding.EncodeToString([]byte(`{"kind":"evidence","id":"` + f.evidence.ID.String() + `","path":"private"}`))
	_, err = decodeExamIntegrityReviewCursor(unknown, "evidence")
	require.Error(t, err, "cursor accepted an unknown private field")
}

func TestIntegrityFlagsCarryALocalizedSummaryWithoutApplicationNames(t *testing.T) {
	t.Parallel()
	f := newReviewFixture(t)
	flag := func(kind model.IntegrityPolicyKind) model.IntegrityFlag {
		value := *f.flag
		value.ID, value.Kind = model.NewIntegrityFlagID(), kind
		return value
	}
	f.reviewStore.flagSummaries = []store.ExamIntegrityFlagSummary{
		{Flag: flag(model.IntegrityPolicyFocusLoss), EvidenceCount: 2, OverflowCount: 1},
		{Flag: flag(model.IntegrityPolicyConnectionLoss), EvidenceCount: 1},
		{Flag: flag(model.IntegrityPolicyBrowserNavigation), EvidenceCount: 4,
			Browser: &model.BrowserIntegrityGroup{ParticipationID: model.NewAttemptParticipationID(),
				PolicyRevisionID: model.NewExamRevisionID(), RuleID: "block-search"}},
		{Flag: flag("native.applications"), EvidenceCount: 1},
		{Flag: func() model.IntegrityFlag {
			value := flag("native.display")
			value.NativeRuleID = "baseline.single_display"
			return value
		}(), EvidenceCount: 2},
		{Flag: flag("example-plugin.gaze"), EvidenceCount: 5},
	}
	httpAPI := f.api(t)
	response := httptest.NewRecorder()
	httpAPI.ServeHTTP(response, f.request(t, http.MethodGet, "/api/v1/submissions/"+f.submission.ID.String()+"/integrity-flags", "credential"))
	require.Equal(t, http.StatusOK, response.Code, response.Body.String())
	var page examIntegrityFlagListResponse
	require.NoError(t, json.Unmarshal(response.Body.Bytes(), &page))
	summaries := make([]string, 0, len(page.Items))
	for _, item := range page.Items {
		summaries = append(summaries, item.Summary)
	}
	require.Equal(t, []string{
		"The exam window lost focus 3 times",
		"The secure connection was lost 1 time",
		"4 pages were blocked by the “block-search” browser rule",
		"The “applications” desktop check was not met 1 time",
		"The “baseline.single_display” desktop check was not met 2 times",
		"The “example-plugin.gaze” check reported this 5 times",
	}, summaries, "evidence beyond what was kept still counts, and a desktop check is named by its identifier, never by an application")
}

func TestSittingSubmissionListMapsFiltersAndShowsTheReview(t *testing.T) {
	t.Parallel()
	f := newReviewFixture(t)
	httpAPI := f.api(t)
	base := "/api/v1/exams/" + f.examID.String() + "/sittings/" + f.sitting.ID.String() + "/submissions"
	response := httptest.NewRecorder()
	httpAPI.ServeHTTP(response, f.request(t, http.MethodGet,
		base+"?review_state=none&review_state=draft&release_state=withheld&has_flags=true&sort=name&limit=5", "credential"))
	require.Equal(t, http.StatusOK, response.Code, response.Body.String())
	require.Len(t, f.reviewStore.submissionLists, 1)
	listed := f.reviewStore.submissionLists[0]
	require.Equal(t, []store.SubmissionListReviewState{store.SubmissionListReviewNone, store.SubmissionListReviewDraft}, listed.Filter.ReviewStates)
	require.Equal(t, []model.SubmissionReviewReleaseState{model.SubmissionReviewWithheld}, listed.Filter.ReleaseStates)
	require.NotNil(t, listed.Filter.HasFlags)
	require.True(t, *listed.Filter.HasFlags)
	require.Equal(t, store.SubmissionListByName, listed.Sort)
	require.Equal(t, 6, listed.Limit, "the Store reads one row past the page")
	require.True(t, listed.IncludeCounts)
	require.True(t, listed.ReviewVisible)
	require.Equal(t, f.caller.UserID, listed.ExcludeCandidateUserID)

	var page submissionListResponse
	require.NoError(t, json.Unmarshal(response.Body.Bytes(), &page))
	require.Len(t, page.Items, 1)
	require.Equal(t, f.submission.ReceiptCode.Display(), page.Items[0].ReceiptCode)
	require.Nil(t, page.Items[0].Exam, "a sitting's list names no exam on each row")
	require.NotNil(t, page.Items[0].Review)
	require.Equal(t, "draft", page.Items[0].Review.State)
	require.NotNil(t, page.Counts)
	require.NotContains(t, response.Body.String(), "Manager-only", "no notes or rationale reach the list")

	for _, query := range []string{"review_state=open", "review_state=draft&review_state=draft", "has_flags=yes", "sort=oldest", "release_state=hidden"} {
		refused := httptest.NewRecorder()
		httpAPI.ServeHTTP(refused, f.request(t, http.MethodGet, base+"?"+query, "credential"))
		assertHTTPProblem(t, refused, http.StatusBadRequest, "request.invalid")
	}
	require.Len(t, f.reviewStore.submissionLists, 1, "a refused query reads nothing")

	across := httptest.NewRecorder()
	httpAPI.ServeHTTP(across, f.request(t, http.MethodGet, "/api/v1/submissions?review_state=none&review_state=draft", "credential"))
	require.Equal(t, http.StatusOK, across.Code, across.Body.String())
	acrossOptions := f.reviewStore.submissionLists[1]
	require.NotNil(t, acrossOptions.Visibility)
	require.NotNil(t, acrossOptions.ReviewVisibility)
	require.False(t, acrossOptions.IncludeCounts, "the list across exams is not counted")
	require.NoError(t, json.Unmarshal(across.Body.Bytes(), &page))
	require.NotNil(t, page.Items[0].Exam, "a row across exams names its exam")
	require.Equal(t, "Algorithms", page.Items[0].Exam.Title)
}

func TestLiveAttemptTimelineReadsTheWindowAndSaysItIsIncomplete(t *testing.T) {
	t.Parallel()
	f := newReviewFixture(t)
	httpAPI := f.api(t)
	base := "/api/v1/exams/" + f.examID.String() + "/sittings/" + f.sitting.ID.String() + "/attempts/" + f.attemptID.String() + "/timeline"
	response := httptest.NewRecorder()
	httpAPI.ServeHTTP(response, f.request(t, http.MethodGet, base+"?from=2026-08-15T17:00:00Z&to=2026-08-15T19:00:00Z&limit=10", "credential"))
	require.Equal(t, http.StatusOK, response.Code, response.Body.String())
	require.Len(t, f.reviewStore.liveTimelines, 1)
	read := f.reviewStore.liveTimelines[0]
	require.Equal(t, f.attemptID, read.AttemptID)
	require.Equal(t, 10, read.Limit)
	require.True(t, read.Window.From.Equal(time.Date(2026, 8, 15, 17, 0, 0, 0, time.UTC)))
	require.True(t, read.Window.To.Equal(time.Date(2026, 8, 15, 19, 0, 0, 0, time.UTC)))
	var page liveTimelineListResponse
	require.NoError(t, json.Unmarshal(response.Body.Bytes(), &page))
	require.False(t, page.Complete, "a running attempt's evidence is not complete")
	require.Len(t, page.Items, 1)
	require.Equal(t, "flag", page.Items[0].Kind)
	require.NotNil(t, page.Items[0].Flag)
	require.NotEmpty(t, page.Items[0].Flag.Summary)

	for _, query := range []string{"from=yesterday", "from=2026-08-15T19:00:00Z&to=2026-08-15T17:00:00Z", "limit=101"} {
		refused := httptest.NewRecorder()
		httpAPI.ServeHTTP(refused, f.request(t, http.MethodGet, base+"?"+query, "credential"))
		assertHTTPProblem(t, refused, http.StatusBadRequest, "request.invalid")
	}
	require.Len(t, f.reviewStore.liveTimelines, 1, "a refused window reads nothing")

	candidate := httptest.NewRecorder()
	httpAPI.ServeHTTP(candidate, f.request(t, http.MethodGet, base, "candidate"))
	require.Equal(t, http.StatusNotFound, candidate.Code, "the candidate never reads evidence about themself")
	require.Len(t, f.reviewStore.liveTimelines, 1)
}

func TestBulkResultReleaseReportsEachSubmission(t *testing.T) {
	t.Parallel()
	f := newReviewFixture(t)
	httpAPI := f.api(t)
	path := "/api/v1/exams/" + f.examID.String() + "/sittings/" + f.sitting.ID.String() + "/result-releases"
	send := func(body string) *httptest.ResponseRecorder {
		request := newJSONRequest(http.MethodPost, path, strings.NewReader(body))
		request.Header.Set("Authorization", "Bearer credential")
		request.Header.Set("Idempotency-Key", "bulk-release")
		response := httptest.NewRecorder()
		httpAPI.ServeHTTP(response, request)
		return response
	}
	response := send(`{"submission_ids":["` + f.submission.ID.String() + `"]}`)
	require.Equal(t, http.StatusOK, response.Code, response.Body.String())
	require.JSONEq(t, `{"items":[{"submission_id":"`+f.submission.ID.String()+`","outcome":"not_finalized"}]}`, response.Body.String(),
		"a draft review is not released")
	assertHTTPProblem(t, send(`{"submission_ids":["`+f.submission.ID.String()+`"],"all_finalized":true}`), http.StatusBadRequest, "request.invalid")
	assertHTTPProblem(t, send(`{}`), http.StatusBadRequest, "request.invalid")
}

func TestIntegrityReviewHTTPListsAttemptEvidenceWithItsDetail(t *testing.T) {
	t.Parallel()
	f := newReviewFixture(t)
	at := time.Date(2026, 11, 14, 9, 0, 0, 0, time.UTC)
	// The service refuses a page that does not continue past the cursor, so
	// the listed record sorts after the cursor's.
	after, recordID := model.NewIntegrityEvidenceID(), model.NewIntegrityEvidenceID()
	if recordID.String() < after.String() {
		after, recordID = recordID, after
	}
	record := model.AttemptEvidence{ID: recordID, AttemptID: f.attemptID,
		Kind: model.AttemptEvidenceGap, Author: model.NewEnvironmentWorkspaceAuthor(1, ""), Sequence: 3,
		Detail: json.RawMessage(`{"gap":"environment_restarted"}`), ObservedAt: at, RecordedAt: at}
	f.reviewStore.attemptEvidence = &store.AttemptEvidencePage{Items: []model.AttemptEvidence{record}, HasMore: true}
	httpAPI := f.api(t)
	cursor, err := encodeExamIntegrityReviewCursor("attempt_evidence", after.String())
	require.NoError(t, err)
	path := "/api/v1/submissions/" + f.submission.ID.String() + "/attempt-evidence?limit=1&cursor=" + cursor
	response := httptest.NewRecorder()
	httpAPI.ServeHTTP(response, f.request(t, http.MethodGet, path, "credential"))
	require.Equal(t, http.StatusOK, response.Code, "body=%s", response.Body.String())
	require.Equal(t, "no-store", response.Header().Get("Cache-Control"))
	require.Equal(t, []store.AttemptEvidenceListOptions{{SubmissionID: f.submission.ID, AfterID: after, Limit: 1}}, f.reviewStore.attemptLists)
	var page struct {
		Items      []map[string]any `json:"items"`
		NextCursor string           `json:"next_cursor"`
	}
	require.NoError(t, json.Unmarshal(response.Body.Bytes(), &page))
	require.Len(t, page.Items, 1)
	require.Equal(t, "execution.gap", page.Items[0]["kind"])
	require.Equal(t, map[string]any{"gap": "environment_restarted"}, page.Items[0]["detail"])
	require.Equal(t, map[string]any{"kind": "environment", "ref": "1"}, page.Items[0]["author"])
	id, err := decodeExamIntegrityReviewCursor(page.NextCursor, "attempt_evidence")
	require.NoError(t, err)
	require.Equal(t, record.ID.String(), id)

	_, err = decodeExamIntegrityReviewCursor(page.NextCursor, "native_condition")
	require.Error(t, err, "a Attempt evidence cursor is not a native condition cursor")
}

func TestIntegrityReviewEndpointsForwardExactCursorsAndMapMalformedCursors(t *testing.T) {
	t.Parallel()
	f := newReviewFixture(t)
	httpAPI := f.api(t)
	flagID := model.NewIntegrityFlagID()
	evidenceID := model.NewIntegrityEvidenceID()
	discrepancyID := model.NewIntegrityDiscrepancyID()
	tests := []struct {
		name   string
		kind   string
		id     string
		path   string
		reads  func() int
		assert func(*testing.T)
	}{
		{
			name: "flags", kind: "flag", id: flagID.String(),
			path:  "/api/v1/submissions/" + f.submission.ID.String() + "/integrity-flags",
			reads: func() int { return len(f.reviewStore.flagLists) },
			assert: func(t *testing.T) {
				require.Equal(t, flagID, f.reviewStore.flagLists[len(f.reviewStore.flagLists)-1].AfterFlagID)
			},
		},
		{
			name: "evidence", kind: "evidence", id: evidenceID.String(),
			path:  "/api/v1/submissions/" + f.submission.ID.String() + "/integrity-flags/" + f.flag.ID.String() + "/evidence",
			reads: func() int { return len(f.reviewStore.evidenceLists) },
			assert: func(t *testing.T) {
				require.Equal(t, evidenceID, f.reviewStore.evidenceLists[len(f.reviewStore.evidenceLists)-1].AfterEvidenceID)
			},
		},
		{
			name: "discrepancies", kind: "discrepancy", id: discrepancyID.String(),
			path:  "/api/v1/submissions/" + f.submission.ID.String() + "/integrity-discrepancies",
			reads: func() int { return len(f.reviewStore.discrepancyLists) },
			assert: func(t *testing.T) {
				require.Equal(t, discrepancyID, f.reviewStore.discrepancyLists[len(f.reviewStore.discrepancyLists)-1].AfterDiscrepancyID)
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			cursor, err := encodeExamIntegrityReviewCursor(test.kind, test.id)
			require.NoError(t, err)
			response := httptest.NewRecorder()
			httpAPI.ServeHTTP(response, f.request(t, http.MethodGet, test.path+"?cursor="+url.QueryEscape(cursor), "credential"))
			require.Equal(t, http.StatusOK, response.Code, "cursor response = %d: %s", response.Code, response.Body.String())
			test.assert(t)

			reads := test.reads()
			malformedResponse := httptest.NewRecorder()
			httpAPI.ServeHTTP(malformedResponse, f.request(t, http.MethodGet, test.path+"?cursor=not-a-cursor", "credential"))
			assertHTTPProblem(t, malformedResponse, http.StatusBadRequest, "request.invalid")
			require.Equal(t, reads, test.reads(), "a malformed cursor reaches no listing")
		})
	}
}

func TestStudentResultHTTPExposesOnlyReleasedSanitizedProjection(t *testing.T) {
	t.Parallel()
	f := newReviewFixture(t)
	httpAPI := f.api(t)
	path := "/api/v1/exam-attempts/" + f.attemptID.String() + "/result"
	response := httptest.NewRecorder()
	httpAPI.ServeHTTP(response, f.request(t, http.MethodGet, path, "candidate"))
	require.Equal(t, http.StatusOK, response.Code, "response=%d headers=%v body=%s", response.Code, response.Header(), response.Body.String())
	require.Equal(t, "no-store", response.Header().Get("Cache-Control"), "response=%d headers=%v body=%s", response.Code, response.Header(), response.Body.String())
	require.Equal(t, []model.ExamAttemptID{f.attemptID}, f.reviewStore.resultReads, "the candidate's result was read once")
	payload := response.Body.String()
	require.Contains(t, payload, "Approved **remarks**")
	for _, forbidden := range []string{"manager_notes", "private_rationale", "integrity_flag", "evidence", "workspace", "candidate_user_id"} {
		require.NotContains(t, payload, forbidden, "student result exposed")
	}
}

func TestIntegrityReviewHTTPListsBoundedLateDiscrepanciesWithoutConnectionSelectors(t *testing.T) {
	t.Parallel()
	f := newReviewFixture(t)
	httpAPI := f.api(t)
	path := "/api/v1/submissions/" + f.submission.ID.String() + "/integrity-discrepancies?limit=1"
	response := httptest.NewRecorder()
	httpAPI.ServeHTTP(response, f.request(t, http.MethodGet, path, "credential"))
	require.Equal(t, http.StatusOK, response.Code, "response=%d body=%s", response.Code, response.Body.String())
	require.Len(t, f.reviewStore.discrepancyLists, 1, "the listing reached the Store")
	require.Equal(t, 1, f.reviewStore.discrepancyLists[0].Limit, "listed=%#v", f.reviewStore.discrepancyLists[0])
	payload := response.Body.String()
	require.Contains(t, payload, f.discrepancy.ID.String())
	require.Contains(t, payload, `"kind":"late_focus_loss"`)
	for _, forbidden := range []string{"connection_id", "session_id", "credential", "continuity"} {
		require.NotContains(t, payload, forbidden, "discrepancy response exposed")
	}
}

func TestExamIntegrityReviewOpenAPIDocument(t *testing.T) {
	t.Parallel()

	base := "/api/v1/submissions/{submission_id}"
	flags := base + "/integrity-flags"
	evidence := flags + "/{integrity_flag_id}/evidence"
	discrepancies := base + "/integrity-discrepancies"

	document := readOpenAPIDocument(t)
	for _, path := range []string{flags, evidence, discrepancies} {
		assertExamAttemptQueryParameters(t, document, path, []string{"limit", "cursor"})
	}
	for _, response := range []string{"ExamIntegrityFlagListOK", "ExamIntegrityEvidenceListOK",
		"ExamIntegrityDiscrepancyListOK", "ExamIntegrityReviewOK", "ExamIntegrityReviewMutationOK", "StudentExamResultOK"} {
		assertProtectedExamCacheControl(t, response, "no-store")
	}
}

func TestIntegrityReviewHTTPReadsSealedPreviewUsage(t *testing.T) {
	t.Parallel()
	f := newReviewFixture(t)
	f.reviewStore.previewUsage = []model.PreviewUsage{{Port: 5173, Connections: 2, BytesIn: 42, BytesOut: 314,
		FirstUsedAt: reviewFixtureTime, LastUsedAt: reviewFixtureTime.Add(time.Second)}}
	api := f.api(t)
	path := "/api/v1/submissions/" + f.submission.ID.String() + "/preview-usage"

	denied := httptest.NewRecorder()
	api.ServeHTTP(denied, f.request(t, http.MethodGet, path, "candidate"))
	require.Equal(t, http.StatusNotFound, denied.Code, denied.Body.String())
	require.Empty(t, f.reviewStore.previewReads)

	response := httptest.NewRecorder()
	api.ServeHTTP(response, f.request(t, http.MethodGet, path, "credential"))
	require.Equal(t, http.StatusOK, response.Code, response.Body.String())
	require.Equal(t, "no-store", response.Header().Get("Cache-Control"))
	require.Equal(t, []model.SubmissionID{f.submission.ID}, f.reviewStore.previewReads)
	var usage previewUsageResponse
	require.NoError(t, json.Unmarshal(response.Body.Bytes(), &usage))
	require.Equal(t, previewUsageResponse{SubmissionID: f.submission.ID.String(), Ports: []previewPortUsageResponse{{
		Port: 5173, Connections: 2, BytesIn: 42, BytesOut: 314,
		FirstUsedAt: model.FormatRFC3339(reviewFixtureTime), LastUsedAt: model.FormatRFC3339(reviewFixtureTime.Add(time.Second)),
	}}}, usage)
}
