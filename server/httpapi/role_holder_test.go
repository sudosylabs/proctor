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
	"net/url"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/sudosylabs/proctor/server/app/administration"
	"github.com/sudosylabs/proctor/server/app/invocation"
	"github.com/sudosylabs/proctor/server/app/paging"
	"github.com/sudosylabs/proctor/server/httpapi/route"
	"github.com/sudosylabs/proctor/server/httpapi/route/routetest"
	"github.com/sudosylabs/proctor/server/model"
)

// roleHolderApplication answers a Role listing with one more page after the
// first, counting only the first, and records every query it receives.
type roleHolderApplication struct {
	focusedRoleBindingApplication
	first   []*model.RoleBinding
	later   []*model.RoleBinding
	queries []administration.ListRoleBindingsQuery
}

func (a *roleHolderApplication) ListRoleBindings(_ context.Context, _ invocation.Invocation, query administration.ListRoleBindingsQuery) (paging.Counted[*model.RoleBinding], error) {
	a.queries = append(a.queries, query)
	if query.BeforeID.IsZero() {
		return paging.Counted[*model.RoleBinding]{
			Page:  paging.Page[*model.RoleBinding]{Items: a.first, HasMore: true},
			Total: paging.Total{Counted: true, Total: len(a.first) + len(a.later)},
		}, nil
	}
	return paging.Counted[*model.RoleBinding]{Page: paging.Page[*model.RoleBinding]{Items: a.later}}, nil
}

// TestRoleBindingListByRolesBindsItsCursorToTheRoleSet pins the Role form of
// the listing: the Roles and page reach the application, the first page alone
// carries the total, and a cursor continues only the same set of Roles.
func TestRoleBindingListByRolesBindsItsCursorToTheRoleSet(t *testing.T) {
	t.Parallel()
	logger, _ := routetest.NewLogger(t)
	reader, marker := model.NewRoleID(), model.NewRoleID()
	binding := func(millis int64, role model.RoleID) *model.RoleBinding {
		return &model.RoleBinding{ID: model.NewRoleBindingID(), UserID: model.NewUserID(), RoleID: role,
			ScopeType: model.RoleScopeInstitution, ScopeID: model.NewID(),
			CreatedAt: model.TimeFromMillis(millis), StartsAt: model.TimeFromMillis(millis)}
	}
	newest, older, oldest := binding(300, reader), binding(200, marker), binding(100, reader)
	application := &roleHolderApplication{first: []*model.RoleBinding{newest, older}, later: []*model.RoleBinding{oldest}}
	httpAPI := newFocusedResourceAPI(t, logger, &academicUnitHTTPApplication{principal: operatorTestPrincipal()}, roleBindingResource(application))
	serve := func(query string) *httptest.ResponseRecorder {
		return serveOperatorRequest(t, httpAPI, http.MethodGet, "/api/v1/role-bindings?"+query, "", true)
	}
	decode := func(response *httptest.ResponseRecorder) route.CountedList[roleBindingResponse] {
		t.Helper()
		require.Equal(t, http.StatusOK, response.Code, response.Body.String())
		var page route.CountedList[roleBindingResponse]
		require.NoError(t, json.Unmarshal(response.Body.Bytes(), &page))
		return page
	}
	roles := "role_id=" + reader.String() + "&role_id=" + marker.String()

	first := decode(serve(roles + "&limit=2"))
	require.Equal(t, []roleBindingResponse{roleBindingResponseFromModel(newest), roleBindingResponseFromModel(older)}, first.Items)
	require.NotNil(t, first.Total, "the first page carries its total")
	require.Equal(t, 3, *first.Total)
	require.NotNil(t, first.TotalIsLowerBound)
	require.False(t, *first.TotalIsLowerBound)
	require.NotEmpty(t, first.NextCursor)
	require.Equal(t, []administration.ListRoleBindingsQuery{{RoleIDs: []model.RoleID{reader, marker}, Limit: 2}}, application.queries)

	cursor := "&cursor=" + url.QueryEscape(first.NextCursor)
	later := decode(serve("role_id=" + marker.String() + "&role_id=" + reader.String() + "&role_id=" + marker.String() + "&limit=2" + cursor))
	require.Equal(t, []roleBindingResponse{roleBindingResponseFromModel(oldest)}, later.Items)
	require.Nil(t, later.Total, "a later page carries no total")
	require.Nil(t, later.TotalIsLowerBound)
	require.Empty(t, later.NextCursor)
	require.Equal(t, administration.ListRoleBindingsQuery{
		RoleIDs: []model.RoleID{marker, reader, marker}, Limit: 2,
		BeforeCreatedAt: older.CreatedAt, BeforeID: older.ID,
	}, application.queries[1], "the same Roles in another order and with a repeat continue after the last binding")

	asked := len(application.queries)
	for name, query := range map[string]string{
		"one of the Roles":    "role_id=" + reader.String() + cursor,
		"another Role":        "role_id=" + model.NewRoleID().String() + cursor,
		"an added Role":       roles + "&role_id=" + model.NewRoleID().String() + cursor,
		"a malformed cursor":  roles + "&cursor=not-a-cursor",
		"a malformed Role ID": "role_id=not-a-role",
		"an empty Role ID":    "role_id=",
	} {
		t.Run(name, func(t *testing.T) {
			assertHTTPProblem(t, serve(query), http.StatusBadRequest, "request.invalid")
		})
	}
	require.Len(t, application.queries, asked, "a refused cursor or Role never reaches the application")
}

