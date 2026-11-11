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
	"github.com/sudosylabs/proctor/server/httpapi/route/routetest"
	"github.com/sudosylabs/proctor/server/model"
	"github.com/sudosylabs/proctor/server/store"
)

type academicUnitMemberHTTPApplication struct {
	pageQuery     academic.ListAcademicUnitMembersPageQuery
	pageResult    *academic.AcademicUnitMemberPage
	result        *model.AcademicUnitMember
	values        []*model.AcademicUnitMember
	createCommand academic.CreateAcademicUnitMemberCommand
	endCommand    academic.EndAcademicUnitMemberCommand
}

func TestAcademicUnitMemberHistoryQueryIncludesEndedMemberships(t *testing.T) {
	t.Parallel()

	request := httptest.NewRequestWithContext(t.Context(), "GET", "/api/v1/academic-units/unit/members?history=true", nil)
	query, _, err := membershipPageQuery(request, model.Resource{Type: model.ResourceAcademicUnit, ID: model.NewID()})
	require.NoError(t, err)
	require.NotNil(t, query.History)
	require.True(t, *query.History, "history")
}

func TestAcademicUnitMemberHistoryQueryRejectsInvalidBoolean(t *testing.T) {
	t.Parallel()

	request := httptest.NewRequestWithContext(t.Context(), "GET", "/api/v1/academic-units/unit/members?history=invalid", nil)
	_, _, err := membershipPageQuery(request, model.Resource{Type: model.ResourceAcademicUnit, ID: model.NewID()})
	require.Error(t, err, "invalid history query was accepted")
}

func TestAcademicUnitMemberFalseHistoryQueryUsesActiveAt(t *testing.T) {
	t.Parallel()

	request := httptest.NewRequestWithContext(t.Context(), "GET", "/api/v1/academic-units/unit/members?history=false&active_at=1970-01-01T00:00:00.123Z", nil)
	query, _, err := membershipPageQuery(request, model.Resource{Type: model.ResourceAcademicUnit, ID: model.NewID()})
	require.NoError(t, err)
	require.NotNil(t, query.ActiveAt)
	require.Equal(t, int64(123), *query.ActiveAt, "active at")
}

func (a *academicUnitMemberHTTPApplication) CreateAcademicUnitMember(_ context.Context, _ invocation.Invocation, command academic.CreateAcademicUnitMemberCommand) (*model.AcademicUnitMember, error) {
	a.createCommand = command
	return a.result, nil
}
func (a *academicUnitMemberHTTPApplication) EndAcademicUnitMember(_ context.Context, _ invocation.Invocation, command academic.EndAcademicUnitMemberCommand) (*model.AcademicUnitMember, error) {
	a.endCommand = command
	return a.result, nil
}

