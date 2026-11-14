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
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/sudosylabs/proctor/server/app/apperror"
	"github.com/sudosylabs/proctor/server/app/exam"
	"github.com/sudosylabs/proctor/server/app/invocation"
	"github.com/sudosylabs/proctor/server/httpapi/route/routetest"
	"github.com/sudosylabs/proctor/server/model"
)

func (a *examHTTPApplication) GetExamDraftPluginSection(
	_ context.Context,
	_ invocation.Invocation,
	examID model.ExamID,
	pluginID model.PluginID,
) (exam.DraftPluginSectionView, error) {
	a.get = examID
	view := a.sectionView
	view.ExamID, view.PluginID = examID, pluginID
	return view, a.sectionErr
}

func (a *examHTTPApplication) ReplaceExamDraftPluginSection(
	_ context.Context,
	_ invocation.Invocation,
	command exam.ReplaceDraftPluginSectionCommand,
) (exam.View, error) {
	a.replaceSection = command
	if a.sectionErr != nil {
		return exam.View{}, a.sectionErr
	}
	view := a.view
	view.Draft.PluginSections = model.ExamPluginSections{}
	if command.Section != nil {
		view.Draft.PluginSections[command.PluginID] = command.Section
	}
	return view, nil
}

func sendExamPluginSection(t *testing.T, handler http.Handler, method, path string, body []byte) *httptest.ResponseRecorder {
	t.Helper()
	request := httptest.NewRequestWithContext(t.Context(), method, path, bytes.NewReader(body))
	request.Header.Set("Authorization", "Bearer credential")
	if body != nil {
		request.Header.Set("Content-Type", "application/json")
		request.Header.Set("Idempotency-Key", "section-retry")
	}
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	return response
}

func TestExamHTTPReplacesAndRemovesADraftPluginSection(t *testing.T) {
	t.Parallel()
	logger, _ := routetest.NewLogger(t)
	principal := testExamHTTPPrincipal()
	view := testExamHTTPView(t, principal.UserID)
	fake := &examHTTPApplication{principal: principal, view: view}
	handler := newFocusedResourceAPI(t, logger, fake, examResource(fake))
	path := "/api/v1/exams/" + view.Exam.ID.String() + "/draft/plugin-sections/execution"

	response := sendExamPluginSection(t, handler, http.MethodPut, path,
		[]byte(`{"expected_revision":3,"section":{"network":"none","image":"go-1.25"}}`))
	require.Equal(t, http.StatusOK, response.Code, response.Body.String())
	require.Equal(t, view.Exam.ID, fake.replaceSection.ExamID)
	require.Equal(t, model.PluginID("execution"), fake.replaceSection.PluginID)
	require.Equal(t, int64(3), fake.replaceSection.ExpectedDraftRevision)
	require.Equal(t, "section-retry", fake.replaceSection.IdempotencyKey)
	require.JSONEq(t, `{"network":"none","image":"go-1.25"}`, string(fake.replaceSection.Section))
	var replaced examResponse
	require.NoError(t, json.Unmarshal(response.Body.Bytes(), &replaced))
	require.JSONEq(t, `{"execution":{"network":"none","image":"go-1.25"}}`, mustMarshalJSON(t, replaced.Draft.PluginSections))

	response = sendExamPluginSection(t, handler, http.MethodPut, path, []byte(`{"expected_revision":4,"section":null}`))
	require.Equal(t, http.StatusOK, response.Code, response.Body.String())
	require.Nil(t, fake.replaceSection.Section, "a null section removes it")
	replaced = examResponse{}
	require.NoError(t, json.Unmarshal(response.Body.Bytes(), &replaced))
	require.Empty(t, replaced.Draft.PluginSections)
	require.Contains(t, response.Body.String(), `"plugin_sections":{}`, "no sections spell an empty object")
}

