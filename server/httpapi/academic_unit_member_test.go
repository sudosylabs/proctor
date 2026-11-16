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
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/sudosylabs/proctor/server/app/access/accesstest"
	"github.com/sudosylabs/proctor/server/httpapi/route/routetest"
	"github.com/sudosylabs/proctor/server/model"
	"github.com/sudosylabs/proctor/server/store"
)

func TestAcademicUnitMemberHistoryQueryIncludesEndedMemberships(t *testing.T) {
	t.Parallel()

	request := httptest.NewRequestWithContext(t.Context(), "GET", "/api/v1/academic-units/unit/members?history=true", nil)
	query, _, _, err := membershipPageQuery(request, model.Resource{Type: model.ResourceAcademicUnit, ID: model.NewID()})
	require.NoError(t, err)
	require.NotNil(t, query.History)
	require.True(t, *query.History, "history")
}

func TestAcademicUnitMemberHistoryQueryRejectsInvalidBoolean(t *testing.T) {
	t.Parallel()

	request := httptest.NewRequestWithContext(t.Context(), "GET", "/api/v1/academic-units/unit/members?history=invalid", nil)
	_, _, _, err := membershipPageQuery(request, model.Resource{Type: model.ResourceAcademicUnit, ID: model.NewID()})
	require.Error(t, err, "invalid history query was accepted")
}

func TestAcademicUnitMemberFalseHistoryQueryUsesActiveAt(t *testing.T) {
	t.Parallel()

	request := httptest.NewRequestWithContext(t.Context(), "GET", "/api/v1/academic-units/unit/members?history=false&active_at=1970-01-01T00:00:00.123Z", nil)
	query, _, _, err := membershipPageQuery(request, model.Resource{Type: model.ResourceAcademicUnit, ID: model.NewID()})
	require.NoError(t, err)
	require.NotNil(t, query.ActiveAt)
	require.Equal(t, int64(123), *query.ActiveAt, "active at")
}

func TestAcademicUnitMemberResourceRunsThroughRoutingKernel(t *testing.T) {
	t.Parallel()
	logger, _ := routetest.NewLogger(t)
	principal := academicCaller()
	unitID := model.NewAcademicUnitID()
	member := academicUser("amina", "Amina Diallo", "amina@example.edu")
	fixture := newAcademicFixture(t, academicOptions{Grants: []accesstest.Grant{
		inAcademicUnit(principal.UserID, unitID, model.ActionAcademicUnitMembersManage, model.ActionAcademicUnitMembersView),
	}})
	fixture.users.add(member)
	httpAPI := newFocusedResourceAPI(t, logger, routetest.Authenticator{Principal: principal}, academicUnitMemberResource(fixture.service))

	create := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/api/v1/academic-units/"+unitID.String()+"/members", strings.NewReader(`{"user_id":"`+member.ID.String()+`","start_at":"1970-01-01T00:00:00.1Z"}`))
	create.Header.Set("Authorization", "Bearer credential")
	create.Header.Set("Content-Type", "application/json")
	created := httptest.NewRecorder()
	httpAPI.ServeHTTP(created, create)
	require.Equal(t, http.StatusCreated, created.Code, "create status = %d: %s", created.Code, created.Body.String())
	require.Len(t, fixture.unitMembers.created, 1, "the create reached the membership Store")
	stored := fixture.unitMembers.created[0]
	require.Equal(t, unitID, stored.Member.AcademicUnitID)
	require.Equal(t, member.ID, stored.Member.UserID)
	require.Equal(t, model.TimeFromMillis(100), stored.Member.StartsAt)
	require.NotNil(t, stored.Notice, "the assignment notice is stored with the membership")
	require.Equal(t, []model.MailTemplateKey{model.MailTemplateAcademicUnitAssigned}, fixture.mail.templates())
	var body map[string]any
	require.NoError(t, json.Unmarshal(created.Body.Bytes(), &body))
	_, exposed := body["revision"]
	require.False(t, exposed, "persistence revision leaked into DTO: %#v", body)

	list := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/api/v1/academic-units/"+unitID.String()+"/members?history=true", nil)
	list.Header.Set("Authorization", "Bearer credential")
	listed := httptest.NewRecorder()
	httpAPI.ServeHTTP(listed, list)
	require.Equal(t, http.StatusOK, listed.Code, "list status = %d: %s", listed.Code, listed.Body.String())
	page := fixture.unitMembers.lastPage(t)
	require.Equal(t, unitID, page.AcademicUnitID)
	require.True(t, page.ActiveAt.Equal(model.TimeFromMillis(0)), "a history listing reads every retained membership: %#v", page)
	require.JSONEq(t, `{"items":[],"total":0,"total_is_lower_bound":false}`, listed.Body.String(), "empty first page with its total")
}

