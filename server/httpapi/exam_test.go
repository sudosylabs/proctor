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
	"net/url"
	"testing"
	"time"

	"github.com/getkin/kin-openapi/openapi3"
	"github.com/stretchr/testify/require"

	"github.com/sudosylabs/proctor/server/app/exam"
	"github.com/sudosylabs/proctor/server/httpapi/route/routetest"
	"github.com/sudosylabs/proctor/server/model"
	"github.com/sudosylabs/proctor/server/store"
)

func TestExamHTTPCreateAndGetUseApplicationFacade(t *testing.T) {
	t.Parallel()
	logger, _ := routetest.NewLogger(t)
	principal := testExamHTTPPrincipal()
	f := newExamFixture(t, principal)
	httpAPI := newFocusedResourceAPI(t, logger, routetest.Authenticator{Principal: principal}, examResource(f.authoring, f.people))

	body := []byte(`{"academic_unit_id":"` + f.unitID.String() + `","title":"Systems","instructions_markdown":"Use **Go**."}`)
	request := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/api/v1/exams", bytes.NewReader(body))
	request.Header.Set("Authorization", "Bearer credential")
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Idempotency-Key", "exam-retry")
	response := httptest.NewRecorder()
	httpAPI.ServeHTTP(response, request)
	require.Equal(t, http.StatusCreated, response.Code, "create status = %d: %s", response.Code, response.Body.String())
	require.Len(t, f.exams.creations, 1, "the creation never reached the Exam Store")
	creation := f.exams.creations[0]
	require.Equal(t, f.unitID, creation.Exam.AcademicUnitID)
	require.Equal(t, "Systems", creation.Draft.Title)
	require.Equal(t, "Use **Go**.", creation.Draft.InstructionsMarkdown)
	require.Equal(t, sha256.Sum256([]byte("exam-retry")), f.exams.lastCommand(t).KeyDigest)
	view := exam.View{Exam: *creation.Exam, Draft: *creation.Draft, OwnerUserID: principal.UserID, ManagerCount: 1}
	assertExamHTTPResponse(t, response.Body.Bytes(), view)

	get := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/api/v1/exams/"+view.Exam.ID.String(), nil)
	get.Header.Set("Authorization", "Bearer credential")
	got := httptest.NewRecorder()
	httpAPI.ServeHTTP(got, get)
	require.Equal(t, http.StatusOK, got.Code, "get = %d reads=%#v body=%s", got.Code, f.exams.gets, got.Body.String())
	require.Equal(t, []model.ExamID{view.Exam.ID}, f.exams.gets, "get = %d reads=%#v body=%s", got.Code, f.exams.gets, got.Body.String())
	assertExamHTTPResponse(t, got.Body.Bytes(), view)
}

