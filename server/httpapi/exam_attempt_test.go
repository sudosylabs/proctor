// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package httpapi

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/textproto"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/sudosylabs/proctor/server/httpapi/route"
	"github.com/sudosylabs/proctor/server/model"
	"github.com/sudosylabs/proctor/server/store"
)

func TestCandidateExamPresentationRequiresBoundHeadersAndNeverEchoesSecrets(t *testing.T) {
	t.Parallel()
	f := newAttemptFixture(t)
	httpAPI := f.api(t, examAttemptResource(f.service))
	request := f.candidateRequest(t, http.MethodGet, f.candidatePath("/presentation"))
	response := httptest.NewRecorder()
	httpAPI.ServeHTTP(response, request)
	require.Equal(t, http.StatusOK, response.Code, "status/cache = %d/%q: %s", response.Code, response.Header().Get("Cache-Control"), response.Body.String())
	require.Equal(t, "no-store", response.Header().Get("Cache-Control"), "status/cache = %d/%q: %s", response.Code, response.Header().Get("Cache-Control"), response.Body.String())
	require.Equal(t, []store.CandidateAttemptAccess{f.candidateAccess()}, f.attemptStore.accesses,
		"the Store proves the Attempt, Connection and only the credential's hash")
	require.NotContains(t, response.Body.String(), f.credential)
	require.NotContains(t, response.Body.String(), "credential")
	require.NotContains(t, response.Body.String(), "file_revision")
	require.NotContains(t, response.Body.String(), "object_id")
	var payload map[string]json.RawMessage
	var capabilities candidateRuntimeCapabilitiesResponse
	var capacity examCapacityPolicyResponse
	require.NoError(t, json.Unmarshal(response.Body.Bytes(), &payload), "presentation shape")
	require.Len(t, payload, 9, "presentation shape")
	require.NoError(t, json.Unmarshal(payload["capacity"], &capacity), "presentation capacity")
	require.Equal(t, examCapacityPolicyResponseFromModel(f.attemptStore.presentation.Capacity), capacity, "presentation capacity")
	require.NoError(t, json.Unmarshal(payload["candidate_runtime_capabilities"], &capabilities), "presentation capabilities = %#v", capabilities)
	require.True(t, capabilities.FocusLossCollectionEnabled, "presentation capabilities = %#v", capabilities)
	require.Equal(t, 1, capabilities.SchemaVersion, "presentation capabilities = %#v", capabilities)
	require.False(t, capabilities.Departure.Allowed, "presentation capabilities = %#v", capabilities)
	require.Equal(t, "attempt_in_progress", capabilities.Departure.Reason, "presentation capabilities = %#v", capabilities)
}

func TestCandidateExamHTTPRejectsMissingOrDuplicateProtectionHeadersBeforeApplication(t *testing.T) {
	t.Parallel()
	f := newAttemptFixture(t)
	httpAPI := f.api(t, examAttemptResource(f.service))
	for _, mutate := range []func(*http.Request){
		func(request *http.Request) { request.Header.Del(candidateAttemptCredentialHeader) },
		func(request *http.Request) {
			request.Header.Add(candidateAttemptConnectionHeader, f.connection.ID.String())
		},
	} {
		request := f.candidateRequest(t, http.MethodGet, f.candidatePath("/presentation"))
		mutate(request)
		response := httptest.NewRecorder()
		httpAPI.ServeHTTP(response, request)
		reads := len(f.attemptStore.accesses)
		require.Equal(t, http.StatusBadRequest, response.Code, "status/reads/cache = %d/%d/%q: %s", response.Code, reads, response.Header().Get("Cache-Control"), response.Body.String())
		require.Equal(t, 0, reads, "status/reads/cache = %d/%d/%q: %s", response.Code, reads, response.Header().Get("Cache-Control"), response.Body.String())
		require.Equal(t, "no-store", response.Header().Get("Cache-Control"), "status/reads/cache = %d/%d/%q: %s", response.Code, reads, response.Header().Get("Cache-Control"), response.Body.String())
	}
}

func TestCandidateExamWorkspaceUsesBoundedOpaqueCursor(t *testing.T) {
	t.Parallel()
	f := newAttemptFixture(t)
	httpAPI := f.api(t, examAttemptResource(f.service))
	request := f.candidateRequest(t, http.MethodGet, f.candidatePath("/workspace?limit=1"))
	response := httptest.NewRecorder()
	httpAPI.ServeHTTP(response, request)
	require.Equal(t, http.StatusOK, response.Code, "status/cache = %d/%q: %s", response.Code, response.Header().Get("Cache-Control"), response.Body.String())
	query := f.workspaceStore.lists[0]
	require.Equal(t, 1, query.Limit, "status/query/cache = %d/%#v/%q: %s", response.Code, query, response.Header().Get("Cache-Control"), response.Body.String())
	require.Equal(t, "no-store", response.Header().Get("Cache-Control"), "status/query/cache = %d/%#v/%q: %s", response.Code, query, response.Header().Get("Cache-Control"), response.Body.String())
	var page candidateExamWorkspaceListResponse
	require.NoError(t, json.Unmarshal(response.Body.Bytes(), &page), "Workspace page = %#v", page)
	require.Len(t, page.Items, 1, "Workspace page = %#v", page)
	require.NotEmpty(t, page.NextCursor, "Workspace page = %#v", page)
	require.NotNil(t, page.Items[0].Size, "Workspace page = %#v", page)
	cursor, err := decodeCandidateWorkspaceCursor(page.NextCursor)
	require.NoError(t, err, "Workspace cursor = %#v", cursor)
	require.Equal(t, f.workspaceStore.cursor, cursor.ExpectedCursor, "Workspace cursor = %#v", cursor)
	require.Equal(t, f.workspaceStore.items[0].EntryID, cursor.ID, "Workspace cursor = %#v", cursor)
	require.Equal(t, int64(-1), query.ExpectedCursor, "Workspace cursor = %#v", cursor)
	second := f.candidateRequest(t, http.MethodGet, f.candidatePath("/workspace?cursor="+url.QueryEscape(page.NextCursor)))
	secondResponse := httptest.NewRecorder()
	httpAPI.ServeHTTP(secondResponse, second)
	query = f.workspaceStore.lists[1]
	require.Equal(t, http.StatusOK, secondResponse.Code, "Workspace cursor forwarding = %d query=%#v body=%s", secondResponse.Code, query, secondResponse.Body.String())
	require.Equal(t, cursor.ExpectedCursor, query.ExpectedCursor, "Workspace cursor forwarding = %d query=%#v body=%s", secondResponse.Code, query, secondResponse.Body.String())
	require.Equal(t, cursor.ID, query.AfterEntryID, "Workspace cursor forwarding = %d query=%#v body=%s", secondResponse.Code, query, secondResponse.Body.String())
	malformed := f.candidateRequest(t, http.MethodGet, f.candidatePath("/workspace?cursor=not-a-cursor"))
	malformedResponse := httptest.NewRecorder()
	httpAPI.ServeHTTP(malformedResponse, malformed)
	assertHTTPProblem(t, malformedResponse, http.StatusBadRequest, "request.invalid")
}

func TestCandidateSubmitExamAttemptUsesStrictCausalSelectorsAndReturnsSafeReceipt(t *testing.T) {
	t.Parallel()

	f := newAttemptFixture(t)
	httpAPI := f.api(t, examAttemptResource(f.service))
	body := `{"participation_id":"` + f.participation.ID.String() + `","generation":1,"expected_current_revision_id":"` +
		f.attempt.AdmissionRevisionID.String() + `","expected_workspace_cursor":4,"final_focus_loss_sequence":0}`
	request := f.candidateRequest(t, http.MethodPost, f.candidatePath("/submissions"))
	request.Body = io.NopCloser(strings.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Idempotency-Key", "submit-once")
	response := httptest.NewRecorder()
	httpAPI.ServeHTTP(response, request)
	require.Equal(t, http.StatusCreated, response.Code, "status=%d headers=%v body=%s", response.Code, response.Header(), response.Body.String())
	require.Len(t, f.submissionStore.seals, 1, "the Submission was not sealed")
	seal := f.submissionStore.seals[0].Access
	require.Equal(t, "no-store", response.Header().Get("Cache-Control"), "status=%d headers=%v seal=%#v body=%s", response.Code, response.Header(), seal, response.Body.String())
	require.Equal(t, f.attempt.ID, seal.AttemptID, "status=%d headers=%v seal=%#v body=%s", response.Code, response.Header(), seal, response.Body.String())
	require.Equal(t, f.participation.ID, seal.ParticipationID, "status=%d headers=%v seal=%#v body=%s", response.Code, response.Header(), seal, response.Body.String())
	require.Equal(t, int64(1), seal.Generation, "status=%d headers=%v seal=%#v body=%s", response.Code, response.Header(), seal, response.Body.String())
	require.Equal(t, f.attempt.AdmissionRevisionID, seal.ExpectedCurrentRevisionID, "status=%d headers=%v seal=%#v body=%s", response.Code, response.Header(), seal, response.Body.String())
	require.Equal(t, int64(4), seal.ExpectedWorkspaceCursor, "status=%d headers=%v seal=%#v body=%s", response.Code, response.Header(), seal, response.Body.String())
	require.Equal(t, int64(0), seal.FinalFocusLossSequence, "status=%d headers=%v seal=%#v body=%s", response.Code, response.Header(), seal, response.Body.String())
	require.Equal(t, sha256.Sum256([]byte("submit-once")), f.submissionStore.lastCommand(t).KeyDigest, "status=%d headers=%v seal=%#v body=%s", response.Code, response.Header(), seal, response.Body.String())
	var payload map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(response.Body.Bytes(), &payload), "receipt")
	require.Len(t, payload, 9, "receipt")
	require.Equal(t, `"MBY3-KBMN-EB83-TFW1"`, string(payload["receipt_code"]), "receipt code is in display form")
	require.Equal(t, `"`+f.attempt.AdmissionRevisionID.String()+`"`, string(payload["exam_revision_id"]), "receipt")
	require.Equal(t, `"submitted"`, string(payload["state"]), "receipt")
	require.Equal(t, "4", string(payload["workspace_cursor"]), "receipt")
	assertNoForbiddenJSONTerms(t, response.Body.Bytes(),
		"participation", "generation", "connection", "credential", "integrity", "gap", "path", "content", "evidence")
}