func TestExamHTTPRejectsMalformedPluginSectionRequests(t *testing.T) {
	t.Parallel()
	logger, _ := routetest.NewLogger(t)
	principal := testExamHTTPPrincipal()
	view := testExamHTTPView(t, principal.UserID)
	fake := &examHTTPApplication{principal: principal, view: view}
	handler := newFocusedResourceAPI(t, logger, fake, examResource(fake))
	base := "/api/v1/exams/" + view.Exam.ID.String() + "/draft/plugin-sections/"

	for name, body := range map[string]string{
		"missing section":      `{"expected_revision":3}`,
		"missing revision":     `{"section":{}}`,
		"array section":        `{"expected_revision":3,"section":[]}`,
		"string section":       `{"expected_revision":3,"section":"{}"}`,
		"unknown member":       `{"expected_revision":3,"section":{},"extra":1}`,
		"duplicate member":     `{"expected_revision":3,"expected_revision":3,"section":{}}`,
		"zero revision":        `{"expected_revision":0,"section":{}}`,
		"trailing JSON values": `{"expected_revision":3,"section":{}} {}`,
	} {
		fake.replaceSection = exam.ReplaceDraftPluginSectionCommand{}
		response := sendExamPluginSection(t, handler, http.MethodPut, base+"execution", []byte(body))
		require.Equal(t, http.StatusBadRequest, response.Code, "%s: %s", name, response.Body.String())
		require.False(t, fake.replaceSection.ExamID.IsValid(), "%s reached the application", name)
	}

	response := sendExamPluginSection(t, handler, http.MethodPut, base+"Not-A-Plugin", []byte(`{"expected_revision":3,"section":{}}`))
	require.NotEqual(t, http.StatusOK, response.Code)
	require.False(t, fake.replaceSection.ExamID.IsValid(), "an invalid plugin ID reached the application")
}

func TestExamHTTPPresentsPluginSectionRefusals(t *testing.T) {
	t.Parallel()
	logger, _ := routetest.NewLogger(t)
	principal := testExamHTTPPrincipal()
	view := testExamHTTPView(t, principal.UserID)
	path := "/api/v1/exams/" + view.Exam.ID.String() + "/draft/plugin-sections/execution"
	for code, status := range map[string]int{
		"exam.plugin_section.invalid":         http.StatusBadRequest,
		"exam.plugin_section.plugin_inactive": http.StatusConflict,
	} {
		fake := &examHTTPApplication{principal: principal, view: view,
			sectionErr: apperror.New(apperror.Code(code)).WithField("plugin_id", "execution")}
		handler := newFocusedResourceAPI(t, logger, fake, examResource(fake))
		response := sendExamPluginSection(t, handler, http.MethodPut, path, []byte(`{"expected_revision":3,"section":{"network":"open"}}`))
		require.Equal(t, status, response.Code, "%s: %s", code, response.Body.String())
		require.Contains(t, response.Body.String(), code)
		require.Contains(t, response.Body.String(), `"plugin_id":"execution"`)
	}
}

func TestExamHTTPReadsADraftPluginSectionWithItsSchema(t *testing.T) {
	t.Parallel()
	logger, _ := routetest.NewLogger(t)
	principal := testExamHTTPPrincipal()
	view := testExamHTTPView(t, principal.UserID)
	schema := `{"type": "object", "properties": {"network": {"enum": ["none"]}}}`
	fake := &examHTTPApplication{principal: principal, view: view, sectionView: exam.DraftPluginSectionView{
		DraftRevision: 3, Section: json.RawMessage(`{"network":"none"}`), Schema: json.RawMessage(schema),
	}}
	handler := newFocusedResourceAPI(t, logger, fake, examResource(fake))
	path := "/api/v1/exams/" + view.Exam.ID.String() + "/draft/plugin-sections/execution"

	response := sendExamPluginSection(t, handler, http.MethodGet, path, nil)
	require.Equal(t, http.StatusOK, response.Code, response.Body.String())
	var got examDraftPluginSectionResponse
	require.NoError(t, json.Unmarshal(response.Body.Bytes(), &got))
	require.Equal(t, view.Exam.ID.String(), got.ExamID)
	require.Equal(t, "execution", got.PluginID)
	require.Equal(t, int64(3), got.DraftRevision)
	require.JSONEq(t, `{"network":"none"}`, string(got.Section))
	require.Equal(t, schema, got.Schema, "the schema is served exactly as the plugin published it")

	fake.sectionView = exam.DraftPluginSectionView{DraftRevision: 3}
	response = sendExamPluginSection(t, handler, http.MethodGet, path, nil)
	require.Equal(t, http.StatusOK, response.Code, response.Body.String())
	require.NotContains(t, response.Body.String(), `"section"`, "an absent section is left out")
	require.NotContains(t, response.Body.String(), `"schema"`, "an inactive plugin's schema is left out")
}

func mustMarshalJSON(t *testing.T, value any) string {
	t.Helper()
	encoded, err := json.Marshal(value)
	require.NoError(t, err)
	return string(encoded)
}