func TestRoleListPresentsHoldersAndContinuesByName(t *testing.T) {
	t.Parallel()
	logger, _ := routetest.NewLogger(t)
	counted := &model.Role{ID: model.NewRoleID(), Name: "marker", DisplayName: "Marker", Permissions: []string{}}
	uncounted := &model.Role{ID: model.NewRoleID(), Name: "reader", DisplayName: "Reader", Permissions: []string{}}
	roles := &roleHTTPApplication{page: paging.Page[administration.RoleResult]{Items: []administration.RoleResult{
		{Role: counted, Holders: &administration.RoleHolders{Bindings: 10_000, Users: 23, Scopes: 6, BindingsIsLowerBound: true, UsersIsLowerBound: true, ScopesIsLowerBound: true}},
		{Role: uncounted},
	}, HasMore: true}, result: counted, holders: &administration.RoleHolders{Bindings: 2, Users: 2, Scopes: 1}}
	httpAPI := newFocusedResourceAPI(t, logger, &academicUnitHTTPApplication{principal: operatorTestPrincipal()}, roleResource(roles))

	response := serveOperatorRequest(t, httpAPI, http.MethodGet, "/api/v1/roles?limit=2", "", true)
	require.Equal(t, http.StatusOK, response.Code, response.Body.String())
	var page struct {
		Items      []map[string]json.RawMessage `json:"items"`
		NextCursor string                       `json:"next_cursor"`
	}
	require.NoError(t, json.Unmarshal(response.Body.Bytes(), &page))
	require.Len(t, page.Items, 2)
	require.JSONEq(t, `{"bindings":10000,"users":23,"scopes":6,"bindings_is_lower_bound":true,"users_is_lower_bound":true,"scopes_is_lower_bound":true}`,
		string(page.Items[0]["holders"]))
	require.NotContains(t, page.Items[1], "holders", "a Role read without holders omits the member")
	require.Equal(t, administration.ListRolesQuery{NameQuery: paging.NameQuery{Limit: 2}}, roles.listQuery)

	response = serveOperatorRequest(t, httpAPI, http.MethodGet, "/api/v1/roles?limit=2&cursor="+url.QueryEscape(page.NextCursor), "", true)
	require.Equal(t, http.StatusOK, response.Code, response.Body.String())
	require.Equal(t, administration.ListRolesQuery{NameQuery: paging.NameQuery{AfterName: uncounted.Name, AfterID: uncounted.ID.String(), Limit: 2}}, roles.listQuery,
		"the cursor continues after the page's last Role by name")
	assertHTTPProblem(t, serveOperatorRequest(t, httpAPI, http.MethodGet, "/api/v1/roles?cursor=not-a-cursor", "", true), http.StatusBadRequest, "request.invalid")

	response = serveOperatorRequest(t, httpAPI, http.MethodGet, "/api/v1/roles/"+counted.ID.String(), "", true)
	require.Equal(t, http.StatusOK, response.Code, response.Body.String())
	var one map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(response.Body.Bytes(), &one))
	require.JSONEq(t, `{"bindings":2,"users":2,"scopes":1,"bindings_is_lower_bound":false,"users_is_lower_bound":false,"scopes_is_lower_bound":false}`,
		string(one["holders"]), "a single Role read carries its holders too")
}
