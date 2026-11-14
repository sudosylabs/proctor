// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package httpapi

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/textproto"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/sudosylabs/proctor/server/app/apperror"
	"github.com/sudosylabs/proctor/server/app/exam/attempt"
	"github.com/sudosylabs/proctor/server/app/invocation"
	"github.com/sudosylabs/proctor/server/httpapi/route"
	"github.com/sudosylabs/proctor/server/httpapi/route/routetest"
	"github.com/sudosylabs/proctor/server/model"
	"github.com/sudosylabs/proctor/server/store"
)

func TestCandidateExamPresentationRequiresBoundHeadersAndNeverEchoesSecrets(t *testing.T) {
	t.Parallel()
	fake := newExamAttemptHTTPFake(t)
	httpAPI := newExamAttemptFocusedAPI(t, fake)
	request := fake.candidateRequest(t, http.MethodGet, "/api/v1/exam-attempts/"+fake.attempt.ID.String()+"/presentation")
	response := httptest.NewRecorder()
	httpAPI.ServeHTTP(response, request)
	require.Equal(t, http.StatusOK, response.Code, "status/cache = %d/%q: %s", response.Code, response.Header().Get("Cache-Control"), response.Body.String())
	require.Equal(t, "no-store", response.Header().Get("Cache-Control"), "status/cache = %d/%q: %s", response.Code, response.Header().Get("Cache-Control"), response.Body.String())
	require.Equal(t, fake.attempt.ID, fake.presentationAccess.AttemptID)
	require.Equal(t, fake.connection.ID, fake.presentationAccess.ConnectionID)
	require.Equal(t, fake.credential, fake.presentationAccess.ContinuityCredential)
	require.NotContains(t, response.Body.String(), fake.credential)
	require.NotContains(t, response.Body.String(), "credential")
	require.NotContains(t, response.Body.String(), "file_revision")
	require.NotContains(t, response.Body.String(), "object_id")
	var payload map[string]json.RawMessage
	var capabilities candidateRuntimeCapabilitiesResponse
	var capacity examCapacityPolicyResponse
	require.NoError(t, json.Unmarshal(response.Body.Bytes(), &payload), "presentation shape")
	require.Len(t, payload, 9, "presentation shape")
	require.NoError(t, json.Unmarshal(payload["capacity"], &capacity), "presentation capacity")
	require.Equal(t, examCapacityPolicyResponseFromModel(fake.presentation.Capacity), capacity, "presentation capacity")
	require.NoError(t, json.Unmarshal(payload["candidate_runtime_capabilities"], &capabilities), "presentation capabilities = %#v", capabilities)
	require.True(t, capabilities.FocusLossCollectionEnabled, "presentation capabilities = %#v", capabilities)
	require.Equal(t, 1, capabilities.SchemaVersion, "presentation capabilities = %#v", capabilities)
	require.False(t, capabilities.Departure.Allowed, "presentation capabilities = %#v", capabilities)
	require.Equal(t, "attempt_in_progress", capabilities.Departure.Reason, "presentation capabilities = %#v", capabilities)
}

func TestCandidateExamHTTPRejectsMissingOrDuplicateProtectionHeadersBeforeApplication(t *testing.T) {
	t.Parallel()
	fake := newExamAttemptHTTPFake(t)
	httpAPI := newExamAttemptFocusedAPI(t, fake)
	for _, mutate := range []func(*http.Request){
		func(request *http.Request) { request.Header.Del(candidateAttemptCredentialHeader) },
		func(request *http.Request) {
			request.Header.Add(candidateAttemptConnectionHeader, fake.connection.ID.String())
		},
	} {
		request := fake.candidateRequest(t, http.MethodGet, "/api/v1/exam-attempts/"+fake.attempt.ID.String()+"/presentation")
		mutate(request)
		response := httptest.NewRecorder()
		httpAPI.ServeHTTP(response, request)
		require.Equal(t, http.StatusBadRequest, response.Code, "status/calls/cache = %d/%d/%q: %s", response.Code, fake.presentationCalls, response.Header().Get("Cache-Control"), response.Body.String())
		require.Equal(t, 0, fake.presentationCalls, "status/calls/cache = %d/%d/%q: %s", response.Code, fake.presentationCalls, response.Header().Get("Cache-Control"), response.Body.String())
		require.Equal(t, "no-store", response.Header().Get("Cache-Control"), "status/calls/cache = %d/%d/%q: %s", response.Code, fake.presentationCalls, response.Header().Get("Cache-Control"), response.Body.String())
	}
}

func TestCandidateExamWorkspaceUsesBoundedOpaqueCursor(t *testing.T) {
	t.Parallel()
	fake := newExamAttemptHTTPFake(t)
	fake.workspacePage.HasMore = true
	httpAPI := newExamAttemptFocusedAPI(t, fake)
	request := fake.candidateRequest(t, http.MethodGet, "/api/v1/exam-attempts/"+fake.attempt.ID.String()+"/workspace?limit=1")
	response := httptest.NewRecorder()
	httpAPI.ServeHTTP(response, request)
	require.Equal(t, http.StatusOK, response.Code, "status/query/cache = %d/%#v/%q: %s", response.Code, fake.workspaceQuery, response.Header().Get("Cache-Control"), response.Body.String())
	require.Equal(t, 1, fake.workspaceQuery.Limit, "status/query/cache = %d/%#v/%q: %s", response.Code, fake.workspaceQuery, response.Header().Get("Cache-Control"), response.Body.String())
	require.Equal(t, "no-store", response.Header().Get("Cache-Control"), "status/query/cache = %d/%#v/%q: %s", response.Code, fake.workspaceQuery, response.Header().Get("Cache-Control"), response.Body.String())
	var page candidateExamWorkspaceListResponse
	require.NoError(t, json.Unmarshal(response.Body.Bytes(), &page), "Workspace page = %#v", page)
	require.Len(t, page.Items, 1, "Workspace page = %#v", page)
	require.NotEmpty(t, page.NextCursor, "Workspace page = %#v", page)
	require.NotNil(t, page.Items[0].Size, "Workspace page = %#v", page)
	cursor, err := decodeCandidateWorkspaceCursor(page.NextCursor)
	require.NoError(t, err, "Workspace cursor = %#v", cursor)
	require.Equal(t, fake.workspacePage.Cursor, cursor.ExpectedCursor, "Workspace cursor = %#v", cursor)
	require.Equal(t, fake.workspacePage.Items[0].EntryID, cursor.ID, "Workspace cursor = %#v", cursor)
	require.Equal(t, int64(-1), fake.workspaceQuery.ExpectedCursor, "Workspace cursor = %#v", cursor)
	fake.workspacePage.HasMore = false
	second := fake.candidateRequest(t, http.MethodGet, "/api/v1/exam-attempts/"+fake.attempt.ID.String()+"/workspace?cursor="+url.QueryEscape(page.NextCursor))
	secondResponse := httptest.NewRecorder()
	httpAPI.ServeHTTP(secondResponse, second)
	require.Equal(t, http.StatusOK, secondResponse.Code, "Workspace cursor forwarding = %d query=%#v body=%s", secondResponse.Code, fake.workspaceQuery, secondResponse.Body.String())
	require.Equal(t, cursor.ExpectedCursor, fake.workspaceQuery.ExpectedCursor, "Workspace cursor forwarding = %d query=%#v body=%s", secondResponse.Code, fake.workspaceQuery, secondResponse.Body.String())
	require.Equal(t, cursor.ID, fake.workspaceQuery.AfterEntryID, "Workspace cursor forwarding = %d query=%#v body=%s", secondResponse.Code, fake.workspaceQuery, secondResponse.Body.String())
	malformed := fake.candidateRequest(t, http.MethodGet, "/api/v1/exam-attempts/"+fake.attempt.ID.String()+"/workspace?cursor=not-a-cursor")
	malformedResponse := httptest.NewRecorder()
	httpAPI.ServeHTTP(malformedResponse, malformed)
	assertHTTPProblem(t, malformedResponse, http.StatusBadRequest, "request.invalid")
}

func TestCandidateSubmitExamAttemptUsesStrictCausalSelectorsAndReturnsSafeReceipt(t *testing.T) {
	t.Parallel()

	fake := newExamAttemptHTTPFake(t)
	httpAPI := newExamAttemptFocusedAPI(t, fake)
	body := `{"participation_id":"` + fake.participation.ID.String() + `","generation":1,"expected_current_revision_id":"` +
		fake.submissionReceipt.ExamRevisionID.String() + `","expected_workspace_cursor":4,"final_focus_loss_sequence":0}`
	path := "/api/v1/exam-attempts/" + fake.attempt.ID.String() + "/submissions"
	request := fake.candidateRequest(t, http.MethodPost, path)
	request.Body = io.NopCloser(strings.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Idempotency-Key", "submit-once")
	response := httptest.NewRecorder()
	httpAPI.ServeHTTP(response, request)
	require.Equal(t, http.StatusCreated, response.Code, "status=%d headers=%v command=%#v body=%s", response.Code, response.Header(), fake.submit, response.Body.String())
	require.Equal(t, "no-store", response.Header().Get("Cache-Control"), "status=%d headers=%v command=%#v body=%s", response.Code, response.Header(), fake.submit, response.Body.String())
	require.Equal(t, fake.attempt.ID, fake.submit.Access.AttemptID, "status=%d headers=%v command=%#v body=%s", response.Code, response.Header(), fake.submit, response.Body.String())
	require.Equal(t, fake.participation.ID, fake.submit.Access.ParticipationID, "status=%d headers=%v command=%#v body=%s", response.Code, response.Header(), fake.submit, response.Body.String())
	require.Equal(t, int64(1), fake.submit.Access.Generation, "status=%d headers=%v command=%#v body=%s", response.Code, response.Header(), fake.submit, response.Body.String())
	require.Equal(t, fake.submissionReceipt.ExamRevisionID, fake.submit.ExpectedCurrentRevisionID, "status=%d headers=%v command=%#v body=%s", response.Code, response.Header(), fake.submit, response.Body.String())
	require.Equal(t, int64(4), fake.submit.ExpectedWorkspaceCursor, "status=%d headers=%v command=%#v body=%s", response.Code, response.Header(), fake.submit, response.Body.String())
	require.Equal(t, int64(0), fake.submit.FinalFocusLossSequence, "status=%d headers=%v command=%#v body=%s", response.Code, response.Header(), fake.submit, response.Body.String())
	require.Equal(t, "submit-once", fake.submit.IdempotencyKey, "status=%d headers=%v command=%#v body=%s", response.Code, response.Header(), fake.submit, response.Body.String())
	var payload map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(response.Body.Bytes(), &payload), "receipt")
	require.Len(t, payload, 9, "receipt")
	require.Equal(t, `"MBY3-KBMN-EB83-TFW1"`, string(payload["receipt_code"]), "receipt code is in display form")
	require.Equal(t, `"`+fake.submissionReceipt.ExamRevisionID.String()+`"`, string(payload["exam_revision_id"]), "receipt")
	require.Equal(t, `"submitted"`, string(payload["state"]), "receipt")
	require.Equal(t, "4", string(payload["workspace_cursor"]), "receipt")
	assertNoForbiddenJSONTerms(t, response.Body.Bytes(),
		"participation", "generation", "connection", "credential", "integrity", "gap", "path", "content", "evidence")
}

