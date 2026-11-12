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
	"github.com/sudosylabs/proctor/server/app/invocation"
	"github.com/sudosylabs/proctor/server/app/paging"
	"github.com/sudosylabs/proctor/server/httpapi/route"
	"github.com/sudosylabs/proctor/server/httpapi/route/routetest"
	"github.com/sudosylabs/proctor/server/model"
)

type programmeHTTPApplication struct {
	archiveCommand academic.ArchiveProgrammeCommand
	result         *model.Programme
	list           []*model.Programme
	createCommand  academic.CreateProgrammeCommand
	updateCommand  academic.UpdateProgrammeCommand
}

func (a *programmeHTTPApplication) GetProgramme(context.Context, invocation.Invocation, model.ProgrammeID) (*model.Programme, error) {
	return a.result, nil
}
func (a *programmeHTTPApplication) ListProgrammes(_ context.Context, _ invocation.Invocation, query academic.ListProgrammesQuery) (paging.Counted[*model.Programme], error) {
	page := paging.Counted[*model.Programme]{Page: paging.Page[*model.Programme]{Items: a.list}}
	if query.AfterID == "" {
		page.Total = paging.CountWhole(len(a.list))
	}
	return page, nil
}
func (a *programmeHTTPApplication) CreateProgramme(_ context.Context, _ invocation.Invocation, command academic.CreateProgrammeCommand) (*model.Programme, error) {
	a.createCommand = command
	return a.result, nil
}
func (a *programmeHTTPApplication) UpdateProgramme(_ context.Context, _ invocation.Invocation, command academic.UpdateProgrammeCommand) (*model.Programme, error) {
	a.updateCommand = command
	return a.result, nil
}
func (a *programmeHTTPApplication) ArchiveProgramme(_ context.Context, _ invocation.Invocation, command academic.ArchiveProgrammeCommand) error {
	a.archiveCommand = command
	return nil
}

func TestProgrammeHTTPMapsDTOWithoutPermissionPreflight(t *testing.T) {
	t.Parallel()
	logger, _ := routetest.NewLogger(t)
	principal := model.Principal{
		UserID: model.NewUserID(), SessionID: model.NewSessionID(), CredentialID: model.PrincipalCredentialID(model.NewID()),
		CredentialType: model.CredentialSessionAccess, AuthenticationMethod: "password",
		AuthenticationStrength: model.AuthenticationSingleFactor,
		ClientType:             model.SessionClientCLI, AuthenticatedAt: time.Now(),
	}
	programme := &model.Programme{ID: model.ProgrammeID(model.NewID()), AcademicUnitID: model.AcademicUnitID(model.NewID()), Name: "computer-science", DisplayName: "Computer Science"}
	programmes := &programmeHTTPApplication{result: programme}
	httpAPI := newFocusedResourceAPI(
		t, logger, routetest.Authenticator{Principal: principal}, programmeResource(programmes),
	)

	request := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/api/v1/academic-units/"+programme.AcademicUnitID.String()+"/programmes", strings.NewReader(`{"name":"computer-science","display_name":"Computer Science"}`))
	request.Header.Set("Authorization", "Bearer credential")
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	httpAPI.ServeHTTP(response, request)
	require.Equal(t, http.StatusCreated, response.Code, "status = %d: %s", response.Code, response.Body.String())
	require.Equal(t, programme.AcademicUnitID, programmes.createCommand.AcademicUnitID)
	require.Equal(t, programme.Name, programmes.createCommand.Name)
	var body programmeResponse
	require.NoError(t, json.Unmarshal(response.Body.Bytes(), &body))
	require.Equal(t, programme.ID.String(), body.ID)
	require.Equal(t, programme.AcademicUnitID.String(), body.AcademicUnitID)
}

func TestProgrammeResourceRejectsInvalidQueryLimit(t *testing.T) {
	t.Parallel()

	logger, _ := routetest.NewLogger(t)
	principal := model.Principal{
		UserID: model.NewUserID(), SessionID: model.NewSessionID(),
		CredentialID:           model.PrincipalCredentialID(model.NewID()),
		CredentialType:         model.CredentialSessionAccess,
		AuthenticationMethod:   "password",
		AuthenticationStrength: model.AuthenticationSingleFactor,
		ClientType:             model.SessionClientCLI,
		AuthenticatedAt:        time.Now(),
	}
	programmes := &programmeHTTPApplication{}
	httpAPI := newFocusedResourceAPI(
		t, logger, routetest.Authenticator{Principal: principal}, programmeResource(programmes),
	)
	request := httptest.NewRequestWithContext(t.Context(),
		http.MethodGet,
		"/api/v1/academic-units/"+model.NewID()+"/programmes?limit=invalid",
		nil,
	)
	request.Header.Set("Authorization", "Bearer credential")
	response := httptest.NewRecorder()
	httpAPI.ServeHTTP(response, request)
	require.Equal(t, http.StatusBadRequest, response.Code, "status = %d: %s", response.Code, response.Body.String())
	var problem route.Problem
	require.NoError(t, json.Unmarshal(response.Body.Bytes(), &problem))
	require.Equal(t, "request.invalid", problem.Code, "problem code")
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