func TestCandidateAcknowledgesExactCorrectionWithBoundSessionFences(t *testing.T) {
	t.Parallel()

	f := newAttemptFixture(t)
	httpAPI := f.api(t, examAttemptResource(f.service))
	correctionID := model.NewExamRevisionID()
	currentRevisionID := model.NewExamRevisionID()
	f.attemptStore.presentation.RuntimeCapabilities.ExamRevision.CurrentRevisionID = currentRevisionID
	path := f.candidatePath("/corrections/" + correctionID.String() + "/acknowledgement")
	body := `{"participation_id":"` + f.participation.ID.String() + `","generation":1,"expected_current_revision_id":"` + currentRevisionID.String() + `"}`
	request := f.candidateRequest(t, http.MethodPut, path)
	request.Body = io.NopCloser(strings.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Idempotency-Key", "acknowledge-correction-once")
	response := httptest.NewRecorder()
	httpAPI.ServeHTTP(response, request)
	require.Equal(t, http.StatusOK, response.Code, "status=%d headers=%v body=%s", response.Code, response.Header(), response.Body.String())
	require.Len(t, f.attemptStore.acknowledgements, 1, "the acknowledgement was not recorded")
	command := f.attemptStore.acknowledgements[0]
	require.Equal(t, "no-store", response.Header().Get("Cache-Control"), "status=%d headers=%v command=%#v body=%s", response.Code, response.Header(), command, response.Body.String())
	require.Equal(t, f.candidateAccess(), command.Access, "status=%d headers=%v command=%#v body=%s", response.Code, response.Header(), command, response.Body.String())
	require.Equal(t, f.participation.ID, command.ParticipationID, "status=%d headers=%v command=%#v body=%s", response.Code, response.Header(), command, response.Body.String())
	require.Equal(t, int64(1), command.Generation, "status=%d headers=%v command=%#v body=%s", response.Code, response.Header(), command, response.Body.String())
	require.Equal(t, correctionID, command.CorrectionRevisionID, "status=%d headers=%v command=%#v body=%s", response.Code, response.Header(), command, response.Body.String())
	require.Equal(t, currentRevisionID, command.ExpectedCurrentRevisionID, "status=%d headers=%v command=%#v body=%s", response.Code, response.Header(), command, response.Body.String())
	require.Equal(t, sha256.Sum256([]byte("acknowledge-correction-once")), f.attemptStore.lastCommand(t).KeyDigest, "status=%d headers=%v command=%#v body=%s", response.Code, response.Header(), command, response.Body.String())
	var payload map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(response.Body.Bytes(), &payload), "acknowledgement response")
	require.Len(t, payload, 5, "acknowledgement response")
	require.Equal(t, `"`+correctionID.String()+`"`, string(payload["revision_id"]), "acknowledgement response")
	require.Equal(t, `"acknowledged"`, string(payload["acknowledgement_state"]), "acknowledgement response")
	assertNoForbiddenJSONTerms(t, response.Body.Bytes(),
		"credential", "connection", "participation", "generation", "audit", "private")

	invalidFixture := newAttemptFixture(t)
	invalidAPI := invalidFixture.api(t, examAttemptResource(invalidFixture.service))
	invalid := invalidFixture.candidateRequest(t, http.MethodPut, invalidFixture.candidatePath(
		"/corrections/"+model.NewExamRevisionID().String()+"/acknowledgement"))
	invalid.Body = io.NopCloser(strings.NewReader(`{"participation_id":"` + invalidFixture.participation.ID.String() +
		`","generation":1,"expected_current_revision_id":"` + model.NewExamRevisionID().String() + `","accepted":true}`))
	invalid.Header.Set("Content-Type", "application/json")
	invalid.Header.Set("Idempotency-Key", "invalid-acknowledgement")
	invalidResponse := httptest.NewRecorder()
	invalidAPI.ServeHTTP(invalidResponse, invalid)
	assertHTTPProblem(t, invalidResponse, http.StatusBadRequest, "request.invalid")
	require.Empty(t, invalidFixture.attemptStore.acknowledgements)
}

func TestSubmissionManifestCursorIsUnversionedStrictAndPathFree(t *testing.T) {
	t.Parallel()

	entryID := model.NewAttemptWorkspaceEntryID()
	raw, err := encodeSubmissionManifestCursor(submissionManifestCursor{EntryID: entryID})
	require.NoError(t, err)
	decoded, err := decodeSubmissionManifestCursor(raw)
	require.NoError(t, err, "decoded cursor = %#v", decoded)
	require.Equal(t, entryID, decoded.EntryID, "decoded cursor = %#v", decoded)
	require.NotContains(t, raw, "src")
	require.NotContains(t, raw, "main.go")
	versioned := base64.RawURLEncoding.EncodeToString([]byte(`{"version":1,"after_entry_id":"` + entryID.String() + `"}`))
	_, err = decodeSubmissionManifestCursor(versioned)
	require.Error(t, err, "versioned cursor was accepted")
	unknown := base64.RawURLEncoding.EncodeToString([]byte(`{"after_entry_id":"` + entryID.String() + `","path":"src/main.go"}`))
	_, err = decodeSubmissionManifestCursor(unknown)
	require.Error(t, err, "cursor containing a path field was accepted")
}

func TestCandidateExamContentIsInlinePrivateNoStoreAndConditional(t *testing.T) {
	t.Parallel()
	f := newAttemptFixture(t)
	httpAPI := f.api(t, examAttemptResource(f.service))
	resource := f.attemptStore.resource.Resource
	path := f.candidatePath("/resources/" + resource.ResourceID.String() + "/content")
	request := f.candidateRequest(t, http.MethodGet, path)
	response := httptest.NewRecorder()
	httpAPI.ServeHTTP(response, request)
	wantETag := `"` + resource.SHA256 + `"`
	require.Equal(t, http.StatusOK, response.Code, "content response = %d %#v", response.Code, response.Header())
	require.Equal(t, "protected", response.Body.String(), "content response = %d %#v", response.Code, response.Header())
	require.Empty(t, response.Header().Get("Content-Disposition"), "content response = %d %#v", response.Code, response.Header())
	require.Equal(t, "private, no-store", response.Header().Get("Cache-Control"), "content response = %d %#v", response.Code, response.Header())
	require.Equal(t, wantETag, response.Header().Get("ETag"), "content response = %d %#v", response.Code, response.Header())

	request = f.candidateRequest(t, http.MethodGet, path)
	request.Header.Set("If-None-Match", wantETag)
	response = httptest.NewRecorder()
	httpAPI.ServeHTTP(response, request)
	require.Equal(t, http.StatusNotModified, response.Code, "conditional response = %d %#v %q", response.Code, response.Header(), response.Body.String())
	require.Equal(t, 0, response.Body.Len(), "conditional response = %d %#v %q", response.Code, response.Header(), response.Body.String())
	require.Equal(t, "private, no-store", response.Header().Get("Cache-Control"), "conditional response = %d %#v %q", response.Code, response.Header(), response.Body.String())
}

func TestCandidateWorkspaceContentUsesContentVersionETagAndExactEntry(t *testing.T) {
	t.Parallel()
	f := newAttemptFixture(t)
	httpAPI := f.api(t, examAttemptResource(f.service))
	entry := f.workspaceStore.items[0]
	request := f.candidateRequest(t, http.MethodGet, f.candidatePath("/workspace/files/"+entry.EntryID.String()+"/content"))
	response := httptest.NewRecorder()
	httpAPI.ServeHTTP(response, request)
	reads := f.workspaceStore.files
	require.Equal(t, http.StatusOK, response.Code, "Workspace content = %d reads=%#v headers=%#v body=%s", response.Code, reads, response.Header(), response.Body.String())
	require.Equal(t, []attemptWorkspaceFileRead{{Access: f.candidateAccess(), EntryID: entry.EntryID}}, reads,
		"the exact entry is read through the candidate's live Connection")
	require.Equal(t, `"`+entry.ContentVersion.String()+`"`, response.Header().Get("ETag"), "Workspace content = %d reads=%#v headers=%#v body=%s", response.Code, reads, response.Header(), response.Body.String())
	require.Equal(t, "private, no-store", response.Header().Get("Cache-Control"), "Workspace content = %d reads=%#v headers=%#v body=%s", response.Code, reads, response.Header(), response.Body.String())
}

func TestManagedExamAttemptListIsBoundedSafeAndOpaque(t *testing.T) {
	t.Parallel()
	f := newAttemptFixture(t)
	other, err := model.NewExamAttempt(model.NewExamAttemptID(), f.examID, f.sitting.ID, model.NewUserID(), f.sitting.ExamRevisionID, attemptFixtureTime.Add(-time.Minute))
	require.NoError(t, err)
	otherWorkspace, err := model.NewExamAttemptWorkspace(model.NewExamAttemptWorkspaceID(), other.ID, other.CreatedAt)
	require.NoError(t, err)
	f.attemptStore.others = []store.ExamAttemptManagerSnapshot{{Attempt: other, Workspace: otherWorkspace}}
	httpAPI := f.api(t, examAttemptResource(f.service))
	path := "/api/v1/exams/" + f.examID.String() + "/sittings/" + f.sitting.ID.String() + "/attempts?limit=1&state=active"
	response := httptest.NewRecorder()
	httpAPI.ServeHTTP(response, f.managerRequest(t, http.MethodGet, path, nil))
	require.Equal(t, http.StatusOK, response.Code, "manager response = %d/%q: %s", response.Code, response.Header().Get("Cache-Control"), response.Body.String())
	query := f.attemptStore.lists[0]
	require.Equal(t, 2, query.Limit, "the Store reads one row past the page: %#v", query)
	require.Len(t, query.States, 1, "manager response/query = %d/%#v/%q: %s", response.Code, query, response.Header().Get("Cache-Control"), response.Body.String())
	require.Equal(t, "no-store", response.Header().Get("Cache-Control"), "manager response/query = %d/%#v/%q: %s", response.Code, query, response.Header().Get("Cache-Control"), response.Body.String())
	require.NotContains(t, response.Body.String(), "credential")
	require.NotContains(t, response.Body.String(), "session_id")
	require.NotContains(t, response.Body.String(), "hash")
	var page examAttemptManagerListResponse
	require.NoError(t, json.Unmarshal(response.Body.Bytes(), &page), "manager page = %#v", page)
	require.Len(t, page.Items, 1, "manager page = %#v", page)
	require.NotEmpty(t, page.NextCursor, "manager page = %#v", page)
	cursor, err := decodeExamAttemptManagerCursor(page.NextCursor)
	require.NoError(t, err)
	secondResponse := httptest.NewRecorder()
	httpAPI.ServeHTTP(secondResponse, f.managerRequest(t, http.MethodGet, path+"&cursor="+url.QueryEscape(page.NextCursor), nil))
	query = f.attemptStore.lists[1]
	require.Equal(t, http.StatusOK, secondResponse.Code, "manager cursor forwarding = %d query=%#v body=%s", secondResponse.Code, query, secondResponse.Body.String())
	require.True(t, query.BeforeCreatedAt.Equal(cursor.CreatedAt), "manager cursor forwarding = %d query=%#v body=%s", secondResponse.Code, query, secondResponse.Body.String())
	require.Equal(t, cursor.ID, query.BeforeAttemptID, "manager cursor forwarding = %d query=%#v body=%s", secondResponse.Code, query, secondResponse.Body.String())
	malformedResponse := httptest.NewRecorder()
	httpAPI.ServeHTTP(malformedResponse, f.managerRequest(t, http.MethodGet, path+"&cursor=not-a-cursor", nil))
	assertHTTPProblem(t, malformedResponse, http.StatusBadRequest, "request.invalid")

	response = httptest.NewRecorder()
	httpAPI.ServeHTTP(response, f.managerRequest(t, http.MethodGet, f.managerPath(""), nil))
	require.Equal(t, http.StatusOK, response.Code, "manager exact = %d reads=%#v headers=%#v body=%s", response.Code, f.attemptStore.gets, response.Header(), response.Body.String())
	require.Equal(t, []attemptRead{{ExamID: f.examID, AttemptID: f.attempt.ID}}, f.attemptStore.gets, "the exact Attempt of the named Exam is read")
	require.Contains(t, f.sittingStore.resolves, f.sitting.ID, "the named Sitting is resolved")
	require.Equal(t, "no-store", response.Header().Get("Cache-Control"), "manager exact = %d headers=%#v body=%s", response.Code, response.Header(), response.Body.String())
}