func TestCandidateAcknowledgesExactCorrectionWithBoundSessionFences(t *testing.T) {
	t.Parallel()

	fake := newExamAttemptHTTPFake(t)
	httpAPI := newExamAttemptFocusedAPI(t, fake)
	correctionID := model.NewExamRevisionID()
	currentRevisionID := model.NewExamRevisionID()
	path := "/api/v1/exam-attempts/" + fake.attempt.ID.String() + "/corrections/" + correctionID.String() + "/acknowledgement"
	body := `{"participation_id":"` + fake.participation.ID.String() + `","generation":1,"expected_current_revision_id":"` + currentRevisionID.String() + `"}`
	request := fake.candidateRequest(t, http.MethodPut, path)
	request.Body = io.NopCloser(strings.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Idempotency-Key", "acknowledge-correction-once")
	response := httptest.NewRecorder()
	httpAPI.ServeHTTP(response, request)
	command := fake.correctionAcknowledgement
	require.Equal(t, http.StatusOK, response.Code, "status=%d headers=%v command=%#v body=%s", response.Code, response.Header(), command, response.Body.String())
	require.Equal(t, "no-store", response.Header().Get("Cache-Control"), "status=%d headers=%v command=%#v body=%s", response.Code, response.Header(), command, response.Body.String())
	require.Equal(t, fake.attempt.ID, command.Access.AttemptID, "status=%d headers=%v command=%#v body=%s", response.Code, response.Header(), command, response.Body.String())
	require.Equal(t, fake.connection.ID, command.Access.ConnectionID, "status=%d headers=%v command=%#v body=%s", response.Code, response.Header(), command, response.Body.String())
	require.Equal(t, fake.credential, command.Access.ContinuityCredential, "status=%d headers=%v command=%#v body=%s", response.Code, response.Header(), command, response.Body.String())
	require.Equal(t, fake.participation.ID, command.Access.ParticipationID, "status=%d headers=%v command=%#v body=%s", response.Code, response.Header(), command, response.Body.String())
	require.Equal(t, int64(1), command.Access.Generation, "status=%d headers=%v command=%#v body=%s", response.Code, response.Header(), command, response.Body.String())
	require.Equal(t, correctionID, command.CorrectionRevisionID, "status=%d headers=%v command=%#v body=%s", response.Code, response.Header(), command, response.Body.String())
	require.Equal(t, currentRevisionID, command.ExpectedCurrentRevisionID, "status=%d headers=%v command=%#v body=%s", response.Code, response.Header(), command, response.Body.String())
	require.Equal(t, "acknowledge-correction-once", command.IdempotencyKey, "status=%d headers=%v command=%#v body=%s", response.Code, response.Header(), command, response.Body.String())
	var payload map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(response.Body.Bytes(), &payload), "acknowledgement response")
	require.Len(t, payload, 5, "acknowledgement response")
	require.Equal(t, `"`+correctionID.String()+`"`, string(payload["revision_id"]), "acknowledgement response")
	require.Equal(t, `"acknowledged"`, string(payload["acknowledgement_state"]), "acknowledgement response")
	assertNoForbiddenJSONTerms(t, response.Body.Bytes(),
		"credential", "connection", "participation", "generation", "audit", "private")

	invalidFake := newExamAttemptHTTPFake(t)
	invalidAPI := newExamAttemptFocusedAPI(t, invalidFake)
	invalid := invalidFake.candidateRequest(t, http.MethodPut, "/api/v1/exam-attempts/"+invalidFake.attempt.ID.String()+
		"/corrections/"+model.NewExamRevisionID().String()+"/acknowledgement")
	invalid.Body = io.NopCloser(strings.NewReader(`{"participation_id":"` + invalidFake.participation.ID.String() +
		`","generation":1,"expected_current_revision_id":"` + model.NewExamRevisionID().String() + `","accepted":true}`))
	invalid.Header.Set("Content-Type", "application/json")
	invalid.Header.Set("Idempotency-Key", "invalid-acknowledgement")
	invalidResponse := httptest.NewRecorder()
	invalidAPI.ServeHTTP(invalidResponse, invalid)
	assertHTTPProblem(t, invalidResponse, http.StatusBadRequest, "request.invalid")
	require.False(t, invalidFake.correctionAcknowledgement.CorrectionRevisionID.IsValid())
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
	fake := newExamAttemptHTTPFake(t)
	httpAPI := newExamAttemptFocusedAPI(t, fake)
	path := "/api/v1/exam-attempts/" + fake.attempt.ID.String() + "/resources/" + fake.resourceID.String() + "/content"
	request := fake.candidateRequest(t, http.MethodGet, path)
	response := httptest.NewRecorder()
	httpAPI.ServeHTTP(response, request)
	wantETag := `"` + fake.content.SHA256 + `"`
	require.Equal(t, http.StatusOK, response.Code, "content response = %d %#v", response.Code, response.Header())
	require.Equal(t, "protected", response.Body.String(), "content response = %d %#v", response.Code, response.Header())
	require.Empty(t, response.Header().Get("Content-Disposition"), "content response = %d %#v", response.Code, response.Header())
	require.Equal(t, "private, no-store", response.Header().Get("Cache-Control"), "content response = %d %#v", response.Code, response.Header())
	require.Equal(t, wantETag, response.Header().Get("ETag"), "content response = %d %#v", response.Code, response.Header())

	request = fake.candidateRequest(t, http.MethodGet, path)
	request.Header.Set("If-None-Match", wantETag)
	response = httptest.NewRecorder()
	httpAPI.ServeHTTP(response, request)
	require.Equal(t, http.StatusNotModified, response.Code, "conditional response = %d %#v %q", response.Code, response.Header(), response.Body.String())
	require.Equal(t, 0, response.Body.Len(), "conditional response = %d %#v %q", response.Code, response.Header(), response.Body.String())
	require.Equal(t, "private, no-store", response.Header().Get("Cache-Control"), "conditional response = %d %#v %q", response.Code, response.Header(), response.Body.String())
}

func TestCandidateWorkspaceContentUsesContentVersionETagAndExactEntry(t *testing.T) {
	t.Parallel()
	fake := newExamAttemptHTTPFake(t)
	httpAPI := newExamAttemptFocusedAPI(t, fake)
	entryID := fake.workspacePage.Items[0].EntryID
	path := "/api/v1/exam-attempts/" + fake.attempt.ID.String() + "/workspace/files/" + entryID.String() + "/content"
	request := fake.candidateRequest(t, http.MethodGet, path)
	response := httptest.NewRecorder()
	httpAPI.ServeHTTP(response, request)
	require.Equal(t, http.StatusOK, response.Code, "Workspace content = %d query=%#v headers=%#v body=%s", response.Code, fake.workspaceOpen, response.Header(), response.Body.String())
	require.Equal(t, entryID, fake.workspaceOpen.EntryID, "Workspace content = %d query=%#v headers=%#v body=%s", response.Code, fake.workspaceOpen, response.Header(), response.Body.String())
	require.True(t, fake.workspaceOpen.Access.ConnectionID.IsValid(), "Workspace content = %d query=%#v headers=%#v body=%s", response.Code, fake.workspaceOpen, response.Header(), response.Body.String())
	require.Equal(t, `"`+fake.workspaceContentVersion.String()+`"`, response.Header().Get("ETag"), "Workspace content = %d query=%#v headers=%#v body=%s", response.Code, fake.workspaceOpen, response.Header(), response.Body.String())
	require.Equal(t, "private, no-store", response.Header().Get("Cache-Control"), "Workspace content = %d query=%#v headers=%#v body=%s", response.Code, fake.workspaceOpen, response.Header(), response.Body.String())
}

func TestManagedExamAttemptListIsBoundedSafeAndOpaque(t *testing.T) {
	t.Parallel()
	fake := newExamAttemptHTTPFake(t)
	fake.managerPage.HasMore = true
	httpAPI := newExamAttemptFocusedAPI(t, fake)
	path := "/api/v1/exams/" + fake.attempt.ExamID.String() + "/sittings/" + fake.attempt.SittingID.String() + "/attempts?limit=1&state=active"
	request := httptest.NewRequestWithContext(t.Context(), http.MethodGet, path, nil)
	request.Header.Set("Authorization", "Bearer credential")
	response := httptest.NewRecorder()
	httpAPI.ServeHTTP(response, request)
	require.Equal(t, http.StatusOK, response.Code, "manager response/query = %d/%#v/%q: %s", response.Code, fake.managerList, response.Header().Get("Cache-Control"), response.Body.String())
	require.Equal(t, 1, fake.managerList.Limit, "manager response/query = %d/%#v/%q: %s", response.Code, fake.managerList, response.Header().Get("Cache-Control"), response.Body.String())
	require.Len(t, fake.managerList.States, 1, "manager response/query = %d/%#v/%q: %s", response.Code, fake.managerList, response.Header().Get("Cache-Control"), response.Body.String())
	require.Equal(t, "no-store", response.Header().Get("Cache-Control"), "manager response/query = %d/%#v/%q: %s", response.Code, fake.managerList, response.Header().Get("Cache-Control"), response.Body.String())
	require.NotContains(t, response.Body.String(), "credential")
	require.NotContains(t, response.Body.String(), "session_id")
	require.NotContains(t, response.Body.String(), "hash")
	var page examAttemptManagerListResponse
	require.NoError(t, json.Unmarshal(response.Body.Bytes(), &page), "manager page = %#v", page)
	require.Len(t, page.Items, 1, "manager page = %#v", page)
	require.NotEmpty(t, page.NextCursor, "manager page = %#v", page)
	cursor, err := decodeExamAttemptManagerCursor(page.NextCursor)
	require.NoError(t, err)
	fake.managerPage.HasMore = false
	second := httptest.NewRequestWithContext(t.Context(), http.MethodGet, path+"&cursor="+url.QueryEscape(page.NextCursor), nil)
	second.Header.Set("Authorization", "Bearer credential")
	secondResponse := httptest.NewRecorder()
	httpAPI.ServeHTTP(secondResponse, second)
	require.Equal(t, http.StatusOK, secondResponse.Code, "manager cursor forwarding = %d query=%#v body=%s", secondResponse.Code, fake.managerList, secondResponse.Body.String())
	require.True(t, fake.managerList.BeforeCreatedAt.Equal(cursor.CreatedAt), "manager cursor forwarding = %d query=%#v body=%s", secondResponse.Code, fake.managerList, secondResponse.Body.String())
	require.Equal(t, cursor.ID, fake.managerList.BeforeAttemptID, "manager cursor forwarding = %d query=%#v body=%s", secondResponse.Code, fake.managerList, secondResponse.Body.String())
	malformed := httptest.NewRequestWithContext(t.Context(), http.MethodGet, path+"&cursor=not-a-cursor", nil)
	malformed.Header.Set("Authorization", "Bearer credential")
	malformedResponse := httptest.NewRecorder()
	httpAPI.ServeHTTP(malformedResponse, malformed)
	assertHTTPProblem(t, malformedResponse, http.StatusBadRequest, "request.invalid")

	member := "/api/v1/exams/" + fake.attempt.ExamID.String() + "/sittings/" + fake.attempt.SittingID.String() + "/attempts/" + fake.attempt.ID.String()
	request = httptest.NewRequestWithContext(t.Context(), http.MethodGet, member, nil)
	request.Header.Set("Authorization", "Bearer credential")
	response = httptest.NewRecorder()
	httpAPI.ServeHTTP(response, request)
	require.Equal(t, http.StatusOK, response.Code, "manager exact = %d query=%#v headers=%#v body=%s", response.Code, fake.managerGet, response.Header(), response.Body.String())
	require.Equal(t, fake.attempt.ExamID, fake.managerGet.ExamID, "manager exact = %d query=%#v headers=%#v body=%s", response.Code, fake.managerGet, response.Header(), response.Body.String())
	require.Equal(t, fake.attempt.SittingID, fake.managerGet.SittingID, "manager exact = %d query=%#v headers=%#v body=%s", response.Code, fake.managerGet, response.Header(), response.Body.String())
	require.Equal(t, fake.attempt.ID, fake.managerGet.AttemptID, "manager exact = %d query=%#v headers=%#v body=%s", response.Code, fake.managerGet, response.Header(), response.Body.String())
	require.Equal(t, "no-store", response.Header().Get("Cache-Control"), "manager exact = %d query=%#v headers=%#v body=%s", response.Code, fake.managerGet, response.Header(), response.Body.String())
}

func TestManagedBrowserActivityIsNestedMinimizedNoStoreAndKeysetPaginated(t *testing.T) {
	t.Parallel()
	fake := newExamAttemptHTTPFake(t)
	fake.browserActivityPage.HasMore = true
	httpAPI := newExamAttemptFocusedAPI(t, fake)
	path := "/api/v1/exams/" + fake.attempt.ExamID.String() + "/sittings/" + fake.attempt.SittingID.String() +
		"/attempts/" + fake.attempt.ID.String() + "/browser-activity?limit=1"
	request := httptest.NewRequestWithContext(t.Context(), http.MethodGet, path, nil)
	request.Header.Set("Authorization", "Bearer credential")
	response := httptest.NewRecorder()
	httpAPI.ServeHTTP(response, request)
	require.Equal(t, http.StatusOK, response.Code, "Browser Activity response/query = %d/%#v/%q: %s", response.Code, fake.browserActivityQuery, response.Header().Get("Cache-Control"), response.Body.String())
	require.Equal(t, 1, fake.browserActivityQuery.Limit, "Browser Activity response/query = %d/%#v/%q: %s", response.Code, fake.browserActivityQuery, response.Header().Get("Cache-Control"), response.Body.String())
	require.Equal(t, "no-store", response.Header().Get("Cache-Control"), "Browser Activity response/query = %d/%#v/%q: %s", response.Code, fake.browserActivityQuery, response.Header().Get("Cache-Control"), response.Body.String())
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
	fake.browserActivityPage.HasMore = false
	second := httptest.NewRequestWithContext(t.Context(), http.MethodGet, path+"&cursor="+url.QueryEscape(page.NextCursor), nil)
	second.Header.Set("Authorization", "Bearer credential")
	secondResponse := httptest.NewRecorder()
	httpAPI.ServeHTTP(secondResponse, second)
	require.Equal(t, http.StatusOK, secondResponse.Code, "Browser Activity cursor forwarding = %d query=%#v body=%s", secondResponse.Code, fake.browserActivityQuery, secondResponse.Body.String())
	require.True(t, fake.browserActivityQuery.AfterReceivedAt.Equal(cursor.ReceivedAt), "Browser Activity cursor forwarding = %d query=%#v body=%s", secondResponse.Code, fake.browserActivityQuery, secondResponse.Body.String())
	require.Equal(t, cursor.SourceSessionID, fake.browserActivityQuery.AfterSourceID, "Browser Activity cursor forwarding = %d query=%#v body=%s", secondResponse.Code, fake.browserActivityQuery, secondResponse.Body.String())
	require.Equal(t, cursor.Sequence, fake.browserActivityQuery.AfterSequence, "Browser Activity cursor forwarding = %d query=%#v body=%s", secondResponse.Code, fake.browserActivityQuery, secondResponse.Body.String())
}

