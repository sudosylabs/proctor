// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package httpapi

import (
	"context"
	"net/http"

	"github.com/sudosylabs/proctor/server/app/academic"
	"github.com/sudosylabs/proctor/server/app/invocation"
	"github.com/sudosylabs/proctor/server/app/paging"
	"github.com/sudosylabs/proctor/server/model"
)

type programmeResponse struct {
	Revision       int64  `json:"revision"`
	ID             string `json:"id"`
	CreatedAt      string `json:"created_at"`
	UpdatedAt      string `json:"updated_at"`
	ArchivedAt     string `json:"archived_at,omitempty"`
	AcademicUnitID string `json:"academic_unit_id"`
	Name           string `json:"name"`
	DisplayName    string `json:"display_name"`
	Description    string `json:"description"`
}

type createProgrammeRequest struct {
	Name        string `json:"name"`
	DisplayName string `json:"display_name"`
	Description string `json:"description"`
}

type updateProgrammeRequest struct {
	ExpectedRevision Optional[int64]  `json:"expected_revision"`
	Name             Optional[string] `json:"name"`
	DisplayName      Optional[string] `json:"display_name"`
	Description      Optional[string] `json:"description"`
}

type programmeResourceModule struct {
	programmes ProgrammeApplication
}

func programmeResource(programmes ProgrammeApplication) apiResource {
	module := programmeResourceModule{programmes: programmes}
	collection := "/academic-units/{academic_unit_id}/programmes"
	member := "/programmes/{programme_id}"
	return newResource(
		"programmes",
		endpoint{
			Method: http.MethodGet,
			Path:   collection,
			Auth:   AuthPrincipalRequired,
			Errors: academicReadErrorCodes("request.invalid", "resource.not_found"),
			Handle: module.list,
		},
		endpoint{
			Method: http.MethodPost,
			Path:   collection,
			Auth:   AuthPrincipalRequired,
			Errors: academicMutationErrorCodes("request.invalid", "resource.not_found", "programme.invalid", "programme.conflict"),
			Handle: module.create,
		},
		endpoint{
			Method: http.MethodGet,
			Path:   member,
			Auth:   AuthPrincipalRequired,
			Errors: academicReadErrorCodes("request.invalid", "resource.not_found"),
			Handle: module.get,
		},
		endpoint{
			Method: http.MethodPatch,
			Path:   member,
			Auth:   AuthPrincipalRequired,
			Errors: academicMutationErrorCodes("request.invalid", "resource.not_found", "programme.invalid", "programme.conflict"),
			Handle: module.patch,
		},
		endpoint{
			Method: http.MethodDelete,
			Path:   member,
			Auth:   AuthPrincipalRequired,
			Errors: academicMutationErrorCodes("request.invalid", "resource.not_found", "programme.conflict"),
			Handle: module.archive,
		},
	)
}

func (m programmeResourceModule) list(request operationRequest) (operationResult, error) {
	unitID, err := pathID[model.AcademicUnitID](request, "academic_unit_id")
	if err != nil {
		return operationResult{}, err
	}

	pageQuery, err := request.academicNamePageQuery()
	if err != nil {
		return operationResult{}, err
	}

	page, err := m.programmes.ListProgrammes(
		request.context,
		request.invocation(),
		academic.ListProgrammesQuery{AcademicUnitID: unitID, Query: request.request.URL.Query().Get("q"), NameQuery: pageQuery},
	)
	if err != nil {
		return operationResult{}, err
	}

	return academicNameListResult(page, programmeResponseFromModel, func(programme *model.Programme) (string, string) {
		return programme.Name, programme.ID.String()
	})
}