func TestManagedBrowserActivityIsNestedMinimizedNoStoreAndKeysetPaginated(t *testing.T) {
	t.Parallel()
	f := newAttemptFixture(t)
	sourceID := model.BrowserSourceSessionID("01912ed5-d4f7-4f02-9b97-928b5e8d3f21")
	record := func(sequence int64) store.BrowserActivityRecord {
		return store.BrowserActivityRecord{AttemptID: f.attempt.ID, ParticipationID: f.participation.ID, Generation: 1, SourceSessionID: sourceID,
			Event: model.BrowserActivityEvent{Sequence: sequence, Kind: model.BrowserActivityOpened, PolicyRevisionID: model.NewExamRevisionID(),
				ClientOccurredAt: attemptFixtureTime, ReceivedAt: attemptFixtureTime.Add(time.Duration(sequence) * time.Second)}}
	}
	f.deliveryStore.browserActivity = []store.BrowserActivityRecord{record(1), record(2)}
	httpAPI := f.api(t, examAttemptResource(f.service))
	path := f.managerPath("/browser-activity?limit=1")
	response := httptest.NewRecorder()
	httpAPI.ServeHTTP(response, f.managerRequest(t, http.MethodGet, path, nil))
	require.Equal(t, http.StatusOK, response.Code, "Browser Activity response = %d/%q: %s", response.Code, response.Header().Get("Cache-Control"), response.Body.String())
	query := f.deliveryStore.browserLists[0]
	require.Equal(t, 2, query.Limit, "the Store reads one record past the page: %#v", query)
	require.Equal(t, "no-store", response.Header().Get("Cache-Control"), "Browser Activity response/query = %d/%#v/%q: %s", response.Code, query, response.Header().Get("Cache-Control"), response.Body.String())
	var page browserActivityListResponse
	require.NoError(t, json.Unmarshal(response.Body.Bytes(), &page), "Browser Activity page = %#v", page)
	require.Len(t, page.Items, 1, "Browser Activity page = %#v", page)
	require.NotEmpty(t, page.NextCursor, "Browser Activity page = %#v", page)
	require.Nil(t, page.Items[0].Location, "Browser Activity page = %#v", page)
	for _, forbidden := range []string{"participation_id", "connection_id", "registration_id", "credential", "review", "location_scheme"} {
		require.NotContains(t, response.Body.String(), forbidden, "Browser Activity response contains")
	}
	cursor, err := decodeBrowserActivityCursor(page.NextCursor)
	require.NoError(t, err)
	secondResponse := httptest.NewRecorder()
	httpAPI.ServeHTTP(secondResponse, f.managerRequest(t, http.MethodGet, path+"&cursor="+url.QueryEscape(page.NextCursor), nil))
	query = f.deliveryStore.browserLists[1]
	require.Equal(t, http.StatusOK, secondResponse.Code, "Browser Activity cursor forwarding = %d query=%#v body=%s", secondResponse.Code, query, secondResponse.Body.String())
	require.True(t, query.AfterReceivedAt.Equal(cursor.ReceivedAt), "Browser Activity cursor forwarding = %d query=%#v body=%s", secondResponse.Code, query, secondResponse.Body.String())
	require.Equal(t, cursor.SourceSessionID, query.AfterSourceID, "Browser Activity cursor forwarding = %d query=%#v body=%s", secondResponse.Code, query, secondResponse.Body.String())
	require.Equal(t, cursor.Sequence, query.AfterSequence, "Browser Activity cursor forwarding = %d query=%#v body=%s", secondResponse.Code, query, secondResponse.Body.String())
}

func TestManagerSubmissionReadManifestAndFileArePurposeSpecificAndProtected(t *testing.T) {
	t.Parallel()

	f := newAttemptFixture(t)
	httpAPI := f.api(t, examAttemptResource(f.service))
	submissionID := f.submissionStore.submission.ID
	base := f.managerPath("/submissions/" + submissionID.String())
	for _, path := range []string{base, base + "/manifest?limit=1"} {
		response := httptest.NewRecorder()
		httpAPI.ServeHTTP(response, f.managerRequest(t, http.MethodGet, path, nil))
		require.Equal(t, http.StatusOK, response.Code, "GET %s = %d headers=%v body=%s", path, response.Code, response.Header(), response.Body.String())
		require.Equal(t, "no-store", response.Header().Get("Cache-Control"), "GET %s = %d headers=%v body=%s", path, response.Code, response.Header(), response.Body.String())
		for _, forbidden := range []string{"starter_object", "attempt_object", "storage_origin", "vfs", "url"} {
			require.NotContains(t, strings.ToLower(response.Body.String()), forbidden, "GET %s exposed", path)
		}
	}
	reads := f.submissionStore.manifestReads
	require.Equal(t, []model.SubmissionID{submissionID}, f.submissionStore.gets, "get=%#v manifest=%#v", f.submissionStore.gets, reads)
	require.Equal(t, 1, reads[0].Limit, "get=%#v manifest=%#v", f.submissionStore.gets, reads)
	require.Equal(t, submissionID, reads[0].SubmissionID, "get=%#v manifest=%#v", f.submissionStore.gets, reads)
	entry := f.submissionStore.manifest[0]
	cursor, err := encodeSubmissionManifestCursor(submissionManifestCursor{EntryID: entry.EntryID})
	require.NoError(t, err)
	cursorResponse := httptest.NewRecorder()
	httpAPI.ServeHTTP(cursorResponse, f.managerRequest(t, http.MethodGet, base+"/manifest?cursor="+url.QueryEscape(cursor), nil))
	reads = f.submissionStore.manifestReads
	require.Equal(t, http.StatusOK, cursorResponse.Code, "manifest cursor forwarding = %d query=%#v body=%s", cursorResponse.Code, reads, cursorResponse.Body.String())
	require.Equal(t, entry.EntryID, reads[1].AfterEntryID, "manifest cursor forwarding = %d query=%#v body=%s", cursorResponse.Code, reads, cursorResponse.Body.String())
	malformedResponse := httptest.NewRecorder()
	httpAPI.ServeHTTP(malformedResponse, f.managerRequest(t, http.MethodGet, base+"/manifest?cursor=not-a-cursor", nil))
	assertHTTPProblem(t, malformedResponse, http.StatusBadRequest, "request.invalid")

	contentPath := base + "/files/" + entry.EntryID.String() + "/content"
	response := httptest.NewRecorder()
	httpAPI.ServeHTTP(response, f.managerRequest(t, http.MethodGet, contentPath, nil))
	files := f.submissionStore.files
	require.Equal(t, http.StatusOK, response.Code, "content=%d headers=%v files=%#v body", response.Code, response.Header(), files)
	require.Equal(t, "protected", response.Body.String(), "content=%d headers=%v files=%#v body", response.Code, response.Header(), files)
	require.Equal(t, "private, no-store", response.Header().Get("Cache-Control"), "content=%d headers=%v files=%#v body", response.Code, response.Header(), files)
	require.Equal(t, `"`+entry.SHA256+`"`, response.Header().Get("ETag"), "content=%d headers=%v files=%#v body", response.Code, response.Header(), files)
	require.Empty(t, response.Header().Get("Content-Disposition"), "content=%d headers=%v files=%#v body", response.Code, response.Header(), files)
	require.Contains(t, response.Header().Values("Content-Security-Policy"), "sandbox", "authored content renders with an opaque origin")
	require.Equal(t, []model.AttemptWorkspaceEntryID{entry.EntryID}, files, "content=%d headers=%v files=%#v body", response.Code, response.Header(), files)
}

func TestManagerSubmissionIntegrityRetirementOmitsFormerCounters(t *testing.T) {
	t.Parallel()
	for _, retired := range []bool{false, true} {
		name := "retained zero counters"
		if retired {
			name = "retired integrity"
		}
		t.Run(name, func(t *testing.T) {
			f := newAttemptFixture(t)
			submission := f.submissionStore.submission
			submission.FinalFocusLossSequence = 0
			submission.UnresolvedIntegrityCount = 0
			submission.IntegrityState = model.SubmissionIntegritySettled
			if retired {
				submission.IntegrityState = model.SubmissionIntegrityRetired
				submission.IntegrityRetiredAt = model.OptionalTimeFrom(submission.SubmittedAt.Add(time.Hour))
				submission.BrowserActivity = model.BrowserSubmissionSettlement{State: "not_applicable", InventoryRevision: 1}
			}
			httpAPI := f.api(t, examAttemptResource(f.service))
			response := httptest.NewRecorder()
			httpAPI.ServeHTTP(response, f.managerRequest(t, http.MethodGet, f.managerPath("/submissions/"+submission.ID.String()), nil))
			require.Equal(t, http.StatusOK, response.Code, "header read = %d: %s", response.Code, response.Body.String())
			require.Equal(t, "no-store", response.Header().Get("Cache-Control"), "header read = %d: %s", response.Code, response.Body.String())
			var fields map[string]json.RawMessage
			require.NoError(t, json.Unmarshal(response.Body.Bytes(), &fields))
			for _, counter := range []string{"final_focus_loss_sequence", "unresolved_integrity_count"} {
				value, exists := fields[counter]
				require.NotEqual(t, retired, exists)
				require.False(t, exists && string(value) != "0")
			}
			_, hasRetirement := fields["integrity_retired_at"]
			require.Equal(t, retired, hasRetirement)
			require.Equal(t, `"`+string(submission.IntegrityState)+`"`, string(fields["integrity_state"]))
			require.Equal(t, `"`+submission.ManifestDigest+`"`, string(fields["manifest_digest"]))
			require.NotEmpty(t, fields["workspace_cursor"])
		})
	}
}

func TestCandidateAttemptHeadersRequireOneCanonicalSensitiveCredentialAndConnection(t *testing.T) {
	t.Parallel()
	credential := model.NewCredentialToken()
	connectionID := model.NewAttemptConnectionID()
	valid := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/", nil)
	valid.Header.Set(candidateAttemptCredentialHeader, credential)
	valid.Header.Set(candidateAttemptConnectionHeader, connectionID.String())
	access, err := candidateAttemptAccessHeaders(valid)
	require.NoError(t, err, "valid headers = %#v", access)
	require.Equal(t, credential, access.ContinuityCredential, "valid headers = %#v", access)
	require.Equal(t, connectionID, access.ConnectionID, "valid headers = %#v", access)

	tests := []struct {
		name string
		set  func(http.Header)
	}{
		{name: "missing credential", set: func(header http.Header) { header.Set(candidateAttemptConnectionHeader, connectionID.String()) }},
		{name: "invalid credential", set: func(header http.Header) {
			header.Set(candidateAttemptCredentialHeader, "short")
			header.Set(candidateAttemptConnectionHeader, connectionID.String())
		}},
		{name: "whitespace credential", set: func(header http.Header) {
			header.Set(candidateAttemptCredentialHeader, " "+credential)
			header.Set(candidateAttemptConnectionHeader, connectionID.String())
		}},
		{name: "duplicate credential", set: func(header http.Header) {
			header.Add(candidateAttemptCredentialHeader, credential)
			header.Add(candidateAttemptCredentialHeader, credential)
			header.Set(candidateAttemptConnectionHeader, connectionID.String())
		}},
		{name: "missing connection", set: func(header http.Header) { header.Set(candidateAttemptCredentialHeader, credential) }},
		{name: "invalid connection", set: func(header http.Header) {
			header.Set(candidateAttemptCredentialHeader, credential)
			header.Set(candidateAttemptConnectionHeader, "bad")
		}},
		{name: "duplicate connection", set: func(header http.Header) {
			header.Set(candidateAttemptCredentialHeader, credential)
			header.Add(candidateAttemptConnectionHeader, connectionID.String())
			header.Add(candidateAttemptConnectionHeader, connectionID.String())
		}},
	}
	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			request := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/", nil)
			test.set(request.Header)
			_, headerErr := candidateAttemptAccessHeaders(request)
			require.Error(t, headerErr, "invalid candidate Attempt headers were accepted")
		})
	}
}

func TestExamAttemptManagerCursorIsOpaqueStrictAndVersioned(t *testing.T) {
	t.Parallel()
	want := examAttemptManagerCursor{CreatedAt: time.Date(2026, time.August, 17, 9, 0, 0, 123000000, time.UTC), ID: model.NewExamAttemptID()}
	encoded, err := encodeExamAttemptManagerCursor(want)
	require.NoError(t, err)
	require.NotEmpty(t, encoded)
	require.False(t, strings.ContainsAny(encoded, "+/="))
	got, err := decodeExamAttemptManagerCursor(encoded)
	require.NoError(t, err, "round trip")
	require.Equal(t, want, got, "round trip")
	for _, invalid := range []string{"", "%%%", encoded + "trailing"} {
		_, decodeErr := decodeExamAttemptManagerCursor(invalid)
		require.Error(t, decodeErr, "invalid cursor %q was accepted", invalid)
	}
}