func TestExamHTTPListUsesBoundedCatalogQueryAndSummary(t *testing.T) {
	t.Parallel()
	logger, _ := routetest.NewLogger(t)
	principal := testExamHTTPPrincipal()
	f := newExamFixture(t, principal)
	unitID, examID := f.unitID, model.NewExamID()
	f.exams.listed = []store.ExamSummary{{
		ID: examID, AcademicUnitID: unitID, CreatorUserID: principal.UserID, OwnerUserID: principal.UserID,
		Title: "Systems", UpdatedAt: time.Date(2026, 8, 14, 8, 0, 0, 0, time.UTC), Revision: 2, ManagerCount: 1,
	}}
	httpAPI := newFocusedResourceAPI(t, logger, routetest.Authenticator{Principal: principal}, examResource(f.authoring, f.people))
	request := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/api/v1/exams?academic_unit_id="+unitID.String()+"&q=systems+design&archive_state=all&limit=25", nil)
	request.Header.Set("Authorization", "Bearer credential")
	response := httptest.NewRecorder()
	httpAPI.ServeHTTP(response, request)
	require.Equal(t, http.StatusOK, response.Code, "list status = %d: %s", response.Code, response.Body.String())
	require.Len(t, f.exams.lists, 1, "the listing never reached the Exam Store")
	require.Equal(t, unitID, f.exams.lists[0].AcademicUnitID)
	require.Equal(t, "systems design", f.exams.lists[0].Query)
	require.Equal(t, store.ExamArchiveAll, f.exams.lists[0].ArchiveFilter)
	require.Equal(t, 26, f.exams.lists[0].Limit, "the catalog reads one past the limit")
	require.True(t, bytes.Contains(response.Body.Bytes(), []byte(`"title":"Systems"`)))
	require.False(t, bytes.Contains(response.Body.Bytes(), []byte("instructions_markdown")))
	require.False(t, bytes.Contains(response.Body.Bytes(), []byte("policy")))
	before := time.Date(2026, 8, 13, 7, 6, 5, 432123789, time.UTC)
	wantBefore := time.Date(2026, 8, 13, 7, 6, 5, 432123000, time.UTC)
	cursor, err := encodeExamCatalogCursor(examCatalogCursor{UpdatedAt: before.Format(time.RFC3339Nano), ExamID: examID.String()})
	require.NoError(t, err)
	second := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/api/v1/exams?cursor="+url.QueryEscape(cursor), nil)
	second.Header.Set("Authorization", "Bearer credential")
	secondResponse := httptest.NewRecorder()
	httpAPI.ServeHTTP(secondResponse, second)
	require.Equal(t, http.StatusOK, secondResponse.Code, "catalog cursor forwarding = %d lists=%#v body=%s", secondResponse.Code, f.exams.lists, secondResponse.Body.String())
	require.Len(t, f.exams.lists, 2, "catalog cursor forwarding = %d body=%s", secondResponse.Code, secondResponse.Body.String())
	require.True(t, f.exams.lists[1].BeforeUpdatedAt.Equal(wantBefore), "catalog cursor forwarding = %d lists=%#v body=%s", secondResponse.Code, f.exams.lists, secondResponse.Body.String())
	require.Equal(t, examID, f.exams.lists[1].BeforeExamID, "catalog cursor forwarding = %d lists=%#v body=%s", secondResponse.Code, f.exams.lists, secondResponse.Body.String())
	malformed := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/api/v1/exams?cursor=not-a-cursor", nil)
	malformed.Header.Set("Authorization", "Bearer credential")
	malformedResponse := httptest.NewRecorder()
	httpAPI.ServeHTTP(malformedResponse, malformed)
	assertHTTPProblem(t, malformedResponse, http.StatusBadRequest, "request.invalid")
	require.Len(t, f.exams.lists, 2, "a malformed cursor reached the Exam Store")
}

func TestExamCatalogCursorRoundTripsAndRejectsInvalidInput(t *testing.T) {
	t.Parallel()
	cursor := examCatalogCursor{UpdatedAt: "2026-08-14T08:00:00.123456789Z", ExamID: model.NewExamID().String()}
	encodedCursor, err := encodeExamCatalogCursor(cursor)
	require.NoError(t, err)
	got, err := decodeExamCatalogCursor(encodedCursor)
	require.NoError(t, err, "cursor")
	require.Equal(t, cursor, got, "cursor")
	_, err = decodeExamCatalogCursor("not-a-cursor")
	require.Error(t, err, "invalid cursor accepted")
	for _, version := range []string{"0", "1"} {
		versioned := []byte(`{"version":` + version + `,"updated_at":"2026-08-14T08:00:00Z","exam_id":"` + cursor.ExamID + `"}`)
		_, err = decodeExamCatalogCursor(base64.RawURLEncoding.EncodeToString(versioned))
		require.Error(t, err, "versioned cursor accepted")
	}
	encoded, _ := json.Marshal(cursor)
	_, err = decodeExamCatalogCursor(base64.RawURLEncoding.EncodeToString(append(encoded, []byte(`{}`)...)))
	require.Error(t, err, "cursor with trailing JSON accepted")
}

