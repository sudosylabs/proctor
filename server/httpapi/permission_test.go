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

	"github.com/sudosylabs/proctor/server/httpapi/route/routetest"
	"github.com/sudosylabs/proctor/server/model"
)

// TestPermissionCatalogueDescribesEveryGrantableAction pins the role editor's
// catalogue: titled grantable actions only, the scopes a binding can grant
// each at, and overrides pointing at what they widen.
func TestPermissionCatalogueDescribesEveryGrantableAction(t *testing.T) {
	t.Parallel()
	logger, _ := routetest.NewLogger(t)
	principal := operatorTestPrincipal()
	fixture := newAdministrationFixture(t, institutionWide(principal.UserID, model.ActionRoleView))
	httpAPI := newFocusedResourceAPI(t, logger, routetest.Authenticator{Principal: principal}, roleResource(fixture.service))

	response := serveOperatorRequest(t, httpAPI, http.MethodGet, "/api/v1/permissions", "", true)
	require.Equal(t, http.StatusOK, response.Code, response.Body.String())
	var catalogue permissionCatalogueResponse
	require.NoError(t, json.Unmarshal(response.Body.Bytes(), &catalogue))

	require.Len(t, catalogue.Permissions, len(model.GrantableActions()))
	byName := map[string]permissionResponse{}
	for _, permission := range catalogue.Permissions {
		require.NotEqual(t, permission.Name, permission.Title, "%s has no title", permission.Name)
		require.NotEmpty(t, permission.Description, "%s has no description", permission.Name)
		byName[permission.Name] = permission
	}
	require.Equal(t, permissionResponse{
		Name: "exam.manage.override", Title: "Manage any exam",
		Description: byName["exam.manage.override"].Description, Area: model.ActionAreaExams,
		ScopeTypes: []model.RoleScopeType{model.RoleScopeInstitution, model.RoleScopeAcademicUnit},
		OverrideOf: "exam.manage",
	}, byName["exam.manage.override"])
	require.Equal(t, []model.RoleScopeType{model.RoleScopeInstitution, model.RoleScopeAcademicUnit, model.RoleScopeClass},
		byName["class.view"].ScopeTypes)
	require.NotContains(t, byName, string(model.ActionInstallationBootstrap), "protected actions stay out")

	require.Len(t, catalogue.Areas, len(model.ActionAreas()))
	require.Equal(t, permissionAreaResponse{Area: model.ActionAreaExams, Title: "Exams"}, catalogue.Areas[0])

	denied := newAdministrationFixture(t)
	deniedAPI := newFocusedResourceAPI(t, logger, routetest.Authenticator{Principal: principal}, roleResource(denied.service))
	require.Equal(t, http.StatusForbidden,
		serveOperatorRequest(t, deniedAPI, http.MethodGet, "/api/v1/permissions", "", true).Code)
}