func TestManagerSubmissionReadManifestAndFileArePurposeSpecificAndProtected(t *testing.T) {
	t.Parallel()

	fake := newExamAttemptHTTPFake(t)
	fake.submissionManifest.HasMore = true
	httpAPI := newExamAttemptFocusedAPI(t, fake)
	base := "/api/v1/exams/" + fake.attempt.ExamID.String() + "/sittings/" + fake.attempt.SittingID.String() +
		"/attempts/" + fake.attempt.ID.String() + "/submissions/" + fake.submissionView.Submission.ID.String()
	for _, path := range []string{base, base + "/manifest?limit=1"} {
		request := httptest.NewRequestWithContext(t.Context(), http.MethodGet, path, nil)
		request.Header.Set("Authorization", "Bearer credential")
		response := httptest.NewRecorder()
		httpAPI.ServeHTTP(response, request)
		require.Equal(t, http.StatusOK, response.Code, "GET %s = %d headers=%v body=%s", path, response.Code, response.Header(), response.Body.String())
		require.Equal(t, "no-store", response.Header().Get("Cache-Control"), "GET %s = %d headers=%v body=%s", path, response.Code, response.Header(), response.Body.String())
		for _, forbidden := range []string{"starter_object", "attempt_object", "storage_origin", "vfs", "url"} {
			require.NotContains(t, strings.ToLower(response.Body.String()), forbidden, "GET %s exposed", path)
		}
	}
	require.Equal(t, fake.submissionView.Submission.ID, fake.submissionGet.SubmissionID, "get=%#v manifest=%#v", fake.submissionGet, fake.submissionManifestQuery)
	require.Equal(t, 1, fake.submissionManifestQuery.Limit, "get=%#v manifest=%#v", fake.submissionGet, fake.submissionManifestQuery)
	require.Equal(t, fake.submissionView.Submission.ID, fake.submissionManifestQuery.SubmissionID, "get=%#v manifest=%#v", fake.submissionGet, fake.submissionManifestQuery)
	entry := fake.submissionManifest.Items[0]
	cursor, err := encodeSubmissionManifestCursor(submissionManifestCursor{EntryID: entry.EntryID})
	require.NoError(t, err)
	fake.submissionManifest.HasMore = false
	cursorRequest := httptest.NewRequestWithContext(t.Context(), http.MethodGet, base+"/manifest?cursor="+url.QueryEscape(cursor), nil)
	cursorRequest.Header.Set("Authorization", "Bearer credential")
	cursorResponse := httptest.NewRecorder()
	httpAPI.ServeHTTP(cursorResponse, cursorRequest)
	require.Equal(t, http.StatusOK, cursorResponse.Code, "manifest cursor forwarding = %d query=%#v body=%s", cursorResponse.Code, fake.submissionManifestQuery, cursorResponse.Body.String())
	require.Equal(t, entry.EntryID, fake.submissionManifestQuery.AfterEntryID, "manifest cursor forwarding = %d query=%#v body=%s", cursorResponse.Code, fake.submissionManifestQuery, cursorResponse.Body.String())
	malformed := httptest.NewRequestWithContext(t.Context(), http.MethodGet, base+"/manifest?cursor=not-a-cursor", nil)
	malformed.Header.Set("Authorization", "Bearer credential")
	malformedResponse := httptest.NewRecorder()
	httpAPI.ServeHTTP(malformedResponse, malformed)
	assertHTTPProblem(t, malformedResponse, http.StatusBadRequest, "request.invalid")

	contentPath := base + "/files/" + entry.EntryID.String() + "/content"
	request := httptest.NewRequestWithContext(t.Context(), http.MethodGet, contentPath, nil)
	request.Header.Set("Authorization", "Bearer credential")
	response := httptest.NewRecorder()
	httpAPI.ServeHTTP(response, request)
	require.Equal(t, http.StatusOK, response.Code, "content=%d headers=%v query=%#v body", response.Code, response.Header(), fake.submissionOpen)
	require.Equal(t, "protected", response.Body.String(), "content=%d headers=%v query=%#v body", response.Code, response.Header(), fake.submissionOpen)
	require.Equal(t, "private, no-store", response.Header().Get("Cache-Control"), "content=%d headers=%v query=%#v body", response.Code, response.Header(), fake.submissionOpen)
	require.Equal(t, `"`+entry.SHA256+`"`, response.Header().Get("ETag"), "content=%d headers=%v query=%#v body", response.Code, response.Header(), fake.submissionOpen)
	require.Empty(t, response.Header().Get("Content-Disposition"), "content=%d headers=%v query=%#v body", response.Code, response.Header(), fake.submissionOpen)
	require.Contains(t, response.Header().Values("Content-Security-Policy"), "sandbox", "authored content renders with an opaque origin")
	require.Equal(t, entry.EntryID, fake.submissionOpen.EntryID, "content=%d headers=%v query=%#v body", response.Code, response.Header(), fake.submissionOpen)
}