func TestExamManagerHTTPListAndMutationsUseStrictBoundedContracts(t *testing.T) {
	t.Parallel()
	logger, _ := routetest.NewLogger(t)
	principal := testExamHTTPPrincipal()
	f := newExamFixture(t, principal)
	examID := f.examID
	target := model.NewUserID()
	f.users.add(activeExamUser(target, "target"))
	f.members.join(target, f.unitID)
	grantedAt := time.Date(2026, 8, 14, 10, 0, 0, 0, time.UTC)
	manager, err := model.NewExamManager(examID, target, principal.UserID, grantedAt)
	require.NoError(t, err)
	f.exams.managersListed = []store.ExamManagerSummary{{Manager: *manager}}
	httpAPI := newFocusedResourceAPI(t, logger, routetest.Authenticator{Principal: principal}, examResource(f.authoring, f.people))

	list := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/api/v1/exams/"+examID.String()+"/managers?limit=1", nil)
	list.Header.Set("Authorization", "Bearer credential")
	listed := httptest.NewRecorder()
	httpAPI.ServeHTTP(listed, list)
	require.Equal(t, http.StatusOK, listed.Code, "list = %d lists=%#v body=%s", listed.Code, f.exams.managerLists, listed.Body.String())
	require.Len(t, f.exams.managerLists, 1, "the Manager listing never reached the Exam Store")
	require.Equal(t, 2, f.exams.managerLists[0].Limit, "the Manager listing reads one past the limit")
	require.True(t, bytes.Contains(listed.Body.Bytes(), []byte(target.String())), "list = %d body=%s", listed.Code, listed.Body.String())
	// Who manages an exam is part of its record, so managers are named; the
	// caller cannot see them in the directory, so no username or picture.
	require.True(t, bytes.Contains(listed.Body.Bytes(), []byte(`"display_name":"target"`)), "list = %d body=%s", listed.Code, listed.Body.String())
	require.False(t, bytes.Contains(listed.Body.Bytes(), []byte("username")), "list = %d body=%s", listed.Code, listed.Body.String())
	cursor, err := encodeExamManagerCursor(examManagerCursor{GrantedAt: grantedAt.Format(time.RFC3339Nano), UserID: target.String()})
	require.NoError(t, err)
	second := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/api/v1/exams/"+examID.String()+"/managers?cursor="+url.QueryEscape(cursor), nil)
	second.Header.Set("Authorization", "Bearer credential")
	secondResponse := httptest.NewRecorder()
	httpAPI.ServeHTTP(secondResponse, second)
	require.Equal(t, http.StatusOK, secondResponse.Code, "manager cursor forwarding = %d body=%s", secondResponse.Code, secondResponse.Body.String())
	require.Len(t, f.exams.managerLists, 2, "manager cursor forwarding = %d body=%s", secondResponse.Code, secondResponse.Body.String())
	require.True(t, f.exams.managerLists[1].BeforeGrantedAt.Equal(grantedAt), "manager cursor forwarding = %d lists=%#v", secondResponse.Code, f.exams.managerLists)
	require.Equal(t, target, f.exams.managerLists[1].BeforeUserID, "manager cursor forwarding = %d lists=%#v", secondResponse.Code, f.exams.managerLists)
	malformed := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/api/v1/exams/"+examID.String()+"/managers?cursor=not-a-cursor", nil)
	malformed.Header.Set("Authorization", "Bearer credential")
	malformedResponse := httptest.NewRecorder()
	httpAPI.ServeHTTP(malformedResponse, malformed)
	assertHTTPProblem(t, malformedResponse, http.StatusBadRequest, "request.invalid")
	require.Len(t, f.exams.managerLists, 2, "a malformed cursor reached the Exam Store")

	add := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/api/v1/exams/"+examID.String()+"/managers", bytes.NewReader([]byte(`{"user_id":"`+target.String()+`","expected_revision":1}`)))
	add.Header.Set("Authorization", "Bearer credential")
	add.Header.Set("Content-Type", "application/json")
	add.Header.Set("Idempotency-Key", "add-manager")
	added := httptest.NewRecorder()
	httpAPI.ServeHTTP(added, add)
	require.Equal(t, http.StatusCreated, added.Code, "add = %d body=%s", added.Code, added.Body.String())
	require.Len(t, f.exams.managerChanges, 1, "the addition never reached the Exam Store")
	require.Equal(t, target, f.exams.managerChanges[0].TargetUserID, "add = %d body=%s", added.Code, added.Body.String())
	require.Equal(t, int64(1), f.exams.managerChanges[0].ExpectedRevision, "add = %d body=%s", added.Code, added.Body.String())
	require.Equal(t, sha256.Sum256([]byte("add-manager")), f.exams.lastCommand(t).KeyDigest, "add = %d body=%s", added.Code, added.Body.String())

	invalidDelete := httptest.NewRequestWithContext(t.Context(), http.MethodDelete, "/api/v1/exams/"+examID.String()+"/managers/"+target.String(), bytes.NewReader([]byte(`{"expected_revision":2,"unexpected":true}`)))
	invalidDelete.Header.Set("Authorization", "Bearer credential")
	invalidDelete.Header.Set("Content-Type", "application/json")
	invalidDelete.Header.Set("Idempotency-Key", "remove-manager")
	invalid := httptest.NewRecorder()
	httpAPI.ServeHTTP(invalid, invalidDelete)
	require.Equal(t, http.StatusBadRequest, invalid.Code, "strict DELETE = %d: %s", invalid.Code, invalid.Body.String())
	require.Len(t, f.exams.managerChanges, 1, "a strict DELETE reached the Exam Store")
}