func TestCandidateWorkspaceCursorContainsNoReversiblePath(t *testing.T) {
	t.Parallel()
	want := candidateWorkspaceCursor{ExpectedCursor: 7, ID: model.NewAttemptWorkspaceEntryID()}
	encoded, err := encodeCandidateWorkspaceCursor(want)
	require.NoError(t, err)
	got, err := decodeCandidateWorkspaceCursor(encoded)
	require.NoError(t, err, "round trip")
	require.Equal(t, want, got, "round trip")
	decoded, err := base64.RawURLEncoding.Strict().DecodeString(encoded)
	require.NoError(t, err, "cursor exposes Workspace path")
	require.NotContains(t, string(decoded), "cmd/main.go", "cursor exposes Workspace path")
	require.NotContains(t, string(decoded), `"path"`, "cursor exposes Workspace path")
	_, encodeErr := encodeCandidateWorkspaceCursor(candidateWorkspaceCursor{ExpectedCursor: -1, ID: want.ID})
	require.Error(t, encodeErr, "negative Workspace cursor encoded successfully")
	withoutEntry, err := encodeCandidateWorkspaceCursor(candidateWorkspaceCursor{ExpectedCursor: want.ExpectedCursor})
	require.NoError(t, err)
	decodedWithoutEntry, err := decodeCandidateWorkspaceCursor(withoutEntry)
	require.NoError(t, err, "snapshot-only Workspace cursor = %#v", decodedWithoutEntry)
	require.Equal(t, want.ExpectedCursor, decodedWithoutEntry.ExpectedCursor, "snapshot-only Workspace cursor = %#v", decodedWithoutEntry)
	require.False(t, decodedWithoutEntry.ID.IsValid(), "snapshot-only Workspace cursor = %#v", decodedWithoutEntry)
	for _, invalid := range []string{"", "%%%"} {
		_, decodeErr := decodeCandidateWorkspaceCursor(invalid)
		require.Error(t, decodeErr, "invalid cursor %q was accepted", invalid)
	}
}

// suspendByPolicy puts an active policy Attempt Suspension on the seeded
// Attempt, as a lost secure connection would.
func suspendByPolicy(f *attemptFixture, revision, generation int64) *store.ExamAttemptSuspensionView {
	f.moveAttemptTo(model.ExamAttemptSuspended, revision)
	f.attemptStore.activeSuspension = &store.ExamAttemptSuspensionView{ID: model.NewAttemptSuspensionID(), AttemptID: f.attempt.ID,
		ParticipationID: f.participation.ID, FlagID: model.NewIntegrityFlagID(), Generation: generation,
		State: model.AttemptSuspensionActive, Source: model.AttemptSuspensionSourcePolicy,
		CandidateReason: model.AttemptSuspensionCandidateReasonSecureContinuityLost, StartedAt: f.attempt.UpdatedAt}
	return f.attemptStore.activeSuspension
}

func TestManagerReallowExamAttemptUsesExactSuspensionAndNeverReturnsPrivateReason(t *testing.T) {
	t.Parallel()
	f := newAttemptFixture(t)
	suspensionID := suspendByPolicy(f, 2, 1).ID
	httpAPI := f.api(t, examAttemptResource(f.service))
	request := newJSONRequest(http.MethodPost, f.managerPath("/reallow"), strings.NewReader(`{"suspension_id":"`+suspensionID.String()+`","expected_revision":2,"reason":"manager verified connectivity"}`))
	request.Header.Set("Authorization", "Bearer credential")
	request.Header.Set("Idempotency-Key", "reallow-once")
	response := httptest.NewRecorder()
	httpAPI.ServeHTTP(response, request)
	require.Equal(t, http.StatusOK, response.Code, "status/cache=%d/%q body=%s", response.Code, response.Header().Get("Cache-Control"), response.Body.String())
	require.Equal(t, "no-store", response.Header().Get("Cache-Control"), "status/cache=%d/%q body=%s", response.Code, response.Header().Get("Cache-Control"), response.Body.String())
	require.Len(t, f.attemptStore.reallows, 1)
	reallow := f.attemptStore.reallows[0]
	require.Equal(t, suspensionID, reallow.SuspensionID)
	require.Equal(t, int64(2), reallow.ExpectedRevision)
	require.Equal(t, "manager verified connectivity", reallow.PrivateReason)
	require.Equal(t, sha256.Sum256([]byte("reallow-once")), f.attemptStore.lastCommand(t).KeyDigest)
	require.NotContains(t, response.Body.String(), "manager verified connectivity")
	require.NotContains(t, response.Body.String(), "private")
	require.NotContains(t, response.Body.String(), "replayed")
}

func TestManagerSuspendExamAttemptUsesRevisionFenceAndNeverReturnsPrivateReason(t *testing.T) {
	t.Parallel()
	f := newAttemptFixture(t)
	f.moveAttemptTo(model.ExamAttemptActive, 2)
	httpAPI := f.api(t, examAttemptResource(f.service))
	path := f.managerPath("/suspend")
	request := newJSONRequest(http.MethodPost, path, strings.NewReader(`{"expected_revision":2,"reason":"candidate left the room"}`))
	request.Header.Set("Authorization", "Bearer credential")
	request.Header.Set("Idempotency-Key", "kick-once")
	response := httptest.NewRecorder()
	httpAPI.ServeHTTP(response, request)
	require.Equal(t, http.StatusOK, response.Code, "status/cache=%d/%q body=%s", response.Code, response.Header().Get("Cache-Control"), response.Body.String())
	require.Equal(t, "no-store", response.Header().Get("Cache-Control"), "status/cache=%d/%q body=%s", response.Code, response.Header().Get("Cache-Control"), response.Body.String())
	require.Len(t, f.attemptStore.suspensions, 1)
	suspension := f.attemptStore.suspensions[0]
	require.Equal(t, f.attempt.ID, suspension.AttemptID)
	require.Equal(t, int64(2), suspension.ExpectedRevision)
	require.Equal(t, "candidate left the room", suspension.PrivateReason)
	require.Equal(t, sha256.Sum256([]byte("kick-once")), f.attemptStore.lastCommand(t).KeyDigest)
	body := response.Body.String()
	require.NotContains(t, body, "candidate left the room")
	require.NotContains(t, body, "private")
	require.NotContains(t, body, "replayed")
	require.Contains(t, body, `"source":"manual"`)
	require.Contains(t, body, `"candidate_reason":"manager_suspended"`)
	require.Contains(t, body, `"connection_closed":true`)
	missing := newJSONRequest(http.MethodPost, path, strings.NewReader(`{"expected_revision":0,"reason":"x"}`))
	missing.Header.Set("Authorization", "Bearer credential")
	missing.Header.Set("Idempotency-Key", "kick-invalid")
	invalid := httptest.NewRecorder()
	httpAPI.ServeHTTP(invalid, missing)
	require.Equal(t, http.StatusBadRequest, invalid.Code, "invalid revision status = %d body=%s", invalid.Code, invalid.Body.String())
}

func TestManagerReplaceEnvironmentNamesTheIncarnationSeen(t *testing.T) {
	t.Parallel()
	f := newAttemptFixture(t)
	httpAPI := f.api(t, examAttemptResource(f.service))
	path := f.managerPath("/environment/replace")
	send := func(body, key string) *httptest.ResponseRecorder {
		request := newJSONRequest(http.MethodPost, path, strings.NewReader(body))
		request.Header.Set("Authorization", "Bearer credential")
		request.Header.Set("Idempotency-Key", key)
		response := httptest.NewRecorder()
		httpAPI.ServeHTTP(response, request)
		return response
	}
	response := send(`{"expected_incarnation":0}`, "replace-once")
	require.Equal(t, http.StatusOK, response.Code, "body=%s", response.Body.String())
	require.Equal(t, "no-store", response.Header().Get("Cache-Control"))
	require.Len(t, f.environmentStore.replacements, 1)
	require.Equal(t, f.attempt.ID, f.environmentStore.replacements[0].AttemptID)
	require.Equal(t, int64(0), f.environmentStore.replacements[0].ExpectedIncarnation)
	require.Equal(t, sha256.Sum256([]byte("replace-once")), f.environmentStore.commands[0].KeyDigest)
	require.Contains(t, response.Body.String(), `"replace_before":1`)
	require.Equal(t, http.StatusBadRequest, send(`{"expected_incarnation":-1}`, "replace-negative").Code)
}

func TestManagerScreenClipRequestNeverReturnsItsPrivateReason(t *testing.T) {
	t.Parallel()
	f := newAttemptFixture(t)
	httpAPI := f.api(t, examAttemptResource(f.service))
	path := f.managerPath("/screen-clips")
	send := func(body, key string) *httptest.ResponseRecorder {
		request := newJSONRequest(http.MethodPost, path, strings.NewReader(body))
		request.Header.Set("Authorization", "Bearer credential")
		request.Header.Set("Idempotency-Key", key)
		response := httptest.NewRecorder()
		httpAPI.ServeHTTP(response, request)
		return response
	}

	response := send(`{"from_session_start":true,"end":"2026-11-12T10:00:00Z","reason":"second voice heard"}`, "clip-once")
	require.Equal(t, http.StatusCreated, response.Code, "body=%s", response.Body.String())
	require.Equal(t, "no-store", response.Header().Get("Cache-Control"))
	requested := f.attemptStore.clipRequests[0].Clip
	require.True(t, requested.Range.FromSessionStart)
	require.Equal(t, "second voice heard", requested.PrivateReason)
	require.Equal(t, sha256.Sum256([]byte("clip-once")), f.attemptStore.lastCommand(t).KeyDigest)
	body := response.Body.String()
	require.NotContains(t, body, "second voice heard")
	require.NotContains(t, body, "reason")
	require.Contains(t, body, `"state":"requested"`)
	require.Contains(t, body, `"from_session_start":true`)

	response = send(`{"from_session_start":false,"start":"2026-11-12T09:58:00Z","end":"2026-11-12T10:00:00Z","reason":"r"}`, "clip-range")
	require.Equal(t, http.StatusCreated, response.Code, "body=%s", response.Body.String())
	requested = f.attemptStore.clipRequests[1].Clip
	require.False(t, requested.Range.FromSessionStart)
	require.Equal(t, time.Date(2026, 11, 12, 9, 58, 0, 0, time.UTC), requested.Range.Start)

	for name, body := range map[string]string{
		"both start forms": `{"from_session_start":true,"start":"2026-11-12T09:58:00Z","end":"2026-11-12T10:00:00Z","reason":"r"}`,
		"no start":         `{"from_session_start":false,"end":"2026-11-12T10:00:00Z","reason":"r"}`,
		"bad end":          `{"from_session_start":true,"end":"soon","reason":"r"}`,
	} {
		require.Equal(t, http.StatusBadRequest, send(body, "clip-bad").Code, name)
	}
	require.Len(t, f.attemptStore.clipRequests, 2, "an invalid request reached the Store")
}

