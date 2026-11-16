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

	"github.com/stretchr/testify/require"

	"github.com/sudosylabs/proctor/server/httpapi/route/routetest"
	"github.com/sudosylabs/proctor/server/model"
	"github.com/sudosylabs/proctor/server/store"
)

func TestSearchResourcePresentsTypedItemsWithoutEmail(t *testing.T) {
	t.Parallel()
	logger, _ := routetest.NewLogger(t)
	principal := academicCaller()
	fixture := newSearchFixture(t, institutionWide(principal.UserID, model.ActionExamView, model.ActionClassView, model.ActionUserView))
	examSummary := store.ExamSummary{}
	examSummary.ID, examSummary.AcademicUnitID, examSummary.Title = model.ExamID(model.NewID()), model.AcademicUnitID(model.NewID()), "Algorithms midterm"
	class := &model.Class{ID: model.ClassID(model.NewID()), ProgrammeLevelID: model.ProgrammeLevelID(model.NewID()), DisplayName: "Algorithms A"}
	user := &model.User{ID: model.NewUserID(), Username: "ada.example", DisplayName: "Ada Example", Email: "ada@example.invalid"}
	fixture.exams.listed = []store.ExamSummary{examSummary}
	fixture.classes.listed = []*model.Class{class}
	fixture.users.listed = []*model.User{user}
	httpAPI := newFocusedResourceAPI(t, logger, routetest.Authenticator{Principal: principal}, searchResource(fixture.service))
	serve := func(target string) *httptest.ResponseRecorder {
		request := httptest.NewRequestWithContext(context.Background(), http.MethodGet, target, nil)
		request.Header.Set("Authorization", "Bearer credential")
		response := httptest.NewRecorder()
		httpAPI.ServeHTTP(response, request)
		return response
	}
	type item = map[string]string
	decode := func(response *httptest.ResponseRecorder) []item {
		t.Helper()
		require.Equal(t, http.StatusOK, response.Code, response.Body.String())
		require.NotContains(t, response.Body.String(), "ada@example.invalid")
		require.NotContains(t, response.Body.String(), "next_cursor")
		var body struct {
			Items []item `json:"items"`
		}
		require.NoError(t, json.Unmarshal(response.Body.Bytes(), &body))
		return body.Items
	}
	examItem := item{"type": "exam", "id": examSummary.ID.String(), "title": "Algorithms midterm", "academic_unit_id": examSummary.AcademicUnitID.String()}
	classItem := item{"type": "class", "id": class.ID.String(), "display_name": "Algorithms A", "programme_level_id": class.ProgrammeLevelID.String()}
	userItem := item{"type": "user", "id": user.ID.String(), "display_name": "Ada Example", "username": "ada.example"}

	// The query, its types and its limit reach each requested type's Store,
	// and only those.
	items := decode(serve("/api/v1/search?q=algo&types=exam&types=user&limit=7"))
	require.Equal(t, []item{examItem, userItem}, items)
	require.Len(t, fixture.exams.lists, 1)
	require.Equal(t, "algo", fixture.exams.lists[0].Query)
	require.Equal(t, 8, fixture.exams.lists[0].Limit, "the Exam catalog reads one past the limit")
	require.Equal(t, "algo", fixture.users.lastList(t).Query)
	require.Equal(t, 8, fixture.users.lastList(t).Limit, "the User directory reads one past the limit")
	require.Empty(t, fixture.classes.searches, "Classes were not requested")

	// Without types every type is searched, each kind in its own order.
	require.Equal(t, []item{examItem, classItem, userItem}, decode(serve("/api/v1/search?q=algo")))
	require.Len(t, fixture.classes.searches, 1)
	require.Equal(t, store.ClassSearchOptions{Term: "algo", Limit: 5, Visibility: fixture.classes.searches[0].Visibility}, fixture.classes.searches[0])

	reads := fixture.storeReads()
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
	require.Equal(t, reads, fixture.storeReads(), "rejected requests reached a Store")
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