func TestExamManagerCursorIsUnversionedAndStrict(t *testing.T) {
	t.Parallel()
	cursor := examManagerCursor{GrantedAt: "2026-08-14T10:00:00Z", UserID: model.NewUserID().String()}
	encodedCursor, err := encodeExamManagerCursor(cursor)
	require.NoError(t, err)
	decoded, err := decodeExamManagerCursor(encodedCursor)
	require.NoError(t, err, "cursor")
	require.Equal(t, cursor, decoded, "cursor")
	versioned := []byte(`{"version":1,"granted_at":"2026-08-14T10:00:00Z","user_id":"` + cursor.UserID + `"}`)
	_, err = decodeExamManagerCursor(base64.RawURLEncoding.EncodeToString(versioned))
	require.Error(t, err, "versioned Manager cursor accepted")
}

func TestExamHTTPArchiveUsesRequiredIdempotentApplicationCommand(t *testing.T) {
	t.Parallel()
	logger, _ := routetest.NewLogger(t)
	principal := testExamHTTPPrincipal()
	f := newExamFixture(t, principal)
	httpAPI := newFocusedResourceAPI(t, logger, routetest.Authenticator{Principal: principal}, examResource(f.authoring, f.people))
	request := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/api/v1/exams/"+f.examID.String()+"/archive", bytes.NewReader([]byte(`{"expected_revision":1}`)))
	request.Header.Set("Authorization", "Bearer credential")
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Idempotency-Key", "archive-once")
	response := httptest.NewRecorder()
	httpAPI.ServeHTTP(response, request)
	require.Equal(t, http.StatusOK, response.Code, "archive status = %d: %s", response.Code, response.Body.String())
	require.Len(t, f.exams.archives, 1, "the archival never reached the Exam Store")
	require.Equal(t, f.examID, f.exams.archives[0].ExamID)
	require.Equal(t, int64(1), f.exams.archives[0].ExpectedRevision)
	require.Equal(t, sha256.Sum256([]byte("archive-once")), f.exams.lastCommand(t).KeyDigest)
}

func TestExamHTTPCreateRequiresIdempotencyKey(t *testing.T) {
	t.Parallel()
	logger, _ := routetest.NewLogger(t)
	principal := testExamHTTPPrincipal()
	f := newExamFixture(t, principal)
	httpAPI := newFocusedResourceAPI(t, logger, routetest.Authenticator{Principal: principal}, examResource(f.authoring, f.people))
	request := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/api/v1/exams", bytes.NewReader([]byte(`{"academic_unit_id":"`+f.unitID.String()+`","title":"Systems"}`)))
	request.Header.Set("Authorization", "Bearer credential")
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	httpAPI.ServeHTTP(response, request)
	require.Equal(t, http.StatusBadRequest, response.Code, "response = %d: %s", response.Code, response.Body.String())
	require.True(t, bytes.Contains(response.Body.Bytes(), []byte(`"code":"idempotency.key_required"`)), "response = %d: %s", response.Code, response.Body.String())
	require.Zero(t, f.exams.writes(), "an unkeyed creation reached the Exam Store")
}