func TestCandidateScreenClipChunksAreNumberedFromOneOnTheWire(t *testing.T) {
	t.Parallel()
	f := newAttemptFixture(t)
	clipID := f.attemptStore.deliverClip(model.ScreenClipRecording, nil)
	httpAPI := f.api(t, examAttemptResource(f.service))
	base := f.candidatePath("/screen-clip-requests/" + clipID.String() + "/upload")
	send := func(method, path string, body io.Reader) *httptest.ResponseRecorder {
		request := httptest.NewRequestWithContext(t.Context(), method, path, body)
		for name, values := range f.candidateRequest(t, method, path).Header {
			request.Header[name] = values
		}
		if body != nil && method != http.MethodPut {
			request.Header.Set("Content-Type", "application/json")
		}
		response := httptest.NewRecorder()
		httpAPI.ServeHTTP(response, request)
		return response
	}

	hashes := `["` + strings.Repeat("a", 64) + `","` + digestOf([]byte("0123456789")) + `"]`
	begun := send(http.MethodPost, base, strings.NewReader(`{"media_type":"video/mp4","size_bytes":8388618,"chunk_sha256":`+hashes+`}`))
	require.Equal(t, http.StatusOK, begun.Code, "body=%s", begun.Body.String())
	require.Equal(t, int64(8388618), f.attemptStore.clipUploads[0].Upload.SizeBytes)
	require.Contains(t, begun.Body.String(), `"chunk_count":2`)
	require.Contains(t, begun.Body.String(), `"received_chunks":[]`)

	// The Store already holds the first chunk from an earlier upload.
	f.attemptStore.received = []int{0}
	status := send(http.MethodGet, base, nil)
	require.Equal(t, http.StatusOK, status.Code, "body=%s", status.Body.String())
	require.Contains(t, status.Body.String(), `"received_chunks":[1]`, "the first chunk is chunk 1")

	chunk := send(http.MethodPut, base+"/chunks/2", strings.NewReader("0123456789"))
	require.Equal(t, http.StatusOK, chunk.Code, "body=%s", chunk.Body.String())
	require.Len(t, f.attemptStore.clipChunks, 1)
	require.Equal(t, 1, f.attemptStore.clipChunks[0].Index, "chunk 2 is the second chunk")
	require.Equal(t, int64(10), f.attemptStore.clipChunks[0].SizeBytes)
	require.Equal(t, "0123456789", string(f.content.chunks[1]))
	require.Contains(t, chunk.Body.String(), `"chunk":2`)

	require.Equal(t, http.StatusNotFound, send(http.MethodPut, base+"/chunks/0", strings.NewReader("x")).Code,
		"there is no chunk 0")

	signature := strings.Repeat("s", model.DesktopSignatureLength)
	completed := send(http.MethodPost, base+"/complete",
		strings.NewReader(`{"content_digest":"`+strings.Repeat("d", 64)+`","signature":"`+signature+`"}`))
	require.Equal(t, http.StatusOK, completed.Code, "body=%s", completed.Body.String())
	require.Equal(t, strings.Repeat("d", 64), f.attemptStore.clipCompletions[0].ContentDigest)
	require.Equal(t, signature, f.attemptStore.clipCompletions[0].Signature)
	require.Contains(t, completed.Body.String(), `"state":"ready"`)
}

func TestManagersPlayScreenClipsByRange(t *testing.T) {
	t.Parallel()
	f := newAttemptFixture(t)
	content := "0123456789abcdefghij"
	clipID := f.attemptStore.deliverClip(model.ScreenClipReady, &model.ScreenClipUpload{MediaType: "video/mp4",
		SizeBytes: int64(len(content)), ChunkSHA256: []string{digestOf([]byte(content))}})
	f.content.chunks[0] = []byte(content)
	httpAPI := f.api(t, examAttemptResource(f.service))
	path := f.managerPath("/screen-clips/" + clipID.String() + "/content")
	play := func(rangeHeader string) *httptest.ResponseRecorder {
		request := f.managerRequest(t, http.MethodGet, path, nil)
		if rangeHeader != "" {
			request.Header.Set("Range", rangeHeader)
		}
		response := httptest.NewRecorder()
		httpAPI.ServeHTTP(response, request)
		return response
	}

	whole := play("")
	require.Equal(t, http.StatusOK, whole.Code, "body=%s", whole.Body.String())
	require.Equal(t, content, whole.Body.String())
	require.Equal(t, "bytes", whole.Header().Get("Accept-Ranges"))
	require.Equal(t, "video/mp4", whole.Header().Get("Content-Type"))

	require.Equal(t, 1, f.attemptStore.clipReads, "a request reads the clip from the Store once")

	part := play("bytes=10-14")
	require.Equal(t, http.StatusPartialContent, part.Code, "body=%s", part.Body.String())
	require.Equal(t, 2, f.attemptStore.clipReads, "a range request reads the clip from the Store once")
	require.Equal(t, "abcde", part.Body.String())
	require.Equal(t, "bytes 10-14/20", part.Header().Get("Content-Range"))
	require.Equal(t, "5", part.Header().Get("Content-Length"))

	tail := play("bytes=-3")
	require.Equal(t, http.StatusPartialContent, tail.Code)
	require.Equal(t, "hij", tail.Body.String())

	require.Equal(t, http.StatusBadRequest, play("bytes=30-").Code, "a range past the end is refused")
	require.Equal(t, http.StatusBadRequest, play("bytes=0-1,4-5").Code, "multiple ranges are refused")
}

func TestManagerEndExamAttemptUsesRevisionFenceAndReturnsOnlySafeReceipt(t *testing.T) {
	t.Parallel()
	f := newAttemptFixture(t)
	f.moveAttemptTo(model.ExamAttemptActive, 1)
	httpAPI := f.api(t, examAttemptResource(f.service))
	path := f.managerPath("/end")
	privateReason := "candidate requested an assisted early end"
	request := newJSONRequest(http.MethodPost, path, strings.NewReader(`{"expected_revision":1,"reason":"`+privateReason+`"}`))
	request.Header.Set("Authorization", "Bearer credential")
	request.Header.Set("Idempotency-Key", "manager-end-once")
	response := httptest.NewRecorder()
	httpAPI.ServeHTTP(response, request)
	require.Equal(t, http.StatusOK, response.Code, "status/cache=%d/%q body=%s", response.Code, response.Header().Get("Cache-Control"), response.Body.String())
	require.Equal(t, "no-store", response.Header().Get("Cache-Control"), "status/cache=%d/%q body=%s", response.Code, response.Header().Get("Cache-Control"), response.Body.String())
	require.Len(t, f.submissionStore.endPreparation, 1)
	end := f.submissionStore.endPreparation[0]
	require.Equal(t, f.examID, end.ExamID)
	require.Equal(t, f.sitting.ID, end.SittingID)
	require.Equal(t, f.attempt.ID, end.AttemptID)
	require.Equal(t, int64(1), end.ExpectedAttemptRevision)
	require.Equal(t, privateReason, end.PrivateReason)
	require.Len(t, f.submissionStore.ends, 1)
	require.Equal(t, sha256.Sum256([]byte("manager-end-once")), f.submissionStore.lastCommand(t).KeyDigest)
	require.NotContains(t, response.Body.String(), privateReason)
	require.NotContains(t, response.Body.String(), "private")
	require.Contains(t, response.Body.String(), f.submissionStore.ends[0].SubmissionID.String())
	require.Contains(t, response.Body.String(), `"receipt_code":"MBY3-KBMN-EB83-TFW1"`)

	invalid := newJSONRequest(http.MethodPost, path, strings.NewReader(`{"expected_revision":1,"reason":"valid","extra":true}`))
	invalid.Header.Set("Authorization", "Bearer credential")
	invalid.Header.Set("Idempotency-Key", "manager-end-invalid")
	invalidResponse := httptest.NewRecorder()
	httpAPI.ServeHTTP(invalidResponse, invalid)
	require.Equal(t, http.StatusBadRequest, invalidResponse.Code, "unknown member status=%d body=%s", invalidResponse.Code, invalidResponse.Body.String())
}

func TestManagerAttemptRefetchExposesPrivateFreeActiveSuspensionIdentity(t *testing.T) {
	t.Parallel()
	f := newAttemptFixture(t)
	suspension := suspendByPolicy(f, 2, 2)
	httpAPI := f.api(t, examAttemptResource(f.service))
	response := httptest.NewRecorder()
	httpAPI.ServeHTTP(response, f.managerRequest(t, http.MethodGet, f.managerPath(""), nil))
	require.Equal(t, http.StatusOK, response.Code, "manager suspension response")
	require.Contains(t, response.Body.String(), suspension.ID.String(), "manager suspension response")
	require.Contains(t, response.Body.String(), `"candidate_reason":"secure_connectivity_lost"`, "manager suspension response")
	require.NotContains(t, response.Body.String(), "private_reason", "manager suspension response")
}

func TestCandidateWorkspaceJournalPagesExactProtectedQueries(t *testing.T) {
	t.Parallel()
	f := newAttemptFixture(t)
	item := f.workspaceStore.items[0]
	change := func(cursor int64) model.AttemptWorkspaceJournalEntry {
		return model.AttemptWorkspaceJournalEntry{WorkspaceID: f.workspace.ID, Cursor: cursor, EntryID: item.EntryID,
			EntryKind: item.Kind, Operation: model.AttemptWorkspaceMutationReplaceFile, OldPath: item.Path,
			NewPath: item.Path, ContentVersion: item.ContentVersion, ChangedAt: attemptFixtureTime,
			Author: model.NewCandidateWorkspaceAuthor(f.connection.ID)}
	}
	f.workspaceStore.cursor, f.workspaceStore.journal = 5, []model.AttemptWorkspaceJournalEntry{change(4), change(5)}
	httpAPI := f.api(t, examAttemptResource(f.service))
	base := f.candidatePath("/workspace")

	journalResponse := httptest.NewRecorder()
	httpAPI.ServeHTTP(journalResponse, f.candidateRequest(t, http.MethodGet, base+"/changes?workspace_cursor=3&limit=20"))
	query := f.workspaceStore.journalReads[0]
	require.Equal(t, http.StatusOK, journalResponse.Code, "journal=%d %s query=%#v", journalResponse.Code, journalResponse.Body.String(), query)
	require.Equal(t, f.candidateAccess(), query.Access, "journal=%d %s query=%#v", journalResponse.Code, journalResponse.Body.String(), query)
	require.Equal(t, int64(3), query.AfterCursor, "journal=%d %s query=%#v", journalResponse.Code, journalResponse.Body.String(), query)
	require.Equal(t, 20, query.Limit, "journal=%d %s query=%#v", journalResponse.Code, journalResponse.Body.String(), query)
	require.Contains(t, journalResponse.Body.String(), `"current_cursor":5`, "journal=%d %s query=%#v", journalResponse.Code, journalResponse.Body.String(), query)
	require.Contains(t, journalResponse.Body.String(), `"author":{"kind":"candidate","ref":"`+f.connection.ID.String()+`"}`,
		"every change names its author")
	require.Contains(t, journalResponse.Header().Get("Cache-Control"), "no-store", "journal=%d %s query=%#v", journalResponse.Code, journalResponse.Body.String(), query)
	recursive := &f.workspaceStore.journal[1]
	recursive.Operation = model.AttemptWorkspaceMutationDeleteEntry
	recursive.EntryKind = model.StarterWorkspaceEntryDirectory
	recursive.OldPath = "src"
	recursive.NewPath, recursive.ContentVersion = "", ""
	recursive.Recursive = true
	recursive.Author = model.NewEnvironmentWorkspaceAuthor(2, "terminal:1")
	journalResponse = httptest.NewRecorder()
	httpAPI.ServeHTTP(journalResponse, f.candidateRequest(t, http.MethodGet, base+"/changes?workspace_cursor=3&limit=20"))
	require.Equal(t, http.StatusOK, journalResponse.Code, "recursive journal")
	require.Contains(t, journalResponse.Body.String(), `"recursive":true`, "recursive journal")
	require.Contains(t, journalResponse.Body.String(), `"author":{"kind":"environment","ref":"2/terminal:1"}`,
		"an environment change names its author and the process that wrote it")
	require.Contains(t, journalResponse.Body.String(), `"old_path":"src"`, "recursive journal")
	journalResponse = httptest.NewRecorder()
	httpAPI.ServeHTTP(journalResponse, f.candidateRequest(t, http.MethodGet, base+"/changes?workspace_cursor=3&limit=1"))
	require.Equal(t, http.StatusOK, journalResponse.Code, "paged journal")
	var journal candidateWorkspaceJournalResponse
	require.NoError(t, json.Unmarshal(journalResponse.Body.Bytes(), &journal))
	next, err := route.DecodeCursor(journal.NextCursor, candidateWorkspaceJournalCursorSpec())
	require.NoError(t, err, "next_cursor")
	require.Equal(t, f.workspaceStore.journal[0].Cursor, next.After, "continuation after the last change of the page")
	journalResponse = httptest.NewRecorder()
	httpAPI.ServeHTTP(journalResponse, f.candidateRequest(t, http.MethodGet, base+"/changes?cursor="+journal.NextCursor))
	require.Equal(t, http.StatusOK, journalResponse.Code, "continued journal")
	require.Equal(t, next.After, f.workspaceStore.journalReads[len(f.workspaceStore.journalReads)-1].AfterCursor, "continued journal")
	reads := len(f.workspaceStore.journalReads)
	journalResponse = httptest.NewRecorder()
	httpAPI.ServeHTTP(journalResponse, f.candidateRequest(t, http.MethodGet, base+"/changes?workspace_cursor=3&cursor="+journal.NextCursor))
	require.Equal(t, http.StatusBadRequest, journalResponse.Code, "a continuation cannot restart at another Workspace Cursor")
	require.Len(t, f.workspaceStore.journalReads, reads, "a conflicting continuation reached the Store")
}

