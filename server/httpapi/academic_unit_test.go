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
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/sudosylabs/proctor/server/app/academic"
	"github.com/sudosylabs/proctor/server/app/apperror"
	"github.com/sudosylabs/proctor/server/app/invocation"
	"github.com/sudosylabs/proctor/server/app/paging"
	"github.com/sudosylabs/proctor/server/httpapi/route"
	"github.com/sudosylabs/proctor/server/httpapi/route/routetest"
	"github.com/sudosylabs/proctor/server/model"
)

type academicUnitHTTPApplication struct {
	InstitutionApplication
	principal      model.Principal
	unit           *model.AcademicUnit
	created        *model.AcademicUnit
	createCommand  academic.CreateAcademicUnitCommand
	updateCommand  academic.UpdateAcademicUnitCommand
	archiveCommand academic.ArchiveAcademicUnitCommand
	getErr         error
}

func (a *academicUnitHTTPApplication) AuthenticateAccess(
	context.Context, string,
) (*model.Principal, error) {
	principal := a.principal
	return &principal, nil
}

func (a *academicUnitHTTPApplication) AuthenticateBearer(
	context.Context, string,
) (*model.Principal, error) {
	principal := a.principal
	return &principal, nil
}

func (a *academicUnitHTTPApplication) GetAcademicUnit(
	_ context.Context,
	inv invocation.Invocation,
	id model.AcademicUnitID,
) (*model.AcademicUnit, error) {
	if inv.Principal().UserID != a.principal.UserID || id != a.unit.ID {
		return nil, apperror.New("request.invalid")
	}
	if a.getErr != nil {
		return nil, a.getErr
	}
	return a.unit, nil
}

func (a *academicUnitHTTPApplication) ListAcademicUnits(
	context.Context,
	invocation.Invocation,
	academic.ListAcademicUnitsQuery,
) (paging.Page[*model.AcademicUnit], error) {
	return paging.Page[*model.AcademicUnit]{Items: []*model.AcademicUnit{}}, nil
}

func (a *academicUnitHTTPApplication) SearchAcademicUnits(
	context.Context,
	invocation.Invocation,
	academic.SearchAcademicUnitsQuery,
) (paging.Page[*model.AcademicUnit], error) {
	return paging.Page[*model.AcademicUnit]{Items: []*model.AcademicUnit{}}, nil
}

func (a *academicUnitHTTPApplication) CreateAcademicUnit(
	_ context.Context,
	inv invocation.Invocation,
	command academic.CreateAcademicUnitCommand,
) (*model.AcademicUnit, error) {
	if inv.Principal().UserID != a.principal.UserID {
		return nil, apperror.New("request.invalid")
	}
	a.createCommand = command
	return a.created, nil
}

func (a *academicUnitHTTPApplication) UpdateAcademicUnit(
	_ context.Context,
	inv invocation.Invocation,
	command academic.UpdateAcademicUnitCommand,
) (*model.AcademicUnit, error) {
	if inv.Principal().UserID != a.principal.UserID {
		return nil, apperror.New("request.invalid")
	}
	a.updateCommand = command
	return a.unit, nil
}