func TestExamHTTPPatchDraftPreservesOmittedAndExplicitEmptyFields(t *testing.T) {
	t.Parallel()
	logger, _ := routetest.NewLogger(t)
	principal := testExamHTTPPrincipal()
	f := newExamFixture(t, principal)
	httpAPI := newFocusedResourceAPI(t, logger, routetest.Authenticator{Principal: principal}, examResource(f.authoring, f.people))
	request := httptest.NewRequestWithContext(t.Context(), http.MethodPatch, "/api/v1/exams/"+f.examID.String()+"/draft", bytes.NewReader([]byte(`{"expected_revision":1,"instructions_markdown":""}`)))
	request.Header.Set("Authorization", "Bearer credential")
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Idempotency-Key", "clear-instructions")
	response := httptest.NewRecorder()
	httpAPI.ServeHTTP(response, request)
	require.Equal(t, http.StatusOK, response.Code, "patch status = %d: %s", response.Code, response.Body.String())
	require.Len(t, f.exams.textUpdates, 1, "the edit never reached the Exam Store")
	update := f.exams.textUpdates[0]
	require.Equal(t, f.examID, update.ExamID)
	require.Equal(t, int64(1), update.ExpectedRevision)
	require.Nil(t, update.Title)
	require.NotNil(t, update.InstructionsMarkdown)
	require.Empty(t, *update.InstructionsMarkdown)
	require.Equal(t, sha256.Sum256([]byte("clear-instructions")), f.exams.lastCommand(t).KeyDigest)
}

func TestExamHTTPPatchDraftTreatsNullAsUnchangedAndRequiresAValue(t *testing.T) {
	t.Parallel()
	logger, _ := routetest.NewLogger(t)
	principal := testExamHTTPPrincipal()
	f := newExamFixture(t, principal)
	httpAPI := newFocusedResourceAPI(t, logger, routetest.Authenticator{Principal: principal}, examResource(f.authoring, f.people))
	request := httptest.NewRequestWithContext(t.Context(), http.MethodPatch, "/api/v1/exams/"+f.examID.String()+"/draft", bytes.NewReader([]byte(`{"expected_revision":1,"title":null,"instructions_markdown":null}`)))
	request.Header.Set("Authorization", "Bearer credential")
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Idempotency-Key", "null-fields")
	response := httptest.NewRecorder()
	httpAPI.ServeHTTP(response, request)
	require.Equal(t, http.StatusBadRequest, response.Code, "response = %d: %s", response.Code, response.Body.String())
	require.True(t, bytes.Contains(response.Body.Bytes(), []byte(`"code":"request.invalid"`)), "response = %d: %s", response.Code, response.Body.String())
	require.Zero(t, f.exams.writes(), "an edit without a value reached the Exam Store")
}

func TestExamHTTPPatchDraftRequiresIdempotencyKey(t *testing.T) {
	t.Parallel()
	logger, _ := routetest.NewLogger(t)
	principal := testExamHTTPPrincipal()
	f := newExamFixture(t, principal)
	httpAPI := newFocusedResourceAPI(t, logger, routetest.Authenticator{Principal: principal}, examResource(f.authoring, f.people))
	request := httptest.NewRequestWithContext(t.Context(), http.MethodPatch, "/api/v1/exams/"+f.examID.String()+"/draft", bytes.NewReader([]byte(`{"expected_revision":1,"title":"Algorithms"}`)))
	request.Header.Set("Authorization", "Bearer credential")
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	httpAPI.ServeHTTP(response, request)
	require.Equal(t, http.StatusBadRequest, response.Code, "response = %d: %s", response.Code, response.Body.String())
	require.True(t, bytes.Contains(response.Body.Bytes(), []byte(`"code":"idempotency.key_required"`)), "response = %d: %s", response.Code, response.Body.String())
	require.Zero(t, f.exams.writes(), "an unkeyed edit reached the Exam Store")
}

