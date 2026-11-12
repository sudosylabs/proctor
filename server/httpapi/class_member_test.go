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
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/sudosylabs/proctor/server/app/academic"
	"github.com/sudosylabs/proctor/server/app/invocation"
	"github.com/sudosylabs/proctor/server/app/paging"
	"github.com/sudosylabs/proctor/server/httpapi/route/routetest"
	"github.com/sudosylabs/proctor/server/model"
)

type classMemberHTTPApplication struct {
	pageQuery     academic.ListClassMembersPageQuery
	pageResult    *academic.ClassMemberPage
	result        *model.ClassEnrollment
	ended         *model.ClassMember
	enrollCommand academic.EnrollClassMemberCommand
	endCommand    academic.EndClassMemberCommand
}

func (a *classMemberHTTPApplication) EnrollClassMember(_ context.Context, _ invocation.Invocation, command academic.EnrollClassMemberCommand) (*model.ClassEnrollment, error) {
	a.enrollCommand = command
	return a.result, nil
}

func (a *classMemberHTTPApplication) EndClassMember(_ context.Context, _ invocation.Invocation, command academic.EndClassMemberCommand) (*model.ClassMember, error) {
	a.endCommand = command
	return a.ended, nil
}

func TestClassMemberHTTPUsesDTOAndRouteOwnedClass(t *testing.T) {
	t.Parallel()
	logger, _ := routetest.NewLogger(t)
	principal := model.Principal{UserID: model.NewUserID(), SessionID: model.NewSessionID(), CredentialID: model.PrincipalCredentialID(model.NewID()), CredentialType: model.CredentialSessionAccess, AuthenticationMethod: "password", AuthenticationStrength: model.AuthenticationSingleFactor, ClientType: model.SessionClientCLI, AuthenticatedAt: time.Now()}
	classID, userID := model.NewID(), model.NewID()
	member := &model.ClassMember{
		ID: model.NewClassMemberID(), CreatedAt: model.TimeFromMillis(100), UpdatedAt: model.TimeFromMillis(100),
		Revision: 1, ClassID: model.ClassID(classID), AcademicPeriodID: model.NewAcademicPeriodID(),
		UserID: model.UserID(userID), StartsAt: model.TimeFromMillis(100),
	}
	classMembers := &classMemberHTTPApplication{result: &model.ClassEnrollment{Membership: member}}
	httpAPI := newFocusedResourceAPI(t, logger, routetest.Authenticator{Principal: principal}, classMemberResource(classMembers))
	request := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/api/v1/classes/"+classID+"/members", strings.NewReader(`{"user_id":"`+userID+`","start_at":"1970-01-01T00:00:00.1Z"}`))
	request.Header.Set("Authorization", "Bearer credential")
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	httpAPI.ServeHTTP(response, request)
	require.Equal(t, http.StatusCreated, response.Code, "status = %d: %s", response.Code, response.Body.String())
	require.Equal(t, model.ClassID(classID), classMembers.enrollCommand.ClassID)
	require.Equal(t, model.UserID(userID), classMembers.enrollCommand.UserID)
	var body map[string]any
	require.NoError(t, json.Unmarshal(response.Body.Bytes(), &body))
	membership := body["membership"].(map[string]any)
	_, exposed := membership["revision"]
	require.False(t, exposed, "persistence revision leaked into DTO: %#v", membership)
}

