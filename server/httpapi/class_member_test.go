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
)

func TestClassMemberHTTPUsesDTOAndRouteOwnedClass(t *testing.T) {
	t.Parallel()
	logger, _ := routetest.NewLogger(t)
	principal := academicCaller()
	world := newAcademicWorld()
	user := academicUser("amina", "Amina Diallo", "amina@example.edu")
	fixture := newAcademicFixture(t, academicOptions{
		Grants: []accesstest.Grant{inAcademicUnit(principal.UserID, world.unit.ID, model.ActionClassMembersManage)},
		Units:  world.units(),
	})
	fixture.hold(world)
	fixture.users.add(user)
	classID := world.class.ID
	httpAPI := newFocusedResourceAPI(t, logger, routetest.Authenticator{Principal: principal}, classMemberResource(fixture.service))
	request := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/api/v1/classes/"+classID.String()+"/members", strings.NewReader(`{"user_id":"`+user.ID.String()+`","start_at":"1970-01-01T00:00:00.1Z"}`))
	request.Header.Set("Authorization", "Bearer credential")
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	httpAPI.ServeHTTP(response, request)
	require.Equal(t, http.StatusCreated, response.Code, "status = %d: %s", response.Code, response.Body.String())
	require.Len(t, fixture.classMembers.enrollments, 1, "the enrollment reached the Class Membership Store")
	enrolled := fixture.classMembers.enrollments[0]
	require.Equal(t, classID, enrolled.Member.ClassID)
	require.Equal(t, user.ID, enrolled.Member.UserID)
	require.Equal(t, world.period.ID, enrolled.Member.AcademicPeriodID, "the membership joins the Class's Period")
	require.Equal(t, 1, fixture.mail.renders(), "one notice was rendered")
	require.NotNil(t, enrolled.Notice, "the enrollment notice is stored with the membership")
	require.Equal(t, model.MailTemplateAcademicClassEnrolled, enrolled.Notice.Delivery.TemplateKey)
	var body map[string]any
	require.NoError(t, json.Unmarshal(response.Body.Bytes(), &body))
	membership := body["membership"].(map[string]any)
	_, exposed := membership["revision"]
	require.False(t, exposed, "persistence revision leaked into DTO: %#v", membership)
}

func TestClassMemberResourceForwardsHistoryAndEndsMembership(t *testing.T) {
	t.Parallel()
	logger, _ := routetest.NewLogger(t)
	principal := academicCaller()
	world := newAcademicWorld()
	user := academicUser("amina", "Amina Diallo", "amina@example.edu")
	fixture := newAcademicFixture(t, academicOptions{
		Grants: []accesstest.Grant{inAcademicUnit(principal.UserID, world.unit.ID, model.ActionClassMembersView, model.ActionClassMembersManage)},
		Units:  world.units(),
	})
	fixture.hold(world)
	fixture.users.add(user)
	classID := world.class.ID
	member := &model.ClassMember{ClassID: classID, AcademicPeriodID: world.period.ID, UserID: user.ID, StartsAt: model.TimeFromMillis(100)}
	member.PrepareCreate(model.NewClassMemberID(), model.TimeFromMillis(100))
	fixture.classMembers.byID[member.ID] = member
	httpAPI := newFocusedResourceAPI(t, logger, routetest.Authenticator{Principal: principal}, classMemberResource(fixture.service))

	list := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/api/v1/classes/"+classID.String()+"/members?history=true", nil)
	list.Header.Set("Authorization", "Bearer credential")
	listed := httptest.NewRecorder()
	httpAPI.ServeHTTP(listed, list)
	require.Equal(t, http.StatusOK, listed.Code, "list status = %d: %s", listed.Code, listed.Body.String())
	page := fixture.classMembers.lastPage(t)
	require.Equal(t, classID, page.ClassID)
	require.True(t, page.ActiveAt.Equal(model.TimeFromMillis(0)), "a history listing reads every retained membership: %#v", page)
	require.JSONEq(t, `{"items":[],"total":0,"total_is_lower_bound":false}`, listed.Body.String(), "empty first page with its total")

	end := httptest.NewRequestWithContext(t.Context(), http.MethodDelete, "/api/v1/class-members/"+member.ID.String(), nil)
	end.Header.Set("Authorization", "Bearer credential")
	ended := httptest.NewRecorder()
	httpAPI.ServeHTTP(ended, end)
	require.Equal(t, http.StatusOK, ended.Code, "end status/cache = %d/%q: %s", ended.Code, ended.Header().Get("Cache-Control"), ended.Body.String())
	require.Len(t, fixture.classMembers.ends, 1, "the end reached the Class Membership Store")
	require.Equal(t, member.ID, fixture.classMembers.ends[0].ID)
	require.Equal(t, "no-store", ended.Header().Get("Cache-Control"), "end status/cache = %d/%q: %s", ended.Code, ended.Header().Get("Cache-Control"), ended.Body.String())
}

func TestClassMemberHistoryQueryIsForwarded(t *testing.T) {
	t.Parallel()
	request := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/api/v1/classes/class/members?history=true", nil)
	query, _, _, err := membershipPageQuery(request, model.Resource{Type: model.ResourceClass, ID: model.NewID()})
	require.NoError(t, err)
	require.NotNil(t, query.History)
	require.True(t, *query.History, "history")
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
