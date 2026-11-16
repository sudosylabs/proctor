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
	"sort"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/sudosylabs/proctor/server/app/access/accesstest"
	"github.com/sudosylabs/proctor/server/httpapi/route"
	"github.com/sudosylabs/proctor/server/httpapi/route/routetest"
	"github.com/sudosylabs/proctor/server/model"
)

func TestAcademicPeriodHTTPMapsDTOAndNormalizesRFC3339Bounds(t *testing.T) {
	t.Parallel()
	logger, _ := routetest.NewLogger(t)
	principal := academicCaller()
	owner := model.NewAcademicUnitAcademicPeriodOwner(model.NewAcademicUnitID())
	fixture := newAcademicFixture(t, academicOptions{Grants: []accesstest.Grant{inAcademicUnit(principal.UserID, owner.AcademicUnitID, model.ActionAcademicPeriodManage)}})
	httpAPI := newFocusedResourceAPI(
		t, logger, routetest.Authenticator{Principal: principal}, academicPeriodResource(fixture.service),
	)
	request := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/api/v1/academic-periods", strings.NewReader(`{"owner_type":"academic_unit","owner_id":"`+owner.ID()+`","name":"2026-2027","display_name":"2026-2027","start_at":"1970-01-01T00:00:00.1Z","end_at":"1970-01-01T01:00:00.2+01:00"}`))
	request.Header.Set("Authorization", "Bearer credential")
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	httpAPI.ServeHTTP(response, request)
	require.Equal(t, http.StatusCreated, response.Code, "status = %d: %s", response.Code, response.Body.String())
	require.Len(t, fixture.periods.created, 1, "the create reached the Academic Period Store")
	created := fixture.periods.created[0].Period
	require.Equal(t, owner, created.Owner)
	require.Equal(t, "2026-2027", created.Name)
	require.True(t, model.TimeFromMillis(100).Equal(created.StartsAt), "start = %v", created.StartsAt)
	require.True(t, model.TimeFromMillis(200).Equal(created.EndsAt), "end = %v", created.EndsAt)
	var body academicPeriodResponse
	require.NoError(t, json.Unmarshal(response.Body.Bytes(), &body))
	require.Equal(t, created.ID.String(), body.ID)
	require.Equal(t, string(model.ResourceAcademicUnit), body.OwnerType)
	require.Equal(t, owner.ID(), body.OwnerID)
}

func TestAcademicPeriodResourceRejectsInvalidQueryLimit(t *testing.T) {
	t.Parallel()

	logger, _ := routetest.NewLogger(t)
	principal := academicCaller()
	fixture := newAcademicFixture(t, academicOptions{Grants: []accesstest.Grant{institutionWide(principal.UserID, model.ActionAcademicPeriodView)}})
	httpAPI := newFocusedResourceAPI(
		t, logger, routetest.Authenticator{Principal: principal}, academicPeriodResource(fixture.service),
	)
	request := httptest.NewRequestWithContext(t.Context(),
		http.MethodGet, "/api/v1/academic-periods?limit=0", nil,
	)
	request.Header.Set("Authorization", "Bearer credential")
	response := httptest.NewRecorder()
	httpAPI.ServeHTTP(response, request)
	require.Equal(t, http.StatusBadRequest, response.Code, "status = %d: %s", response.Code, response.Body.String())
	var problem route.Problem
	require.NoError(t, json.Unmarshal(response.Body.Bytes(), &problem))
	require.Equal(t, "request.invalid", problem.Code, "problem code")
	require.Zero(t, fixture.periods.lists, "an invalid limit reached the Academic Period Store")
}

func TestAcademicPeriodPatchOptionalWireStates(t *testing.T) {
	t.Parallel()
	var body updateAcademicPeriodRequest
	require.NoError(t, json.Unmarshal([]byte(`{"start_at":null,"end_at":"2026-01-02T03:04:05Z"}`), &body))
	require.Nil(t, body.StartAt.ValuePointer(), "explicit null must preserve v1 no-op semantics")
	end := body.EndAt.ValuePointer()
	require.NotNil(t, end)
	require.Equal(t, "2026-01-02T03:04:05Z", *end)
}

func TestAcademicPeriodOpenAPIDocument(t *testing.T) {
	t.Parallel()

	document := readOpenAPIDocument(t)
	var listOperation openAPIOperation
	require.NoError(t, json.Unmarshal(document.Paths[model.APIURLSuffix+"/academic-periods"]["get"], &listOperation))
	require.Equal(t, []string{"cursor", "limit", "q"}, academicStructureQueryParameterNames(listOperation.Parameters), "Academic Period list query parameters")
	list := document.Components.Schemas["AcademicPeriodListResponse"]
	requireOpenAPIListEnvelope(t, list, "#/components/schemas/AcademicPeriodResponse")
	require.Equal(t, "#/components/headers/NoStore", document.Components.Responses["AcademicPeriodArchived"].Headers["Cache-Control"].Ref)
}

func academicStructureQueryParameterNames(parameters []openAPIParameter) []string {
	names := make([]string, 0, len(parameters))
	for _, parameter := range parameters {
		switch {
		case parameter.In == "query":
			names = append(names, parameter.Name)
		case strings.HasSuffix(parameter.Ref, "/PageLimit"):
			names = append(names, "limit")
		case strings.HasSuffix(parameter.Ref, "/PageCursor"):
			names = append(names, "cursor")
		}
	}
	sort.Strings(names)
	return names
}
