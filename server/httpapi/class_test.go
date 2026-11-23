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
	"time"

	"github.com/stretchr/testify/require"

	"github.com/sudosylabs/proctor/server/app/access/accesstest"
	"github.com/sudosylabs/proctor/server/app/apperror"
	"github.com/sudosylabs/proctor/server/httpapi/route"
	"github.com/sudosylabs/proctor/server/httpapi/route/routetest"
	"github.com/sudosylabs/proctor/server/logging"
	"github.com/sudosylabs/proctor/server/model"
	"github.com/sudosylabs/proctor/server/store"
)

func TestClassResourceRunsThroughRoutingKernelWithNarrowDependencies(t *testing.T) {
	t.Parallel()

	logger, _ := routetest.NewLogger(t)
	principal := academicCaller()
	world := newAcademicWorld()
	fixture := newAcademicFixture(t, academicOptions{
		Grants: []accesstest.Grant{inAcademicUnit(principal.UserID, world.unit.ID, model.ActionClassManage)},
		Units:  world.units(),
	})
	fixture.hold(world)
	httpAPI := newFocusedResourceAPI(
		t,
		logger,
		routetest.Authenticator{Principal: principal},
		classResource(fixture.service),
	)

	request := httptest.NewRequestWithContext(t.Context(),
		http.MethodPost,
		"/api/v1/programme-levels/"+world.level.ID.String()+"/classes",
		strings.NewReader(`{"academic_period_id":"`+world.period.ID.String()+`","name":"class-a","display_name":"Class A"}`),
	)
	request.Header.Set("Authorization", "Bearer credential")
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	httpAPI.ServeHTTP(response, request)
	require.Equal(t, http.StatusCreated, response.Code, "status = %d: %s", response.Code, response.Body.String())
	require.Len(t, fixture.classes.created, 1, "the create reached the Class Store")
	created := fixture.classes.created[0].Class
	require.Equal(t, world.level.ID, created.ProgrammeLevelID)
	require.Equal(t, world.period.ID, created.AcademicPeriodID)
	var body classResponse
	require.NoError(t, json.Unmarshal(response.Body.Bytes(), &body))
	require.Equal(t, created.ID.String(), body.ID)

	invalid := httptest.NewRequestWithContext(t.Context(),
		http.MethodPost,
		"/api/v1/programme-levels/"+world.level.ID.String()+"/classes",
		strings.NewReader(`{"academic_period_id":"`+world.period.ID.String()+`","name":"class-a","display_name":"Class A","unknown":true}`),
	)
	invalid.Header.Set("Authorization", "Bearer credential")
	invalid.Header.Set("Content-Type", "application/json")
	invalidResponse := httptest.NewRecorder()
	httpAPI.ServeHTTP(invalidResponse, invalid)
	require.Equal(t, http.StatusBadRequest, invalidResponse.Code, "invalid status = %d: %s", invalidResponse.Code, invalidResponse.Body.String())
	require.Len(t, fixture.classes.created, 1, "a rejected create reached the Class Store")

	archive := httptest.NewRequestWithContext(t.Context(),
		http.MethodDelete,
		"/api/v1/classes/"+world.class.ID.String(),
		nil,
	)
	archive.Header.Set("Authorization", "Bearer credential")
	archiveResponse := httptest.NewRecorder()
	httpAPI.ServeHTTP(archiveResponse, archive)
	require.Equal(t, http.StatusNoContent, archiveResponse.Code, "archive status/cache/body = %d/%q/%q", archiveResponse.Code, archiveResponse.Header().Get("Cache-Control"), archiveResponse.Body.String())
	require.Equal(t, "no-store", archiveResponse.Header().Get("Cache-Control"), "archive status/cache/body = %d/%q/%q", archiveResponse.Code, archiveResponse.Header().Get("Cache-Control"), archiveResponse.Body.String())
	require.Equal(t, 0, archiveResponse.Body.Len(), "archive status/cache/body = %d/%q/%q", archiveResponse.Code, archiveResponse.Header().Get("Cache-Control"), archiveResponse.Body.String())
	require.Len(t, fixture.classes.archives, 1, "the archive reached the Class Store")
	require.Equal(t, world.class.ID, fixture.classes.archives[0].ID)
}