func TestExamHTTPConfigureDraftFocusLossUsesTypedApplicationCommand(t *testing.T) {
	t.Parallel()
	logger, _ := routetest.NewLogger(t)
	principal := testExamHTTPPrincipal()
	f := newExamFixture(t, principal)
	httpAPI := newFocusedResourceAPI(t, logger, routetest.Authenticator{Principal: principal}, examResource(f.authoring, f.people))
	request := httptest.NewRequestWithContext(t.Context(), http.MethodPut, "/api/v1/exams/"+f.examID.String()+"/draft/policies/focus-loss", bytes.NewReader([]byte(`{"expected_revision":1,"enabled":false,"minimum_duration_milliseconds":500,"incident_count":1,"window_milliseconds":10000,"outcome":"flag"}`)))
	request.Header.Set("Authorization", "Bearer credential")
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Idempotency-Key", "focus-loss-policy")
	response := httptest.NewRecorder()
	httpAPI.ServeHTTP(response, request)
	require.Equal(t, http.StatusOK, response.Code, "put status = %d: %s", response.Code, response.Body.String())
	require.Len(t, f.exams.focusLossUpdates, 1, "the policy never reached the Exam Store")
	update := f.exams.focusLossUpdates[0]
	require.Equal(t, f.examID, update.ExamID)
	require.Equal(t, int64(1), update.ExpectedRevision)
	require.False(t, update.FocusLoss.Enabled)
	require.Equal(t, 500*time.Millisecond, update.FocusLoss.MinimumDuration)
	require.Equal(t, 1, update.FocusLoss.IncidentCount)
	require.Equal(t, 10*time.Second, update.FocusLoss.Window)
	require.Equal(t, model.IntegrityOutcomeFlag, update.FocusLoss.Outcome)
	require.Equal(t, sha256.Sum256([]byte("focus-loss-policy")), f.exams.lastCommand(t).KeyDigest)
}

func TestExamHTTPConfigureDraftFocusLossRequiresExplicitEnabledAndRejectsRawPolicy(t *testing.T) {
	t.Parallel()
	principal := testExamHTTPPrincipal()
	for name, body := range map[string]string{
		"enabled omitted":   `{"expected_revision":1,"minimum_duration_milliseconds":500,"incident_count":1,"window_milliseconds":10000,"outcome":"flag"}`,
		"raw policy member": `{"expected_revision":1,"enabled":true,"minimum_duration_milliseconds":500,"incident_count":1,"window_milliseconds":10000,"outcome":"flag","connection_loss":{"outcome":"flag"}}`,
		"duplicate enabled": `{"expected_revision":1,"enabled":true,"enabled":false,"minimum_duration_milliseconds":500,"incident_count":1,"window_milliseconds":10000,"outcome":"flag"}`,
		"duplicate outcome": `{"expected_revision":1,"enabled":true,"minimum_duration_milliseconds":500,"incident_count":1,"window_milliseconds":10000,"outcome":"flag","outcome":"flag_and_suspend"}`,
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			logger, _ := routetest.NewLogger(t)
			f := newExamFixture(t, principal)
			httpAPI := newFocusedResourceAPI(t, logger, routetest.Authenticator{Principal: principal}, examResource(f.authoring, f.people))
			request := httptest.NewRequestWithContext(t.Context(), http.MethodPut, "/api/v1/exams/"+f.examID.String()+"/draft/policies/focus-loss", bytes.NewReader([]byte(body)))
			request.Header.Set("Authorization", "Bearer credential")
			request.Header.Set("Content-Type", "application/json")
			request.Header.Set("Idempotency-Key", "invalid-focus-loss-policy")
			response := httptest.NewRecorder()
			httpAPI.ServeHTTP(response, request)
			require.Equal(t, http.StatusBadRequest, response.Code, "response = %d: %s", response.Code, response.Body.String())
			require.True(t, bytes.Contains(response.Body.Bytes(), []byte(`"code":"request.invalid"`)), "response = %d: %s", response.Code, response.Body.String())
			require.Empty(t, f.exams.focusLossUpdates, "an invalid policy reached the Exam Store")
			require.Empty(t, f.exams.accesses, "an invalid policy reached the Exam Store")
		})
	}
}