func TestCandidateWorkspaceBatchMapsEveryMutationAndStreamsBodiesInOrder(t *testing.T) {
	t.Parallel()
	f := newAttemptFixture(t)
	httpAPI := f.api(t, examAttemptResource(f.service))
	entryID := f.workspaceStore.items[0].EntryID
	version := f.workspaceStore.items[0].ContentVersion
	directoryID := model.NewAttemptWorkspaceEntryID()
	manifest := f.workspaceManifest(4,
		map[string]any{"operation": "create_directory", "path": "src"},
		map[string]any{"operation": "create_file", "path": "src/a.txt", "media_type": "text/plain", "size": 5, "sha256": digestOf([]byte("hello"))},
		map[string]any{"operation": "replace_file", "entry_id": entryID.String(), "expected_path": "cmd/main.go",
			"expected_content_version": version.String(), "media_type": "text/x-go", "size": 3, "sha256": digestOf([]byte("new"))},
		map[string]any{"operation": "move_entry", "entry_id": entryID.String(), "expected_path": "cmd/main.go", "destination_path": "src/main.go"},
		map[string]any{"operation": "delete_entry", "entry_id": directoryID.String(), "expected_path": "old", "recursive": true},
	)
	body, contentType := candidateWorkspaceBatch(t, manifest, []byte("hello"), []byte("new"))
	response := httptest.NewRecorder()
	httpAPI.ServeHTTP(response, candidateWorkspaceBatchRequest(f, body, contentType, "batch-once"))
	assertCandidateWorkspaceMutationHTTP(t, f, response, http.StatusOK)

	require.Len(t, f.workspaceStore.batches, 1)
	batch := f.workspaceStore.lastBatch(t)
	require.Equal(t, store.ExamAttemptWorkspaceWriteAccess(f.editorAccess()), batch.Access,
		"the batch is fenced to the live Participation through the Connection and only the credential's hash")
	require.Equal(t, sha256.Sum256([]byte("batch-once")), f.workspaceStore.commands[0].KeyDigest)
	require.NotNil(t, batch.ExpectedWorkspaceCursor)
	require.Equal(t, int64(4), *batch.ExpectedWorkspaceCursor)
	require.Len(t, batch.Mutations, 5)
	for index, mutation := range batch.Mutations {
		if index < 2 {
			require.True(t, mutation.EntryID.IsValid(), "the server names created entry %d", index)
		}
		require.Equal(t, index == 1 || index == 2, mutation.ObjectID.IsValid(), "only file mutations carry a body: %d", index)
		mutation.EntryID, mutation.ObjectID = "", ""
		batch.Mutations[index] = mutation
	}
	require.Equal(t, []store.ExamAttemptWorkspaceMutation{
		{Operation: model.AttemptWorkspaceMutationCreateDirectory, DestinationPath: "src"},
		{Operation: model.AttemptWorkspaceMutationCreateFile, DestinationPath: "src/a.txt"},
		{Operation: model.AttemptWorkspaceMutationReplaceFile, ExpectedPath: "cmd/main.go", ExpectedContentVersion: version},
		{Operation: model.AttemptWorkspaceMutationMoveEntry, ExpectedPath: "cmd/main.go", DestinationPath: "src/main.go"},
		{Operation: model.AttemptWorkspaceMutationDeleteEntry, ExpectedPath: "old", Recursive: true},
	}, batch.Mutations)
	require.Equal(t, [][]byte{[]byte("hello"), []byte("new")}, f.content.staged, "bodies reach the content store in manifest order")
	require.Len(t, f.workspaceStore.ready, 1)
	ready := f.workspaceStore.ready[0].Objects
	require.Equal(t, model.AttemptWorkspaceContent{MediaType: "text/plain", SizeBytes: 5, SHA256: digestOf([]byte("hello"))}, ready[0].Content)
	require.Equal(t, model.AttemptWorkspaceContent{MediaType: "text/x-go", SizeBytes: 3, SHA256: digestOf([]byte("new"))}, ready[1].Content)

	var shape map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(response.Body.Bytes(), &shape))
	require.ElementsMatch(t, []string{"workspace_id", "first_workspace_cursor", "workspace_cursor", "changes"}, mapKeys(shape))
	var result candidateWorkspaceBatchResponse
	require.NoError(t, json.Unmarshal(response.Body.Bytes(), &result))
	require.Equal(t, f.workspace.ID.String(), result.WorkspaceID)
	require.Equal(t, int64(5), result.FirstWorkspaceCursor)
	require.Equal(t, int64(9), result.WorkspaceCursor)
	require.Len(t, result.Changes, 5)
	for index, change := range result.Changes {
		require.Equal(t, int64(5+index), change.Cursor, "change %d", index)
		require.Equal(t, string(batch.Mutations[index].Operation), change.Operation, "change %d", index)
		require.Equal(t, model.FormatRFC3339(batch.AuditAt), change.ChangedAt, "change %d", index)
	}
	require.Equal(t, "src", result.Changes[0].NewPath)
	require.Equal(t, string(model.StarterWorkspaceEntryDirectory), result.Changes[0].Kind)
	require.Equal(t, entryID.String(), result.Changes[3].EntryID)
	require.Equal(t, "cmd/main.go", result.Changes[3].OldPath)
	require.Equal(t, "src/main.go", result.Changes[3].NewPath)
	require.True(t, result.Changes[4].Recursive)
	require.Contains(t, response.Body.String(), `"recursive":true`)
}

func TestCandidateWorkspaceBatchWithoutFilesNeedsNoContentPart(t *testing.T) {
	t.Parallel()
	f := newAttemptFixture(t)
	httpAPI := f.api(t, examAttemptResource(f.service))
	body, contentType := candidateWorkspaceBatch(t, f.workspaceManifest(-1, map[string]any{"operation": "create_directory", "path": "src"}))
	response := httptest.NewRecorder()
	httpAPI.ServeHTTP(response, candidateWorkspaceBatchRequest(f, body, contentType, "mkdir-once"))
	assertCandidateWorkspaceMutationHTTP(t, f, response, http.StatusOK)
	require.Equal(t, "src", f.workspaceStore.lastBatch(t).Mutations[0].DestinationPath)
	require.Nil(t, f.workspaceStore.lastBatch(t).ExpectedWorkspaceCursor)
	require.Empty(t, f.content.staged)

	empty := f.workspaceManifest(-1, map[string]any{"operation": "create_file", "path": "empty.txt",
		"media_type": "text/plain", "size": 0, "sha256": digestOf(nil)})
	body, contentType = candidateWorkspaceBatch(t, empty, []byte{})
	response = httptest.NewRecorder()
	httpAPI.ServeHTTP(response, candidateWorkspaceBatchRequest(f, body, contentType, "empty-once"))
	assertCandidateWorkspaceMutationHTTP(t, f, response, http.StatusOK)
	require.Equal(t, int64(0), f.workspaceStore.ready[0].Objects[0].Content.SizeBytes, "an empty file declares size zero")
	require.Equal(t, [][]byte{{}}, f.content.staged)
}

func TestCandidateWorkspaceBatchRejectsMalformedRequestsBeforeTheApplication(t *testing.T) {
	t.Parallel()
	participationID := model.NewAttemptParticipationID().String()
	entryID := model.NewAttemptWorkspaceEntryID().String()
	directory := map[string]any{"operation": "create_directory", "path": "src"}
	file := func(operation string, extra map[string]any) map[string]any {
		mutation := map[string]any{"operation": operation, "media_type": "text/plain", "size": 1, "sha256": strings.Repeat("a", 64)}
		if operation == "create_file" {
			mutation["path"] = "a.txt"
		} else {
			mutation["entry_id"], mutation["expected_path"] = entryID, "a.txt"
		}
		for key, value := range extra {
			if value == nil {
				delete(mutation, key)
				continue
			}
			mutation[key] = value
		}
		return mutation
	}
	manifest := func(mutations ...any) map[string]any {
		if mutations == nil {
			mutations = []any{}
		}
		return map[string]any{"participation_id": participationID, "generation": 1, "mutations": mutations}
	}
	tooMany := make([]any, model.AttemptWorkspaceBatchMaximumMutations+1)
	for index := range tooMany {
		tooMany[index] = map[string]any{"operation": "create_directory", "path": fmt.Sprintf("d%d", index)}
	}
	validManifest, err := json.Marshal(manifest(directory))
	require.NoError(t, err)
	oversized := `{"participation_id":"` + participationID + `","generation":1,"mutations":[{"operation":"create_directory","path":"src"}]` +
		strings.Repeat(" ", model.AttemptWorkspaceBatchManifestMaximumBytes) + `}`

	for _, test := range []struct {
		name  string
		parts []candidateWorkspacePart
		raw   string
	}{
		{name: "not multipart", raw: string(validManifest)},
		{name: "missing manifest part", parts: []candidateWorkspacePart{}},
		{name: "manifest not first", parts: []candidateWorkspacePart{{name: "content", body: []byte("x")}, {name: "manifest", body: validManifest}}},
		{name: "manifest sent as a file", parts: []candidateWorkspacePart{{name: "manifest", filename: "manifest.json", body: validManifest}}},
		{name: "manifest over 512 KiB", parts: []candidateWorkspacePart{{name: "manifest", body: []byte(oversized)}}},
		{name: "duplicate top-level key", parts: []candidateWorkspacePart{{name: "manifest", body: []byte(
			`{"participation_id":"` + participationID + `","generation":1,"generation":2,"mutations":[{"operation":"create_directory","path":"src"}]}`)}}},
		{name: "trailing JSON value", parts: []candidateWorkspacePart{{name: "manifest", body: append(append([]byte{}, validManifest...), []byte(`{}`)...)}}},
		{name: "unknown top-level field", parts: candidateWorkspaceManifestParts(t, map[string]any{"participation_id": participationID, "generation": 1, "mutations": []any{directory}, "extra": true})},
		{name: "unknown mutation field", parts: candidateWorkspaceManifestParts(t, manifest(map[string]any{"operation": "create_directory", "path": "src", "mode": "0755"}))},
		{name: "no mutations", parts: candidateWorkspaceManifestParts(t, manifest())},
		{name: "129 mutations", parts: candidateWorkspaceManifestParts(t, manifest(tooMany...))},
		{name: "unknown operation", parts: candidateWorkspaceManifestParts(t, manifest(map[string]any{"operation": "chmod_entry", "entry_id": entryID}))},
		{name: "create with entry_id", parts: candidateWorkspaceManifestParts(t, manifest(map[string]any{"operation": "create_directory", "path": "src", "entry_id": entryID}))},
		{name: "create with destination_path", parts: candidateWorkspaceManifestParts(t, manifest(map[string]any{"operation": "create_directory", "path": "src", "destination_path": "dst"}))},
		{name: "non-create with path", parts: candidateWorkspaceManifestParts(t, manifest(map[string]any{"operation": "move_entry", "entry_id": entryID, "path": "a", "expected_path": "a", "destination_path": "b"}))},
		{name: "non-create with invalid entry_id", parts: candidateWorkspaceManifestParts(t, manifest(map[string]any{"operation": "delete_entry", "entry_id": "not-an-id", "expected_path": "a"}))},
		{name: "invalid expected_content_version", parts: candidateWorkspaceManifestParts(t, manifest(file("replace_file", map[string]any{"expected_content_version": "v1"})))},
		{name: "create_file without size", parts: candidateWorkspaceManifestParts(t, manifest(file("create_file", map[string]any{"size": nil})), []byte("x"))},
		{name: "replace_file without size", parts: candidateWorkspaceManifestParts(t, manifest(file("replace_file", map[string]any{"size": nil})), []byte("x"))},
		{name: "non-file operation with size", parts: candidateWorkspaceManifestParts(t, manifest(map[string]any{"operation": "create_directory", "path": "src", "size": 0}))},
		{name: "content part in a batch without files", parts: candidateWorkspaceManifestParts(t, manifest(directory), []byte("x"))},
		{name: "any part in a batch without files", parts: append(candidateWorkspaceManifestParts(t, manifest(directory)), candidateWorkspacePart{name: "note", body: []byte("x")})},
		{name: "null expected_workspace_cursor", parts: candidateWorkspaceManifestParts(t, map[string]any{"participation_id": participationID, "generation": 1,
			"expected_workspace_cursor": nil, "mutations": []any{map[string]any{"operation": "delete_entry", "entry_id": entryID, "expected_path": "src", "recursive": true}}})},
		{name: "invalid participation", parts: candidateWorkspaceManifestParts(t, map[string]any{"participation_id": "bad", "generation": 1, "mutations": []any{directory}})},
		{name: "zero generation", parts: candidateWorkspaceManifestParts(t, map[string]any{"participation_id": participationID, "generation": 0, "mutations": []any{directory}})},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			f := newAttemptFixture(t)
			httpAPI := f.api(t, examAttemptResource(f.service))
			var request *http.Request
			if test.parts == nil {
				request = candidateWorkspaceBatchRequest(f, strings.NewReader(test.raw), "application/json", "malformed")
			} else {
				body, contentType := candidateWorkspaceMultipart(t, test.parts...)
				request = candidateWorkspaceBatchRequest(f, body, contentType, "malformed")
			}
			response := httptest.NewRecorder()
			httpAPI.ServeHTTP(response, request)
			problem := requireCandidateWorkspaceProblem(t, response, http.StatusBadRequest, "request.invalid")
			require.Empty(t, problem.Fields["item"])
			require.Equal(t, 0, f.workspaceStore.writes(), "a malformed batch reached the Store")
			require.Empty(t, f.content.staged, "a malformed batch staged content")
		})
	}
}

