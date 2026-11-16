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
	"github.com/sudosylabs/proctor/server/httpapi/route"
	"github.com/sudosylabs/proctor/server/httpapi/route/routetest"
	"github.com/sudosylabs/proctor/server/model"
)

func TestProgrammeHTTPMapsDTOWithoutPermissionPreflight(t *testing.T) {
	t.Parallel()
	logger, _ := routetest.NewLogger(t)
	principal := academicCaller()
	unitID := model.NewAcademicUnitID()
	fixture := newAcademicFixture(t, academicOptions{Grants: []accesstest.Grant{inAcademicUnit(principal.UserID, unitID, model.ActionProgrammeManage)}})
	httpAPI := newFocusedResourceAPI(
		t, logger, routetest.Authenticator{Principal: principal}, programmeResource(fixture.service),
	)

	request := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/api/v1/academic-units/"+unitID.String()+"/programmes", strings.NewReader(`{"name":"computer-science","display_name":"Computer Science"}`))
	request.Header.Set("Authorization", "Bearer credential")
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	httpAPI.ServeHTTP(response, request)
	require.Equal(t, http.StatusCreated, response.Code, "status = %d: %s", response.Code, response.Body.String())
	require.Len(t, fixture.programmes.created, 1, "the create reached the Programme Store")
	created := fixture.programmes.created[0].Programme
	require.Equal(t, unitID, created.AcademicUnitID)
	require.Equal(t, "computer-science", created.Name)
	require.Equal(t, "Computer Science", created.DisplayName)
	var body programmeResponse
	require.NoError(t, json.Unmarshal(response.Body.Bytes(), &body))
	require.Equal(t, created.ID.String(), body.ID)
	require.Equal(t, unitID.String(), body.AcademicUnitID)
}

func TestProgrammeResourceRejectsInvalidQueryLimit(t *testing.T) {
	t.Parallel()

	logger, _ := routetest.NewLogger(t)
	principal := academicCaller()
	unitID := model.NewAcademicUnitID()
	fixture := newAcademicFixture(t, academicOptions{Grants: []accesstest.Grant{inAcademicUnit(principal.UserID, unitID, model.ActionProgrammeView)}})
	httpAPI := newFocusedResourceAPI(
		t, logger, routetest.Authenticator{Principal: principal}, programmeResource(fixture.service),
	)
	request := httptest.NewRequestWithContext(t.Context(),
		http.MethodGet,
		"/api/v1/academic-units/"+unitID.String()+"/programmes?limit=invalid",
		nil,
	)
	request.Header.Set("Authorization", "Bearer credential")
	response := httptest.NewRecorder()
	httpAPI.ServeHTTP(response, request)
	require.Equal(t, http.StatusBadRequest, response.Code, "status = %d: %s", response.Code, response.Body.String())
	var problem route.Problem
	require.NoError(t, json.Unmarshal(response.Body.Bytes(), &problem))
	require.Equal(t, "request.invalid", problem.Code, "problem code")
	require.Empty(t, fixture.programmes.page.listOptions, "an invalid limit reached the Programme Store")
}

func TestProgrammePatchOptionalWireStates(t *testing.T) {
	t.Parallel()
	var body updateProgrammeRequest
	require.NoError(t, json.Unmarshal([]byte(`{"name":null,"display_name":""}`), &body))
	require.Nil(t, body.Name.ValuePointer(), "explicit null must preserve v1 no-op semantics")
	value := body.DisplayName.ValuePointer()
	require.NotNil(t, value)
	require.Empty(t, *value)
}

func TestProgrammeOpenAPIDocument(t *testing.T) {
	t.Parallel()

	document := readOpenAPIDocument(t)
	var listOperation openAPIOperation
	path := model.APIURLSuffix + "/academic-units/{academic_unit_id}/programmes"
	require.NoError(t, json.Unmarshal(document.Paths[path]["get"], &listOperation))
	require.Equal(t, []string{"cursor", "limit", "q"}, academicStructureQueryParameterNames(listOperation.Parameters), "Programme list query parameters")
	list := document.Components.Schemas["ProgrammeListResponse"]
	requireOpenAPIListEnvelope(t, list, "#/components/schemas/ProgrammeResponse")
	require.Equal(t, "#/components/headers/NoStore", document.Components.Responses["ProgrammeArchived"].Headers["Cache-Control"].Ref)
}
