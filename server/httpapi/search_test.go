// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/sudosylabs/proctor/server/app/invocation"
	"github.com/sudosylabs/proctor/server/app/search"
	"github.com/sudosylabs/proctor/server/httpapi/route/routetest"
	"github.com/sudosylabs/proctor/server/model"
	"github.com/sudosylabs/proctor/server/store"
)

type searchHTTPApplication struct {
	calls  int
	query  search.Query
	result search.Result
}

func (a *searchHTTPApplication) Search(_ context.Context, _ invocation.Invocation, query search.Query) (search.Result, error) {
	a.calls++
	a.query = query
	return a.result, nil
}

func TestSearchResourcePresentsTypedItemsWithoutEmail(t *testing.T) {
	t.Parallel()
	logger, _ := routetest.NewLogger(t)
	principal := model.Principal{
		UserID: model.NewUserID(), SessionID: model.NewSessionID(), CredentialID: model.PrincipalCredentialID(model.NewID()),
		CredentialType: model.CredentialSessionAccess, AuthenticationMethod: "password",
		AuthenticationStrength: model.AuthenticationSingleFactor, ClientType: model.SessionClientCLI, AuthenticatedAt: time.Now(),
	}
	examSummary := store.ExamSummary{}
	examSummary.ID, examSummary.AcademicUnitID, examSummary.Title = model.ExamID(model.NewID()), model.AcademicUnitID(model.NewID()), "Algorithms midterm"
	class := &model.Class{ID: model.ClassID(model.NewID()), ProgrammeLevelID: model.ProgrammeLevelID(model.NewID()), DisplayName: "Algorithms A"}
	user := &model.User{ID: model.NewUserID(), Username: "ada.example", DisplayName: "Ada Example", Email: "ada@example.invalid"}
	application := &searchHTTPApplication{result: search.Result{
		Exams: []store.ExamSummary{examSummary}, Classes: []*model.Class{class}, Users: []*model.User{user},
	}}
	httpAPI := newFocusedResourceAPI(t, logger, routetest.Authenticator{Principal: principal}, searchResource(application))
	serve := func(target string) *httptest.ResponseRecorder {
		request := httptest.NewRequestWithContext(context.Background(), http.MethodGet, target, nil)
		request.Header.Set("Authorization", "Bearer credential")
		response := httptest.NewRecorder()
		httpAPI.ServeHTTP(response, request)
		return response
	}

	response := serve("/api/v1/search?q=algo&types=exam&types=user&limit=7")
	require.Equal(t, http.StatusOK, response.Code, response.Body.String())
	require.Equal(t, search.Query{Query: "algo", Types: []search.Type{"exam", "user"}, Limit: 7}, application.query)
	require.NotContains(t, response.Body.String(), "ada@example.invalid")
	require.NotContains(t, response.Body.String(), "next_cursor")
	var body struct {
		Items []map[string]string `json:"items"`
	}
	require.NoError(t, json.Unmarshal(response.Body.Bytes(), &body))
	require.Equal(t, []map[string]string{
		{"type": "exam", "id": examSummary.ID.String(), "title": "Algorithms midterm", "academic_unit_id": examSummary.AcademicUnitID.String()},
		{"type": "class", "id": class.ID.String(), "display_name": "Algorithms A", "programme_level_id": class.ProgrammeLevelID.String()},
		{"type": "user", "id": user.ID.String(), "display_name": "Ada Example", "username": "ada.example"},
	}, body.Items)

	calls := application.calls
	for _, target := range []string{
		"/api/v1/search?q=algo&cursor=abc",
		"/api/v1/search?q=algo&limit=0",
		"/api/v1/search?q=algo&limit=many",
		"/api/v1/search?q=algo&limit=1&limit=2",
		"/api/v1/search?q=algo&q=other",
	} {
		rejected := serve(target)
		require.Equal(t, http.StatusBadRequest, rejected.Code, "%s: %s", target, rejected.Body.String())
		require.Contains(t, rejected.Body.String(), `"request.invalid"`, target)
	}
	require.Equal(t, calls, application.calls, "rejected requests reached the application")
}

func TestSearchOpenAPIDocument(t *testing.T) {
	t.Parallel()

	document := readOpenAPIDocument(t)
	var operation openAPIOperation
	require.NoError(t, json.Unmarshal(document.Paths[model.APIURLSuffix+"/search"]["get"], &operation))
	require.Equal(t, []string{"cursor", "limit", "q", "types"}, academicStructureQueryParameterNames(operation.Parameters), "search query parameters")
	requireOpenAPIListEnvelope(t, document.Components.Schemas["SearchResponse"], "#/components/schemas/SearchItem")
	for _, variant := range []string{"SearchExamItem", "SearchClassItem", "SearchUserItem"} {
		_, exposesEmail := document.Components.Schemas[variant].Properties["email"]
		require.False(t, exposesEmail, "%s exposes email", variant)
	}
}