func TestAcademicUnitMemberResourceRunsThroughRoutingKernel(t *testing.T) {
	t.Parallel()
	logger, _ := routetest.NewLogger(t)
	principal := model.Principal{UserID: model.NewUserID(), SessionID: model.NewSessionID(), CredentialID: model.PrincipalCredentialID(model.NewID()), CredentialType: model.CredentialSessionAccess, AuthenticationMethod: "password", AuthenticationStrength: model.AuthenticationSingleFactor, ClientType: model.SessionClientCLI, AuthenticatedAt: time.Now()}
	unitID, userID := model.NewID(), model.NewID()
	member := &model.AcademicUnitMember{ID: model.NewAcademicUnitMemberID(), AcademicUnitID: model.AcademicUnitID(unitID), UserID: model.UserID(userID), CreatedAt: model.TimeFromMillis(100), UpdatedAt: model.TimeFromMillis(100), StartsAt: model.TimeFromMillis(100)}
	members := &academicUnitMemberHTTPApplication{result: member}
	httpAPI := newFocusedResourceAPI(t, logger, routetest.Authenticator{Principal: principal}, academicUnitMemberResource(members))

	create := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/api/v1/academic-units/"+unitID+"/members", strings.NewReader(`{"user_id":"`+userID+`","start_at":"1970-01-01T00:00:00.1Z"}`))
	create.Header.Set("Authorization", "Bearer credential")
	create.Header.Set("Content-Type", "application/json")
	created := httptest.NewRecorder()
	httpAPI.ServeHTTP(created, create)
	require.Equal(t, http.StatusCreated, created.Code, "create status = %d: %s", created.Code, created.Body.String())
	require.Equal(t, model.AcademicUnitID(unitID), members.createCommand.AcademicUnitID)
	require.Equal(t, model.UserID(userID), members.createCommand.UserID)
	var body map[string]any
	require.NoError(t, json.Unmarshal(created.Body.Bytes(), &body))
	_, exposed := body["revision"]
	require.False(t, exposed, "persistence revision leaked into DTO: %#v", body)

	list := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/api/v1/academic-units/"+unitID+"/members?history=true", nil)
	list.Header.Set("Authorization", "Bearer credential")
	listed := httptest.NewRecorder()
	httpAPI.ServeHTTP(listed, list)
	require.Equal(t, http.StatusOK, listed.Code, "list status/query = %d/%#v: %s", listed.Code, members.pageQuery, listed.Body.String())
	require.Equal(t, model.AcademicUnitID(unitID), members.pageQuery.AcademicUnitID)
	require.NotNil(t, members.pageQuery.History)
	require.True(t, *members.pageQuery.History, "history listing")
	require.JSONEq(t, `{"items":[]}`, listed.Body.String(), "empty collection envelope")
}

func TestAcademicUnitMemberListNamesMembersAndTheirRolesHere(t *testing.T) {
	t.Parallel()
	logger, _ := routetest.NewLogger(t)
	principal := model.Principal{UserID: model.NewUserID(), SessionID: model.NewSessionID(), CredentialID: model.PrincipalCredentialID(model.NewID()), CredentialType: model.CredentialSessionAccess, AuthenticationMethod: "password", AuthenticationStrength: model.AuthenticationSingleFactor, ClientType: model.SessionClientCLI, AuthenticatedAt: time.Now()}
	unitID, userID, roleID := model.NewAcademicUnitID(), model.NewUserID(), model.NewRoleID()
	member := &model.AcademicUnitMember{ID: model.NewAcademicUnitMemberID(), AcademicUnitID: unitID, UserID: userID, CreatedAt: model.TimeFromMillis(100), UpdatedAt: model.TimeFromMillis(100), StartsAt: model.TimeFromMillis(100)}
	identity := store.UserIdentity{ID: userID, Username: "amina", DisplayName: "Amina Diallo"}
	members := &academicUnitMemberHTTPApplication{}
	httpAPI := newFocusedResourceAPI(t, logger, routetest.Authenticator{Principal: principal}, academicUnitMemberResource(members))
	list := func() map[string]json.RawMessage {
		t.Helper()
		request := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/api/v1/academic-units/"+unitID.String()+"/members", nil)
		request.Header.Set("Authorization", "Bearer credential")
		response := httptest.NewRecorder()
		httpAPI.ServeHTTP(response, request)
		require.Equal(t, http.StatusOK, response.Code, "status = %d: %s", response.Code, response.Body.String())
		var body struct {
			Items []map[string]json.RawMessage `json:"items"`
		}
		require.NoError(t, json.Unmarshal(response.Body.Bytes(), &body))
		require.Len(t, body.Items, 1)
		return body.Items[0]
	}

	members.pageResult = &academic.AcademicUnitMemberPage{Members: []*model.AcademicUnitMember{member}, Users: map[model.UserID]store.UserIdentity{userID: identity}}
	item := list()
	require.JSONEq(t, `{"display_name":"Amina Diallo","username":"amina"}`, string(item["user"]), "no email without Institution-wide user.view")
	_, rolesShown := item["roles_here"]
	require.False(t, rolesShown, "no roles_here without role_binding.view on the unit")
	_, moreShown := item["roles_here_more"]
	require.False(t, moreShown, "no roles_here_more without role_binding.view on the unit")

	identity.Email = "amina@example.edu"
	members.pageResult = &academic.AcademicUnitMemberPage{
		Members: []*model.AcademicUnitMember{member}, Users: map[model.UserID]store.UserIdentity{userID: identity},
		RolesHere: map[model.UserID]store.UserRoleBindingSummary{userID: {UserID: userID, More: 2, Bindings: []store.RoleBindingSummary{{
			RoleID: roleID, DisplayName: "Reviewer", ScopeType: model.RoleScopeAcademicUnit, ScopeID: unitID.String(),
		}}}},
	}
	item = list()
	require.JSONEq(t, `{"display_name":"Amina Diallo","username":"amina","email":"amina@example.edu"}`, string(item["user"]))
	require.JSONEq(t, `[{"role_id":"`+roleID.String()+`","display_name":"Reviewer"}]`, string(item["roles_here"]))
	require.JSONEq(t, `2`, string(item["roles_here_more"]))
}

