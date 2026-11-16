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
	"github.com/sudosylabs/proctor/server/store"
)

func TestAffiliationHTTPUsesDTOAndRouteOwnedUser(t *testing.T) {
	t.Parallel()
	logger, _ := routetest.NewLogger(t)
	principal := academicCaller()
	user := academicUser("amina", "Amina Diallo", "amina@example.edu")
	fixture := newAcademicFixture(t, academicOptions{Grants: []accesstest.Grant{institutionWide(principal.UserID, model.ActionUserManage)}})
	fixture.users.add(user)
	httpAPI := newFocusedResourceAPI(t, logger, routetest.Authenticator{Principal: principal}, affiliationResource(fixture.service))
	request := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/api/v1/users/"+user.ID.String()+"/affiliations", strings.NewReader(`{"kind":"teacher","start_at":"1970-01-01T00:00:00.1Z"}`))
	request.Header.Set("Authorization", "Bearer credential")
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	httpAPI.ServeHTTP(response, request)
	require.Equal(t, http.StatusCreated, response.Code, "status = %d: %s", response.Code, response.Body.String())
	require.Len(t, fixture.affiliations.created, 1, "the create reached the Affiliation Store")
	created := fixture.affiliations.created[0].Affiliation
	require.Equal(t, user.ID, created.UserID)
	require.Equal(t, model.AffiliationTeacher, created.Kind)
	var body map[string]any
	require.NoError(t, json.Unmarshal(response.Body.Bytes(), &body))
	_, exposed := body["revision"]
	require.False(t, exposed, "persistence revision leaked into compatibility DTO: %#v", body)
}

func TestAffiliationResourceSerializesAllowedAuthorizationFailure(t *testing.T) {
	t.Parallel()
	logger, _ := routetest.NewLogger(t)
	principal := academicCaller()
	// The caller holds no grant and lists another User's Affiliations.
	other := academicUser("amina", "Amina Diallo", "amina@example.edu")
	fixture := newAcademicFixture(t, academicOptions{})
	fixture.users.add(other)
	httpAPI := newFocusedResourceAPI(t, logger, routetest.Authenticator{Principal: principal}, affiliationResource(fixture.service))

	request := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/api/v1/users/"+other.ID.String()+"/affiliations", nil)
	request.Header.Set("Authorization", "Bearer credential")
	response := httptest.NewRecorder()
	httpAPI.ServeHTTP(response, request)
	require.Equal(t, http.StatusForbidden, response.Code, "status = %d: %s", response.Code, response.Body.String())
	var problem route.Problem
	require.NoError(t, json.Unmarshal(response.Body.Bytes(), &problem))
	require.Equal(t, "authorization.denied", problem.Code, "problem = %#v body=%s", problem, response.Body.String())
	require.Empty(t, fixture.affiliations.lists, "a denied listing reached the Affiliation Store")
}

func TestAffiliationResourceFailsClosedForUndeclaredError(t *testing.T) {
	t.Parallel()
	logger, _ := routetest.NewLogger(t)
	principal := academicCaller()
	other := academicUser("amina", "Amina Diallo", "amina@example.edu")
	fixture := newAcademicFixture(t, academicOptions{Grants: []accesstest.Grant{institutionWide(principal.UserID, model.ActionUserView)}})
	fixture.users.add(other)
	// A Store conflict is presented as affiliation.conflict, which the listing
	// route does not declare.
	fixture.affiliations.listErr = store.NewErrConflict("affiliation", "affiliation_pkey", nil)
	httpAPI := newFocusedResourceAPI(t, logger, routetest.Authenticator{Principal: principal}, affiliationResource(fixture.service))

	request := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/api/v1/users/"+other.ID.String()+"/affiliations", nil)
	request.Header.Set("Authorization", "Bearer credential")
	response := httptest.NewRecorder()
	httpAPI.ServeHTTP(response, request)
	require.Equal(t, http.StatusInternalServerError, response.Code, "status = %d: %s", response.Code, response.Body.String())
	var problem route.Problem
	require.NoError(t, json.Unmarshal(response.Body.Bytes(), &problem))
	require.Equal(t, "server.internal", problem.Code)
	require.Equal(t, []model.UserID{other.ID}, fixture.affiliations.lists, "the listing reached the Affiliation Store")
}

func TestAffiliationResourceRejectsInvalidBodyAndIdentifier(t *testing.T) {
	t.Parallel()
	logger, _ := routetest.NewLogger(t)
	principal := academicCaller()
	fixture := newAcademicFixture(t, academicOptions{Grants: []accesstest.Grant{institutionWide(principal.UserID, model.ActionUserView, model.ActionUserManage)}})
	httpAPI := newFocusedResourceAPI(t, logger, routetest.Authenticator{Principal: principal}, affiliationResource(fixture.service))

	invalidBody := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/api/v1/users/"+model.NewID()+"/affiliations", strings.NewReader(`{"kind":"teacher","unknown":true}`))
	invalidBody.Header.Set("Authorization", "Bearer credential")
	invalidBody.Header.Set("Content-Type", "application/json")
	bodyResponse := httptest.NewRecorder()
	httpAPI.ServeHTTP(bodyResponse, invalidBody)
	require.Equal(t, http.StatusBadRequest, bodyResponse.Code, "body status = %d: %s", bodyResponse.Code, bodyResponse.Body.String())

	invalidID := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/api/v1/users/not-a-canonical-id/affiliations", nil)
	invalidID.Header.Set("Authorization", "Bearer credential")
	idResponse := httptest.NewRecorder()
	httpAPI.ServeHTTP(idResponse, invalidID)
	require.Equal(t, http.StatusNotFound, idResponse.Code, "identifier status = %d: %s", idResponse.Code, idResponse.Body.String())
	require.Empty(t, fixture.affiliations.created, "a rejected create reached the Affiliation Store")
	require.Empty(t, fixture.affiliations.lists, "a rejected listing reached the Affiliation Store")
}

func TestAffiliationOpenAPIDocument(t *testing.T) {
	t.Parallel()

	// AffiliationListResponse is a frozen legacy bare-array compatibility shape,
	// not an ordinary DTO contract and not a pattern for new collection routes.
	document := readOpenAPIDocument(t)
	list := document.Components.Schemas["AffiliationListResponse"]
	requireOpenAPIListEnvelope(t, list, "#/components/schemas/AffiliationResponse")
}
