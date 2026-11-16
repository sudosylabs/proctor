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
	"time"

	"github.com/stretchr/testify/require"

	"github.com/sudosylabs/proctor/server/app/access/accesstest"
	"github.com/sudosylabs/proctor/server/httpapi/route"
	"github.com/sudosylabs/proctor/server/httpapi/route/routetest"
	"github.com/sudosylabs/proctor/server/model"
	"github.com/sudosylabs/proctor/server/store"
)

func TestAcademicUnitMemberHTTPPagingUsesTheCollectionEnvelope(t *testing.T) {
	t.Parallel()
	logger, _ := routetest.NewLogger(t)
	principal := academicCaller()
	scopeID := model.NewAcademicUnitID()
	user := academicUser("amina", "Amina Diallo", "amina@example.edu")
	member := &model.AcademicUnitMember{ID: model.NewAcademicUnitMemberID(), UserID: user.ID, AcademicUnitID: scopeID, StartsAt: time.Date(2026, 9, 1, 8, 0, 0, 0, time.UTC)}
	// The clock fixes the effective instant a first page freezes.
	fixture := newAcademicFixture(t, academicOptions{
		Grants: []accesstest.Grant{inAcademicUnit(principal.UserID, scopeID, model.ActionAcademicUnitMembersView)},
		Now:    func() time.Time { return model.TimeFromMillis(1000) },
	})
	fixture.users.add(user)
	members := fixture.unitMembers
	firstPage, emptyPage := store.AcademicUnitMemberPage{Members: []*model.AcademicUnitMember{member}, HasMore: true}, store.AcademicUnitMemberPage{}
	members.page, members.total = firstPage, 2
	api := newFocusedResourceAPI(t, logger, routetest.Authenticator{Principal: principal}, academicUnitMemberResource(fixture.service))
	path := "/api/v1/academic-units/" + scopeID.String() + "/members"
	call := func(target string) *httptest.ResponseRecorder {
		request := httptest.NewRequestWithContext(t.Context(), http.MethodGet, target, nil)
		request.Header.Set("Authorization", "Bearer credential")
		response := httptest.NewRecorder()
		api.ServeHTTP(response, request)
		return response
	}
	response := call(path + "?limit=1")
	require.Equal(t, http.StatusOK, response.Code, "page status = %d: %s", response.Code, response.Body.String())
	result := decodeMembershipPage(t, response)
	require.Len(t, result.Items, 1, "page: %s", response.Body.String())
	require.Empty(t, response.Header().Get("Link"), "continuation travels in the body")
	cursor, err := route.DecodeCursor(result.NextCursor, membershipPageCursorSpec())
	require.NoError(t, err, "next_cursor")
	require.Equal(t, int64(1000), *cursor.ActiveAt, "continuation freezes the effective instant")
	require.NotNil(t, result.Total, "the first page carries its total")
	require.Equal(t, 2, *result.Total)
	target := path + "?limit=1&cursor=" + url.QueryEscape(result.NextCursor)
	members.page = emptyPage
	counted := len(members.countOptions)
	response = call(target)
	page := members.lastPage(t)
	require.Equal(t, http.StatusOK, response.Code, "final page/options = %d %#v", response.Code, page)
	require.Empty(t, decodeMembershipPage(t, response).NextCursor, "final page/options = %d %#v", response.Code, page)
	require.Equal(t, member.ID, page.AfterID, "final page/options = %d %#v", response.Code, page)
	require.True(t, model.TimeFromMillis(1000).Equal(page.ActiveAt), "the continuation reads at the frozen instant: %#v", page)
	require.True(t, member.StartsAt.Equal(page.AfterStartAt), "the default order continues from the start: %#v", page)
	require.True(t, page.AfterUserID.IsZero(), "the start order carries no member: %#v", page)
	require.Nil(t, decodeMembershipPage(t, response).Total, "a later page carries no total")
	require.Len(t, members.countOptions, counted, "a later page is not counted")
	members.page = firstPage
	byName := decodeMembershipPage(t, call(path+"?limit=1&sort=name&dir=desc"))
	members.page = emptyPage
	response = call(path + "?limit=1&sort=name&dir=desc&cursor=" + url.QueryEscape(byName.NextCursor))
	require.Equal(t, http.StatusOK, response.Code, response.Body.String())
	page = members.lastPage(t)
	require.Equal(t, member.UserID, page.AfterUserID, "the name order continues from the member: %#v", page)
	require.True(t, page.AfterStartAt.IsZero(), "the name order carries no start: %#v", page)
	require.Equal(t, store.MembershipOrderName, page.Order)
	require.True(t, page.Descending)
	require.Equal(t, http.StatusBadRequest, call(path+"?limit=1&cursor="+url.QueryEscape(byName.NextCursor)).Code,
		"a name cursor cannot continue the default order")
	listed := len(members.pageOptions)
	for _, suffix := range []string{"?limit=0", "?limit=201", "?limit=1&limit=2", "?cursor=", "?limit=1&history=true&active_at=1970-01-01T00:00:01Z", "?limit=1&history=invalid"} {
		require.Equal(t, http.StatusBadRequest, call(path+suffix).Code, "invalid page query %s", suffix)
	}
	require.Len(t, members.pageOptions, listed, "an invalid page query reached the membership Store")
	members.page, members.total = store.AcademicUnitMemberPage{HasMore: true, Members: []*model.AcademicUnitMember{member}}, 1
	response = call(path + "?limit=1&history=true")
	require.Equal(t, http.StatusOK, response.Code)
	historyCursor, err := route.DecodeCursor(decodeMembershipPage(t, response).NextCursor, membershipPageCursorSpec())
	require.NoError(t, err)
	require.Equal(t, int64(0), *historyCursor.ActiveAt, "history continuation keeps the history filter")
}