func (m programmeResourceModule) create(request operationRequest) (operationResult, error) {
	unitID, err := pathID[model.AcademicUnitID](request, "academic_unit_id")
	if err != nil {
		return operationResult{}, err
	}

	var body createProgrammeRequest
	if err = request.decodeJSON(&body); err != nil {
		return operationResult{}, err
	}

	saved, err := m.programmes.CreateProgramme(
		request.context,
		request.invocation(),
		academic.CreateProgrammeCommand{AcademicUnitID: unitID, Name: body.Name, DisplayName: body.DisplayName, Description: body.Description},
	)
	if err != nil {
		return operationResult{}, err
	}

	return jsonResult(http.StatusCreated, programmeResponseFromModel(saved)), nil
}

func (m programmeResourceModule) get(request operationRequest) (operationResult, error) {
	id, err := pathID[model.ProgrammeID](request, "programme_id")
	if err != nil {
		return operationResult{}, err
	}

	programme, err := m.programmes.GetProgramme(
		request.context,
		request.invocation(),
		academic.GetProgrammeQuery{ID: id},
	)
	if err != nil {
		return operationResult{}, err
	}

	return jsonResult(http.StatusOK, programmeResponseFromModel(programme)), nil
}

func (m programmeResourceModule) patch(request operationRequest) (operationResult, error) {
	id, err := pathID[model.ProgrammeID](request, "programme_id")
	if err != nil {
		return operationResult{}, err
	}

	var body updateProgrammeRequest
	if err = request.decodeJSON(&body); err != nil {
		return operationResult{}, err
	}
	if err := validateAcademicRevision(body.ExpectedRevision); err != nil {
		return operationResult{}, err
	}

	programme, err := m.programmes.UpdateProgramme(
		request.context,
		request.invocation(),
		academic.UpdateProgrammeCommand{
			ExpectedRevision: body.ExpectedRevision.ValuePointer(),
			ID:               id,
			Name:             body.Name.ValuePointer(),
			DisplayName:      body.DisplayName.ValuePointer(),
			Description:      body.Description.ValuePointer(),
		},
	)
	if err != nil {
		return operationResult{}, err
	}

	return jsonResult(http.StatusOK, programmeResponseFromModel(programme)), nil
}

func (m programmeResourceModule) archive(request operationRequest) (operationResult, error) {
	id, err := pathID[model.ProgrammeID](request, "programme_id")
	if err != nil {
		return operationResult{}, err
	}

	expectedRevision, err := request.academicRevision()
	if err != nil {
		return operationResult{}, err
	}

	err = m.programmes.ArchiveProgramme(
		request.context,
		request.invocation(),
		academic.ArchiveProgrammeCommand{ExpectedRevision: expectedRevision, ID: id},
	)
	if err != nil {
		return operationResult{}, err
	}

	return noContentResult(), nil
}

func programmeResponseFromModel(programme *model.Programme) programmeResponse {
	if programme == nil {
		return programmeResponse{}
	}
	return programmeResponse{
		Revision:       programme.Revision,
		ID:             programme.ID.String(),
		CreatedAt:      model.FormatRFC3339(programme.CreatedAt),
		UpdatedAt:      model.FormatRFC3339(programme.UpdatedAt),
		ArchivedAt:     programme.ArchivedAt.FormatRFC3339(),
		AcademicUnitID: programme.AcademicUnitID.String(),
		Name:           programme.Name,
		DisplayName:    programme.DisplayName,
		Description:    programme.Description,
	}
}

// ProgrammeApplication is the application port its handlers call; it reads and changes Programmes.
type ProgrammeApplication interface {
	GetProgramme(context.Context, invocation.Invocation, academic.GetProgrammeQuery) (*model.Programme, error)
	ListProgrammes(context.Context, invocation.Invocation, academic.ListProgrammesQuery) (paging.Page[*model.Programme], error)
	CreateProgramme(context.Context, invocation.Invocation, academic.CreateProgrammeCommand) (*model.Programme, error)
	UpdateProgramme(context.Context, invocation.Invocation, academic.UpdateProgrammeCommand) (*model.Programme, error)
	ArchiveProgramme(context.Context, invocation.Invocation, academic.ArchiveProgrammeCommand) error
}