func TestAcademicUnitMemberResourceRejectsInvalidQueryAndMissingPrincipal(t *testing.T) {
	t.Parallel()
	logger, _ := routetest.NewLogger(t)
	principal := model.Principal{UserID: model.NewUserID(), SessionID: model.NewSessionID(), CredentialID: model.PrincipalCredentialID(model.NewID()), CredentialType: model.CredentialSessionAccess, AuthenticationMethod: "password", AuthenticationStrength: model.AuthenticationSingleFactor, ClientType: model.SessionClientCLI, AuthenticatedAt: time.Now()}
	httpAPI := newFocusedResourceAPI(t, logger, routetest.Authenticator{Principal: principal}, academicUnitMemberResource(&academicUnitMemberHTTPApplication{}))

	invalid := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/api/v1/academic-units/"+model.NewID()+"/members?history=invalid", nil)
	invalid.Header.Set("Authorization", "Bearer credential")
	invalidResponse := httptest.NewRecorder()
	httpAPI.ServeHTTP(invalidResponse, invalid)
	require.Equal(t, http.StatusBadRequest, invalidResponse.Code, "invalid query status = %d: %s", invalidResponse.Code, invalidResponse.Body.String())

	unauthenticated := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/api/v1/academic-units/"+model.NewID()+"/members", nil)
	unauthenticatedResponse := httptest.NewRecorder()
	httpAPI.ServeHTTP(unauthenticatedResponse, unauthenticated)
	require.Equal(t, http.StatusUnauthorized, unauthenticatedResponse.Code, "unauthenticated status = %d: %s", unauthenticatedResponse.Code, unauthenticatedResponse.Body.String())
}

func (a *academicUnitMemberHTTPApplication) ListAcademicUnitMembersPage(_ context.Context, _ invocation.Invocation, query academic.ListAcademicUnitMembersPageQuery) (*academic.AcademicUnitMemberPage, error) {
	a.pageQuery = query
	if a.pageResult != nil {
		return a.pageResult, nil
	}
	return &academic.AcademicUnitMemberPage{}, nil
}

func TestAcademicUnitMemberOpenAPIDocument(t *testing.T) {
	t.Parallel()

	// Relationship history remains an explicit wire-level compatibility rule:
	// clients can select an instant or request the complete historical roster.
	document := readOpenAPIDocument(t)
	var listOperation struct {
		Parameters []struct {
			Name string `json:"name"`
			In   string `json:"in"`
		} `json:"parameters"`
	}
	require.NoError(t, json.Unmarshal(document.Paths["/api/v1/academic-units/{academic_unit_id}/members"]["get"], &listOperation))
	queryParameters := map[string]bool{}
	for _, parameter := range listOperation.Parameters {
		if parameter.In == "query" {
			queryParameters[parameter.Name] = true
		}
	}
	require.True(t, queryParameters["active_at"], "list query parameters = %#v, want active_at and history", queryParameters)
	require.True(t, queryParameters["history"], "list query parameters = %#v, want active_at and history", queryParameters)

	// This v1 relationship collection remains a legacy bare array.
	list := document.Components.Schemas["AcademicUnitMemberListResponse"]
	requireOpenAPIListEnvelope(t, list, "#/components/schemas/AcademicUnitMemberListItemResponse")
}
