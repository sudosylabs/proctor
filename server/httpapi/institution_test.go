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

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/sudosylabs/proctor/server/app/access/accesstest"
	"github.com/sudosylabs/proctor/server/httpapi/route"
	"github.com/sudosylabs/proctor/server/httpapi/route/routetest"
	"github.com/sudosylabs/proctor/server/model"
)

func TestInstitutionHTTPMapsDTOsWithoutPermissionPreflight(t *testing.T) {
	t.Parallel()

	logger, _ := routetest.NewLogger(t)
	principal := academicCaller()
	fixture := newAcademicFixture(t, academicOptions{Grants: []accesstest.Grant{institutionWide(principal.UserID, model.ActionInstitutionManage)}})
	institution := &model.Institution{
		ID: fixture.access.Institution.ID, CreatedAt: model.TimeFromMillis(10), UpdatedAt: model.TimeFromMillis(20),
		Name: "northbridge", DisplayName: "Northbridge University",
		Description: "A university", ExamCapacity: model.DefaultExamCapacityPolicy(),
	}
	fixture.institution.current = institution
	httpAPI := newFocusedResourceAPI(t, logger, routetest.Authenticator{Principal: principal}, institutionResource(fixture.service))

	getRequest := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/api/v1/institution", nil)
	getRequest.Header.Set("Authorization", "Bearer test-credential")
	getResponse := httptest.NewRecorder()
	httpAPI.ServeHTTP(getResponse, getRequest)
	require.Equal(t, http.StatusOK, getResponse.Code, "get status = %d: %s", getResponse.Code, getResponse.Body.String())
	var got institutionResponse
	require.NoError(t, json.Unmarshal(getResponse.Body.Bytes(), &got))
	require.Equal(t, institution.ID.String(), got.ID)
	require.Equal(t, institution.Name, got.Name)
	require.Equal(t, examCapacityPolicyResponseFromModel(model.DefaultExamCapacityPolicy()), got.ExamCapacity)

	patchRequest := httptest.NewRequestWithContext(t.Context(),
		http.MethodPatch, "/api/v1/institution",
		strings.NewReader(`{"display_name":"Northbridge","exam_capacity":{"resource_maximum_count":25,"resource_maximum_bytes":20971520,"workspace_maximum_entries":750,"workspace_maximum_file_bytes":20971520,"workspace_maximum_total_bytes":104857600}}`),
	)
	patchRequest.Header.Set("Authorization", "Bearer test-credential")
	patchRequest.Header.Set("Content-Type", "application/json")
	patchResponse := httptest.NewRecorder()
	httpAPI.ServeHTTP(patchResponse, patchRequest)
	require.Equal(t, http.StatusOK, patchResponse.Code, "patch status = %d: %s", patchResponse.Code, patchResponse.Body.String())
	require.Len(t, fixture.institution.updates, 1, "the PATCH reached the Institution Store")
	updated := fixture.institution.updates[0].Institution
	require.Equal(t, "Northbridge", updated.DisplayName)
	require.Equal(t, institution.Name, updated.Name, "an omitted field keeps its value")
	wantCapacity := model.ExamCapacityPolicy{ResourceMaximumCount: 25, ResourceMaximumBytes: 20 << 20,
		WorkspaceMaximumEntries: 750, WorkspaceMaximumFileBytes: 20 << 20, WorkspaceMaximumTotalBytes: 100 << 20}
	require.Equal(t, wantCapacity, updated.ExamCapacity)
	require.Equal(t, string(model.ActionInstitutionManage), fixture.onlyAttempt(t).Action)
}

func TestInstitutionResourceRejectsUnknownPatchFields(t *testing.T) {
	t.Parallel()

	logger, _ := routetest.NewLogger(t)
	principal := academicCaller()
	fixture := newAcademicFixture(t, academicOptions{Grants: []accesstest.Grant{institutionWide(principal.UserID, model.ActionInstitutionManage)}})
	httpAPI := newFocusedResourceAPI(t, logger, routetest.Authenticator{Principal: principal}, institutionResource(fixture.service))
	request := httptest.NewRequestWithContext(t.Context(),
		http.MethodPatch, "/api/v1/institution",
		strings.NewReader(`{"display_name":"Northbridge","unknown":true}`),
	)
	request.Header.Set("Authorization", "Bearer test-credential")
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	httpAPI.ServeHTTP(response, request)
	require.Equal(t, http.StatusBadRequest, response.Code, "status = %d: %s", response.Code, response.Body.String())
	var problem route.Problem
	require.NoError(t, json.Unmarshal(response.Body.Bytes(), &problem))
	require.Equal(t, "request.invalid", problem.Code, "problem code")
	require.Empty(t, fixture.institution.updates, "a rejected PATCH reached the Institution Store")
}

func TestOptionalDistinguishesInstitutionPatchWireStates(t *testing.T) {
	t.Parallel()

	var omitted updateInstitutionRequest
	require.NoError(t, json.Unmarshal([]byte(`{}`), &omitted))
	require.False(t, omitted.Name.IsSet())
	require.False(t, omitted.Name.IsNull())
	require.Nil(t, omitted.Name.ValuePointer())

	var explicitNull updateInstitutionRequest
	require.NoError(t, json.Unmarshal([]byte(`{"name":null}`), &explicitNull))
	require.True(t, explicitNull.Name.IsSet())
	require.True(t, explicitNull.Name.IsNull())
	require.Nil(t, explicitNull.Name.ValuePointer())

	var zeroValue updateInstitutionRequest
	require.NoError(t, json.Unmarshal([]byte(`{"name":""}`), &zeroValue))
	value := zeroValue.Name.ValuePointer()
	require.True(t, zeroValue.Name.IsSet(), "zero name = %#v, value", zeroValue.Name)
	require.False(t, zeroValue.Name.IsNull(), "zero name = %#v, value", zeroValue.Name)
	require.NotNil(t, value, "zero name = %#v, value", zeroValue.Name)
	require.Empty(t, *value, "zero name = %#v, value", zeroValue.Name)

	var nullCapacity updateInstitutionRequest
	require.NoError(t, json.Unmarshal([]byte(`{"exam_capacity":null}`), &nullCapacity))
	require.True(t, nullCapacity.ExamCapacity.IsSet())
	require.True(t, nullCapacity.ExamCapacity.IsNull())
	require.Nil(t, nullCapacity.ExamCapacity.ValuePointer())
}

func TestInstitutionOpenAPIDocument(t *testing.T) {
	t.Parallel()

	document := readOpenAPIDocument(t)

	patchSchema := document.Components.Schemas["UpdateInstitutionRequest"]
	for _, propertyName := range []string{"name", "display_name", "description"} {
		var property struct {
			Description string `json:"description"`
		}
		require.NoError(t, json.Unmarshal(patchSchema.Properties[propertyName], &property))
		assert.Contains(t, property.Description, "Omitted or null leaves it unchanged", "%s semantics", propertyName)
		assert.Contains(t, property.Description, "An empty string", "%s semantics", propertyName)
	}
}