func TestExamHTTPResponseIsBoundedAndContainsTypedPolicy(t *testing.T) {
	t.Parallel()
	editorID := model.NewUserID()
	view := testExamHTTPView(t, editorID)
	encoded, err := json.Marshal(examResponseFromView(view))
	require.NoError(t, err)
	var document map[string]any
	require.NoError(t, json.Unmarshal(encoded, &document))
	_, exists := document["managers"]
	require.False(t, exists, "bounded view exposed manager collection")
	examDocument := document["exam"].(map[string]any)
	for _, field := range []string{"created_at", "updated_at"} {
		value, ok := examDocument[field].(string)
		require.True(t, ok, "%s = %#v, want RFC3339 string", field, examDocument[field])
		_, err = time.Parse(time.RFC3339Nano, value)
		require.NoError(t, err, "parse %s", field)
	}
	_, present := examDocument["archived_at"]
	require.False(t, present, "archived_at = %#v, want omitted for active exam", examDocument["archived_at"])
	for _, legacy := range []string{"create_at", "update_at", "delete_at"} {
		_, exists := examDocument[legacy]
		require.False(t, exists, "response exposed legacy timestamp field %q", legacy)
	}
	draft := document["draft"].(map[string]any)
	_, err = time.Parse(time.RFC3339Nano, draft["updated_at"].(string))
	require.NoError(t, err)
	policy := draft["policy"].(map[string]any)
	capacity := draft["capacity"].(map[string]any)
	require.NotNil(t, policy["native"])
	require.Equal(t, float64(1), document["manager_count"])
	require.Equal(t, float64(0), draft["resource_count"])
	require.Equal(t, false, draft["has_starter_workspace"])
	require.Equal(t, editorID.String(), draft["updated_by_user_id"], "the draft names its last editor")
	require.Equal(t, float64(model.ExamResourceDefaultMaximumCount), capacity["resource_maximum_count"])
	require.Equal(t, float64(model.ExamWorkspaceDefaultMaximumEntries), capacity["workspace_maximum_entries"])
}

func testExamHTTPView(t *testing.T, userID model.UserID) exam.View {
	t.Helper()
	at := time.Now().UTC()
	examRecord, err := model.NewExam(model.NewExamID(), model.NewAcademicUnitID(), userID, at)
	require.NoError(t, err)
	draft, err := model.NewExamDraft(examRecord.ID, "Systems", "Use **Go**.", model.DefaultExamPolicySet(), userID, at)
	require.NoError(t, err)
	return exam.View{Exam: *examRecord, Draft: *draft, OwnerUserID: userID, ManagerCount: 1}
}

func assertExamHTTPResponse(t *testing.T, data []byte, view exam.View) {
	t.Helper()
	var got examResponse
	require.NoError(t, json.Unmarshal(data, &got))
	require.Equal(t, view.Exam.ID.String(), got.Exam.ID)
	require.Equal(t, view.Draft.Title, got.Draft.Title)
	require.Equal(t, view.OwnerUserID.String(), got.OwnerUserID)
	require.Equal(t, 1, got.ManagerCount)
}

func TestExamOpenAPIDocument(t *testing.T) {
	t.Parallel()

	document := readOpenAPIDocument(t)
	var listOperation openAPIOperation
	require.NoError(t, json.Unmarshal(document.Paths[model.APIURLSuffix+"/exams"]["get"], &listOperation))
	require.Equal(t, []string{"academic_unit_id", "archive_state", "cursor", "ends_after", "limit", "q", "sitting_state", "starts_before"}, academicStructureQueryParameterNames(listOperation.Parameters))
}

func TestEditExamDraftTextOpenAPISchemaRequiresANonNullAuthoredValue(t *testing.T) {
	t.Parallel()
	document, err := openapi3.NewLoader().LoadFromFile(openAPIDocumentPath(t))
	require.NoError(t, err)
	schema := document.Components.Schemas["EditExamDraftTextRequest"]
	require.NotNil(t, schema, "EditExamDraftTextRequest schema is missing")
	require.NotNil(t, schema.Value, "EditExamDraftTextRequest schema is missing")
	for _, invalid := range []map[string]any{
		{"expected_revision": float64(1)},
		{"expected_revision": float64(1), "title": nil, "instructions_markdown": nil},
	} {
		require.Error(t, schema.Value.VisitJSON(invalid), "schema accepted %#v", invalid)
	}
	for _, valid := range []map[string]any{
		{"expected_revision": float64(1), "title": "Algorithms"},
		{"expected_revision": float64(1), "instructions_markdown": ""},
	} {
		require.NoError(t, schema.Value.VisitJSON(valid), "schema rejected %#v", valid)
	}
}