func TestCandidateWorkspaceBatchRefusesFileBodiesThatDoNotMatchTheManifest(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name   string
		parts  func(manifest []candidateWorkspacePart) []candidateWorkspacePart
		item   string
		bodies [][]byte
	}{
		{name: "missing content part", parts: func(manifest []candidateWorkspacePart) []candidateWorkspacePart {
			return append(manifest, candidateWorkspacePart{name: "content", body: []byte("a")})
		}, item: "1", bodies: [][]byte{[]byte("a")}},
		{name: "part not named content", parts: func(manifest []candidateWorkspacePart) []candidateWorkspacePart {
			return append(manifest, candidateWorkspacePart{name: "content", body: []byte("a")}, candidateWorkspacePart{name: "body", body: []byte("b")})
		}, item: "1", bodies: [][]byte{[]byte("a")}},
		{name: "extra part after the last body", parts: func(manifest []candidateWorkspacePart) []candidateWorkspacePart {
			return append(manifest, candidateWorkspacePart{name: "content", body: []byte("a")}, candidateWorkspacePart{name: "content", body: []byte("b")},
				candidateWorkspacePart{name: "content", body: []byte("c")})
		}, item: "1", bodies: [][]byte{[]byte("a")}},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			f := newAttemptFixture(t)
			twoFiles := f.workspaceManifest(-1,
				map[string]any{"operation": "create_file", "path": "a.txt", "media_type": "text/plain", "size": 1, "sha256": digestOf([]byte("a"))},
				map[string]any{"operation": "create_file", "path": "b.txt", "media_type": "text/plain", "size": 1, "sha256": digestOf([]byte("b"))},
			)
			httpAPI := f.api(t, examAttemptResource(f.service))
			body, contentType := candidateWorkspaceMultipart(t, test.parts(candidateWorkspaceManifestParts(t, twoFiles))...)
			response := httptest.NewRecorder()
			httpAPI.ServeHTTP(response, candidateWorkspaceBatchRequest(f, body, contentType, "bodies"))
			require.Empty(t, f.workspaceStore.batches, "the batch applied a body the manifest does not declare")
			require.Equal(t, test.bodies, f.content.staged)
			require.Len(t, f.workspaceStore.reclaimed, 1, "the refused batch gave back its reserved objects")
			require.Equal(t, f.workspaceStore.reservations[0].ObjectIDs, f.workspaceStore.reclaimed[0])
			problem := requireCandidateWorkspaceProblem(t, response, http.StatusBadRequest, "exam.attempt.invalid")
			require.Equal(t, "workspace_content", problem.Fields["field"])
			require.Equal(t, test.item, problem.Fields["item"])
		})
	}
}

func TestCandidateWorkspaceBatchRequiresIdempotencyAndProtectionHeaders(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name   string
		mutate func(*http.Request)
		code   string
	}{
		{name: "missing idempotency key", mutate: func(request *http.Request) { request.Header.Del("Idempotency-Key") }, code: "idempotency.key_required"},
		{name: "missing Attempt credential", mutate: func(request *http.Request) { request.Header.Del(candidateAttemptCredentialHeader) }, code: "request.invalid"},
		{name: "duplicate Attempt Connection", mutate: func(request *http.Request) {
			request.Header.Add(candidateAttemptConnectionHeader, model.NewAttemptConnectionID().String())
		}, code: "request.invalid"},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			f := newAttemptFixture(t)
			httpAPI := f.api(t, examAttemptResource(f.service))
			body, contentType := candidateWorkspaceBatch(t, f.workspaceManifest(-1, map[string]any{"operation": "create_directory", "path": "src"}))
			request := candidateWorkspaceBatchRequest(f, body, contentType, "headers")
			test.mutate(request)
			response := httptest.NewRecorder()
			httpAPI.ServeHTTP(response, request)
			_ = requireCandidateWorkspaceProblem(t, response, http.StatusBadRequest, test.code)
			require.Equal(t, "no-store", response.Header().Get("Cache-Control"))
			require.Equal(t, 0, f.workspaceStore.writes(), "an unprotected batch reached the Store")
		})
	}
}

func TestCandidateWorkspaceBatchPresentsTheRefusingItem(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name     string
		firstDir string
		storeErr error
		status   int
		code     string
		fields   map[string]string
	}{
		{name: "path conflict", firstDir: "src",
			storeErr: &store.ErrAttemptWorkspaceBatchItem{Index: 1, Err: store.NewErrConflict("attempt_workspace", "attempt_workspace_path", nil)},
			status:   http.StatusConflict, code: "exam.attempt.workspace.path_conflict", fields: map[string]string{"item": "1"}},
		{name: "invalid path", firstDir: "src/../src", status: http.StatusBadRequest, code: "exam.attempt.invalid",
			fields: map[string]string{"field": "workspace_path", "item": "0"}},
		{name: "batch capacity names no item", firstDir: "src",
			storeErr: store.NewErrConflict("attempt_workspace", "attempt_workspace_entry_limit", nil),
			status:   http.StatusConflict, code: "exam.attempt.workspace.entry_limit"},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			f := newAttemptFixture(t)
			f.workspaceStore.batchErr = test.storeErr
			httpAPI := f.api(t, examAttemptResource(f.service))
			body, contentType := candidateWorkspaceBatch(t, f.workspaceManifest(-1,
				map[string]any{"operation": "create_directory", "path": test.firstDir},
				map[string]any{"operation": "create_directory", "path": "src"}))
			response := httptest.NewRecorder()
			httpAPI.ServeHTTP(response, candidateWorkspaceBatchRequest(f, body, contentType, "refused"))
			problem := requireCandidateWorkspaceProblem(t, response, test.status, test.code)
			require.Equal(t, test.fields, problem.Fields)
		})
	}
}

func TestCandidateWorkspaceBatchForwardsTheRecursiveDeleteSnapshot(t *testing.T) {
	t.Parallel()
	zero := int64(0)
	for _, test := range []struct {
		name, fields string
		recursive    bool
		// want is the snapshot the Store is asked to fence by; nil means the
		// application refuses the batch, because only a recursive delete
		// names one and it is never negative.
		want *int64
	}{
		{name: "zero cursor", fields: `,"expected_workspace_cursor":0`, recursive: true, want: &zero},
		{name: "missing cursor", recursive: true},
		{name: "negative cursor", fields: `,"expected_workspace_cursor":-1`, recursive: true},
		{name: "cursor without recursion", fields: `,"expected_workspace_cursor":0`},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			f := newAttemptFixture(t)
			httpAPI := f.api(t, examAttemptResource(f.service))
			entryID := model.NewAttemptWorkspaceEntryID()
			raw := `{"participation_id":"` + f.participation.ID.String() + `","generation":1` + test.fields +
				`,"mutations":[{"operation":"delete_entry","entry_id":"` + entryID.String() + `","expected_path":"src"` +
				fmt.Sprintf(`,"recursive":%t}]}`, test.recursive)
			body, contentType := candidateWorkspaceMultipart(t, candidateWorkspacePart{name: "manifest", body: []byte(raw)})
			response := httptest.NewRecorder()
			httpAPI.ServeHTTP(response, candidateWorkspaceBatchRequest(f, body, contentType, "recursive"))
			if test.want == nil {
				problem := requireCandidateWorkspaceProblem(t, response, http.StatusBadRequest, "exam.attempt.invalid")
				require.Equal(t, "workspace_recursive_delete", problem.Fields["field"])
				require.Equal(t, 0, f.workspaceStore.writes(), "the application owns the cursor rule")
				return
			}
			assertCandidateWorkspaceMutationHTTP(t, f, response, http.StatusOK)
			batch := f.workspaceStore.lastBatch(t)
			require.Equal(t, test.recursive, batch.Mutations[0].Recursive)
			require.Equal(t, entryID, batch.Mutations[0].EntryID)
			require.Equal(t, test.want, batch.ExpectedWorkspaceCursor)
		})
	}
}

func TestCandidateWorkspaceBatchRefusesToPresentAResultWithoutOneChangePerMutation(t *testing.T) {
	t.Parallel()
	f := newAttemptFixture(t)
	f.workspaceStore.result = &store.ExamAttemptWorkspaceBatchResult{SittingID: f.sitting.ID, ClassID: f.sitting.ClassID,
		CandidateUserID: f.candidate.UserID, WorkspaceID: f.workspace.ID}
	httpAPI := f.api(t, examAttemptResource(f.service))
	body, contentType := candidateWorkspaceBatch(t, f.workspaceManifest(-1, map[string]any{"operation": "create_directory", "path": "src"}))
	response := httptest.NewRecorder()
	httpAPI.ServeHTTP(response, candidateWorkspaceBatchRequest(f, body, contentType, "empty-result"))
	_ = requireCandidateWorkspaceProblem(t, response, http.StatusServiceUnavailable, "exam.attempt.unavailable")
	require.Len(t, f.workspaceStore.batches, 1)
}

