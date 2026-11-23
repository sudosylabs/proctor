// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package httpapi

import (
	"crypto/sha256"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/sudosylabs/proctor/server/httpapi/route/routetest"
	"github.com/sudosylabs/proctor/server/model"
)

func recordsHTTPPrincipal() model.Principal {
	return model.Principal{UserID: model.NewUserID(), SessionID: model.NewSessionID(), CredentialID: model.PrincipalCredentialID(model.NewID()), CredentialType: model.CredentialSessionAccess,
		AuthenticationMethod: "password", AuthenticationStrength: model.AuthenticationSingleFactor, AuthenticatedAt: time.Now(), ClientType: model.SessionClientWeb}
}

func TestExamRecordsHTTPRequiresExplicitAcknowledgementAndIdempotency(t *testing.T) {
	logger, _ := routetest.NewLogger(t)
	principal := recordsHTTPPrincipal()
	f := newExamFixture(t, principal)
	api := newFocusedResourceAPI(t, logger, routetest.Authenticator{Principal: principal}, examRecordsResource(f.records, f.people))
	examID, sittingID := f.examID, model.NewExamSittingID()
	path := "/api/v1/exams/" + examID.String() + "/sittings/" + sittingID.String() + "/records-completion"
	serve := func(body, key string) *httptest.ResponseRecorder {
		request := httptest.NewRequestWithContext(t.Context(), http.MethodPost, path, strings.NewReader(body))
		request.Header.Set("Authorization", "Bearer session")
		request.Header.Set("Content-Type", "application/json")
		if key != "" {
			request.Header.Set("Idempotency-Key", key)
		}
		response := httptest.NewRecorder()
		api.ServeHTTP(response, request)
		return response
	}
	for _, body := range []string{`{}`, `{"expected_revision":1}`, `{"expected_revision":1,"acknowledged_evidence_revision":null}`, `{"expected_revision":1,"acknowledged_evidence_revision":0,"acknowledged_evidence_revision":2}`, `{"expected_revision":1,"acknowledged_evidence_revision":-1}`, `{"expected_revision":1,"acknowledged_evidence_revision":0,"unknown":true}`} {
		response := serve(body, "completion-key")
		require.Equal(t, http.StatusBadRequest, response.Code, "invalid completion %s = %d %s", body, response.Code, response.Body.String())
		require.Zero(t, f.ledger.requests(), "invalid completion %s = %d %s", body, response.Code, response.Body.String())
	}
	response := serve(`{"expected_revision":1,"acknowledged_evidence_revision":0}`, "")
	require.Equal(t, http.StatusBadRequest, response.Code, "missing key = %d %s", response.Code, response.Body.String())
	require.Zero(t, f.ledger.requests(), "missing key = %d %s", response.Code, response.Body.String())
	response = serve(`{"expected_revision":1,"acknowledged_evidence_revision":0}`, "completion-key")
	require.Equal(t, http.StatusOK, response.Code, "completion = %d %s", response.Code, response.Body.String())
	require.Equal(t, "private, no-store", response.Header().Get("Cache-Control"), "completion = %d %s", response.Code, response.Body.String())
	require.Len(t, f.ledger.completions, 1, "the completion never reached the records Store")
	require.Equal(t, model.RetentionHoldScope{ExamID: examID, SittingID: sittingID}, f.ledger.completions[0].Scope, "completion = %d %s", response.Code, response.Body.String())
	require.Equal(t, int64(1), f.ledger.completions[0].ExpectedRevision, "completion = %d %s", response.Code, response.Body.String())
	require.Equal(t, sha256.Sum256([]byte("completion-key")), f.ledger.commands[0].KeyDigest, "completion = %d %s", response.Code, response.Body.String())
	var value examRecordsCompletionResponse
	require.NoError(t, json.Unmarshal(response.Body.Bytes(), &value))
	require.True(t, value.Current)
	require.Equal(t, int64(2), value.Revision)
	require.NotEmpty(t, value.CompletedAt)
	require.NotEmpty(t, value.CompletedByUserID)
}