func TestClassResourceFailsClosedForUndeclaredApplicationError(t *testing.T) {
	t.Parallel()

	logger, _ := routetest.NewLogger(t)
	principal := academicCaller()
	unitID := model.NewAcademicUnitID()
	fixture := newAcademicFixture(t, academicOptions{Grants: []accesstest.Grant{inAcademicUnit(principal.UserID, unitID, model.ActionClassView)}})
	// The Store refuses the search as invalid input, which the service
	// presents as class.invalid: a code the search route does not declare.
	fixture.classes.search.err = store.NewErrInvalidInput("class", "name", "")
	httpAPI := newFocusedResourceAPI(
		t,
		logger,
		routetest.Authenticator{Principal: principal},
		classResource(fixture.service),
	)

	request := httptest.NewRequestWithContext(t.Context(),
		http.MethodGet,
		"/api/v1/academic-units/"+unitID.String()+"/classes",
		nil,
	)
	request.Header.Set("Authorization", "Bearer credential")
	response := httptest.NewRecorder()
	httpAPI.ServeHTTP(response, request)
	require.Equal(t, http.StatusInternalServerError, response.Code, "status = %d: %s", response.Code, response.Body.String())
	var problem route.Problem
	require.NoError(t, json.Unmarshal(response.Body.Bytes(), &problem))
	require.Equal(t, "server.internal", problem.Code, "problem code")
	require.Len(t, fixture.classes.search.listOptions, 1, "the search reached the Class Store")
}

func TestClassResourceFailsClosedForUndeclaredAuthenticationError(t *testing.T) {
	t.Parallel()

	logger, _ := routetest.NewLogger(t)
	fixture := newAcademicFixture(t, academicOptions{})
	httpAPI := newFocusedResourceAPI(
		t,
		logger,
		routetest.Authenticator{Err: apperror.New("user.conflict")},
		classResource(fixture.service),
	)
	request := httptest.NewRequestWithContext(t.Context(),
		http.MethodGet,
		"/api/v1/classes/"+model.NewID(),
		nil,
	)
	request.Header.Set("Authorization", "Bearer credential")
	response := httptest.NewRecorder()
	httpAPI.ServeHTTP(response, request)
	require.Equal(t, http.StatusInternalServerError, response.Code, "status = %d: %s", response.Code, response.Body.String())
	var problem route.Problem
	require.NoError(t, json.Unmarshal(response.Body.Bytes(), &problem))
	require.Equal(t, "server.internal", problem.Code, "problem code")
	require.Empty(t, fixture.access.Audits.Decisions(), "an unauthenticated request reached the service")
}

func newFocusedResourceAPI(
	t *testing.T,
	logger *logging.Logger,
	authenticator route.Authenticator,
	resources ...route.Resource,
) *route.API {
	t.Helper()
	httpAPI, err := route.New(route.Settings{
		Logger:                  logger,
		Localizer:               routetest.NewLocalizer(t),
		PublicURL:               "http://localhost:8065",
		MaxBodyBytes:            1 << 20,
		RecentAuthenticationTTL: time.Minute,
	}, authenticator, resources...)
	require.NoError(t, err)
	return httpAPI
}

