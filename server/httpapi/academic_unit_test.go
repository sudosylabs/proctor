// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/sudosylabs/proctor/server/app/academic"
	"github.com/sudosylabs/proctor/server/app/access/accesstest"
	"github.com/sudosylabs/proctor/server/httpapi/route"
	"github.com/sudosylabs/proctor/server/httpapi/route/routetest"
	"github.com/sudosylabs/proctor/server/model"
	"github.com/sudosylabs/proctor/server/store"
)

func TestAcademicUnitResponsePreservesExistingWireShape(t *testing.T) {
	t.Parallel()

	unitID := model.NewID()
	institutionID := model.NewID()
	parentID := model.NewID()
	unit := &model.AcademicUnit{
		ID: model.AcademicUnitID(unitID), CreatedAt: model.TimeFromMillis(10),
		UpdatedAt:     model.TimeFromMillis(20),
		InstitutionID: model.InstitutionID(institutionID),
		ParentID:      model.AcademicUnitID(parentID),
		Name:          "computing", DisplayName: "Computing", Description: "School",
	}
	want, err := json.Marshal(academicUnitResponse{
		ID: unitID, CreatedAt: "1970-01-01T00:00:00.01Z", UpdatedAt: "1970-01-01T00:00:00.02Z",
		InstitutionID: institutionID, ParentID: parentID,
		Name: "computing", DisplayName: "Computing", Description: "School",
	})
	require.NoError(t, err)
	got, err := json.Marshal(academicUnitResponseFromModel(unit))
	require.NoError(t, err)
	require.Equal(t, string(want), string(got), "response")
}

func TestAcademicUnitCollectionEnvelopeHasNonNullItemsForNoResults(t *testing.T) {
	t.Parallel()

	response, err := route.NewList([]*model.AcademicUnit(nil), false, academicUnitResponseFromModel, nil)
	require.NoError(t, err)
	encoded, err := json.Marshal(response)
	require.NoError(t, err)
	require.JSONEq(t, `{"items":[]}`, string(encoded), "empty collection")
}

func TestAcademicUnitHTTPReadMapsDTOWithoutPermissionPreflight(t *testing.T) {
	t.Parallel()

	logger, _ := routetest.NewLogger(t)
	unit := &model.AcademicUnit{
		ID: model.AcademicUnitID(model.NewID()), CreatedAt: model.TimeFromMillis(10), UpdatedAt: model.TimeFromMillis(20),
		Name: "computing", DisplayName: "Computing",
	}
	principal := academicCaller()
	fixture := newAcademicFixture(t, academicOptions{
		Grants: []accesstest.Grant{inAcademicUnit(principal.UserID, unit.ID, model.ActionAcademicUnitView)},
		Units:  []*model.AcademicUnit{unit},
	})
	httpAPI := newFocusedResourceAPI(t, logger, routetest.Authenticator{Principal: principal}, academicUnitResource(fixture.service, nil))

	request := httptest.NewRequestWithContext(t.Context(),
		http.MethodGet, "/api/v1/academic-units/"+unit.ID.String(), nil,
	)
	request.Header.Set("Authorization", "Bearer test-credential")
	response := httptest.NewRecorder()
	httpAPI.ServeHTTP(response, request)
	require.Equal(t, http.StatusOK, response.Code, "status = %d: %s", response.Code, response.Body.String())
	want, err := json.Marshal(academicUnitResponseFromModel(fixture.units.byID[unit.ID]))
	require.NoError(t, err)
	var got academicUnitResponse
	require.NoError(t, json.Unmarshal(response.Body.Bytes(), &got))
	encoded, err := json.Marshal(got)
	require.NoError(t, err)
	require.Equal(t, string(want), string(encoded), "response")
}

