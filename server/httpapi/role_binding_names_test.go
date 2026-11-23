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
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/sudosylabs/proctor/server/httpapi/route"
	"github.com/sudosylabs/proctor/server/httpapi/route/routetest"
	"github.com/sudosylabs/proctor/server/model"
	"github.com/sudosylabs/proctor/server/store"
)

// TestRoleBindingListNamesRoleScopeAndGranter pins what a Binding reader
// learns beside the IDs: the role and scope names, and who granted it. The
// granter's username follows the reader's own user visibility.
func TestRoleBindingListNamesRoleScopeAndGranter(t *testing.T) {
	t.Parallel()
	logger, _ := routetest.NewLogger(t)
	role, unit := model.NewRoleID(), model.NewAcademicUnitID()
	granter := &model.User{ID: model.NewUserID(), Username: "priya", DisplayName: "Priya Raman"}
	removed := model.NewUserID()
	granted := func(by model.UserID) *model.RoleBinding {
		return &model.RoleBinding{ID: model.NewRoleBindingID(), UserID: model.NewUserID(), RoleID: role,
			ScopeType: model.RoleScopeAcademicUnit, ScopeID: unit.String(),
			CreatedAt: model.TimeFromMillis(100), StartsAt: model.TimeFromMillis(100),
			GrantedByUserID: by, GrantedVia: model.RoleBindingGrantedManually}
	}
	byPriya, byRemoved := granted(granter.ID), granted(removed)

	for _, test := range []struct {
		name         string
		userView     bool
		wantUsername string
	}{
		{name: "a reader who can see the granter", userView: true, wantUsername: "priya"},
		{name: "a reader who cannot see the granter"},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			principal := operatorTestPrincipal()
			actions := []model.Action{model.ActionRoleBindingView, model.ActionRoleView}
			if test.userView {
				actions = append(actions, model.ActionUserView)
			}
			fixture := newAdministrationFixture(t, institutionWide(principal.UserID, actions...))
			fixture.roles.add(&model.Role{ID: role, Name: "teacher"})
			fixture.users.add(granter)
			fixture.bindings.holders = []*model.RoleBinding{byPriya, byRemoved}
			fixture.audits.names = store.AuditNames{
				Roles:         map[model.RoleID]store.AuditRoleName{role: {Name: "teacher", DisplayName: "Teacher"}},
				AcademicUnits: map[model.AcademicUnitID]string{unit: "Mathematics"},
			}
			httpAPI := newFocusedResourceAPI(t, logger, routetest.Authenticator{Principal: principal}, roleBindingResource(fixture.service))
			response := serveOperatorRequest(t, httpAPI, http.MethodGet, "/api/v1/role-bindings?role_id="+role.String(), "", true)
			require.Equal(t, http.StatusOK, response.Code, response.Body.String())
			var page route.CountedList[roleBindingResponse]
			require.NoError(t, json.Unmarshal(response.Body.Bytes(), &page))
			require.Len(t, page.Items, 2)

			named := page.Items[0]
			require.Equal(t, roleBindingRoleResponse{ID: role.String(), DisplayName: "Teacher"}, named.Role)
			require.Equal(t, roleBindingScopeResponse{Type: model.RoleScopeAcademicUnit, ID: unit.String(), Name: "Mathematics"}, named.Scope)
			require.Equal(t, "manual", named.GrantedVia)
			require.NotNil(t, named.GrantedBy)
			require.Equal(t, granter.ID.String(), named.GrantedBy.ID)
			require.Equal(t, "Priya Raman", named.GrantedBy.DisplayName, "who granted is part of the record")
			require.Equal(t, test.wantUsername, named.GrantedBy.Username)
			if test.wantUsername == "" {
				require.Empty(t, named.GrantedBy.ProfilePictureURL)
			}

			require.NotNil(t, page.Items[1].GrantedBy)
			require.Equal(t, removed.String(), page.Items[1].GrantedBy.ID, "a removed account keeps its ID")
			require.Empty(t, page.Items[1].GrantedBy.DisplayName)
		})
	}
}