func TestManagerSubmissionIntegrityRetirementOmitsFormerCounters(t *testing.T) {
	t.Parallel()
	for _, retired := range []bool{false, true} {
		name := "retained zero counters"
		if retired {
			name = "retired integrity"
		}
		t.Run(name, func(t *testing.T) {
			fake := newExamAttemptHTTPFake(t)
			submission := &fake.submissionView.Submission
			submission.FinalFocusLossSequence = 0
			submission.UnresolvedIntegrityCount = 0
			submission.IntegrityState = model.SubmissionIntegritySettled
			if retired {
				submission.IntegrityState = model.SubmissionIntegrityRetired
				submission.IntegrityRetiredAt = model.OptionalTimeFrom(submission.SubmittedAt.Add(time.Hour))
				submission.BrowserActivity = model.BrowserSubmissionSettlement{State: "not_applicable", InventoryRevision: 1}
			}
			httpAPI := newExamAttemptFocusedAPI(t, fake)
			path := "/api/v1/exams/" + fake.attempt.ExamID.String() + "/sittings/" + fake.attempt.SittingID.String() +
				"/attempts/" + fake.attempt.ID.String() + "/submissions/" + submission.ID.String()
			request := httptest.NewRequestWithContext(t.Context(), http.MethodGet, path, nil)
			request.Header.Set("Authorization", "Bearer credential")
			response := httptest.NewRecorder()
			httpAPI.ServeHTTP(response, request)
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

func TestManagerReallowExamAttemptUsesExactSuspensionAndNeverReturnsPrivateReason(t *testing.T) {
	t.Parallel()
	fake := newExamAttemptHTTPFake(t)
	httpAPI := newExamAttemptFocusedAPI(t, fake)
	suspensionID := model.NewAttemptSuspensionID()
	path := "/api/v1/exams/" + fake.attempt.ExamID.String() + "/sittings/" + fake.attempt.SittingID.String() +
		"/attempts/" + fake.attempt.ID.String() + "/reallow"
	request := newJSONRequest(http.MethodPost, path, strings.NewReader(`{"suspension_id":"`+suspensionID.String()+`","expected_revision":2,"reason":"manager verified connectivity"}`))
	request.Header.Set("Authorization", "Bearer credential")
	request.Header.Set("Idempotency-Key", "reallow-once")
	response := httptest.NewRecorder()
	httpAPI.ServeHTTP(response, request)
	require.Equal(t, http.StatusOK, response.Code, "status/cache=%d/%q body=%s", response.Code, response.Header().Get("Cache-Control"), response.Body.String())
	require.Equal(t, "no-store", response.Header().Get("Cache-Control"), "status/cache=%d/%q body=%s", response.Code, response.Header().Get("Cache-Control"), response.Body.String())
	require.Equal(t, suspensionID, fake.reallow.SuspensionID)
	require.Equal(t, int64(2), fake.reallow.ExpectedAttemptRevision)
	require.Equal(t, "manager verified connectivity", fake.reallow.PrivateReason)
	require.Equal(t, "reallow-once", fake.reallow.IdempotencyKey)
	require.NotContains(t, response.Body.String(), "manager verified connectivity")
	require.NotContains(t, response.Body.String(), "private")
	require.NotContains(t, response.Body.String(), "replayed")
}

func TestManagerSuspendExamAttemptUsesRevisionFenceAndNeverReturnsPrivateReason(t *testing.T) {
	t.Parallel()
	fake := newExamAttemptHTTPFake(t)
	httpAPI := newExamAttemptFocusedAPI(t, fake)
	path := "/api/v1/exams/" + fake.attempt.ExamID.String() + "/sittings/" + fake.attempt.SittingID.String() +
		"/attempts/" + fake.attempt.ID.String() + "/suspend"
	request := newJSONRequest(http.MethodPost, path, strings.NewReader(`{"expected_revision":2,"reason":"candidate left the room"}`))
	request.Header.Set("Authorization", "Bearer credential")
	request.Header.Set("Idempotency-Key", "kick-once")
	response := httptest.NewRecorder()
	httpAPI.ServeHTTP(response, request)
	require.Equal(t, http.StatusOK, response.Code, "status/cache=%d/%q body=%s", response.Code, response.Header().Get("Cache-Control"), response.Body.String())
	require.Equal(t, "no-store", response.Header().Get("Cache-Control"), "status/cache=%d/%q body=%s", response.Code, response.Header().Get("Cache-Control"), response.Body.String())
	require.Equal(t, fake.attempt.ID, fake.suspend.AttemptID)
	require.Equal(t, int64(2), fake.suspend.ExpectedAttemptRevision)
	require.Equal(t, "candidate left the room", fake.suspend.PrivateReason)
	require.Equal(t, "kick-once", fake.suspend.IdempotencyKey)
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

func TestManagerScreenClipRequestNeverReturnsItsPrivateReason(t *testing.T) {
	t.Parallel()
	fake := newExamAttemptHTTPFake(t)
	httpAPI := newExamAttemptFocusedAPI(t, fake)
	path := "/api/v1/exams/" + fake.attempt.ExamID.String() + "/sittings/" + fake.attempt.SittingID.String() +
		"/attempts/" + fake.attempt.ID.String() + "/screen-clips"
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
	require.True(t, fake.screenClip.Range.FromSessionStart)
	require.Equal(t, "second voice heard", fake.screenClip.PrivateReason)
	require.Equal(t, "clip-once", fake.screenClip.IdempotencyKey)
	body := response.Body.String()
	require.NotContains(t, body, "second voice heard")
	require.NotContains(t, body, "reason")
	require.Contains(t, body, `"state":"requested"`)
	require.Contains(t, body, `"from_session_start":true`)

	response = send(`{"from_session_start":false,"start":"2026-11-12T09:58:00Z","end":"2026-11-12T10:00:00Z","reason":"r"}`, "clip-range")
	require.Equal(t, http.StatusCreated, response.Code, "body=%s", response.Body.String())
	require.False(t, fake.screenClip.Range.FromSessionStart)
	require.Equal(t, time.Date(2026, 11, 12, 9, 58, 0, 0, time.UTC), fake.screenClip.Range.Start)

	for name, body := range map[string]string{
		"both start forms": `{"from_session_start":true,"start":"2026-11-12T09:58:00Z","end":"2026-11-12T10:00:00Z","reason":"r"}`,
		"no start":         `{"from_session_start":false,"end":"2026-11-12T10:00:00Z","reason":"r"}`,
		"bad end":          `{"from_session_start":true,"end":"soon","reason":"r"}`,
	} {
		require.Equal(t, http.StatusBadRequest, send(body, "clip-bad").Code, name)
	}
}

func TestCandidateScreenClipChunksAreNumberedFromOneOnTheWire(t *testing.T) {
	t.Parallel()
	fake := newExamAttemptHTTPFake(t)
	httpAPI := newExamAttemptFocusedAPI(t, fake)
	clipID := model.NewScreenClipID()
	base := "/api/v1/exam-attempts/" + fake.attempt.ID.String() + "/screen-clip-requests/" + clipID.String() + "/upload"
	send := func(method, path string, body io.Reader) *httptest.ResponseRecorder {
		request := httptest.NewRequestWithContext(t.Context(), method, path, body)
		request.Header.Set("Authorization", "Bearer credential")
		request.Header.Set(candidateAttemptCredentialHeader, model.NewCredentialToken())
		request.Header.Set(candidateAttemptConnectionHeader, model.NewAttemptConnectionID().String())
		if body != nil && method != http.MethodPut {
			request.Header.Set("Content-Type", "application/json")
		}
		response := httptest.NewRecorder()
		httpAPI.ServeHTTP(response, request)
		return response
	}

	hashes := `["` + strings.Repeat("a", 64) + `","` + strings.Repeat("b", 64) + `"]`
	begun := send(http.MethodPost, base, strings.NewReader(`{"media_type":"video/mp4","size_bytes":8388618,"chunk_sha256":`+hashes+`}`))
	require.Equal(t, http.StatusOK, begun.Code, "body=%s", begun.Body.String())
	require.Equal(t, int64(8388618), fake.clipUpload.SizeBytes)
	require.Contains(t, begun.Body.String(), `"chunk_count":2`)
	require.Contains(t, begun.Body.String(), `"received_chunks":[]`)

	status := send(http.MethodGet, base, nil)
	require.Equal(t, http.StatusOK, status.Code, "body=%s", status.Body.String())
	require.Contains(t, status.Body.String(), `"received_chunks":[1]`, "the first chunk is chunk 1")

	chunk := send(http.MethodPut, base+"/chunks/2", strings.NewReader("0123456789"))
	require.Equal(t, http.StatusOK, chunk.Code, "body=%s", chunk.Body.String())
	require.Equal(t, 1, fake.clipChunk.Index, "chunk 2 is the second chunk")
	require.Equal(t, int64(10), fake.clipChunk.Size)
	require.Equal(t, "0123456789", string(fake.clipChunkBytes))
	require.Contains(t, chunk.Body.String(), `"chunk":2`)

	require.Equal(t, http.StatusNotFound, send(http.MethodPut, base+"/chunks/0", strings.NewReader("x")).Code,
		"there is no chunk 0")

	signature := strings.Repeat("s", model.DesktopSignatureLength)
	completed := send(http.MethodPost, base+"/complete",
		strings.NewReader(`{"content_digest":"`+strings.Repeat("d", 64)+`","signature":"`+signature+`"}`))
	require.Equal(t, http.StatusOK, completed.Code, "body=%s", completed.Body.String())
	require.Equal(t, strings.Repeat("d", 64), fake.clipCompletion.ContentDigest)
	require.Equal(t, signature, fake.clipCompletion.Signature)
	require.Contains(t, completed.Body.String(), `"state":"ready"`)
}

func TestManagersPlayScreenClipsByRange(t *testing.T) {
	t.Parallel()
	fake := newExamAttemptHTTPFake(t)
	fake.clipContent = "0123456789abcdefghij"
	httpAPI := newExamAttemptFocusedAPI(t, fake)
	path := "/api/v1/exams/" + fake.attempt.ExamID.String() + "/sittings/" + fake.attempt.SittingID.String() +
		"/attempts/" + fake.attempt.ID.String() + "/screen-clips/" + model.NewScreenClipID().String() + "/content"
	play := func(rangeHeader string) *httptest.ResponseRecorder {
		request := httptest.NewRequestWithContext(t.Context(), http.MethodGet, path, nil)
		request.Header.Set("Authorization", "Bearer credential")
		if rangeHeader != "" {
			request.Header.Set("Range", rangeHeader)
		}
		response := httptest.NewRecorder()
		httpAPI.ServeHTTP(response, request)
		return response
	}

	whole := play("")
	require.Equal(t, http.StatusOK, whole.Code, "body=%s", whole.Body.String())
	require.Equal(t, fake.clipContent, whole.Body.String())
	require.Equal(t, "bytes", whole.Header().Get("Accept-Ranges"))
	require.Equal(t, "video/mp4", whole.Header().Get("Content-Type"))

	require.Equal(t, 1, fake.playedClips, "a request asks the application once")

	part := play("bytes=10-14")
	require.Equal(t, http.StatusPartialContent, part.Code, "body=%s", part.Body.String())
	require.Equal(t, 2, fake.playedClips, "a range request asks the application once")
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
	fake := newExamAttemptHTTPFake(t)
	httpAPI := newExamAttemptFocusedAPI(t, fake)
	path := "/api/v1/exams/" + fake.attempt.ExamID.String() + "/sittings/" + fake.attempt.SittingID.String() +
		"/attempts/" + fake.attempt.ID.String() + "/end"
	privateReason := "candidate requested an assisted early end"
	request := newJSONRequest(http.MethodPost, path, strings.NewReader(`{"expected_revision":1,"reason":"`+privateReason+`"}`))
	request.Header.Set("Authorization", "Bearer credential")
	request.Header.Set("Idempotency-Key", "manager-end-once")
	response := httptest.NewRecorder()
	httpAPI.ServeHTTP(response, request)
	require.Equal(t, http.StatusOK, response.Code, "status/cache=%d/%q body=%s", response.Code, response.Header().Get("Cache-Control"), response.Body.String())
	require.Equal(t, "no-store", response.Header().Get("Cache-Control"), "status/cache=%d/%q body=%s", response.Code, response.Header().Get("Cache-Control"), response.Body.String())
	require.Equal(t, fake.attempt.ExamID, fake.managerEnd.ExamID)
	require.Equal(t, fake.attempt.SittingID, fake.managerEnd.SittingID)
	require.Equal(t, fake.attempt.ID, fake.managerEnd.AttemptID)
	require.Equal(t, int64(1), fake.managerEnd.ExpectedAttemptRevision)
	require.Equal(t, privateReason, fake.managerEnd.PrivateReason)
	require.Equal(t, "manager-end-once", fake.managerEnd.IdempotencyKey)
	require.NotContains(t, response.Body.String(), privateReason)
	require.NotContains(t, response.Body.String(), "private")
	require.Contains(t, response.Body.String(), fake.submissionReceipt.SubmissionID.String())
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
	fake := newExamAttemptHTTPFake(t)
	fake.attempt.State = model.ExamAttemptSuspended
	suspension := &store.ExamAttemptSuspensionView{ID: model.NewAttemptSuspensionID(), AttemptID: fake.attempt.ID,
		ParticipationID: model.NewAttemptParticipationID(), FlagID: model.NewIntegrityFlagID(), Generation: 2,
		State: model.AttemptSuspensionActive, Source: model.AttemptSuspensionSourcePolicy,
		CandidateReason: model.AttemptSuspensionCandidateReasonSecureContinuityLost, StartedAt: fake.attempt.UpdatedAt}
	fake.managerPage.Items[0].ActiveSuspension = suspension
	httpAPI := newExamAttemptFocusedAPI(t, fake)
	path := "/api/v1/exams/" + fake.attempt.ExamID.String() + "/sittings/" + fake.attempt.SittingID.String() + "/attempts/" + fake.attempt.ID.String()
	request := httptest.NewRequestWithContext(t.Context(), http.MethodGet, path, nil)
	request.Header.Set("Authorization", "Bearer credential")
	response := httptest.NewRecorder()
	httpAPI.ServeHTTP(response, request)
	require.Equal(t, http.StatusOK, response.Code, "manager suspension response")
	require.Contains(t, response.Body.String(), suspension.ID.String(), "manager suspension response")
	require.Contains(t, response.Body.String(), `"candidate_reason":"secure_connectivity_lost"`, "manager suspension response")
	require.NotContains(t, response.Body.String(), "private_reason", "manager suspension response")
}

func TestCandidateWorkspaceJournalPagesExactProtectedQueries(t *testing.T) {
	t.Parallel()
	fake := newExamAttemptHTTPFake(t)
	httpAPI := newExamAttemptFocusedAPI(t, fake)
	base := "/api/v1/exam-attempts/" + fake.attempt.ID.String() + "/workspace"

	journalRequest := fake.candidateRequest(t, http.MethodGet, base+"/changes?workspace_cursor=3&limit=20")
	journalResponse := httptest.NewRecorder()
	httpAPI.ServeHTTP(journalResponse, journalRequest)
	require.Equal(t, http.StatusOK, journalResponse.Code, "journal=%d %s query=%#v", journalResponse.Code, journalResponse.Body.String(), fake.journalQuery)
	require.Equal(t, int64(3), fake.journalQuery.AfterCursor, "journal=%d %s query=%#v", journalResponse.Code, journalResponse.Body.String(), fake.journalQuery)
	require.Equal(t, 20, fake.journalQuery.Limit, "journal=%d %s query=%#v", journalResponse.Code, journalResponse.Body.String(), fake.journalQuery)
	require.Contains(t, journalResponse.Body.String(), `"current_cursor":5`, "journal=%d %s query=%#v", journalResponse.Code, journalResponse.Body.String(), fake.journalQuery)
	require.Contains(t, journalResponse.Body.String(), `"origin":"editor"`, "every change names its writer")
	require.Contains(t, journalResponse.Header().Get("Cache-Control"), "no-store", "journal=%d %s query=%#v", journalResponse.Code, journalResponse.Body.String(), fake.journalQuery)
	fake.journalPage.Entries[0].Operation = model.AttemptWorkspaceMutationDeleteEntry
	fake.journalPage.Entries[0].EntryKind = model.StarterWorkspaceEntryDirectory
	fake.journalPage.Entries[0].OldPath = "src"
	fake.journalPage.Entries[0].NewPath, fake.journalPage.Entries[0].ContentVersion = "", ""
	fake.journalPage.Entries[0].Recursive = true
	fake.journalPage.Entries[0].Origin = model.AttemptWorkspaceOriginEnvironment
	journalResponse = httptest.NewRecorder()
	httpAPI.ServeHTTP(journalResponse, fake.candidateRequest(t, http.MethodGet, base+"/changes?workspace_cursor=3&limit=20"))
	require.Equal(t, http.StatusOK, journalResponse.Code, "recursive journal")
	require.Contains(t, journalResponse.Body.String(), `"recursive":true`, "recursive journal")
	require.Contains(t, journalResponse.Body.String(), `"origin":"environment"`, "an environment change names its writer")
	require.Contains(t, journalResponse.Body.String(), `"old_path":"src"`, "recursive journal")
	fake.journalPage.HasMore = true
	journalResponse = httptest.NewRecorder()
	httpAPI.ServeHTTP(journalResponse, fake.candidateRequest(t, http.MethodGet, base+"/changes?workspace_cursor=3&limit=1"))
	require.Equal(t, http.StatusOK, journalResponse.Code, "paged journal")
	var journal candidateWorkspaceJournalResponse
	require.NoError(t, json.Unmarshal(journalResponse.Body.Bytes(), &journal))
	next, err := route.DecodeCursor(journal.NextCursor, candidateWorkspaceJournalCursorSpec())
	require.NoError(t, err, "next_cursor")
	require.Equal(t, fake.journalPage.Entries[len(fake.journalPage.Entries)-1].Cursor, next.After, "continuation after the last change")
	journalResponse = httptest.NewRecorder()
	httpAPI.ServeHTTP(journalResponse, fake.candidateRequest(t, http.MethodGet, base+"/changes?cursor="+journal.NextCursor))
	require.Equal(t, http.StatusOK, journalResponse.Code, "continued journal")
	require.Equal(t, next.After, fake.journalQuery.AfterCursor, "continued journal")
	journalResponse = httptest.NewRecorder()
	httpAPI.ServeHTTP(journalResponse, fake.candidateRequest(t, http.MethodGet, base+"/changes?workspace_cursor=3&cursor="+journal.NextCursor))
	require.Equal(t, http.StatusBadRequest, journalResponse.Code, "a continuation cannot restart at another Workspace Cursor")
	fake.journalPage.HasMore = false
}

func TestCandidateWorkspaceBatchMapsEveryMutationAndStreamsBodiesInOrder(t *testing.T) {
	t.Parallel()
	fake := newExamAttemptHTTPFake(t)
	httpAPI := newExamAttemptFocusedAPI(t, fake)
	entryID := fake.workspacePage.Items[0].EntryID
	version := fake.workspacePage.Items[0].ContentVersion
	directoryID := model.NewAttemptWorkspaceEntryID()
	manifest := fake.workspaceManifest(4,
		map[string]any{"operation": "create_directory", "path": "src"},
		map[string]any{"operation": "create_file", "path": "src/a.txt", "media_type": "text/plain", "size": 5, "sha256": strings.Repeat("e", 64)},
		map[string]any{"operation": "replace_file", "entry_id": entryID.String(), "expected_path": "cmd/main.go",
			"expected_content_version": version.String(), "media_type": "text/x-go", "size": 3, "sha256": strings.Repeat("f", 64)},
		map[string]any{"operation": "move_entry", "entry_id": entryID.String(), "expected_path": "cmd/main.go", "destination_path": "src/main.go"},
		map[string]any{"operation": "delete_entry", "entry_id": directoryID.String(), "expected_path": "old",
			"expected_content_version": version.String(), "recursive": true},
	)
	body, contentType := candidateWorkspaceBatch(t, manifest, []byte("hello"), []byte("new"))
	response := httptest.NewRecorder()
	httpAPI.ServeHTTP(response, candidateWorkspaceBatchRequest(fake, body, contentType, "batch-once"))
	assertCandidateWorkspaceMutationHTTP(t, fake, response, http.StatusOK)

	command := fake.workspaceBatch
	require.Equal(t, 1, fake.workspaceBatchCalls)
	require.Equal(t, fake.attempt.ID, command.Access.AttemptID)
	require.Equal(t, fake.connection.ID, command.Access.ConnectionID)
	require.Equal(t, fake.credential, command.Access.ContinuityCredential)
	require.Equal(t, fake.participation.ID, command.Access.ParticipationID)
	require.Equal(t, int64(1), command.Access.Generation)
	require.Equal(t, "batch-once", command.IdempotencyKey)
	require.NotNil(t, command.ExpectedWorkspaceCursor)
	require.Equal(t, int64(4), *command.ExpectedWorkspaceCursor)
	require.Equal(t, []attempt.WorkspaceBatchMutation{
		{Operation: model.AttemptWorkspaceMutationCreateDirectory, DestinationPath: "src"},
		{Operation: model.AttemptWorkspaceMutationCreateFile, DestinationPath: "src/a.txt", MediaType: "text/plain", Size: 5, SHA256: strings.Repeat("e", 64)},
		{Operation: model.AttemptWorkspaceMutationReplaceFile, EntryID: entryID, ExpectedPath: "cmd/main.go",
			ExpectedContentVersion: version, MediaType: "text/x-go", Size: 3, SHA256: strings.Repeat("f", 64)},
		{Operation: model.AttemptWorkspaceMutationMoveEntry, EntryID: entryID, ExpectedPath: "cmd/main.go", DestinationPath: "src/main.go"},
		{Operation: model.AttemptWorkspaceMutationDeleteEntry, EntryID: directoryID, ExpectedPath: "old",
			ExpectedContentVersion: version, Recursive: true},
	}, command.Mutations)
	require.Equal(t, [][]byte{[]byte("hello"), []byte("new")}, fake.workspaceBodies, "bodies reach the application in manifest order")
	require.NoError(t, fake.workspaceContentErr)

	var shape map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(response.Body.Bytes(), &shape))
	require.ElementsMatch(t, []string{"workspace_id", "first_workspace_cursor", "workspace_cursor", "changes"}, mapKeys(shape))
	var result candidateWorkspaceBatchResponse
	require.NoError(t, json.Unmarshal(response.Body.Bytes(), &result))
	require.Equal(t, fake.workspace.ID.String(), result.WorkspaceID)
	require.Equal(t, int64(5), result.FirstWorkspaceCursor)
	require.Equal(t, int64(9), result.WorkspaceCursor)
	require.Len(t, result.Changes, 5)
	for index, change := range result.Changes {
		require.Equal(t, int64(5+index), change.Cursor, "change %d", index)
		require.Equal(t, string(command.Mutations[index].Operation), change.Operation, "change %d", index)
		require.Equal(t, model.FormatRFC3339(fake.workspaceChangedAt), change.ChangedAt, "change %d", index)
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
	fake := newExamAttemptHTTPFake(t)
	httpAPI := newExamAttemptFocusedAPI(t, fake)
	body, contentType := candidateWorkspaceBatch(t, fake.workspaceManifest(-1, map[string]any{"operation": "create_directory", "path": "src"}))
	response := httptest.NewRecorder()
	httpAPI.ServeHTTP(response, candidateWorkspaceBatchRequest(fake, body, contentType, "mkdir-once"))
	assertCandidateWorkspaceMutationHTTP(t, fake, response, http.StatusOK)
	require.Equal(t, "src", fake.workspaceBatch.Mutations[0].DestinationPath)
	require.Nil(t, fake.workspaceBatch.ExpectedWorkspaceCursor)
	require.Empty(t, fake.workspaceBodies)

	empty := fake.workspaceManifest(-1, map[string]any{"operation": "create_file", "path": "empty.txt",
		"media_type": "text/plain", "size": 0, "sha256": strings.Repeat("e", 64)})
	body, contentType = candidateWorkspaceBatch(t, empty, []byte{})
	response = httptest.NewRecorder()
	httpAPI.ServeHTTP(response, candidateWorkspaceBatchRequest(fake, body, contentType, "empty-once"))
	assertCandidateWorkspaceMutationHTTP(t, fake, response, http.StatusOK)
	require.Equal(t, int64(0), fake.workspaceBatch.Mutations[0].Size, "an empty file declares size zero")
	require.Equal(t, [][]byte{{}}, fake.workspaceBodies)
}

func TestCandidateWorkspaceBatchRejectsMalformedRequestsBeforeTheApplication(t *testing.T) {
	t.Parallel()
	probe := newExamAttemptHTTPFake(t)
	participationID := probe.participation.ID.String()
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
			fake := newExamAttemptHTTPFake(t)
			fake.participation = probe.participation
			httpAPI := newExamAttemptFocusedAPI(t, fake)
			var request *http.Request
			if test.parts == nil {
				request = candidateWorkspaceBatchRequest(fake, strings.NewReader(test.raw), "application/json", "malformed")
			} else {
				body, contentType := candidateWorkspaceMultipart(t, test.parts...)
				request = candidateWorkspaceBatchRequest(fake, body, contentType, "malformed")
			}
			response := httptest.NewRecorder()
			httpAPI.ServeHTTP(response, request)
			problem := requireCandidateWorkspaceProblem(t, response, http.StatusBadRequest, "request.invalid")
			require.Empty(t, problem.Fields["item"])
			require.Equal(t, 0, fake.workspaceBatchCalls, "a malformed batch reached the application")
		})
	}
}

func TestCandidateWorkspaceBatchRefusesFileBodiesThatDoNotMatchTheManifest(t *testing.T) {
	t.Parallel()
	probe := newExamAttemptHTTPFake(t)
	twoFiles := probe.workspaceManifest(-1,
		map[string]any{"operation": "create_file", "path": "a.txt", "media_type": "text/plain", "size": 1, "sha256": strings.Repeat("a", 64)},
		map[string]any{"operation": "create_file", "path": "b.txt", "media_type": "text/plain", "size": 1, "sha256": strings.Repeat("b", 64)},
	)
	manifest := candidateWorkspaceManifestParts(t, twoFiles)
	for _, test := range []struct {
		name   string
		parts  []candidateWorkspacePart
		item   string
		bodies [][]byte
	}{
		{name: "missing content part", parts: append(append([]candidateWorkspacePart{}, manifest...), candidateWorkspacePart{name: "content", body: []byte("a")}),
			item: "1", bodies: [][]byte{[]byte("a")}},
		{name: "part not named content", parts: append(append([]candidateWorkspacePart{}, manifest...),
			candidateWorkspacePart{name: "content", body: []byte("a")}, candidateWorkspacePart{name: "body", body: []byte("b")}),
			item: "1", bodies: [][]byte{[]byte("a")}},
		{name: "extra part after the last body", parts: append(append([]candidateWorkspacePart{}, manifest...),
			candidateWorkspacePart{name: "content", body: []byte("a")}, candidateWorkspacePart{name: "content", body: []byte("b")},
			candidateWorkspacePart{name: "content", body: []byte("c")}),
			item: "1", bodies: [][]byte{[]byte("a")}},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			fake := newExamAttemptHTTPFake(t)
			fake.participation = probe.participation
			httpAPI := newExamAttemptFocusedAPI(t, fake)
			body, contentType := candidateWorkspaceMultipart(t, test.parts...)
			response := httptest.NewRecorder()
			httpAPI.ServeHTTP(response, candidateWorkspaceBatchRequest(fake, body, contentType, "bodies"))
			require.Error(t, fake.workspaceContentErr, "the batch content accepted a body the manifest does not declare")
			require.Equal(t, test.bodies, fake.workspaceBodies)
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
			fake := newExamAttemptHTTPFake(t)
			httpAPI := newExamAttemptFocusedAPI(t, fake)
			body, contentType := candidateWorkspaceBatch(t, fake.workspaceManifest(-1, map[string]any{"operation": "create_directory", "path": "src"}))
			request := candidateWorkspaceBatchRequest(fake, body, contentType, "headers")
			test.mutate(request)
			response := httptest.NewRecorder()
			httpAPI.ServeHTTP(response, request)
			requireCandidateWorkspaceProblem(t, response, http.StatusBadRequest, test.code)
			require.Equal(t, "no-store", response.Header().Get("Cache-Control"))
			require.Equal(t, 0, fake.workspaceBatchCalls, "an unprotected batch reached the application")
		})
	}
}

