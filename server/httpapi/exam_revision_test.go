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
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/sudosylabs/proctor/server/httpapi/route/routetest"
	"github.com/sudosylabs/proctor/server/model"
	"github.com/sudosylabs/proctor/server/store"
)

func TestExamRevisionHTTPPublishUsesStrictIdempotentCommandAndSafeSummary(t *testing.T) {
	t.Parallel()
	logger, _ := routetest.NewLogger(t)
	principal := testExamHTTPPrincipal()
	f := newExamRevisionFixture(t, principal)
	httpAPI := newFocusedResourceAPI(t, logger, routetest.Authenticator{Principal: principal}, examRevisionResource(f.publication, f.people))

	request := newJSONRequest(http.MethodPost, "/api/v1/exams/"+f.examID.String()+"/revisions", bytes.NewReader([]byte(`{"expected_revision":4}`)))
	request.Header.Set("Authorization", "Bearer credential")
	request.Header.Set("Idempotency-Key", "publish-once")
	response := httptest.NewRecorder()
	httpAPI.ServeHTTP(response, request)

	require.Equal(t, http.StatusCreated, response.Code, "status = %d, body = %s", response.Code, response.Body.String())
	require.Len(t, f.revisions.publications, 1, "the publication never reached the Exam Revision Store")
	require.Equal(t, f.examID, f.revisions.publications[0].ExamID)
	require.Equal(t, int64(4), f.revisions.publications[0].ExpectedDraftRevision)
	require.Equal(t, sha256.Sum256([]byte("publish-once")), f.revisions.commands[0].KeyDigest)
	summary := f.revisions.summary
	var got examRevisionResponse
	require.NoError(t, json.Unmarshal(response.Body.Bytes(), &got))
	require.Equal(t, summary.ID.String(), got.ID)
	require.Equal(t, int64(3), got.Number)
	require.Equal(t, summary.PolicyDigest, got.PolicyDigest)
	require.Equal(t, 2, got.StarterWorkspaceEntryCount)
	require.Equal(t, int64(37), got.StarterWorkspaceTotalBytes)
	for _, forbidden := range [][]byte{[]byte(`"instructions_markdown"`), []byte(`"policy"`), []byte(`"resources"`), []byte(`"path"`), []byte(`"object_id"`), []byte(`"file_revision_id"`)} {
		require.False(t, bytes.Contains(response.Body.Bytes(), forbidden), "response exposed frozen authored or storage data %q: %s", forbidden, response.Body.String())
	}
}

func TestExamRevisionHTTPGetAndListUseExactIdentityAndVersionedTupleCursor(t *testing.T) {
	t.Parallel()
	logger, _ := routetest.NewLogger(t)
	principal := testExamHTTPPrincipal()
	f := newExamRevisionFixture(t, principal)
	summary := f.revisions.summary
	older := summary
	older.ID = model.NewExamRevisionID()
	older.Number = 2
	oldest := summary
	oldest.ID = model.NewExamRevisionID()
	oldest.Number = 1
	// The listing reads one past the limit of two, so a third Revision says
	// another page follows.
	f.revisions.listed = []store.ExamRevisionSummary{summary, older, oldest}
	httpAPI := newFocusedResourceAPI(t, logger, routetest.Authenticator{Principal: principal}, examRevisionResource(f.publication, f.people))

	getRequest := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/api/v1/exams/"+f.examID.String()+"/revisions/"+summary.ID.String(), nil)
	getRequest.Header.Set("Authorization", "Bearer credential")
	getResponse := httptest.NewRecorder()
	httpAPI.ServeHTTP(getResponse, getRequest)
	require.Equal(t, http.StatusOK, getResponse.Code, "status = %d reads = %#v body = %s", getResponse.Code, f.revisions.gets, getResponse.Body.String())
	require.Equal(t, []model.ExamRevisionID{summary.ID}, f.revisions.gets, "status = %d body = %s", getResponse.Code, getResponse.Body.String())
	var gotRevision examRevisionResponse
	require.NoError(t, json.Unmarshal(getResponse.Body.Bytes(), &gotRevision))
	require.Equal(t, f.examID.String(), gotRevision.ExamID, "the Store answered another Exam's Revision")

	beforeRevisionID := model.NewExamRevisionID()
	cursor, err := encodeExamRevisionCursor(examRevisionCursor{Number: 5, RevisionID: beforeRevisionID.String()})
	require.NoError(t, err)
	listRequest := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/api/v1/exams/"+f.examID.String()+"/revisions?limit=2&cursor="+cursor, nil)
	listRequest.Header.Set("Authorization", "Bearer credential")
	listResponse := httptest.NewRecorder()
	httpAPI.ServeHTTP(listResponse, listRequest)
	require.Equal(t, http.StatusOK, listResponse.Code, "status = %d lists = %#v body = %s", listResponse.Code, f.revisions.lists, listResponse.Body.String())
	require.Len(t, f.revisions.lists, 1, "the listing never reached the Exam Revision Store")
	require.Equal(t, f.examID, f.revisions.lists[0].ExamID, "status = %d lists = %#v", listResponse.Code, f.revisions.lists)
	require.Equal(t, 3, f.revisions.lists[0].Limit, "the listing reads one past the limit")
	require.Equal(t, int64(5), f.revisions.lists[0].BeforeNumber, "status = %d lists = %#v", listResponse.Code, f.revisions.lists)
	require.Equal(t, beforeRevisionID, f.revisions.lists[0].BeforeRevisionID, "status = %d lists = %#v", listResponse.Code, f.revisions.lists)
	malformedRequest := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/api/v1/exams/"+f.examID.String()+"/revisions?cursor=not-a-cursor", nil)
	malformedRequest.Header.Set("Authorization", "Bearer credential")
	malformedResponse := httptest.NewRecorder()
	httpAPI.ServeHTTP(malformedResponse, malformedRequest)
	assertHTTPProblem(t, malformedResponse, http.StatusBadRequest, "request.invalid")
	require.Len(t, f.revisions.lists, 1, "a malformed cursor reached the Exam Revision Store")
	var page examRevisionListResponse
	require.NoError(t, json.Unmarshal(listResponse.Body.Bytes(), &page))
	require.Len(t, page.Items, 2)
	require.NotEmpty(t, page.NextCursor)
	next, err := decodeExamRevisionCursor(page.NextCursor)
	require.NoError(t, err, "next cursor = %#v, err", next)
	require.Equal(t, older.Number, next.Number, "next cursor = %#v, err", next)
	require.Equal(t, older.ID.String(), next.RevisionID, "next cursor = %#v, err", next)

	f.revisions.listed = []store.ExamRevisionSummary{summary, older}
	terminalResponse := httptest.NewRecorder()
	httpAPI.ServeHTTP(terminalResponse, listRequest.Clone(listRequest.Context()))
	require.Equal(t, http.StatusOK, terminalResponse.Code, "terminal status = %d, body = %s", terminalResponse.Code, terminalResponse.Body.String())
	var terminal examRevisionListResponse
	require.NoError(t, json.Unmarshal(terminalResponse.Body.Bytes(), &terminal))
	require.Len(t, terminal.Items, 2)
	require.Empty(t, terminal.NextCursor)
	// The service reports more only past a full page, so an empty listing is
	// the last page rather than a page that made no progress.
	f.revisions.listed = nil
	emptyResponse := httptest.NewRecorder()
	httpAPI.ServeHTTP(emptyResponse, listRequest.Clone(listRequest.Context()))
	require.Equal(t, http.StatusOK, emptyResponse.Code, "empty status = %d, body = %s", emptyResponse.Code, emptyResponse.Body.String())
	var empty examRevisionListResponse
	require.NoError(t, json.Unmarshal(emptyResponse.Body.Bytes(), &empty))
	require.Empty(t, empty.Items)
	require.Empty(t, empty.NextCursor)
}