func TestAcademicUnitHTTPErrorUsesProblemDetailsContract(t *testing.T) {
	t.Parallel()

	logger, _ := routetest.NewLogger(t)
	principal := academicCaller()
	// The caller may view every unit; the unit it asks for does not exist.
	fixture := newAcademicFixture(t, academicOptions{Grants: []accesstest.Grant{institutionWide(principal.UserID, model.ActionAcademicUnitView)}})
	httpAPI := newFocusedResourceAPI(t, logger, routetest.Authenticator{Principal: principal}, academicUnitResource(fixture.service, nil))

	request := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/api/v1/academic-units/"+model.NewID(), nil)
	request.Header.Set("Authorization", "Bearer test-credential")
	response := httptest.NewRecorder()
	httpAPI.ServeHTTP(response, request)
	require.Equal(t, http.StatusNotFound, response.Code, "status = %d: %s", response.Code, response.Body.String())
	require.Equal(t, "application/problem+json", response.Header().Get("Content-Type"))
	require.Equal(t, "no-store", response.Header().Get("Cache-Control"))
	var problem route.Problem
	require.NoError(t, json.Unmarshal(response.Body.Bytes(), &problem))
	require.Equal(t, "resource.not_found", problem.Code)
	require.Equal(t, http.StatusNotFound, problem.Status)
	require.Equal(t, "about:blank", problem.Type)
	require.Equal(t, "academic_unit", problem.Fields["resource"])
}

func TestAcademicUnitHTTPCreateMapsCommandWithoutPermissionPreflight(t *testing.T) {
	t.Parallel()

	parentID := model.NewID()
	tests := []struct {
		name         string
		path         string
		wantParentID string
	}{
		{name: "root", path: "/api/v1/academic-units"},
		{
			name: "child", path: "/api/v1/academic-units/" + parentID + "/children",
			wantParentID: parentID,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			logger, _ := routetest.NewLogger(t)
			principal := academicCaller()
			grant := institutionWide(principal.UserID, model.ActionAcademicUnitManage)
			if tt.wantParentID != "" {
				grant = inAcademicUnit(principal.UserID, model.AcademicUnitID(tt.wantParentID), model.ActionAcademicUnitManage)
			}
			fixture := newAcademicFixture(t, academicOptions{Grants: []accesstest.Grant{grant}})
			settings := validHTTPSettings(t)
			settings.Logger = logger
			httpAPI, err := newAPI(settings, routetest.Authenticator{Principal: principal}, academicUnitResource(fixture.service, nil), institutionResource(fixture.service))
			require.NoError(t, err)
			t.Cleanup(func() { _ = httpAPI.Close() })

			request := httptest.NewRequestWithContext(t.Context(),
				http.MethodPost, tt.path,
				strings.NewReader(`{
					"kind":"school",
					"name":"computing",
					"display_name":"Computing",
					"description":"School"
				}`),
			)
			request.Header.Set("Authorization", "Bearer test-credential")
			request.Header.Set("Content-Type", "application/json")
			response := httptest.NewRecorder()
			httpAPI.ServeHTTP(response, request)
			require.Equal(t, http.StatusCreated, response.Code, "status = %d: %s", response.Code, response.Body.String())
			require.Len(t, fixture.units.created, 1, "the create reached the Academic Unit Store")
			created := fixture.units.created[0].Unit
			require.Equal(t, model.AcademicUnitID(tt.wantParentID), created.ParentID)
			require.Equal(t, "computing", created.Name)
			require.Equal(t, "Computing", created.DisplayName)
			require.Equal(t, "School", created.Description)
			require.Equal(t, model.AcademicUnitKindSchool, created.Kind)
			var got academicUnitResponse
			require.NoError(t, json.Unmarshal(response.Body.Bytes(), &got))
			require.Equal(t, "school", got.Kind)
			require.Equal(t, created.ID.String(), got.ID)
			require.Equal(t, tt.wantParentID, got.ParentID)
		})
	}
}