func TestCandidateWorkspaceBatchPresentsTheRefusingItem(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name   string
		err    error
		status int
		fields map[string]string
	}{
		{name: "path conflict", err: apperror.New("exam.attempt.workspace.path_conflict").WithField("item", "1"),
			status: http.StatusConflict, fields: map[string]string{"item": "1"}},
		{name: "invalid path", err: apperror.Invalid("exam.attempt.invalid", "workspace_path").WithField("item", "0"),
			status: http.StatusBadRequest, fields: map[string]string{"field": "workspace_path", "item": "0"}},
		{name: "batch capacity names no item", err: apperror.New("exam.attempt.workspace.entry_limit"),
			status: http.StatusConflict},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			fake := newExamAttemptHTTPFake(t)
			fake.workspaceBatchErr = test.err
			httpAPI := newExamAttemptFocusedAPI(t, fake)
			body, contentType := candidateWorkspaceBatch(t, fake.workspaceManifest(-1,
				map[string]any{"operation": "create_directory", "path": "src"},
				map[string]any{"operation": "create_directory", "path": "src"}))
			response := httptest.NewRecorder()
			httpAPI.ServeHTTP(response, candidateWorkspaceBatchRequest(fake, body, contentType, "refused"))
			var fault *apperror.Error
			require.ErrorAs(t, test.err, &fault)
			problem := requireCandidateWorkspaceProblem(t, response, test.status, fault.Code())
			require.Equal(t, test.fields, problem.Fields)
		})
	}
}

func TestCandidateWorkspaceBatchForwardsTheRecursiveDeleteSnapshot(t *testing.T) {
	t.Parallel()
	zero, negative := int64(0), int64(-1)
	for _, test := range []struct {
		name, fields string
		recursive    bool
		want         *int64
	}{
		{name: "zero cursor", fields: `,"expected_workspace_cursor":0`, recursive: true, want: &zero},
		{name: "missing cursor", recursive: true},
		{name: "negative cursor", fields: `,"expected_workspace_cursor":-1`, recursive: true, want: &negative},
		{name: "cursor without recursion", fields: `,"expected_workspace_cursor":0`, want: &zero},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			fake := newExamAttemptHTTPFake(t)
			httpAPI := newExamAttemptFocusedAPI(t, fake)
			entryID := model.NewAttemptWorkspaceEntryID()
			raw := `{"participation_id":"` + fake.participation.ID.String() + `","generation":1` + test.fields +
				`,"mutations":[{"operation":"delete_entry","entry_id":"` + entryID.String() + `","expected_path":"src"` +
				fmt.Sprintf(`,"recursive":%t}]}`, test.recursive)
			body, contentType := candidateWorkspaceMultipart(t, candidateWorkspacePart{name: "manifest", body: []byte(raw)})
			response := httptest.NewRecorder()
			httpAPI.ServeHTTP(response, candidateWorkspaceBatchRequest(fake, body, contentType, "recursive"))
			assertCandidateWorkspaceMutationHTTP(t, fake, response, http.StatusOK)
			require.Equal(t, test.recursive, fake.workspaceBatch.Mutations[0].Recursive)
			require.Equal(t, entryID, fake.workspaceBatch.Mutations[0].EntryID)
			require.Equal(t, test.want, fake.workspaceBatch.ExpectedWorkspaceCursor, "the application owns the cursor rule")
		})
	}
}

