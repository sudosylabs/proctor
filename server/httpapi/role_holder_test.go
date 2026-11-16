// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package httpapi

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/sudosylabs/proctor/server/httpapi/route"
	"github.com/sudosylabs/proctor/server/httpapi/route/routetest"
	"github.com/sudosylabs/proctor/server/model"
	"github.com/sudosylabs/proctor/server/store"
)

// TestRoleBindingListByRolesBindsItsCursorToTheRoleSet pins the Role form of
// the listing: the Roles and page reach the Store, the first page alone
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
	principal := operatorTestPrincipal()
	fixture := newAdministrationFixture(t, institutionWide(principal.UserID, model.ActionRoleBindingView, model.ActionRoleView))
	fixture.roles.add(&model.Role{ID: reader, Name: "reader"}, &model.Role{ID: marker, Name: "marker"})
	fixture.bindings.holders = []*model.RoleBinding{newest, older, oldest}
	httpAPI := newFocusedResourceAPI(t, logger, routetest.Authenticator{Principal: principal}, roleBindingResource(fixture.service))
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
	listed := fixture.bindings.listedHolders()
	require.Len(t, listed, 1)
	require.Equal(t, []model.RoleID{reader, marker}, listed[0].RoleIDs)
	require.Equal(t, 3, listed[0].Limit, "the page size reached the Store with its probe row")
	require.True(t, listed[0].BeforeCreatedAt.IsZero())
	require.True(t, listed[0].BeforeID.IsZero())
	require.True(t, listed[0].Visibility.InstitutionWide)

	cursor := "&cursor=" + url.QueryEscape(first.NextCursor)
	later := decode(serve("role_id=" + marker.String() + "&role_id=" + reader.String() + "&role_id=" + marker.String() + "&limit=2" + cursor))
	require.Equal(t, []roleBindingResponse{roleBindingResponseFromModel(oldest)}, later.Items)
	require.Nil(t, later.Total, "a later page carries no total")
	require.Nil(t, later.TotalIsLowerBound)
	require.Empty(t, later.NextCursor)
	listed = fixture.bindings.listedHolders()
	require.Len(t, listed, 2)
	require.Equal(t, []model.RoleID{marker, reader}, listed[1].RoleIDs,
		"the same Roles in another order and with a repeat continue after the last binding")
	require.Equal(t, 3, listed[1].Limit)
	require.True(t, older.CreatedAt.Equal(listed[1].BeforeCreatedAt))
	require.Equal(t, older.ID, listed[1].BeforeID)

	asked := len(listed)
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
	require.Len(t, fixture.bindings.listedHolders(), asked, "a refused cursor or Role never reaches the Store")
}

func TestRoleListPresentsHoldersAndContinuesByName(t *testing.T) {
	t.Parallel()
	logger, _ := routetest.NewLogger(t)
	counted := &model.Role{ID: model.NewRoleID(), Name: "marker", DisplayName: "Marker", Permissions: []string{}}
	second := &model.Role{ID: model.NewRoleID(), Name: "reader", DisplayName: "Reader", Permissions: []string{}}
	later := &model.Role{ID: model.NewRoleID(), Name: "supervisor", DisplayName: "Supervisor", Permissions: []string{}}
	holderViewer, roleViewer := operatorTestPrincipal(), operatorTestPrincipal()
	fixture := newAdministrationFixture(t,
		institutionWide(holderViewer.UserID, model.ActionRoleView, model.ActionRoleBindingView),
		institutionWide(roleViewer.UserID, model.ActionRoleView))
	fixture.roles.add(later, second, counted)
	fixture.bindings.setCount(store.RoleHolderCount{RoleID: counted.ID, Bindings: store.MaximumListCount, Users: 23, Scopes: 6})
	httpAPI := newFocusedResourceAPI(t, logger, routetest.Authenticator{Principal: holderViewer}, roleResource(fixture.service))
	type rolePage struct {
		Items      []map[string]json.RawMessage `json:"items"`
		NextCursor string                       `json:"next_cursor"`
	}
	decode := func(response *httptest.ResponseRecorder) rolePage {
		t.Helper()
		require.Equal(t, http.StatusOK, response.Code, response.Body.String())
		var page rolePage
		require.NoError(t, json.Unmarshal(response.Body.Bytes(), &page))
		return page
	}

	page := decode(serveOperatorRequest(t, httpAPI, http.MethodGet, "/api/v1/roles?limit=2", "", true))
	require.Len(t, page.Items, 2)
	require.JSONEq(t, `"marker"`, string(page.Items[0]["name"]), "Roles are listed by name")
	require.JSONEq(t, `{"bindings":10000,"users":23,"scopes":6,"bindings_is_lower_bound":true,"users_is_lower_bound":true,"scopes_is_lower_bound":true}`,
		string(page.Items[0]["holders"]))
	require.JSONEq(t, `{"bindings":0,"users":0,"scopes":0,"bindings_is_lower_bound":false,"users_is_lower_bound":false,"scopes_is_lower_bound":false}`,
		string(page.Items[1]["holders"]), "a Role nobody holds counts zero holders")
	require.NotEmpty(t, page.NextCursor)

	withoutHolders := decode(serveOperatorRequest(t,
		newFocusedResourceAPI(t, logger, routetest.Authenticator{Principal: roleViewer}, roleResource(fixture.service)),
		http.MethodGet, "/api/v1/roles?limit=2", "", true))
	require.Len(t, withoutHolders.Items, 2)
	require.NotContains(t, withoutHolders.Items[0], "holders", "a Role read without holders omits the member")
	require.NotContains(t, withoutHolders.Items[1], "holders", "a Role read without holders omits the member")

	next := decode(serveOperatorRequest(t, httpAPI, http.MethodGet, "/api/v1/roles?limit=2&cursor="+url.QueryEscape(page.NextCursor), "", true))
	require.Len(t, next.Items, 1, "the cursor continues after the page's last Role by name")
	require.JSONEq(t, `"`+later.ID.String()+`"`, string(next.Items[0]["id"]), "the cursor continues after the page's last Role by name")
	require.Empty(t, next.NextCursor)
	assertHTTPProblem(t, serveOperatorRequest(t, httpAPI, http.MethodGet, "/api/v1/roles?cursor=not-a-cursor", "", true), http.StatusBadRequest, "request.invalid")

	fixture.bindings.setCount(store.RoleHolderCount{RoleID: counted.ID, Bindings: 2, Users: 2, Scopes: 1})
	response := serveOperatorRequest(t, httpAPI, http.MethodGet, "/api/v1/roles/"+counted.ID.String(), "", true)
	require.Equal(t, http.StatusOK, response.Code, response.Body.String())
	var one map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(response.Body.Bytes(), &one))
	require.JSONEq(t, `{"bindings":2,"users":2,"scopes":1,"bindings_is_lower_bound":false,"users_is_lower_bound":false,"scopes_is_lower_bound":false}`,
		string(one["holders"]), "a single Role read carries its holders too")
}