func TestClassSearchMapsMissingAcademicUnit(t *testing.T) {
	t.Parallel()
	logger, _ := routetest.NewLogger(t)
	principal := academicCaller()
	// The caller may view every Class; the unit it names does not exist.
	fixture := newAcademicFixture(t, academicOptions{Grants: []accesstest.Grant{institutionWide(principal.UserID, model.ActionClassView)}})
	settings := validHTTPSettings(t)
	settings.Logger = logger
	httpAPI, err := newAPI(settings, routetest.Authenticator{Principal: principal},
		academicUnitResource(fixture.service, nil), institutionResource(fixture.service), classResource(fixture.service))
	require.NoError(t, err)
	t.Cleanup(func() { _ = httpAPI.Close() })
	request := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/api/v1/academic-units/"+model.NewID()+"/classes", nil)
	request.Header.Set("Authorization", "Bearer credential")
	response := httptest.NewRecorder()
	httpAPI.ServeHTTP(response, request)
	require.Equal(t, http.StatusNotFound, response.Code, "status = %d: %s", response.Code, response.Body.String())
	require.Empty(t, fixture.classes.search.listOptions, "a search of a missing unit reached the Class Store")
}

func TestClassHTTPMapsBothParentsFromRouteAndBody(t *testing.T) {
	t.Parallel()
	logger, _ := routetest.NewLogger(t)
	principal := academicCaller()
	world := newAcademicWorld()
	fixture := newAcademicFixture(t, academicOptions{
		Grants: []accesstest.Grant{inAcademicUnit(principal.UserID, world.unit.ID, model.ActionClassManage)},
		Units:  world.units(),
	})
	fixture.hold(world)
	settings := validHTTPSettings(t)
	settings.Logger = logger
	httpAPI, err := newAPI(settings, routetest.Authenticator{Principal: principal},
		academicUnitResource(fixture.service, nil), institutionResource(fixture.service), classResource(fixture.service))
	require.NoError(t, err)
	t.Cleanup(func() { _ = httpAPI.Close() })
	request := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/api/v1/programme-levels/"+world.level.ID.String()+"/classes", strings.NewReader(`{"academic_period_id":"`+world.period.ID.String()+`","name":"class-a","display_name":"Class A"}`))
	request.Header.Set("Authorization", "Bearer credential")
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	httpAPI.ServeHTTP(response, request)
	require.Equal(t, http.StatusCreated, response.Code, "status = %d: %s", response.Code, response.Body.String())
	require.Len(t, fixture.classes.created, 1, "the create reached the Class Store")
	created := fixture.classes.created[0].Class
	require.Equal(t, world.level.ID, created.ProgrammeLevelID)
	require.Equal(t, world.period.ID, created.AcademicPeriodID)
	var body classResponse
	require.NoError(t, json.Unmarshal(response.Body.Bytes(), &body))
	require.Equal(t, created.ID.String(), body.ID)
	require.Equal(t, world.period.ID.String(), body.AcademicPeriodID)
}

func TestClassPatchOptionalWireStates(t *testing.T) {
	t.Parallel()
	var body updateClassRequest
	require.NoError(t, json.Unmarshal([]byte(`{"academic_period_id":null,"programme_level_id":""}`), &body))
	require.Nil(t, body.AcademicPeriodID.ValuePointer(), "null must remain a no-op")
	value := body.ProgrammeLevelID.ValuePointer()
	require.NotNil(t, value)
	require.Empty(t, *value)
}

func TestClassOpenAPIDocument(t *testing.T) {
	t.Parallel()

	document := readOpenAPIDocument(t)
	var searchOperation openAPIOperation
	path := model.APIURLSuffix + "/academic-units/{academic_unit_id}/classes"
	require.NoError(t, json.Unmarshal(document.Paths[path]["get"], &searchOperation))
	require.Equal(t, []string{"academic_period_id", "cursor", "dir", "limit", "q"}, academicStructureQueryParameterNames(searchOperation.Parameters), "Class search query parameters")
	list := document.Components.Schemas["ClassListResponse"]
	requireOpenAPIListEnvelope(t, list, "#/components/schemas/ClassListItemResponse")
	require.Equal(t, "#/components/headers/NoStore", document.Components.Responses["ClassArchived"].Headers["Cache-Control"].Ref)
}