func TestExamRecordsHTTPHoldScopeAndPartialRetirementDisclosure(t *testing.T) {
	logger, _ := routetest.NewLogger(t)
	principal := recordsHTTPPrincipal()
	f := newExamFixture(t, principal)
	f.ledger.workRetired, f.ledger.integrityRetired = 1, 2
	api := newFocusedResourceAPI(t, logger, routetest.Authenticator{Principal: principal}, examRecordsResource(f.records, f.people))
	examID := f.examID
	path := "/api/v1/exams/" + examID.String() + "/retention-holds"
	serve := func(body string) *httptest.ResponseRecorder {
		request := httptest.NewRequestWithContext(t.Context(), http.MethodPost, path, strings.NewReader(body))
		request.Header.Set("Authorization", "Bearer session")
		request.Header.Set("Content-Type", "application/json")
		request.Header.Set("Idempotency-Key", "hold-key")
		response := httptest.NewRecorder()
		api.ServeHTTP(response, request)
		return response
	}
	for _, body := range []string{`{"exam_sitting_id":null,"reason_code":"other","private_reason":"Preserve."}`, `{"submission_id":"` + model.NewSubmissionID().String() + `","reason_code":"other","private_reason":"Preserve."}`, `{"reason_code":"john_doe","private_reason":"Preserve."}`, `{"reason_code":"other","private_reason":"Preserve.","private_reason":"Changed."}`} {
		response := serve(body)
		require.Equal(t, http.StatusBadRequest, response.Code, "invalid hold = %d %s", response.Code, response.Body.String())
		require.Zero(t, f.ledger.requests(), "invalid hold = %d %s", response.Code, response.Body.String())
	}
	response := serve(`{"reason_code":"institution_request","private_reason":"Preserve the current institutional record."}`)
	require.Equal(t, http.StatusCreated, response.Code, "hold = %d %s", response.Code, response.Body.String())
	require.Equal(t, "private, no-store", response.Header().Get("Cache-Control"), "hold = %d %s", response.Code, response.Body.String())
	require.Len(t, f.ledger.creations, 1, "the hold never reached the records Store")
	created := f.ledger.creations[0]
	require.Equal(t, model.RetentionHoldScope{ExamID: examID}, created.Scope, "hold = %d %s", response.Code, response.Body.String())
	require.Equal(t, "institution_request", created.ReasonCode, "hold = %d %s", response.Code, response.Body.String())
	require.Equal(t, sha256.Sum256([]byte("hold-key")), f.ledger.commands[0].KeyDigest, "hold = %d %s", response.Code, response.Body.String())
	var value retentionHoldResponse
	require.NoError(t, json.Unmarshal(response.Body.Bytes(), &value))
	require.Equal(t, int64(1), value.WorkRetiredSubmissionCount)
	require.Equal(t, int64(2), value.IntegrityRetiredSubmissionCount)
	require.Equal(t, "Preserve the current institutional record.", created.PrivateReason)
	require.Equal(t, created.PrivateReason, value.PrivateReason)
}