func TestCandidateWorkspaceSingleMutationRoutesAreGone(t *testing.T) {
	t.Parallel()
	f := newAttemptFixture(t)
	httpAPI := f.api(t, examAttemptResource(f.service))
	base := f.candidatePath("/workspace")
	entryID := f.workspaceStore.items[0].EntryID.String()
	for _, test := range []struct{ method, path string }{
		{http.MethodPost, base + "/directories"},
		{http.MethodPost, base + "/files"},
		{http.MethodPatch, base + "/entries/" + entryID},
		{http.MethodPut, base + "/files/" + entryID + "/content"},
		{http.MethodDelete, base + "/entries/" + entryID},
	} {
		body := strings.NewReader(`{"participation_id":"` + f.participation.ID.String() + `","generation":1,"path":"src"}`)
		request := candidateWorkspaceRequest(f, test.method, test.path, body, "application/json", "gone")
		response := httptest.NewRecorder()
		httpAPI.ServeHTTP(response, request)
		require.Contains(t, []int{http.StatusNotFound, http.StatusMethodNotAllowed}, response.Code, "%s %s = %d %s", test.method, test.path, response.Code, response.Body.String())
	}
	require.Equal(t, 0, f.workspaceStore.writes())
}

func candidateWorkspaceRequest(f *attemptFixture, method, path string, body io.Reader, contentType, key string) *http.Request {
	request := newJSONRequest(method, path, body)
	request.Header.Set("Authorization", "Bearer candidate")
	request.Header.Set(candidateAttemptCredentialHeader, f.credential)
	request.Header.Set(candidateAttemptConnectionHeader, f.connection.ID.String())
	request.Header.Set("Content-Type", contentType)
	if key != "" {
		request.Header.Set("Idempotency-Key", key)
	}
	return request
}

func candidateWorkspaceBatchRequest(f *attemptFixture, body io.Reader, contentType, key string) *http.Request {
	return candidateWorkspaceRequest(f, http.MethodPost, f.candidatePath("/workspace/batches"), body, contentType, key)
}

// workspaceManifest is a valid manifest for the fixture's Participation. A
// negative cursor leaves expected_workspace_cursor out.
func (f *attemptFixture) workspaceManifest(cursor int64, mutations ...map[string]any) map[string]any {
	manifest := map[string]any{"participation_id": f.participation.ID.String(), "generation": 1, "mutations": mutations}
	if cursor >= 0 {
		manifest["expected_workspace_cursor"] = cursor
	}
	return manifest
}

type candidateWorkspacePart struct {
	name, filename string
	body           []byte
}

func candidateWorkspaceManifestParts(t *testing.T, manifest any, bodies ...[]byte) []candidateWorkspacePart {
	t.Helper()
	raw, err := json.Marshal(manifest)
	require.NoError(t, err)
	parts := []candidateWorkspacePart{{name: "manifest", body: raw}}
	for _, body := range bodies {
		parts = append(parts, candidateWorkspacePart{name: "content", filename: "workspace", body: body})
	}
	return parts
}

func candidateWorkspaceBatch(t *testing.T, manifest any, bodies ...[]byte) (*bytes.Buffer, string) {
	t.Helper()
	return candidateWorkspaceMultipart(t, candidateWorkspaceManifestParts(t, manifest, bodies...)...)
}

func candidateWorkspaceMultipart(t *testing.T, parts ...candidateWorkspacePart) (*bytes.Buffer, string) {
	t.Helper()
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	for _, part := range parts {
		var (
			target io.Writer
			err    error
		)
		switch {
		case part.filename != "":
			target, err = writer.CreateFormFile(part.name, part.filename)
		case part.name == "manifest":
			header := make(textproto.MIMEHeader)
			header.Set("Content-Disposition", `form-data; name="manifest"`)
			header.Set("Content-Type", "application/json")
			target, err = writer.CreatePart(header)
		default:
			target, err = writer.CreateFormField(part.name)
		}
		require.NoError(t, err)
		_, err = target.Write(part.body)
		require.NoError(t, err)
	}
	require.NoError(t, writer.Close())
	return &body, writer.FormDataContentType()
}

func requireCandidateWorkspaceProblem(t *testing.T, response *httptest.ResponseRecorder, status int, code string) route.Problem {
	t.Helper()
	require.Equal(t, status, response.Code, "response=%d body=%s", response.Code, response.Body.String())
	require.Equal(t, "application/problem+json", response.Header().Get("Content-Type"))
	var problem route.Problem
	require.NoError(t, json.Unmarshal(response.Body.Bytes(), &problem))
	require.Equal(t, code, problem.Code, "problem=%s", response.Body.String())
	return problem
}

func mapKeys[V any](values map[string]V) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	return keys
}

func assertCandidateWorkspaceMutationHTTP(t *testing.T, f *attemptFixture, response *httptest.ResponseRecorder, status int) {
	t.Helper()
	require.Equal(t, status, response.Code, "response=%d headers=%v body=%s", response.Code, response.Header(), response.Body.String())
	require.Contains(t, response.Header().Get("Cache-Control"), "no-store", "response=%d headers=%v body=%s", response.Code, response.Header(), response.Body.String())
	body := response.Body.String()
	for _, forbidden := range []string{f.credential, "continuity_credential", "credential_hash", "participation_id", "generation", "object_id", "session_id"} {
		if forbidden != "" {
			require.NotContains(t, body, forbidden, "response contains")
		}
	}
}

// assertNoForbiddenJSONTerms rejects forbidden terms in member names and in
// non-identifier string values. Random opaque IDs are skipped because their
// base32 text can spell a forbidden word by chance.
func assertNoForbiddenJSONTerms(t *testing.T, body []byte, forbidden ...string) {
	t.Helper()
	var document any
	require.NoError(t, json.Unmarshal(body, &document), "decode %s", body)
	var texts []string
	var walk func(any)
	walk = func(value any) {
		switch typed := value.(type) {
		case map[string]any:
			for key, member := range typed {
				texts = append(texts, key)
				walk(member)
			}
		case []any:
			for _, item := range typed {
				walk(item)
			}
		case string:
			if !model.IsValidOpaqueID(typed) {
				texts = append(texts, typed)
			}
		}
	}
	walk(document)
	for _, text := range texts {
		for _, term := range forbidden {
			require.NotContains(t, strings.ToLower(text), term, "response exposes %q in %q: %s", term, text, body)
		}
	}
}

func TestWorkspaceContentNeverRendersAsAnActiveDocument(t *testing.T) {
	t.Parallel()

	for mediaType, want := range map[string]string{
		"text/x-go":                 "text/x-go",
		"text/plain; charset=utf-8": "text/plain; charset=utf-8",
		"image/png":                 "image/png",
		"application/pdf":           "application/pdf",
		"text/html":                 "text/plain; charset=utf-8",
		"Text/HTML; charset=utf-8":  "text/plain; charset=utf-8",
		"application/xhtml+xml":     "text/plain; charset=utf-8",
		"image/svg+xml":             "text/plain; charset=utf-8",
		"application/xml":           "text/plain; charset=utf-8",
		"text/xml":                  "text/plain; charset=utf-8",
		"not a media type;;":        "application/octet-stream",
	} {
		t.Run(mediaType, func(t *testing.T) {
			t.Parallel()
			headers := http.Header{"Content-Type": {mediaType}}
			untrustedWorkspaceContentHeaders(headers)
			require.Equal(t, want, headers.Get("Content-Type"))
			require.Equal(t, "sandbox", headers.Get("Content-Security-Policy"))
		})
	}
}

func TestExamAttemptOpenAPIDocument(t *testing.T) {
	t.Parallel()
	managerBase := "/api/v1/exams/{exam_id}/sittings/{exam_sitting_id}/attempts"
	managerMember := managerBase + "/{exam_attempt_id}"
	managerBrowserActivity := managerMember + "/browser-activity"
	candidateBase := "/api/v1/exam-attempts/{exam_attempt_id}"
	presentation := candidateBase + "/presentation"
	workspace := candidateBase + "/workspace"
	workspaceChanges := workspace + "/changes"
	workspaceBatches := workspace + "/batches"
	resourceContent := candidateBase + "/resources/{exam_resource_id}/content"
	workspaceContent := candidateBase + "/workspace/files/{attempt_workspace_entry_id}/content"
	candidateSubmissions := candidateBase + "/submissions"
	correctionAcknowledgement := candidateBase + "/corrections/{exam_revision_id}/acknowledgement"
	managerSubmission := managerMember + "/submissions/{submission_id}"
	managerSubmissionManifest := managerSubmission + "/manifest"
	managerSubmissionContent := managerSubmission + "/files/{attempt_workspace_entry_id}/content"

	document := readOpenAPIDocument(t)
	for _, path := range []string{presentation, workspace, workspaceChanges, workspaceBatches, resourceContent, workspaceContent, candidateSubmissions, correctionAcknowledgement} {
		method := "get"
		if path == workspaceBatches || path == candidateSubmissions {
			method = "post"
		}
		if path == correctionAcknowledgement {
			method = "put"
		}
		operation := decodeExamContentOpenAPIOperation(t, document, path, method)
		for _, expected := range []struct{ name, ref string }{
			{name: candidateAttemptCredentialHeader, ref: "#/components/parameters/CandidateAttemptCredential"},
			{name: candidateAttemptConnectionHeader, ref: "#/components/parameters/CandidateAttemptConnectionID"},
		} {
			t.Run(expected.name, func(t *testing.T) {
				if !hasOpenAPIHeaderParameter(operation, expected.name) {
					assert.True(t, hasOpenAPIParameterRef(operation, expected.ref), "GET %s does not document %s", path, expected.name)
				}
			})
		}
	}
	for _, path := range []string{resourceContent, workspaceContent} {
		assertProtectedExamContentResponse(t, document, path, "CandidateExamProtectedContent")
		operation := decodeExamContentOpenAPIOperation(t, document, path, "get")
		assert.Equal(t, "#/components/responses/CandidateExamContentNotModified", operation.Responses["304"].Ref, "GET %s 304 response = %#v", path, operation.Responses["304"])
	}
	assertProtectedExamContentResponse(t, document, managerSubmissionContent, "ExamSubmissionFileContent")
	managerContentOperation := decodeExamContentOpenAPIOperation(t, document, managerSubmissionContent, "get")
	assert.Equal(t, "#/components/responses/ExamSubmissionFileNotModified", managerContentOperation.Responses["304"].Ref, "GET %s 304 response = %#v", managerSubmissionContent, managerContentOperation.Responses["304"])
	assertProtectedExamCacheControl(t, "ExamSubmissionFileContent", "private, no-store")
	_, ok := document.Components.Responses["ExamSubmissionFileContent"].Headers["Content-Disposition"]
	assert.False(t, ok, "Submission protected response documents forbidden Content-Disposition")
	assertProtectedExamCacheControl(t, "CandidateExamProtectedContent", "private, no-store")
	_, ok = document.Components.Responses["CandidateExamProtectedContent"].Headers["Content-Disposition"]
	assert.False(t, ok, "candidate protected response documents forbidden Content-Disposition")
	assertExamAttemptQueryParameters(t, document, managerBase, []string{"state", "limit", "cursor"})
	assertExamAttemptQueryParameters(t, document, managerBrowserActivity, []string{"limit", "cursor"})
	assertExamAttemptQueryParameters(t, document, workspace, []string{"limit", "cursor"})
	assertExamAttemptQueryParameters(t, document, workspaceChanges, []string{"cursor", "limit", "workspace_cursor"})
	assertExamAttemptQueryParameters(t, document, managerSubmissionManifest, []string{"limit", "cursor"})
}

func hasOpenAPIParameterRef(operation openAPIOperation, ref string) bool {
	for _, parameter := range operation.Parameters {
		if parameter.Ref == ref {
			return true
		}
	}
	return false
}

func assertExamAttemptQueryParameters(t *testing.T, document openAPIDocument, path string, want []string) {
	t.Helper()
	operation := decodeExamContentOpenAPIOperation(t, document, path, "get")
	got := make([]string, 0, len(operation.Parameters))
	got = append(got, academicStructureQueryParameterNames(operation.Parameters)...)
	for _, name := range want {
		assert.Contains(t, got, name, "GET %s omits query parameter", path)
	}
	if len(got) != len(want) {
		encoded, _ := json.Marshal(got)
		assert.Fail(t, fmt.Sprintf("GET %s query parameters = %s, want exactly %v", path, encoded, want))
	}
}