func TestExamRevisionHTTPCursorAndPublishBodyAreStrict(t *testing.T) {
	t.Parallel()
	revisionID := model.NewExamRevisionID().String()
	unsupported := []byte(`{"version":1,"number":2,"revision_id":"` + revisionID + `"}`)
	valid, _ := json.Marshal(examRevisionCursor{Number: 2, RevisionID: revisionID})
	for name, raw := range map[string]string{
		"malformed":        "not-a-cursor",
		"versioned":        base64.RawURLEncoding.EncodeToString(unsupported),
		"trailing payload": base64.RawURLEncoding.EncodeToString(append(valid, []byte(`{}`)...)),
	} {
		t.Run(name, func(t *testing.T) {
			_, err := decodeExamRevisionCursor(raw)
			require.Error(t, err, "cursor was accepted")
		})
	}

	logger, _ := routetest.NewLogger(t)
	principal := testExamHTTPPrincipal()
	f := newExamRevisionFixture(t, principal)
	httpAPI := newFocusedResourceAPI(t, logger, routetest.Authenticator{Principal: principal}, examRevisionResource(f.publication, f.people))
	for name, body := range map[string]string{
		"unknown field":    `{"expected_revision":4,"unexpected":true}`,
		"missing revision": `{}`,
		"trailing JSON":    `{"expected_revision":4}{}`,
	} {
		t.Run(name, func(t *testing.T) {
			request := newJSONRequest(http.MethodPost, "/api/v1/exams/"+f.examID.String()+"/revisions", bytes.NewBufferString(body))
			request.Header.Set("Authorization", "Bearer credential")
			request.Header.Set("Idempotency-Key", "publish-once")
			response := httptest.NewRecorder()
			httpAPI.ServeHTTP(response, request)
			require.Equal(t, http.StatusBadRequest, response.Code, "status = %d body = %s", response.Code, response.Body.String())
			require.Empty(t, f.revisions.publications, "status = %d body = %s", response.Code, response.Body.String())
		})
	}
}

// newExamRevisionFixture is the Exam fixture whose Exam Revision Store
// publishes and answers Revision 3 of the fixture's Exam.
func newExamRevisionFixture(t *testing.T, principal model.Principal) *examFixture {
	t.Helper()
	f := newExamFixture(t, principal)
	at := time.Date(2026, time.August, 15, 9, 30, 0, 123456789, time.UTC)
	f.revisions.summary = store.ExamRevisionSummary{
		ID: model.NewExamRevisionID(), ExamID: f.examID, Number: 3, SourceDraftRevision: 4,
		Title: "Distributed Systems", PolicySchemaVersion: 1,
		PolicyDigest: strings.Repeat("a", 64), StarterWorkspaceDigest: strings.Repeat("b", 64),
		ContentDigest: strings.Repeat("c", 64), ResourceCount: 1, StarterWorkspaceEntries: 2,
		StarterWorkspaceBytes: 37, PublishedByUserID: principal.UserID, PublishedAt: at,
		BaseRevisionID: model.NewExamRevisionID(), Kind: model.ExamRevisionPublicationStandard,
	}
	return f
}