func TestExamRecordsHTTPCursorBindsScopeAndReleaseFilter(t *testing.T) {
	principal := recordsHTTPPrincipal()
	f := newExamFixture(t, principal)
	examID, sittingID := f.examID, model.NewExamSittingID()
	scope := model.RetentionHoldScope{ExamID: examID, SittingID: sittingID}
	hold := model.RetentionHold{ID: model.NewRetentionHoldID(), Scope: scope, Revision: 1, CreatedAt: time.Now(), CreatedByUserID: model.NewUserID(), ReasonCode: "other", PrivateReason: "Preserve."}
	logger, _ := routetest.NewLogger(t)
	f.ledger.holds, f.ledger.more = []model.RetentionHold{hold}, true
	api := newFocusedResourceAPI(t, logger, routetest.Authenticator{Principal: principal}, examRecordsResource(f.records, f.people))
	path := "/api/v1/exams/" + examID.String() + "/retention-holds?exam_sitting_id=" + sittingID.String() + "&limit=1"
	serve := func(path string) *httptest.ResponseRecorder {
		request := httptest.NewRequestWithContext(t.Context(), http.MethodGet, path, nil)
		request.Header.Set("Authorization", "Bearer session")
		response := httptest.NewRecorder()
		api.ServeHTTP(response, request)
		return response
	}
	response := serve(path)
	var page retentionHoldPageResponse
	require.NoError(t, json.Unmarshal(response.Body.Bytes(), &page))
	require.Equal(t, http.StatusOK, response.Code, "page = %d %s", response.Code, response.Body.String())
	require.NotEmpty(t, page.NextCursor, "page = %d %s", response.Code, response.Body.String())
	require.Len(t, page.Items, 1, "page = %d %s", response.Code, response.Body.String())
	f.ledger.more = false
	response = serve(path + "&cursor=" + page.NextCursor)
	require.Equal(t, http.StatusOK, response.Code, "continuation = %d %s", response.Code, response.Body.String())
	require.Len(t, f.ledger.holdLists, 2, "continuation = %d %s", response.Code, response.Body.String())
	continuation := f.ledger.holdLists[1]
	require.Equal(t, hold.ID, continuation.After, "continuation = %d %s", response.Code, response.Body.String())
	require.Equal(t, scope, continuation.Scope, "continuation = %d %s", response.Code, response.Body.String())
	require.Equal(t, 1, continuation.Limit, "continuation = %d %s", response.Code, response.Body.String())
	for _, changed := range []string{path + "&include_released=true&cursor=" + page.NextCursor, strings.Replace(path, sittingID.String(), model.NewExamSittingID().String(), 1) + "&cursor=" + page.NextCursor, path + "&limit=2", path + "&include_released=1", path + "&unexpected=true"} {
		requests := f.ledger.requests()
		response = serve(changed)
		require.Equal(t, http.StatusBadRequest, response.Code, "changed scope/filter = %d %s", response.Code, response.Body.String())
		require.Equal(t, requests, f.ledger.requests(), "changed scope/filter = %d %s", response.Code, response.Body.String())
	}
}

func TestExamRecordsHTTPReleaseRequiresStrongRecentSession(t *testing.T) {
	logger, _ := routetest.NewLogger(t)
	principal := recordsHTTPPrincipal()
	f := newExamFixture(t, principal)
	api := newFocusedResourceAPI(t, logger, routetest.Authenticator{Principal: principal}, examRecordsResource(f.records, f.people))
	request := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/api/v1/exams/"+f.examID.String()+"/retention-holds/"+model.NewRetentionHoldID().String()+"/release", strings.NewReader(`{"expected_revision":1,"reason_code":"case_closed","private_reason":"Resolved."}`))
	request.Header.Set("Authorization", "Bearer session")
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Idempotency-Key", "release-key")
	response := httptest.NewRecorder()
	api.ServeHTTP(response, request)
	require.Equal(t, http.StatusForbidden, response.Code, "single-factor release")
	require.Zero(t, f.ledger.requests(), "single-factor release")
	require.Contains(t, response.Body.String(), "authentication.strong_required", "single-factor release")
}

func TestExamRecordsOpenAPIDocument(t *testing.T) {
	doc := readOpenAPIDocument(t)
	for _, name := range []string{"ExamRecordsSnapshotOK", "ExamRecordsCompletionOK", "SubmissionReviewWaiverOK", "RetentionHoldPageOK", "RetentionHoldOK", "RetentionHoldCreated"} {
		require.Equal(t, "#/components/headers/PrivateNoStore", doc.Components.Responses[name].Headers["Cache-Control"].Ref, "%s lacks private no-store", name)
	}
}