func TestCandidateWorkspaceBatchRefusesToPresentAResultWithoutOneChangePerMutation(t *testing.T) {
	t.Parallel()
	fake := newExamAttemptHTTPFake(t)
	fake.workspaceBatchResult = &attempt.WorkspaceBatchResult{WorkspaceID: fake.workspace.ID}
	httpAPI := newExamAttemptFocusedAPI(t, fake)
	body, contentType := candidateWorkspaceBatch(t, fake.workspaceManifest(-1, map[string]any{"operation": "create_directory", "path": "src"}))
	response := httptest.NewRecorder()
	httpAPI.ServeHTTP(response, candidateWorkspaceBatchRequest(fake, body, contentType, "empty-result"))
	require.Equal(t, http.StatusInternalServerError, response.Code, "response=%d body=%s", response.Code, response.Body.String())
	require.Equal(t, 1, fake.workspaceBatchCalls)
}

func TestCandidateWorkspaceSingleMutationRoutesAreGone(t *testing.T) {
	t.Parallel()
	fake := newExamAttemptHTTPFake(t)
	httpAPI := newExamAttemptFocusedAPI(t, fake)
	base := "/api/v1/exam-attempts/" + fake.attempt.ID.String() + "/workspace"
	entryID := fake.workspacePage.Items[0].EntryID.String()
	for _, test := range []struct{ method, path string }{
		{http.MethodPost, base + "/directories"},
		{http.MethodPost, base + "/files"},
		{http.MethodPatch, base + "/entries/" + entryID},
		{http.MethodPut, base + "/files/" + entryID + "/content"},
		{http.MethodDelete, base + "/entries/" + entryID},
	} {
		body := strings.NewReader(`{"participation_id":"` + fake.participation.ID.String() + `","generation":1,"path":"src"}`)
		request := candidateWorkspaceRequest(fake, test.method, test.path, body, "application/json", "gone")
		response := httptest.NewRecorder()
		httpAPI.ServeHTTP(response, request)
		require.Contains(t, []int{http.StatusNotFound, http.StatusMethodNotAllowed}, response.Code, "%s %s = %d %s", test.method, test.path, response.Code, response.Body.String())
	}
	require.Equal(t, 0, fake.workspaceBatchCalls)
}

func candidateWorkspaceRequest(fake *examAttemptHTTPFake, method, path string, body io.Reader, contentType, key string) *http.Request {
	request := newJSONRequest(method, path, body)
	request.Header.Set("Authorization", "Bearer credential")
	request.Header.Set(candidateAttemptCredentialHeader, fake.credential)
	request.Header.Set(candidateAttemptConnectionHeader, fake.connection.ID.String())
	request.Header.Set("Content-Type", contentType)
	if key != "" {
		request.Header.Set("Idempotency-Key", key)
	}
	return request
}

func candidateWorkspaceBatchRequest(fake *examAttemptHTTPFake, body io.Reader, contentType, key string) *http.Request {
	return candidateWorkspaceRequest(fake, http.MethodPost, "/api/v1/exam-attempts/"+fake.attempt.ID.String()+"/workspace/batches", body, contentType, key)
}