func TestAcademicUnitMemberListNamesMembersAndTheirRolesHere(t *testing.T) {
	t.Parallel()
	logger, _ := routetest.NewLogger(t)
	unitID, roleID := model.NewAcademicUnitID(), model.NewRoleID()
	user := academicUser("amina", "Amina Diallo", "amina@example.edu")
	member := &model.AcademicUnitMember{ID: model.NewAcademicUnitMemberID(), AcademicUnitID: unitID, UserID: user.ID, CreatedAt: model.TimeFromMillis(100), UpdatedAt: model.TimeFromMillis(100), StartsAt: model.TimeFromMillis(100)}
	// list lists the unit's members as a caller holding academic_unit.members.view
	// and, when described is set, Institution-wide user.view and
	// role_binding.view on the unit.
	list := func(described bool) (map[string]json.RawMessage, *academicFixture) {
		t.Helper()
		principal := academicCaller()
		grants := []accesstest.Grant{inAcademicUnit(principal.UserID, unitID, model.ActionAcademicUnitMembersView)}
		if described {
			grants = append(grants, institutionWide(principal.UserID, model.ActionUserView),
				inAcademicUnit(principal.UserID, unitID, model.ActionRoleBindingView))
		}
		fixture := newAcademicFixture(t, academicOptions{Grants: grants})
		fixture.users.add(user)
		fixture.unitMembers.page = store.AcademicUnitMemberPage{Members: []*model.AcademicUnitMember{member}}
		fixture.bindings.summaries = []store.UserRoleBindingSummary{{UserID: user.ID, More: 2, Bindings: []store.RoleBindingSummary{{
			RoleID: roleID, DisplayName: "Reviewer", ScopeType: model.RoleScopeAcademicUnit, ScopeID: unitID.String(),
		}}}}
		httpAPI := newFocusedResourceAPI(t, logger, routetest.Authenticator{Principal: principal}, academicUnitMemberResource(fixture.service))
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
		return body.Items[0], fixture
	}

	item, fixture := list(false)
	require.JSONEq(t, `{"display_name":"Amina Diallo","username":"amina"}`, string(item["user"]), "no email without Institution-wide user.view")
	_, rolesShown := item["roles_here"]
	require.False(t, rolesShown, "no roles_here without role_binding.view on the unit")
	_, moreShown := item["roles_here_more"]
	require.False(t, moreShown, "no roles_here_more without role_binding.view on the unit")
	require.Empty(t, fixture.bindings.options, "the Role Bindings are not read without role_binding.view")

	item, fixture = list(true)
	require.JSONEq(t, `{"display_name":"Amina Diallo","username":"amina","email":"amina@example.edu"}`, string(item["user"]))
	require.JSONEq(t, `[{"role_id":"`+roleID.String()+`","display_name":"Reviewer"}]`, string(item["roles_here"]))
	require.JSONEq(t, `2`, string(item["roles_here_more"]))
	require.Len(t, fixture.bindings.options, 1)
	require.Equal(t, unitID.String(), fixture.bindings.options[0].ScopeID, "only bindings scoped to the unit")
}

func TestAcademicUnitMemberResourceRejectsInvalidQueryAndMissingPrincipal(t *testing.T) {
	t.Parallel()
	logger, _ := routetest.NewLogger(t)
	principal := academicCaller()
	unitID := model.NewAcademicUnitID()
	fixture := newAcademicFixture(t, academicOptions{Grants: []accesstest.Grant{inAcademicUnit(principal.UserID, unitID, model.ActionAcademicUnitMembersView)}})
	httpAPI := newFocusedResourceAPI(t, logger, routetest.Authenticator{Principal: principal}, academicUnitMemberResource(fixture.service))

	invalid := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/api/v1/academic-units/"+unitID.String()+"/members?history=invalid", nil)
	invalid.Header.Set("Authorization", "Bearer credential")
	invalidResponse := httptest.NewRecorder()
	httpAPI.ServeHTTP(invalidResponse, invalid)
	require.Equal(t, http.StatusBadRequest, invalidResponse.Code, "invalid query status = %d: %s", invalidResponse.Code, invalidResponse.Body.String())

	unauthenticated := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/api/v1/academic-units/"+unitID.String()+"/members", nil)
	unauthenticatedResponse := httptest.NewRecorder()
	httpAPI.ServeHTTP(unauthenticatedResponse, unauthenticated)
	require.Equal(t, http.StatusUnauthorized, unauthenticatedResponse.Code, "unauthenticated status = %d: %s", unauthenticatedResponse.Code, unauthenticatedResponse.Body.String())
	require.Empty(t, fixture.unitMembers.pageOptions, "a rejected request reached the membership Store")
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
