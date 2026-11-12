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

	"github.com/sudosylabs/proctor/server/app/academic"
	"github.com/sudosylabs/proctor/server/app/paging"
	"github.com/sudosylabs/proctor/server/httpapi/route"
	"github.com/sudosylabs/proctor/server/httpapi/route/routetest"
	"github.com/sudosylabs/proctor/server/model"
)

func TestAcademicUnitMemberHTTPPagingUsesTheCollectionEnvelope(t *testing.T) {
	t.Parallel()
	logger, _ := routetest.NewLogger(t)
	principal := model.Principal{UserID: model.NewUserID(), SessionID: model.NewSessionID(), CredentialID: model.PrincipalCredentialID(model.NewID()), CredentialType: model.CredentialSessionAccess, AuthenticationMethod: "password", AuthenticationStrength: model.AuthenticationSingleFactor, ClientType: model.SessionClientCLI, AuthenticatedAt: time.Now()}
	scopeID := model.NewAcademicUnitID()
	member := &model.AcademicUnitMember{ID: model.NewAcademicUnitMemberID(), UserID: model.NewUserID(), AcademicUnitID: scopeID, StartsAt: time.Date(2026, 9, 1, 8, 0, 0, 0, time.UTC)}
	service := &academicUnitMemberHTTPApplication{pageResult: &academic.AcademicUnitMemberPage{
		Members: []*model.AcademicUnitMember{member}, HasMore: true, ActiveAt: 1000, Total: paging.Total{Counted: true, Total: 2},
	}}
	firstPage, emptyPage := service.pageResult, &academic.AcademicUnitMemberPage{ActiveAt: 1000}
	api := newFocusedResourceAPI(t, logger, routetest.Authenticator{Principal: principal}, academicUnitMemberResource(service))
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
	service.pageResult = &academic.AcademicUnitMemberPage{ActiveAt: 1000}
	response = call(target)
	require.Equal(t, http.StatusOK, response.Code, "final page/query = %d %#v", response.Code, service.pageQuery)
	require.Empty(t, decodeMembershipPage(t, response).NextCursor, "final page/query = %d %#v", response.Code, service.pageQuery)
	require.Equal(t, member.ID, service.pageQuery.AfterID, "final page/query = %d %#v", response.Code, service.pageQuery)
	require.True(t, member.StartsAt.Equal(service.pageQuery.AfterStartAt), "the default order continues from the start: %#v", service.pageQuery)
	require.True(t, service.pageQuery.AfterUserID.IsZero(), "the start order carries no member: %#v", service.pageQuery)
	require.Nil(t, decodeMembershipPage(t, response).Total, "a later page carries no total")
	service.pageResult = firstPage
	byName := decodeMembershipPage(t, call(path+"?limit=1&sort=name&dir=desc"))
	service.pageResult = emptyPage
	response = call(path + "?limit=1&sort=name&dir=desc&cursor=" + url.QueryEscape(byName.NextCursor))
	require.Equal(t, http.StatusOK, response.Code, response.Body.String())
	require.Equal(t, member.UserID, service.pageQuery.AfterUserID, "the name order continues from the member: %#v", service.pageQuery)
	require.True(t, service.pageQuery.AfterStartAt.IsZero(), "the name order carries no start: %#v", service.pageQuery)
	require.True(t, service.pageQuery.Descending)
	require.Equal(t, http.StatusBadRequest, call(path+"?limit=1&cursor="+url.QueryEscape(byName.NextCursor)).Code,
		"a name cursor cannot continue the default order")
	for _, suffix := range []string{"?limit=0", "?limit=201", "?limit=1&limit=2", "?cursor=", "?limit=1&history=true&active_at=1970-01-01T00:00:01Z", "?limit=1&history=invalid"} {
		require.Equal(t, http.StatusBadRequest, call(path+suffix).Code, "invalid page query %s", suffix)
	}
	service.pageResult = &academic.AcademicUnitMemberPage{HasMore: true, Members: []*model.AcademicUnitMember{member}, Total: paging.CountWhole(1)}
	response = call(path + "?limit=1&history=true")
	require.Equal(t, http.StatusOK, response.Code)
	historyCursor, err := route.DecodeCursor(decodeMembershipPage(t, response).NextCursor, membershipPageCursorSpec())
	require.NoError(t, err)
	require.Equal(t, int64(0), *historyCursor.ActiveAt, "history continuation keeps the history filter")
}