// workspaceManifest is a valid manifest for the fake's Participation. A
// negative cursor leaves expected_workspace_cursor out.
func (f *examAttemptHTTPFake) workspaceManifest(cursor int64, mutations ...map[string]any) map[string]any {
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

func assertCandidateWorkspaceMutationHTTP(t *testing.T, fake *examAttemptHTTPFake, response *httptest.ResponseRecorder, status int) {
	t.Helper()
	require.Equal(t, status, response.Code, "response=%d headers=%v body=%s", response.Code, response.Header(), response.Body.String())
	require.Contains(t, response.Header().Get("Cache-Control"), "no-store", "response=%d headers=%v body=%s", response.Code, response.Header(), response.Body.String())
	body := response.Body.String()
	for _, forbidden := range []string{fake.credential, "continuity_credential", "credential_hash", "participation_id", "generation", "object_id", "session_id"} {
		if forbidden != "" {
			require.NotContains(t, body, forbidden, "response contains")
		}
	}
}

type examAttemptHTTPFake struct {
	principal                 model.Principal
	attempt                   *model.ExamAttempt
	workspace                 *model.ExamAttemptWorkspace
	participation             *store.ExamAttemptParticipationView
	connection                *store.ExamAttemptManagerConnection
	credential                string
	resourceID                model.ExamResourceID
	presentation              attempt.Presentation
	workspacePage             attempt.WorkspacePage
	managerPage               attempt.ManagedAttemptPage
	browserActivityPage       attempt.BrowserActivityPage
	browserActivityQuery      attempt.BrowserActivityPageQuery
	correctionAcknowledgement attempt.AcknowledgeCorrectionCommand
	managerEnd                attempt.ManagerEndCommand
	content                   attempt.OpenedContent
	workspaceContentVersion   model.WorkspaceContentVersion
	presentationAccess        attempt.CandidateAccess
	presentationCalls         int
	workspaceQuery            attempt.WorkspaceQuery
	managerList               attempt.ListManagedAttemptsQuery
	managerGet                struct {
		ExamID    model.ExamID
		SittingID model.ExamSittingID
		AttemptID model.ExamAttemptID
	}
	workspaceOpen           attempt.OpenWorkspaceFileQuery
	reallow                 attempt.ReallowCommand
	suspend                 attempt.ManualSuspensionCommand
	screenClip              attempt.RequestScreenClipCommand
	takenClip               model.ScreenClipID
	clipUpload              model.ScreenClipUpload
	clipChunk               attempt.UploadScreenClipChunkCommand
	clipChunkBytes          []byte
	clipCompletion          attempt.CompleteScreenClipUploadCommand
	clipContent             string
	playedClips             int
	playedClip              model.ScreenClipID
	journalQuery            attempt.WorkspaceJournalQuery
	journalPage             attempt.WorkspaceJournalPage
	workspaceBatch          attempt.ApplyWorkspaceBatchCommand
	workspaceBatchCalls     int
	workspaceBatchErr       error
	workspaceBatchResult    *attempt.WorkspaceBatchResult
	workspaceBodies         [][]byte
	workspaceContentErr     error
	workspaceChangedAt      time.Time
	submit                  attempt.SubmitCommand
	submissionReceipt       store.ExamSubmissionReceipt
	submissionView          attempt.ManagedSubmission
	submissionManifest      attempt.SubmissionManifestPage
	submissionGet           attempt.GetSubmissionQuery
	submissionManifestQuery attempt.ListSubmissionManifestQuery
	submissionOpen          attempt.OpenSubmissionFileQuery
}

func newExamAttemptHTTPFake(t *testing.T) *examAttemptHTTPFake {
	t.Helper()
	at := time.Date(2026, time.August, 17, 9, 0, 0, 123000000, time.UTC)
	examAttempt, err := model.NewExamAttempt(model.NewExamAttemptID(), model.NewExamID(), model.NewExamSittingID(), model.NewUserID(), model.NewExamRevisionID(), at)
	require.NoError(t, err)
	workspace, err := model.NewExamAttemptWorkspace(model.NewExamAttemptWorkspaceID(), examAttempt.ID, at)
	require.NoError(t, err)
	connectionID := model.NewAttemptConnectionID()
	resourceID := model.NewExamResourceID()
	workspaceItem := store.CandidateAttemptWorkspaceItem{EntryID: model.NewAttemptWorkspaceEntryID(), Kind: model.StarterWorkspaceEntryFile,
		Path: "cmd/main.go", ContentVersion: model.NewWorkspaceContentVersion(), MediaType: "text/x-go", SizeBytes: 9, SHA256: strings.Repeat("a", 64)}
	principal := testExamHTTPPrincipal()
	fake := &examAttemptHTTPFake{principal: principal, attempt: examAttempt, workspace: workspace,
		participation: &store.ExamAttemptParticipationView{ID: model.NewAttemptParticipationID(), AttemptID: examAttempt.ID, State: model.AttemptParticipationActive,
			Generation: 1, StartedAt: at, UpdatedAt: at, LeaseExpiresAt: at.Add(20 * time.Second)},
		connection: &store.ExamAttemptManagerConnection{ID: connectionID, State: model.AttemptConnectionOpen, OpenedAt: at},
		credential: model.NewCredentialToken(), resourceID: resourceID,
		presentation: attempt.Presentation{AttemptID: examAttempt.ID, SittingID: examAttempt.SittingID,
			Capacity:            model.ExamCapacityPolicy{ResourceMaximumCount: 7, ResourceMaximumBytes: 4096, WorkspaceMaximumEntries: 42, WorkspaceMaximumFileBytes: 1024, WorkspaceMaximumTotalBytes: 8192},
			RuntimeCapabilities: candidateTestRuntimeCapabilities(t, at, examAttempt), Title: "Algorithms",
			InstructionsMarkdown: "Solve safely.", Resources: []attempt.Resource{{ResourceID: resourceID, DisplayName: "Input",
				DescriptionMarkdown: "Read this.", Position: 0, MediaType: model.ExamResourceMediaText, SizeBytes: 9, SHA256: strings.Repeat("b", 64)}}},
		workspacePage: attempt.WorkspacePage{WorkspaceID: workspace.ID, Cursor: 4,
			Items: []store.CandidateAttemptWorkspaceItem{workspaceItem}},
		managerPage: attempt.ManagedAttemptPage{Items: []store.ExamAttemptManagerSnapshot{{Attempt: examAttempt, Workspace: workspace,
			LatestParticipation: &store.ExamAttemptParticipationView{ID: model.NewAttemptParticipationID(), AttemptID: examAttempt.ID, State: model.AttemptParticipationActive,
				Generation: 1, StartedAt: at, UpdatedAt: at, LeaseExpiresAt: at.Add(20 * time.Second)}, CurrentConnection: &store.ExamAttemptManagerConnection{ID: connectionID, State: model.AttemptConnectionOpen, OpenedAt: at}}}},
		content:                 attempt.OpenedContent{Body: io.NopCloser(strings.NewReader("protected")), MediaType: "text/plain", SizeBytes: 9, SHA256: strings.Repeat("c", 64)},
		workspaceContentVersion: model.NewWorkspaceContentVersion()}
	change := model.AttemptWorkspaceJournalEntry{WorkspaceID: workspace.ID, Cursor: 5, EntryID: workspaceItem.EntryID,
		EntryKind: workspaceItem.Kind, Operation: model.AttemptWorkspaceMutationReplaceFile, OldPath: workspaceItem.Path,
		NewPath: workspaceItem.Path, ContentVersion: workspaceItem.ContentVersion, ChangedAt: at,
		Origin: model.AttemptWorkspaceOriginEditor}
	fake.workspaceChangedAt = at
	fake.journalPage = attempt.WorkspaceJournalPage{WorkspaceID: workspace.ID, CurrentCursor: 5,
		Entries: []model.AttemptWorkspaceJournalEntry{change}}
	sourceID := model.BrowserSourceSessionID("01912ed5-d4f7-4f02-9b97-928b5e8d3f21")
	fake.browserActivityPage = attempt.BrowserActivityPage{Items: []store.BrowserActivityRecord{{AttemptID: examAttempt.ID,
		ParticipationID: fake.participation.ID, Generation: 1, SourceSessionID: sourceID,
		Event: model.BrowserActivityEvent{Sequence: 1, Kind: model.BrowserActivityOpened, PolicyRevisionID: model.NewExamRevisionID(),
			ClientOccurredAt: at, ReceivedAt: at.Add(time.Second)}}}}
	fake.submissionReceipt = store.ExamSubmissionReceipt{BrowserActivity: model.BrowserSubmissionSettlement{State: "not_applicable", InventoryRevision: 1}, SubmissionID: model.NewSubmissionID(), ReceiptCode: model.SubmissionReceiptCode("MBY3KBMNEB83TFW1"), AttemptID: examAttempt.ID,
		ExamRevisionID: examAttempt.AdmissionRevisionID, State: model.ExamAttemptSubmitted, WorkspaceCursor: 4,
		ManifestDigest: strings.Repeat("d", 64), SubmittedAt: at.Add(time.Minute)}
	submissionManifest, err := model.NewExamSubmissionManifest(4, []model.ExamSubmissionManifestEntry{{
		EntryID: workspaceItem.EntryID, Kind: model.StarterWorkspaceEntryFile, Path: workspaceItem.Path,
		ContentVersion: workspaceItem.ContentVersion, MediaType: workspaceItem.MediaType, SizeBytes: workspaceItem.SizeBytes,
		SHA256: workspaceItem.SHA256, StorageOrigin: model.AttemptWorkspaceStorageStarter,
		StarterObjectID: model.NewStarterWorkspaceObjectID(),
	}})
	require.NoError(t, err)
	submission, err := model.NewExamSubmission(model.ExamSubmissionSpecification{ID: fake.submissionReceipt.SubmissionID, ReceiptCode: fake.submissionReceipt.ReceiptCode,
		AttemptID: examAttempt.ID, ExamRevisionID: examAttempt.AdmissionRevisionID, WorkspaceID: workspace.ID, Manifest: submissionManifest,
		BrowserActivity: model.BrowserSubmissionSettlement{State: "not_applicable", InventoryRevision: 1},
		Provenance:      model.ExamSubmissionCandidateSubmitted, SubmittedAt: at.Add(time.Minute)})
	require.NoError(t, err)
	fake.submissionView = attempt.ManagedSubmission{Authorization: store.ExamSubmissionAuthorization{
		SubmissionID: submission.ID, ExamID: examAttempt.ExamID, SittingID: examAttempt.SittingID, AttemptID: examAttempt.ID,
		CandidateUserID: examAttempt.CandidateUserID, AcademicUnitID: model.NewAcademicUnitID()}, Submission: *submission}
	fake.submissionManifest = attempt.SubmissionManifestPage{SubmissionID: submission.ID,
		WorkspaceCursor: submission.WorkspaceCursor, ManifestDigest: submission.ManifestDigest,
		Items: []store.ExamSubmissionManifestItem{{EntryID: workspaceItem.EntryID, Kind: workspaceItem.Kind,
			Path: workspaceItem.Path, ContentVersion: workspaceItem.ContentVersion, MediaType: workspaceItem.MediaType,
			SizeBytes: workspaceItem.SizeBytes, SHA256: workspaceItem.SHA256}}}
	return fake
}

func candidateTestRuntimeCapabilities(t *testing.T, at time.Time, record *model.ExamAttempt) store.CandidateRuntimeCapabilities {
	t.Helper()
	manifest := model.CurrentAttemptConfigurationManifestFingerprint()
	configuration, err := (model.AttemptConfigurationCandidate{ManifestFingerprint: manifest, RegistryFingerprint: "fnv1a64:" + strings.Repeat("b", 16), UserSettingsRevision: model.NewUserSettingsRevision(), DesktopBuild: "test-build", DesktopTarget: "darwin-arm64", Presentation: model.AttemptConfigurationPresentation{ColorTheme: "dark", ZoomPercent: 100, EditorFontSizePX: 14, EditorLineHeightPX: 22, ScreenReaderMode: "auto", AnnouncementMode: "auto", CursorStyle: "line", CursorBlinking: "blink"}, ApprovedCommands: []string{}, ApprovedKeybindings: []string{}}).Freeze(model.NewID())
	require.NoError(t, err)
	return store.CandidateRuntimeCapabilities{SchemaVersion: 1, ServerTime: at,
		InteractionState: store.CandidateInteractionInteractive,
		AttemptConfiguration: store.CandidateAttemptConfiguration{Revision: configuration.Revision, Presentation: configuration.Presentation,
			ApprovedCommands: append([]string{}, configuration.ApprovedCommands...), ApprovedKeybindings: append([]string{}, configuration.ApprovedKeybindings...), Digest: configuration.Digest},
		FocusLossCollectionEnabled: true, WorkspaceMutationAllowed: true, SubmissionAllowed: true,
		Browser: store.CandidateBrowserCapability{State: store.CandidateBrowserDisabled},
		ExamRevision: store.CandidateExamRevisionCapability{AdmissionRevisionID: record.AdmissionRevisionID,
			CurrentRevisionID: model.NewExamRevisionID()},
		Departure: store.CandidateDepartureCapability{Allowed: false, Reason: "attempt_in_progress"}}
}

func newExamAttemptFocusedAPI(t *testing.T, fake *examAttemptHTTPFake) *route.API {
	t.Helper()
	logger, _ := routetest.NewLogger(t)
	return newFocusedResourceAPI(t, logger, fake, examAttemptResource(fake))
}

func (f *examAttemptHTTPFake) candidateRequest(t *testing.T, method, path string) *http.Request {
	request := httptest.NewRequestWithContext(t.Context(), method, path, nil)
	request.Header.Set("Authorization", "Bearer credential")
	request.Header.Set(candidateAttemptCredentialHeader, f.credential)
	request.Header.Set(candidateAttemptConnectionHeader, f.connection.ID.String())
	return request
}

func (f *examAttemptHTTPFake) AuthenticateAccess(context.Context, string) (*model.Principal, error) {
	principal := f.principal
	return &principal, nil
}

func (f *examAttemptHTTPFake) AuthenticateBearer(context.Context, string) (*model.Principal, error) {
	principal := f.principal
	return &principal, nil
}

func (f *examAttemptHTTPFake) GetExamAttempt(
	_ context.Context,
	_ invocation.Invocation,
	examID model.ExamID,
	sittingID model.ExamSittingID,
	attemptID model.ExamAttemptID,
) (store.ExamAttemptManagerSnapshot, error) {
	f.managerGet.ExamID, f.managerGet.SittingID, f.managerGet.AttemptID = examID, sittingID, attemptID
	return f.managerPage.Items[0], nil
}

func (f *examAttemptHTTPFake) ListExamAttempts(_ context.Context, _ invocation.Invocation, query attempt.ListManagedAttemptsQuery) (attempt.ManagedAttemptPage, error) {
	f.managerList = query
	return f.managerPage, nil
}

func (f *examAttemptHTTPFake) ListSittingCandidateStatuses(context.Context, invocation.Invocation,
	attempt.SittingCandidateStatusesQuery,
) (attempt.SittingCandidateStatusesPage, error) {
	return attempt.SittingCandidateStatusesPage{}, nil
}

func (f *examAttemptHTTPFake) EndExamAttemptByManager(_ context.Context, _ invocation.Invocation,
	command attempt.ManagerEndCommand,
) (store.ExamSubmissionReceipt, error) {
	f.managerEnd = command
	return f.submissionReceipt, nil
}

func (f *examAttemptHTTPFake) ListExamAttemptBrowserActivity(_ context.Context, _ invocation.Invocation,
	query attempt.BrowserActivityPageQuery,
) (attempt.BrowserActivityPage, error) {
	f.browserActivityQuery = query
	return f.browserActivityPage, nil
}

func (f *examAttemptHTTPFake) AcknowledgeExamAttemptCorrection(_ context.Context, _ invocation.Invocation,
	command attempt.AcknowledgeCorrectionCommand,
) (attempt.CorrectionAcknowledgementResult, error) {
	f.correctionAcknowledgement = command
	capabilities := f.presentation.RuntimeCapabilities
	capabilities.ExamRevision.CurrentRevisionID = command.ExpectedCurrentRevisionID
	return attempt.CorrectionAcknowledgementResult{CorrectionRevisionID: command.CorrectionRevisionID,
		CurrentRevisionID: command.ExpectedCurrentRevisionID, RuntimeCapabilities: capabilities, AcknowledgedAt: model.OptionalTimeFrom(f.attempt.UpdatedAt.Add(time.Minute))}, nil
}

func (f *examAttemptHTTPFake) ReallowExamAttempt(_ context.Context, inv invocation.Invocation, command attempt.ReallowCommand) (attempt.ReallowResult, error) {
	f.reallow = command
	examAttempt := *f.attempt
	examAttempt.State, examAttempt.UpdatedAt, examAttempt.Revision = model.ExamAttemptActive, examAttempt.UpdatedAt.Add(time.Minute), command.ExpectedAttemptRevision+1
	return attempt.ReallowResult{ExamID: examAttempt.ExamID, SittingID: examAttempt.SittingID,
		ClassID: model.NewClassID(), CandidateUserID: examAttempt.CandidateUserID, Attempt: examAttempt,
		Suspension: store.ExamAttemptSuspensionView{ID: command.SuspensionID, AttemptID: examAttempt.ID,
			ParticipationID: model.NewAttemptParticipationID(), FlagID: model.NewIntegrityFlagID(), Generation: 1,
			State: model.AttemptSuspensionClosed, Source: model.AttemptSuspensionSourcePolicy,
			CandidateReason: model.AttemptSuspensionCandidateReasonSecureContinuityLost, StartedAt: examAttempt.CreatedAt,
			EndedAt: model.OptionalTimeFrom(examAttempt.UpdatedAt), ReallowedByUserID: inv.Principal().UserID}}, nil
}

func (f *examAttemptHTTPFake) RequestScreenClip(_ context.Context, inv invocation.Invocation, command attempt.RequestScreenClipCommand) (attempt.RequestScreenClipResult, error) {
	f.screenClip = command
	at := f.attempt.UpdatedAt
	return attempt.RequestScreenClipResult{Clip: store.ExamAttemptScreenClipView{
		ID: model.NewScreenClipID(), AttemptID: command.AttemptID, Source: model.ScreenClipSourceManager,
		RequestedByUserID: inv.Principal().UserID, Range: command.Range, State: model.ScreenClipRequested,
		RequestedAt: at, UpdatedAt: at, Revision: 1,
	}}, nil
}

func (f *examAttemptHTTPFake) PendingScreenClips(context.Context, invocation.Invocation, attempt.CandidateAccess) ([]store.ExamAttemptScreenClipView, error) {
	return []store.ExamAttemptScreenClipView{}, nil
}

func (f *examAttemptHTTPFake) TakeScreenClip(
	_ context.Context,
	_ invocation.Invocation,
	access attempt.CandidateAccess,
	clipID model.ScreenClipID,
) (store.ExamAttemptScreenClipView, error) {
	f.takenClip = clipID
	at := f.attempt.UpdatedAt
	return store.ExamAttemptScreenClipView{
		ID: clipID, AttemptID: access.AttemptID, Source: model.ScreenClipSourceManager,
		Range: model.ScreenClipRange{FromSessionStart: true, End: at}, State: model.ScreenClipRecording,
		DeliveredParticipationID: model.NewAttemptParticipationID(), RequestedAt: at, UpdatedAt: at, Revision: 2,
	}, nil
}

func (f *examAttemptHTTPFake) BeginScreenClipUpload(
	_ context.Context,
	_ invocation.Invocation,
	command attempt.BeginScreenClipUploadCommand,
) (store.ExamAttemptScreenClipView, error) {
	access, clipID, upload := command.Access, command.ClipID, command.Upload
	f.clipUpload = upload
	at := f.attempt.UpdatedAt
	return store.ExamAttemptScreenClipView{
		ID: clipID, AttemptID: access.AttemptID, Source: model.ScreenClipSourceManager,
		Range: model.ScreenClipRange{FromSessionStart: true, End: at}, State: model.ScreenClipUploading,
		DeliveredParticipationID: model.NewAttemptParticipationID(), Upload: upload.Clone(),
		RequestedAt: at, UpdatedAt: at, Revision: 3,
	}, nil
}

func (f *examAttemptHTTPFake) ScreenClipUploadStatus(
	ctx context.Context,
	inv invocation.Invocation,
	access attempt.CandidateAccess,
	clipID model.ScreenClipID,
) (store.ExamAttemptScreenClipUploadStatus, error) {
	clip, err := f.BeginScreenClipUpload(ctx, inv, attempt.BeginScreenClipUploadCommand{Access: access, ClipID: clipID, Upload: f.clipUpload})
	return store.ExamAttemptScreenClipUploadStatus{Clip: clip, Received: []int{0}}, err
}

func (f *examAttemptHTTPFake) UploadScreenClipChunk(_ context.Context, _ invocation.Invocation, command attempt.UploadScreenClipChunkCommand) error {
	body, err := io.ReadAll(command.Body)
	f.clipChunk, f.clipChunkBytes = command, body
	return err
}

func (f *examAttemptHTTPFake) CompleteScreenClipUpload(
	ctx context.Context,
	inv invocation.Invocation,
	command attempt.CompleteScreenClipUploadCommand,
) (store.ExamAttemptScreenClipView, error) {
	f.clipCompletion = command
	clip, err := f.BeginScreenClipUpload(ctx, inv, attempt.BeginScreenClipUploadCommand{
		Access: command.Access, ClipID: command.ClipID, Upload: f.clipUpload,
	})
	clip.State, clip.Signature = model.ScreenClipReady, command.Signature
	return clip, err
}

func (f *examAttemptHTTPFake) ListScreenClips(context.Context, invocation.Invocation, model.ExamID, model.ExamSittingID, model.ExamAttemptID) ([]store.ExamAttemptScreenClipView, error) {
	return []store.ExamAttemptScreenClipView{}, nil
}

func (f *examAttemptHTTPFake) PlayScreenClip(_ context.Context, _ invocation.Invocation, query attempt.ScreenClipQuery) (attempt.PlayScreenClipResult, error) {
	f.playedClips++
	f.playedClip = query.ClipID
	return attempt.PlayScreenClipResult{MediaType: "video/mp4", SizeBytes: int64(len(f.clipContent)),
		Open: func(start, length int64) (io.ReadCloser, error) {
			return io.NopCloser(strings.NewReader(f.clipContent[start : start+length])), nil
		}}, nil
}

func (f *examAttemptHTTPFake) SuspendExamAttempt(_ context.Context, inv invocation.Invocation, command attempt.ManualSuspensionCommand) (attempt.ManualSuspensionResult, error) {
	f.suspend = command
	examAttempt := *f.attempt
	examAttempt.State, examAttempt.UpdatedAt, examAttempt.Revision = model.ExamAttemptSuspended, examAttempt.UpdatedAt.Add(time.Minute), command.ExpectedAttemptRevision+1
	return attempt.ManualSuspensionResult{ExamID: examAttempt.ExamID, SittingID: examAttempt.SittingID,
		ClassID: model.NewClassID(), CandidateUserID: examAttempt.CandidateUserID, Attempt: examAttempt, ConnectionClosed: true,
		Suspension: store.ExamAttemptSuspensionView{ID: model.NewAttemptSuspensionID(), AttemptID: examAttempt.ID,
			ParticipationID: model.NewAttemptParticipationID(), Generation: 1, State: model.AttemptSuspensionActive,
			Source: model.AttemptSuspensionSourceManual, CandidateReason: model.AttemptSuspensionCandidateReasonManagerSuspended,
			StartedAt: examAttempt.UpdatedAt, SuspendedByUserID: inv.Principal().UserID}}, nil
}

func (f *examAttemptHTTPFake) GetCandidateExamPresentation(_ context.Context, _ invocation.Invocation, access attempt.CandidateAccess) (attempt.Presentation, error) {
	f.presentationAccess, f.presentationCalls = access, f.presentationCalls+1
	return f.presentation, nil
}

func (f *examAttemptHTTPFake) ListCandidateExamWorkspace(_ context.Context, _ invocation.Invocation, query attempt.WorkspaceQuery) (attempt.WorkspacePage, error) {
	f.workspaceQuery = query
	return f.workspacePage, nil
}

func (f *examAttemptHTTPFake) ListCandidateExamWorkspaceJournal(_ context.Context, _ invocation.Invocation, query attempt.WorkspaceJournalQuery) (attempt.WorkspaceJournalPage, error) {
	f.journalQuery = query
	return f.journalPage, nil
}

// ApplyCandidateExamWorkspaceBatch reads every file body through the batch
// content, in order, as the application does, and refuses an unreadable body
// with the problem the application presents for it.
func (f *examAttemptHTTPFake) ApplyCandidateExamWorkspaceBatch(ctx context.Context, _ invocation.Invocation, command attempt.ApplyWorkspaceBatchCommand) (attempt.WorkspaceBatchResult, error) {
	f.workspaceBatch, f.workspaceBatchCalls = command, f.workspaceBatchCalls+1
	f.workspaceBodies, f.workspaceContentErr = nil, nil
	for index, mutation := range command.Mutations {
		if mutation.Operation != model.AttemptWorkspaceMutationCreateFile && mutation.Operation != model.AttemptWorkspaceMutationReplaceFile {
			continue
		}
		body, err := command.Content.Next(ctx)
		if err == nil {
			var raw []byte
			if raw, err = io.ReadAll(body); err == nil {
				f.workspaceBodies = append(f.workspaceBodies, raw)
			}
		}
		if err != nil {
			f.workspaceContentErr = err
			return attempt.WorkspaceBatchResult{}, apperror.Invalid("exam.attempt.invalid", "workspace_content").
				WithField("item", strconv.Itoa(index)).Wrap(err)
		}
	}
	if f.workspaceBatchErr != nil {
		return attempt.WorkspaceBatchResult{}, f.workspaceBatchErr
	}
	if f.workspaceBatchResult != nil {
		return *f.workspaceBatchResult, nil
	}

	result := attempt.WorkspaceBatchResult{AttemptID: f.attempt.ID, SittingID: f.attempt.SittingID,
		CandidateUserID: f.attempt.CandidateUserID, WorkspaceID: f.workspace.ID}
	for index, mutation := range command.Mutations {
		change := model.AttemptWorkspaceJournalEntry{WorkspaceID: f.workspace.ID, Cursor: int64(5 + index), EntryID: mutation.EntryID,
			EntryKind: model.StarterWorkspaceEntryFile, Operation: mutation.Operation, OldPath: mutation.ExpectedPath,
			NewPath: mutation.DestinationPath, ChangedAt: f.workspaceChangedAt, Recursive: mutation.Recursive,
			Origin: model.AttemptWorkspaceOriginEditor}
		switch mutation.Operation {
		case model.AttemptWorkspaceMutationCreateDirectory, model.AttemptWorkspaceMutationCreateFile:
			change.EntryID = model.NewAttemptWorkspaceEntryID()
			if mutation.Operation == model.AttemptWorkspaceMutationCreateDirectory {
				change.EntryKind = model.StarterWorkspaceEntryDirectory
			}
		case model.AttemptWorkspaceMutationReplaceFile:
			change.NewPath, change.ContentVersion = mutation.ExpectedPath, model.NewWorkspaceContentVersion()
		case model.AttemptWorkspaceMutationDeleteEntry:
			if mutation.Recursive {
				change.EntryKind = model.StarterWorkspaceEntryDirectory
			}
		}
		result.Changes = append(result.Changes, change)
	}
	return result, nil
}

func (f *examAttemptHTTPFake) SubmitExamAttempt(_ context.Context, _ invocation.Invocation,
	command attempt.SubmitCommand,
) (store.ExamSubmissionReceipt, error) {
	f.submit = command
	return f.submissionReceipt, nil
}

func (f *examAttemptHTTPFake) GetExamSubmission(_ context.Context, _ invocation.Invocation,
	query attempt.GetSubmissionQuery,
) (attempt.ManagedSubmission, error) {
	f.submissionGet = query
	return f.submissionView, nil
}

func (f *examAttemptHTTPFake) ListExamSubmissionManifest(_ context.Context, _ invocation.Invocation,
	query attempt.ListSubmissionManifestQuery,
) (attempt.SubmissionManifestPage, error) {
	f.submissionManifestQuery = query
	return f.submissionManifest, nil
}

func (f *examAttemptHTTPFake) OpenExamSubmissionFile(_ context.Context, _ invocation.Invocation,
	query attempt.OpenSubmissionFileQuery,
) (attempt.OpenedContent, error) {
	f.submissionOpen = query
	f.content.Body = io.NopCloser(strings.NewReader("protected"))
	f.content.ContentVersion = f.submissionManifest.Items[0].ContentVersion
	f.content.SHA256 = f.submissionManifest.Items[0].SHA256
	f.content.MediaType = f.submissionManifest.Items[0].MediaType
	f.content.SizeBytes = f.submissionManifest.Items[0].SizeBytes
	return f.content, nil
}

func (f *examAttemptHTTPFake) OpenCandidateExamResource(context.Context, invocation.Invocation, attempt.OpenResourceQuery) (attempt.OpenedContent, error) {
	f.content.Body = io.NopCloser(strings.NewReader("protected"))
	return f.content, nil
}

func (f *examAttemptHTTPFake) OpenCandidateExamWorkspaceFile(_ context.Context, _ invocation.Invocation, query attempt.OpenWorkspaceFileQuery) (attempt.OpenedContent, error) {
	f.workspaceOpen = query
	f.content.Body = io.NopCloser(strings.NewReader("protected"))
	f.content.ContentVersion = f.workspaceContentVersion
	return f.content, nil
}

func (f *examAttemptHTTPFake) SecurityPolicyOverview(context.Context, invocation.Invocation, model.ExamSittingID) (*store.SecurityPolicyOverview, error) {
	return nil, apperror.New("exam.attempt.unavailable")
}

func (f *examAttemptHTTPFake) PrepareExamSecurityPreflight(context.Context, invocation.Invocation, attempt.PrepareSecurityPreflightCommand) (*store.SecurityPreflightPrepared, error) {
	return nil, errors.New("unexpected preflight prepare")
}
func (f *examAttemptHTTPFake) ReportExamSecurityPreflight(context.Context, invocation.Invocation, attempt.ReportSecurityPreflightCommand) (*model.SecurityPreflightResult, error) {
	return nil, errors.New("unexpected preflight report")
}

func (f *examAttemptHTTPFake) RecoverExamSecurityPolicy(context.Context, invocation.Invocation, model.ExamAttemptID) (*store.SecurityPolicyRecovery, error) {
	return nil, errors.New("unexpected security recovery")
}

func (f *examAttemptHTTPFake) NativeDeliveryStatus(context.Context, invocation.Invocation, attempt.NativeDeliveryQuery) (*model.NativeSecurityStreamStatus, error) {
	return nil, errors.New("unexpected NativeDeliveryStatus")
}

func (f *examAttemptHTTPFake) NativeDeliveryReceipt(context.Context, invocation.Invocation, attempt.NativeDeliveryQuery, int64) (*model.NativeBatchReceipt, error) {
	return nil, errors.New("unexpected NativeDeliveryReceipt")
}

func (f *examAttemptHTTPFake) DeclareNativeDeliveryGaps(context.Context, invocation.Invocation, attempt.NativeDeliveryGapsCommand) (*model.DeliveryGapReceipt, error) {
	return nil, errors.New("unexpected DeclareNativeDeliveryGaps")
}

func (f *examAttemptHTTPFake) SealNativeDelivery(context.Context, invocation.Invocation, attempt.NativeDeliveryFinalCommand) (*model.NativeSecurityStreamStatus, error) {
	return nil, errors.New("unexpected SealNativeDelivery")
}

func (f *examAttemptHTTPFake) UpdateNativeDeliverySummary(context.Context, invocation.Invocation, attempt.NativeDeliverySummaryCommand) (*model.NativeSecurityStreamStatus, error) {
	return nil, errors.New("unexpected UpdateNativeDeliverySummary")
}

func (f *examAttemptHTTPFake) AppendNativeDelivery(context.Context, invocation.Invocation, attempt.NativeDeliveryAppendCommand) (*model.NativeSecurityAcknowledgement, error) {
	return nil, errors.New("unexpected AppendNativeDelivery")
}

func (f *examAttemptHTTPFake) BrowserSourceStatus(context.Context, invocation.Invocation, attempt.BrowserSourceQuery) (*model.BrowserSourceStatus, error) {
	return nil, apperror.New("exam.attempt.unavailable")
}
func (f *examAttemptHTTPFake) BrowserSourceList(context.Context, invocation.Invocation, attempt.BrowserSourceQuery) ([]model.BrowserSourceStatus, error) {
	return nil, apperror.New("exam.attempt.unavailable")
}

func (f *examAttemptHTTPFake) BrowserDeliveryReceipts(context.Context, invocation.Invocation, attempt.BrowserSourceQuery, int64, int) (*model.BrowserReceiptPage, error) {
	return nil, errors.New("unexpected BrowserDeliveryReceipts")
}
func (f *examAttemptHTTPFake) DeclareBrowserDeliveryGaps(context.Context, invocation.Invocation, attempt.BrowserDeliveryGapsCommand) (*model.BrowserDeliveryGapResult, error) {
	return nil, errors.New("unexpected DeclareBrowserDeliveryGaps")
}
func (f *examAttemptHTTPFake) SealBrowserDelivery(context.Context, invocation.Invocation, attempt.BrowserDeliveryFinalCommand) (*model.BrowserSourceStatus, error) {
	return nil, errors.New("unexpected SealBrowserDelivery")
}
func (f *examAttemptHTTPFake) UpdateBrowserDeliverySummary(context.Context, invocation.Invocation, attempt.BrowserDeliverySummaryCommand) (*model.BrowserDeliverySummaryResult, error) {
	return nil, errors.New("unexpected UpdateBrowserDeliverySummary")
}
func (f *examAttemptHTTPFake) AppendHistoricalBrowserDelivery(context.Context, invocation.Invocation, attempt.BrowserDeliveryAppendCommand) (*model.BrowserActivityAcknowledgement, error) {
	return nil, errors.New("unexpected AppendHistoricalBrowserDelivery")
}

func (f *examAttemptHTTPFake) DeliveryBudget(context.Context, invocation.Invocation, attempt.DeliveryBudgetQuery) (*model.DeliveryBudgetSnapshot, error) {
	return nil, errors.New("unexpected DeliveryBudget")
}
func (f *examAttemptHTTPFake) StopDeliveryDetails(context.Context, invocation.Invocation, attempt.StopDeliveryDetailsCommand) (*model.StopDeliveryDetailsResult, error) {
	return nil, errors.New("unexpected StopDeliveryDetails")
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