func TestAcademicUnitHTTPMutationsMapCommandsWithoutPermissionPreflight(t *testing.T) {
	t.Parallel()

	world := newAcademicWorld()
	parent, destination := world.unit, &model.AcademicUnit{ID: model.NewAcademicUnitID(), Name: "engineering-school"}
	unit := &model.AcademicUnit{ParentID: parent.ID, Name: "computing", DisplayName: "Computing", Description: "School"}
	unit.PrepareCreate(model.NewAcademicUnitID(), parent.CreatedAt)
	principal := academicCaller()
	fixture := newAcademicFixture(t, academicOptions{
		Grants: []accesstest.Grant{institutionWide(principal.UserID, model.ActionAcademicUnitManage)},
		Units:  []*model.AcademicUnit{parent, destination, unit},
	})
	logger, _ := routetest.NewLogger(t)
	settings := validHTTPSettings(t)
	settings.Logger = logger
	httpAPI, err := newAPI(settings, routetest.Authenticator{Principal: principal}, academicUnitResource(fixture.service, nil), institutionResource(fixture.service))
	require.NoError(t, err)
	t.Cleanup(func() { _ = httpAPI.Close() })

	name := "engineering"
	patchRequest := httptest.NewRequestWithContext(t.Context(),
		http.MethodPatch, "/api/v1/academic-units/"+unit.ID.String(),
		strings.NewReader(`{"parent_id":"`+destination.ID.String()+`","name":"`+name+`","kind":"department"}`),
	)
	patchRequest.Header.Set("Authorization", "Bearer test-credential")
	patchRequest.Header.Set("Content-Type", "application/json")
	patchResponse := httptest.NewRecorder()
	httpAPI.ServeHTTP(patchResponse, patchRequest)
	require.Equal(t, http.StatusOK, patchResponse.Code, "patch status = %d: %s", patchResponse.Code, patchResponse.Body.String())
	require.Len(t, fixture.units.updates, 1, "the PATCH reached the Academic Unit Store")
	updated := fixture.units.updates[0].Unit
	require.Equal(t, unit.ID, updated.ID)
	require.Equal(t, destination.ID, updated.ParentID)
	require.Equal(t, name, updated.Name)
	require.Equal(t, model.AcademicUnitKindDepartment, updated.Kind)

	archiveRequest := httptest.NewRequestWithContext(t.Context(),
		http.MethodDelete, "/api/v1/academic-units/"+unit.ID.String(), nil,
	)
	archiveRequest.Header.Set("Authorization", "Bearer test-credential")
	archiveResponse := httptest.NewRecorder()
	httpAPI.ServeHTTP(archiveResponse, archiveRequest)
	require.Equal(t, http.StatusNoContent, archiveResponse.Code, "archive status = %d: %s", archiveResponse.Code, archiveResponse.Body.String())
	require.Equal(t, "no-store", archiveResponse.Header().Get("Cache-Control"))
	require.Len(t, fixture.units.archives, 1, "the archive reached the Academic Unit Store")
	require.Equal(t, unit.ID, fixture.units.archives[0].ID)
}

func TestAcademicUnitOpenAPIDocument(t *testing.T) {
	t.Parallel()

	document := readOpenAPIDocument(t)
	require.Equal(t, "3.1.0", document.OpenAPI, "OpenAPI version")
	requireOpenAPIListEnvelope(t, document.Components.Schemas["AcademicUnitListResponse"], "#/components/schemas/AcademicUnitResponse")
	require.Equal(t, "#/components/headers/NoStore", document.Components.Responses["AcademicUnitArchived"].Headers["Cache-Control"].Ref)
}

type unitSummaryMembers struct {
	*academicUnitMemberRecords
	count store.AcademicUnitMemberCount
}

func (s *unitSummaryMembers) CountCurrentByAcademicUnits(_ context.Context, options store.AcademicUnitMemberCountOptions) ([]store.AcademicUnitMemberCount, error) {
	counts := []store.AcademicUnitMemberCount{}
	for _, id := range options.AcademicUnitIDs {
		count := s.count
		count.AcademicUnitID = id
		counts = append(counts, count)
	}
	return counts, nil
}

type unitSummaryBindings struct {
	*academicBindingRecords
	administrators []model.UserID
}

func (s *unitSummaryBindings) ListAcademicUnitAdministrators(_ context.Context, options store.AcademicUnitAdministratorOptions) ([]store.AcademicUnitAdministrators, error) {
	return []store.AcademicUnitAdministrators{{AcademicUnitID: options.AcademicUnitIDs[0], UserIDs: s.administrators, More: 4}}, nil
}

func (s *academicUnitRecords) ListWithinSubtrees(_ context.Context, ids, roots []model.AcademicUnitID) ([]model.AcademicUnitID, error) {
	within := []model.AcademicUnitID{}
	for _, id := range ids {
		for unit, ok := s.byID[id]; ok; unit, ok = s.byID[unit.ParentID] {
			if slices.Contains(roots, unit.ID) {
				within = append(within, id)
				break
			}
		}
	}
	return within, nil
}