func TestClassMemberHTTPPagingUsesTheCollectionEnvelope(t *testing.T) {
	t.Parallel()
	logger, _ := routetest.NewLogger(t)
	principal := model.Principal{UserID: model.NewUserID(), SessionID: model.NewSessionID(), CredentialID: model.PrincipalCredentialID(model.NewID()), CredentialType: model.CredentialSessionAccess, AuthenticationMethod: "password", AuthenticationStrength: model.AuthenticationSingleFactor, ClientType: model.SessionClientCLI, AuthenticatedAt: time.Now()}
	scopeID := model.NewClassID()
	member := &model.ClassMember{ID: model.NewClassMemberID(), UserID: model.NewUserID(), ClassID: scopeID, StartsAt: time.Date(2026, 9, 1, 8, 0, 0, 0, time.UTC)}
	service := &classMemberHTTPApplication{pageResult: &academic.ClassMemberPage{
		Members: []*model.ClassMember{member}, HasMore: true, ActiveAt: 1000, Total: paging.Total{Counted: true, Total: 2},
	}}
	firstPage, emptyPage := service.pageResult, &academic.ClassMemberPage{ActiveAt: 1000}
	api := newFocusedResourceAPI(t, logger, routetest.Authenticator{Principal: principal}, classMemberResource(service))
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
	service.pageResult = &academic.ClassMemberPage{ActiveAt: 1000}
	response = call(target)
	require.Equal(t, http.StatusOK, response.Code, "final page/query = %d %#v", response.Code, service.pageQuery)
	require.Empty(t, decodeMembershipPage(t, response).NextCursor, "final page/query = %d %#v", response.Code, service.pageQuery)
	require.Equal(t, member.ID, service.pageQuery.AfterID, "final page/query = %d %#v", response.Code, service.pageQuery)
	require.True(t, member.StartsAt.Equal(service.pageQuery.AfterStartAt), "the default order continues from the start: %#v", service.pageQuery)
	require.True(t, service.pageQuery.AfterUserID.IsZero(), "the start order carries no member: %#v", service.pageQuery)
	require.Nil(t, decodeMembershipPage(t, response).Total, "a later page carries no total")
	service.pageResult = firstPage
	byName := decodeMembershipPage(t, call(path+"?limit=1&sort=name&dir=desc"))
	service.pageResult = emptyPage
	response = call(path + "?limit=1&sort=name&dir=desc&cursor=" + url.QueryEscape(byName.NextCursor))
	require.Equal(t, http.StatusOK, response.Code, response.Body.String())
	require.Equal(t, member.UserID, service.pageQuery.AfterUserID, "the name order continues from the member: %#v", service.pageQuery)
	require.True(t, service.pageQuery.AfterStartAt.IsZero(), "the name order carries no start: %#v", service.pageQuery)
	require.True(t, service.pageQuery.Descending)
	require.Equal(t, http.StatusBadRequest, call(path+"?limit=1&cursor="+url.QueryEscape(byName.NextCursor)).Code,
		"a name cursor cannot continue the default order")
	for _, suffix := range []string{"?limit=0", "?limit=201", "?limit=1&limit=2", "?cursor=", "?limit=1&history=true&active_at=1970-01-01T00:00:01Z", "?limit=1&history=invalid"} {
		require.Equal(t, http.StatusBadRequest, call(path+suffix).Code, "invalid page query %s", suffix)
	}
	service.pageResult = &academic.ClassMemberPage{HasMore: true, Members: []*model.ClassMember{member}, Total: paging.CountWhole(1)}
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