func TestClassMemberResourceForwardsHistoryAndEndsMembership(t *testing.T) {
	t.Parallel()
	logger, _ := routetest.NewLogger(t)
	principal := model.Principal{UserID: model.NewUserID(), SessionID: model.NewSessionID(), CredentialID: model.PrincipalCredentialID(model.NewID()), CredentialType: model.CredentialSessionAccess, AuthenticationMethod: "password", AuthenticationStrength: model.AuthenticationSingleFactor, ClientType: model.SessionClientCLI, AuthenticatedAt: time.Now()}
	classID := model.NewID()
	member := &model.ClassMember{ID: model.NewClassMemberID(), ClassID: model.ClassID(classID), AcademicPeriodID: model.NewAcademicPeriodID(), UserID: model.NewUserID(), CreatedAt: model.TimeFromMillis(100), UpdatedAt: model.TimeFromMillis(100), StartsAt: model.TimeFromMillis(100)}
	members := &classMemberHTTPApplication{ended: member}
	httpAPI := newFocusedResourceAPI(t, logger, routetest.Authenticator{Principal: principal}, classMemberResource(members))

	list := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/api/v1/classes/"+classID+"/members?history=true", nil)
	list.Header.Set("Authorization", "Bearer credential")
	listed := httptest.NewRecorder()
	httpAPI.ServeHTTP(listed, list)
	require.Equal(t, http.StatusOK, listed.Code, "list status/query = %d/%#v: %s", listed.Code, members.pageQuery, listed.Body.String())
	require.Equal(t, model.ClassID(classID), members.pageQuery.ClassID)
	require.NotNil(t, members.pageQuery.History)
	require.True(t, *members.pageQuery.History, "history listing")
	require.JSONEq(t, `{"items":[],"total":0,"total_is_lower_bound":false}`, listed.Body.String(), "empty first page with its total")

	end := httptest.NewRequestWithContext(t.Context(), http.MethodDelete, "/api/v1/class-members/"+member.ID.String(), nil)
	end.Header.Set("Authorization", "Bearer credential")
	ended := httptest.NewRecorder()
	httpAPI.ServeHTTP(ended, end)
	require.Equal(t, http.StatusOK, ended.Code, "end status/command/cache = %d/%#v/%q: %s", ended.Code, members.endCommand, ended.Header().Get("Cache-Control"), ended.Body.String())
	require.Equal(t, member.ID, members.endCommand.ID, "end status/command/cache = %d/%#v/%q: %s", ended.Code, members.endCommand, ended.Header().Get("Cache-Control"), ended.Body.String())
	require.Equal(t, "no-store", ended.Header().Get("Cache-Control"), "end status/command/cache = %d/%#v/%q: %s", ended.Code, members.endCommand, ended.Header().Get("Cache-Control"), ended.Body.String())
}

func TestClassMemberHistoryQueryIsForwarded(t *testing.T) {
	t.Parallel()
	request := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/api/v1/classes/class/members?history=true", nil)
	query, _, _, err := membershipPageQuery(request, model.Resource{Type: model.ResourceClass, ID: model.NewID()})
	require.NoError(t, err)
	require.NotNil(t, query.History)
	require.True(t, *query.History, "history")
}

func (a *classMemberHTTPApplication) ListClassMembersPage(_ context.Context, _ invocation.Invocation, query academic.ListClassMembersPageQuery) (*academic.ClassMemberPage, error) {
	a.pageQuery = query
	if a.pageResult != nil {
		if query.AfterID.IsZero() && !a.pageResult.Counted {
			counted := *a.pageResult
			counted.Total = paging.CountWhole(len(counted.Members))
			return &counted, nil
		}
		return a.pageResult, nil
	}
	page := &academic.ClassMemberPage{}
	if query.AfterID.IsZero() {
		page.Total = paging.CountWhole(0)
	}
	return page, nil
}

func TestClassMemberOpenAPIDocument(t *testing.T) {
	t.Parallel()

	// Enrollment responses retain both the new membership and an optional
	// historical membership; the DTO registry verifies both.
	// Roster discovery also keeps its explicit point-in-time/history controls.
	document := readOpenAPIDocument(t)
	var listOperation struct {
		Parameters []struct {
			Name string `json:"name"`
			In   string `json:"in"`
		} `json:"parameters"`
	}
	require.NoError(t, json.Unmarshal(document.Paths["/api/v1/classes/{class_id}/members"]["get"], &listOperation))
	queryParameters := map[string]bool{}
	for _, parameter := range listOperation.Parameters {
		if parameter.In == "query" {
			queryParameters[parameter.Name] = true
		}
	}
	require.True(t, queryParameters["active_at"], "list query parameters = %#v, want active_at and history", queryParameters)
	require.True(t, queryParameters["history"], "list query parameters = %#v, want active_at and history", queryParameters)

	// This v1 relationship collection remains a legacy bare array.
	list := document.Components.Schemas["ClassMemberListResponse"]
	requireOpenAPIListEnvelope(t, list, "#/components/schemas/ClassMemberResponse")
}
