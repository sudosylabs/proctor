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
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/sudosylabs/proctor/server/app/access/accesstest"
	"github.com/sudosylabs/proctor/server/httpapi/route/routetest"
	"github.com/sudosylabs/proctor/server/model"
	"github.com/sudosylabs/proctor/server/store"
)

// TestUserMembershipsListOnlyWhatTheCallerMayViewMembersOf pins U2: the
// caller must see the User, the Store is asked only for memberships inside
// the caller's member-view scopes, so a scoped viewer can't widen what it
// sees, and a page continues from its cursor.
func TestUserMembershipsListOnlyWhatTheCallerMayViewMembersOf(t *testing.T) {
	t.Parallel()
	logger, _ := routetest.NewLogger(t)
	world, other := newAcademicWorld(), newAcademicWorld()
	member := &model.User{ID: model.NewUserID(), Username: "member", DisplayName: "Member"}
	start := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	unitMembership := store.UserMembership{Kind: store.UserMembershipAcademicUnit, ID: model.NewID(), StartAt: start,
		AcademicUnitID: world.unit.ID, AcademicUnitName: "Computing"}
	enrolment := store.UserMembership{Kind: store.UserMembershipClass, ID: model.NewID(), StartAt: start,
		EndAt:   model.OptionalTimeFrom(start.Add(300 * 24 * time.Hour)),
		ClassID: world.class.ID, ClassName: "CS2 A", ProgrammeLevelID: world.level.ID, ProgrammeLevelName: "Year 2",
		ProgrammeID: world.programme.ID, ProgrammeName: "BSc Computing", AcademicPeriodID: world.period.ID, AcademicPeriodName: "2026/27"}
	serve := func(t *testing.T, path string, grants ...accesstest.Grant) (*httptest.ResponseRecorder, *academicUserRecords) {
		t.Helper()
		principal := directoryCaller()
		for index := range grants {
			grants[index].UserID = principal.UserID
		}
		fixture := newAcademicFixture(t, academicOptions{Grants: grants, Units: []*model.AcademicUnit{world.unit, other.unit}})
		fixture.hold(world)
		fixture.hold(other)
		fixture.users.add(member)
		fixture.users.memberships = []store.UserMembership{unitMembership, enrolment}
		httpAPI := newFocusedResourceAPI(t, logger, routetest.Authenticator{Principal: principal}, userMembershipResource(fixture.service))
		request := httptest.NewRequestWithContext(t.Context(), http.MethodGet, path, nil)
		request.Header.Set("Authorization", "Bearer credential")
		response := httptest.NewRecorder()
		httpAPI.ServeHTTP(response, request)
		return response, fixture.users
	}
	path := "/api/v1/users/" + member.ID.String() + "/memberships"
	classID := other.class.ID
	grants := []accesstest.Grant{
		institutionWide("", model.ActionUserView),
		inAcademicUnit("", world.unit.ID, model.ActionAcademicUnitMembersView),
		{ScopeType: model.RoleScopeClass, ScopeID: classID.String(), Actions: []model.Action{model.ActionClassMembersView}},
	}

	response, records := serve(t, path+"?limit=1", grants...)
	require.Equal(t, http.StatusOK, response.Code, response.Body.String())
	require.Len(t, records.membershipReads, 1)
	asked := records.membershipReads[0]
	require.Equal(t, member.ID, asked.UserID)
	require.Equal(t, store.ClassVisibilityScope{AcademicUnitRootIDs: []model.AcademicUnitID{world.unit.ID}}, asked.UnitVisibility)
	require.Equal(t, store.ClassVisibilityScope{ClassIDs: []model.ClassID{classID}}, asked.ClassVisibility,
		"only the caller's own member-view scopes reach the Store")
	var page struct {
		Items      []json.RawMessage `json:"items"`
		NextCursor string            `json:"next_cursor"`
	}
	require.NoError(t, json.Unmarshal(response.Body.Bytes(), &page))
	require.Len(t, page.Items, 1)
	require.JSONEq(t, `{"kind": "academic_unit", "id": "`+unitMembership.ID+`", "start_at": "2026-09-01T00:00:00Z",
		"academic_unit": {"id": "`+world.unit.ID.String()+`", "display_name": "Computing"}}`, string(page.Items[0]))
	require.NotEmpty(t, page.NextCursor)

	response, records = serve(t, path+"?cursor="+page.NextCursor, grants...)
	require.Equal(t, http.StatusOK, response.Code, response.Body.String())
	asked = records.membershipReads[0]
	require.Equal(t, store.UserMembershipAcademicUnit, asked.AfterKind)
	require.Equal(t, unitMembership.ID, asked.AfterID)
	require.True(t, start.Equal(asked.AfterStartAt))
	require.NoError(t, json.Unmarshal(response.Body.Bytes(), &page))
	require.JSONEq(t, `{"kind": "class", "id": "`+enrolment.ID+`", "start_at": "2026-09-01T00:00:00Z", "end_at": "2027-06-28T00:00:00Z",
		"class": {"id": "`+world.class.ID.String()+`", "display_name": "CS2 A"},
		"programme_level": {"id": "`+world.level.ID.String()+`", "display_name": "Year 2"},
		"programme": {"id": "`+world.programme.ID.String()+`", "display_name": "BSc Computing"},
		"academic_period": {"id": "`+world.period.ID.String()+`", "display_name": "2026/27"}}`, string(page.Items[1]))

	response, records = serve(t, path, inAcademicUnit("", world.unit.ID, model.ActionAcademicUnitMembersView))
	assertHTTPProblem(t, response, http.StatusForbidden, "authorization.denied")
	require.Empty(t, records.membershipReads, "a User the caller can't see is refused as their profile is, and nothing is listed")

	response, _ = serve(t, path+"?cursor=bm90LWEtY3Vyc29y", grants...)
	assertHTTPProblem(t, response, http.StatusBadRequest, "request.invalid")
}
