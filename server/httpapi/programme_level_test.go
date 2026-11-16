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

func TestProgrammeLevelHTTPPreservesMissingProgrammeProblemField(t *testing.T) {
	t.Parallel()
	logger, _ := routetest.NewLogger(t)
	principal := academicCaller()
	// The caller may view every Level; the Programme it names does not exist.
	fixture := newAcademicFixture(t, academicOptions{Grants: []accesstest.Grant{institutionWide(principal.UserID, model.ActionProgrammeLevelView)}})
	httpAPI := newFocusedResourceAPI(
		t, logger, routetest.Authenticator{Principal: principal}, programmeLevelResource(fixture.service),
	)
	request := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/api/v1/programmes/"+model.NewID()+"/levels", nil)
	request.Header.Set("Authorization", "Bearer credential")
	response := httptest.NewRecorder()
	httpAPI.ServeHTTP(response, request)
	require.Equal(t, http.StatusNotFound, response.Code, "status = %d: %s", response.Code, response.Body.String())
	var problem route.Problem
	require.NoError(t, json.Unmarshal(response.Body.Bytes(), &problem))
	require.Equal(t, "resource.not_found", problem.Code)
	require.Equal(t, "programme", problem.Fields["resource"])
}

func TestProgrammeLevelHTTPMapsDTOWithoutPermissionPreflight(t *testing.T) {
	t.Parallel()
	logger, _ := routetest.NewLogger(t)
	principal := academicCaller()
	world := newAcademicWorld()
	fixture := newAcademicFixture(t, academicOptions{
		Grants: []accesstest.Grant{inAcademicUnit(principal.UserID, world.unit.ID, model.ActionProgrammeLevelManage)},
		Units:  world.units(),
	})
	fixture.hold(world)
	programmeID := world.programme.ID
	httpAPI := newFocusedResourceAPI(
		t, logger, routetest.Authenticator{Principal: principal}, programmeLevelResource(fixture.service),
	)
	request := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/api/v1/programmes/"+programmeID.String()+"/levels", strings.NewReader(`{"name":"year-1","display_name":"Year 1"}`))
	request.Header.Set("Authorization", "Bearer credential")
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	httpAPI.ServeHTTP(response, request)
	require.Equal(t, http.StatusCreated, response.Code, "status = %d: %s", response.Code, response.Body.String())
	require.Len(t, fixture.levels.created, 1, "the create reached the Programme Level Store")
	created := fixture.levels.created[0].Level
	require.Equal(t, programmeID, created.ProgrammeID)
	require.Equal(t, "year-1", created.Name)
	var body programmeLevelResponse
	require.NoError(t, json.Unmarshal(response.Body.Bytes(), &body))
	require.Equal(t, created.ID.String(), body.ID)
	require.Equal(t, programmeID.String(), body.ProgrammeID)
	invalid := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/api/v1/programmes/"+programmeID.String()+"/levels", strings.NewReader(`{"name":"year-1","display_name":"Year 1","unknown":true}`))
	invalid.Header.Set("Authorization", "Bearer credential")
	invalid.Header.Set("Content-Type", "application/json")
	invalidResponse := httptest.NewRecorder()
	httpAPI.ServeHTTP(invalidResponse, invalid)
	require.Equal(t, http.StatusBadRequest, invalidResponse.Code, "invalid status = %d: %s", invalidResponse.Code, invalidResponse.Body.String())
	require.Len(t, fixture.levels.created, 1, "a rejected create reached the Programme Level Store")
}

func TestProgrammeLevelPatchOptionalWireStates(t *testing.T) {
	t.Parallel()
	var body updateProgrammeLevelRequest
	require.NoError(t, json.Unmarshal([]byte(`{"name":null,"display_name":""}`), &body))
	require.Nil(t, body.Name.ValuePointer(), "explicit null must preserve v1 no-op semantics")
	value := body.DisplayName.ValuePointer()
	require.NotNil(t, value)
	require.Empty(t, *value)
}

func TestProgrammeLevelOpenAPIDocument(t *testing.T) {
	t.Parallel()

	document := readOpenAPIDocument(t)
	var listOperation openAPIOperation
	path := model.APIURLSuffix + "/programmes/{programme_id}/levels"
	require.NoError(t, json.Unmarshal(document.Paths[path]["get"], &listOperation))
	require.Equal(t, []string{"cursor", "limit", "q"}, academicStructureQueryParameterNames(listOperation.Parameters), "Programme Level list query parameters")
	list := document.Components.Schemas["ProgrammeLevelListResponse"]
	requireOpenAPIListEnvelope(t, list, "#/components/schemas/ProgrammeLevelResponse")
	require.Equal(t, "#/components/headers/NoStore", document.Components.Responses["ProgrammeLevelArchived"].Headers["Cache-Control"].Ref)
}