func TestClassMemberHTTPPagingUsesTheCollectionEnvelope(t *testing.T) {
	t.Parallel()
	logger, _ := routetest.NewLogger(t)
	principal := academicCaller()
	world := newAcademicWorld()
	scopeID := world.class.ID
	member := &model.ClassMember{ID: model.NewClassMemberID(), UserID: model.NewUserID(), ClassID: scopeID, StartsAt: time.Date(2026, 9, 1, 8, 0, 0, 0, time.UTC)}
	// The clock fixes the effective instant a first page freezes.
	fixture := newAcademicFixture(t, academicOptions{
		Grants: []accesstest.Grant{inAcademicUnit(principal.UserID, world.unit.ID, model.ActionClassMembersView)},
		Units:  world.units(),
		Now:    func() time.Time { return model.TimeFromMillis(1000) },
	})
	fixture.hold(world)
	members := fixture.classMembers
	firstPage, emptyPage := store.ClassMemberPage{Members: []*model.ClassMember{member}, HasMore: true}, store.ClassMemberPage{}
	members.page, members.total = firstPage, 2
	api := newFocusedResourceAPI(t, logger, routetest.Authenticator{Principal: principal}, classMemberResource(fixture.service))
	path := "/api/v1/classes/" + scopeID.String() + "/members"
	call := func(target string) *httptest.ResponseRecorder {
		request := httptest.NewRequestWithContext(t.Context(), http.MethodGet, target, nil)
		request.Header.Set("Authorization", "Bearer credential")
		response := httptest.NewRecorder()
		api.ServeHTTP(response, request)
		return response
	}
	response := call(path + "?limit=1")
	require.Equal(t, http.StatusOK, response.Code, "page status = %d: %s", response.Code, response.Body.String())
	result := decodeMembershipPage(t, response)
	require.Len(t, result.Items, 1, "page: %s", response.Body.String())
	require.Empty(t, response.Header().Get("Link"), "continuation travels in the body")
	cursor, err := route.DecodeCursor(result.NextCursor, membershipPageCursorSpec())
	require.NoError(t, err, "next_cursor")
	require.Equal(t, int64(1000), *cursor.ActiveAt, "continuation freezes the effective instant")
	require.NotNil(t, result.Total, "the first page carries its total")
	require.Equal(t, 2, *result.Total)
	target := path + "?limit=1&cursor=" + url.QueryEscape(result.NextCursor)
	members.page = emptyPage
	counted := len(members.countOptions)
	response = call(target)
	page := members.lastPage(t)
	require.Equal(t, http.StatusOK, response.Code, "final page/options = %d %#v", response.Code, page)
	require.Empty(t, decodeMembershipPage(t, response).NextCursor, "final page/options = %d %#v", response.Code, page)
	require.Equal(t, member.ID, page.AfterID, "final page/options = %d %#v", response.Code, page)
	require.True(t, model.TimeFromMillis(1000).Equal(page.ActiveAt), "the continuation reads at the frozen instant: %#v", page)
	require.True(t, member.StartsAt.Equal(page.AfterStartAt), "the default order continues from the start: %#v", page)
	require.True(t, page.AfterUserID.IsZero(), "the start order carries no member: %#v", page)
	require.Nil(t, decodeMembershipPage(t, response).Total, "a later page carries no total")
	require.Len(t, members.countOptions, counted, "a later page is not counted")
	members.page = firstPage
	byName := decodeMembershipPage(t, call(path+"?limit=1&sort=name&dir=desc"))
	members.page = emptyPage
	response = call(path + "?limit=1&sort=name&dir=desc&cursor=" + url.QueryEscape(byName.NextCursor))
	require.Equal(t, http.StatusOK, response.Code, response.Body.String())
	page = members.lastPage(t)
	require.Equal(t, member.UserID, page.AfterUserID, "the name order continues from the member: %#v", page)
	require.True(t, page.AfterStartAt.IsZero(), "the name order carries no start: %#v", page)
	require.Equal(t, store.MembershipOrderName, page.Order)
	require.True(t, page.Descending)
	require.Equal(t, http.StatusBadRequest, call(path+"?limit=1&cursor="+url.QueryEscape(byName.NextCursor)).Code,
		"a name cursor cannot continue the default order")
	listed := len(members.pageOptions)
	for _, suffix := range []string{"?limit=0", "?limit=201", "?limit=1&limit=2", "?cursor=", "?limit=1&history=true&active_at=1970-01-01T00:00:01Z", "?limit=1&history=invalid"} {
		require.Equal(t, http.StatusBadRequest, call(path+suffix).Code, "invalid page query %s", suffix)
	}
	require.Len(t, members.pageOptions, listed, "an invalid page query reached the membership Store")
	members.page, members.total = store.ClassMemberPage{HasMore: true, Members: []*model.ClassMember{member}}, 1
	response = call(path + "?limit=1&history=true")
	require.Equal(t, http.StatusOK, response.Code)
	historyCursor, err := route.DecodeCursor(decodeMembershipPage(t, response).NextCursor, membershipPageCursorSpec())
	require.NoError(t, err)
	require.Equal(t, int64(0), *historyCursor.ActiveAt, "history continuation keeps the history filter")
}

type membershipPageBody struct {
	Items      []map[string]any `json:"items"`
	NextCursor string           `json:"next_cursor"`
	Total      *int             `json:"total"`
}

func decodeMembershipPage(t *testing.T, response *httptest.ResponseRecorder) membershipPageBody {
	t.Helper()
	var page membershipPageBody
	require.NoError(t, json.Unmarshal(response.Body.Bytes(), &page), "page envelope: %s", response.Body.String())
	require.NotNil(t, page.Items, "page envelope: %s", response.Body.String())
	return page
}