func (a *academicUnitHTTPApplication) ArchiveAcademicUnit(
	_ context.Context,
	inv invocation.Invocation,
	command academic.ArchiveAcademicUnitCommand,
) error {
	if inv.Principal().UserID != a.principal.UserID {
		return apperror.New("request.invalid")
	}
	a.archiveCommand = command
	return nil
}

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
		InstitutionID: model.InstitutionID(model.NewID()), Name: "computing", DisplayName: "Computing",
	}
	application := &academicUnitHTTPApplication{
		principal: model.Principal{
			UserID: model.NewUserID(), SessionID: model.NewSessionID(), CredentialID: model.PrincipalCredentialID(model.NewID()),
			CredentialType:         model.CredentialSessionAccess,
			AuthenticationMethod:   "password",
			AuthenticationStrength: model.AuthenticationSingleFactor,
			ClientType:             model.SessionClientCLI, AuthenticatedAt: time.Now(),
		},
		unit: unit,
	}
	httpAPI := newFocusedResourceAPI(t, logger, application, academicUnitResource(application))

	request := httptest.NewRequestWithContext(t.Context(),
		http.MethodGet, "/api/v1/academic-units/"+unit.ID.String(), nil,
	)
	request.Header.Set("Authorization", "Bearer test-credential")
	response := httptest.NewRecorder()
	httpAPI.ServeHTTP(response, request)
	require.Equal(t, http.StatusOK, response.Code, "status = %d: %s", response.Code, response.Body.String())
	want, err := json.Marshal(academicUnitResponseFromModel(unit))
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
	unit := &model.AcademicUnit{ID: model.AcademicUnitID(model.NewID())}
	fakeApplication := &academicUnitHTTPApplication{
		principal: model.Principal{
			UserID: model.NewUserID(), SessionID: model.NewSessionID(), CredentialID: model.PrincipalCredentialID(model.NewID()),
			CredentialType:         model.CredentialSessionAccess,
			AuthenticationMethod:   "password",
			AuthenticationStrength: model.AuthenticationSingleFactor,
			ClientType:             model.SessionClientCLI,
			AuthenticatedAt:        time.Now(),
		},
		unit: unit,
		getErr: apperror.New("resource.not_found").
			WithField("resource", "academic_unit"),
	}
	httpAPI := newFocusedResourceAPI(t, logger, fakeApplication, academicUnitResource(fakeApplication))

	request := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/api/v1/academic-units/"+unit.ID.String(), nil)
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
	require.Equal(t, "https://proctor.sudosylabs.com/problems/resource.not_found", problem.Type)
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
			created := &model.AcademicUnit{
				ID: model.AcademicUnitID(model.NewID()), CreatedAt: model.TimeFromMillis(10), UpdatedAt: model.TimeFromMillis(10),
				InstitutionID: model.InstitutionID(model.NewID()), ParentID: model.AcademicUnitID(tt.wantParentID),
				Name: "computing", DisplayName: "Computing", Description: "School",
			}
			fakeApplication := &academicUnitHTTPApplication{
				principal: model.Principal{
					UserID: model.NewUserID(), SessionID: model.NewSessionID(), CredentialID: model.PrincipalCredentialID(model.NewID()),
					CredentialType:       model.CredentialSessionAccess,
					AuthenticationMethod: "password", ClientType: model.SessionClientCLI,
					AuthenticationStrength: model.AuthenticationSingleFactor,
					AuthenticatedAt:        time.Now(),
				},
				unit: created, created: created,
			}
			settings := validHTTPSettings(t)
			settings.Logger = logger
			httpAPI, err := newAPI(settings, fakeApplication, academicUnitResource(fakeApplication), institutionResource(fakeApplication))
			require.NoError(t, err)
			t.Cleanup(func() { _ = httpAPI.Close() })

			request := httptest.NewRequestWithContext(t.Context(),
				http.MethodPost, tt.path,
				strings.NewReader(`{
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
			wantCommand := academic.CreateAcademicUnitCommand{
				ParentID: model.AcademicUnitID(tt.wantParentID), Name: "computing",
				DisplayName: "Computing", Description: "School",
			}
			require.Equal(t, wantCommand, fakeApplication.createCommand, "command")
			var got academicUnitResponse
			require.NoError(t, json.Unmarshal(response.Body.Bytes(), &got))
			require.Equal(t, created.ID.String(), got.ID)
			require.Equal(t, tt.wantParentID, got.ParentID)
		})
	}
}

func TestAcademicUnitHTTPMutationsMapCommandsWithoutPermissionPreflight(t *testing.T) {
	t.Parallel()

	unit := &model.AcademicUnit{
		ID: model.AcademicUnitID(model.NewID()), CreatedAt: model.TimeFromMillis(10), UpdatedAt: model.TimeFromMillis(20),
		InstitutionID: model.InstitutionID(model.NewID()), ParentID: model.AcademicUnitID(model.NewID()),
		Name: "computing", DisplayName: "Computing", Description: "School",
	}
	fakeApplication := &academicUnitHTTPApplication{
		principal: model.Principal{
			UserID: model.NewUserID(), SessionID: model.NewSessionID(), CredentialID: model.PrincipalCredentialID(model.NewID()),
			CredentialType:       model.CredentialSessionAccess,
			AuthenticationMethod: "password", ClientType: model.SessionClientCLI,
			AuthenticationStrength: model.AuthenticationSingleFactor,
			AuthenticatedAt:        time.Now(),
		},
		unit: unit,
	}
	logger, _ := routetest.NewLogger(t)
	settings := validHTTPSettings(t)
	settings.Logger = logger
	httpAPI, err := newAPI(settings, fakeApplication, academicUnitResource(fakeApplication), institutionResource(fakeApplication))
	require.NoError(t, err)
	t.Cleanup(func() { _ = httpAPI.Close() })

	parentID := model.NewID()
	name := "engineering"
	patchRequest := httptest.NewRequestWithContext(t.Context(),
		http.MethodPatch, "/api/v1/academic-units/"+unit.ID.String(),
		strings.NewReader(`{"parent_id":"`+parentID+`","name":"`+name+`"}`),
	)
	patchRequest.Header.Set("Authorization", "Bearer test-credential")
	patchRequest.Header.Set("Content-Type", "application/json")
	patchResponse := httptest.NewRecorder()
	httpAPI.ServeHTTP(patchResponse, patchRequest)
	require.Equal(t, http.StatusOK, patchResponse.Code, "patch status = %d: %s", patchResponse.Code, patchResponse.Body.String())
	require.Equal(t, unit.ID, fakeApplication.updateCommand.ID)
	require.NotNil(t, fakeApplication.updateCommand.ParentID)
	require.Equal(t, model.AcademicUnitID(parentID), *fakeApplication.updateCommand.ParentID)
	require.NotNil(t, fakeApplication.updateCommand.Name)
	require.Equal(t, name, *fakeApplication.updateCommand.Name)

	archiveRequest := httptest.NewRequestWithContext(t.Context(),
		http.MethodDelete, "/api/v1/academic-units/"+unit.ID.String(), nil,
	)
	archiveRequest.Header.Set("Authorization", "Bearer test-credential")
	archiveResponse := httptest.NewRecorder()
	httpAPI.ServeHTTP(archiveResponse, archiveRequest)
	require.Equal(t, http.StatusNoContent, archiveResponse.Code, "archive status = %d: %s", archiveResponse.Code, archiveResponse.Body.String())
	require.Equal(t, "no-store", archiveResponse.Header().Get("Cache-Control"))
	require.Equal(t, unit.ID, fakeApplication.archiveCommand.ID)
}

func TestAcademicUnitOpenAPIDocument(t *testing.T) {
	t.Parallel()

	document := readOpenAPIDocument(t)
	require.Equal(t, "3.1.0", document.OpenAPI, "OpenAPI version")
	requireOpenAPIListEnvelope(t, document.Components.Schemas["AcademicUnitListResponse"], "#/components/schemas/AcademicUnitResponse")
	require.Equal(t, "#/components/headers/NoStore", document.Components.Responses["AcademicUnitArchived"].Headers["Cache-Control"].Ref)
}