func TestAcademicUnitHTTPReadShowsCountsAndAdministratorsOnlyWithTheirGrants(t *testing.T) {
	t.Parallel()

	world := newAcademicWorld()
	unit := world.unit
	principal := academicCaller()
	present, removed := &model.User{ID: model.NewUserID(), Username: "praman", DisplayName: "Priya Raman"}, model.NewUserID()
	users := &directoryUserStore{byID: map[model.UserID]*model.User{present.ID: present}}

	read := func(t *testing.T, grants ...accesstest.Grant) map[string]any {
		t.Helper()
		fixture := newAcademicFixture(t, academicOptions{Grants: grants, Units: []*model.AcademicUnit{unit}})
		members := &unitSummaryMembers{academicUnitMemberRecords: fixture.unitMembers, count: store.AcademicUnitMemberCount{
			Members: store.MaximumListCount, ByAffiliation: map[model.AffiliationKind]int{model.AffiliationStudent: 376, model.AffiliationStaff: 36},
		}}
		bindings := &unitSummaryBindings{academicBindingRecords: fixture.bindings, administrators: []model.UserID{present.ID, removed}}
		service, err := academic.New(academic.Dependencies{
			Store:      &academicSummaryCatalog{Catalog: academicCatalog{fixture: fixture}, members: members, bindings: bindings},
			Authorizer: fixture.access.Authorizer, Auditor: fixture.access.Auditor, Now: time.Now, NewID: model.NewID,
			Publisher: fixture.publisher,
		})
		require.NoError(t, err)
		logger, _ := routetest.NewLogger(t)
		httpAPI := newFocusedResourceAPI(t, logger, routetest.Authenticator{Principal: principal},
			academicUnitResource(service, testPeople(users, fixture.access.Authorizer)))
		request := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/api/v1/academic-units/"+unit.ID.String(), nil)
		request.Header.Set("Authorization", "Bearer test-credential")
		response := httptest.NewRecorder()
		httpAPI.ServeHTTP(response, request)
		require.Equal(t, http.StatusOK, response.Code, response.Body.String())
		var body map[string]any
		require.NoError(t, json.Unmarshal(response.Body.Bytes(), &body))
		return body
	}

	viewOnly := read(t, inAcademicUnit(principal.UserID, unit.ID, model.ActionAcademicUnitView))
	for _, field := range []string{"member_count", "member_count_is_lower_bound", "member_counts_by_affiliation", "administrators", "administrators_more"} {
		require.NotContains(t, viewOnly, field, "academic_unit.view alone shows no %s", field)
	}

	full := read(t,
		inAcademicUnit(principal.UserID, unit.ID, model.ActionAcademicUnitView),
		inAcademicUnit(principal.UserID, unit.ID, model.ActionAcademicUnitMembersView),
		inAcademicUnit(principal.UserID, unit.ID, model.ActionRoleBindingView))
	require.InDelta(t, store.MaximumListCount, full["member_count"], 0)
	require.Equal(t, true, full["member_count_is_lower_bound"])
	byKind := full["member_counts_by_affiliation"].(map[string]any)
	require.InDelta(t, 376, byKind["student"], 0)
	require.Equal(t, false, byKind["student_is_lower_bound"])
	require.InDelta(t, 0, byKind["external"], 0)
	administrators := full["administrators"].([]any)
	require.Len(t, administrators, 2)
	require.Equal(t, map[string]any{"id": present.ID.String(), "display_name": "Priya Raman"}, administrators[0],
		"a person outside the caller's directory is named without their username")
	require.Equal(t, map[string]any{"id": removed.String()}, administrators[1], "a removed account has no name")
	require.InDelta(t, 4, full["administrators_more"], 0)
}

type academicSummaryCatalog struct {
	store.Catalog
	members  store.AcademicUnitMemberStore
	bindings store.RoleBindingStore
}

func (c *academicSummaryCatalog) AcademicUnitMember() store.AcademicUnitMemberStore { return c.members }
func (c *academicSummaryCatalog) RoleBinding() store.RoleBindingStore               { return c.bindings }
