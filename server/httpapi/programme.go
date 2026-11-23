// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package httpapi

import (
	"net/http"

	"github.com/sudosylabs/proctor/server/app/academic"
	"github.com/sudosylabs/proctor/server/httpapi/route"
	"github.com/sudosylabs/proctor/server/model"
)

type programmeResponse struct {
	Revision       int64  `json:"revision"`
	ID             string `json:"id"`
	CreatedAt      string `json:"created_at"`
	UpdatedAt      string `json:"updated_at"`
	ArchivedAt     string `json:"archived_at,omitempty"`
	AcademicUnitID string `json:"academic_unit_id"`
	Kind           string `json:"kind"`
	Name           string `json:"name"`
	DisplayName    string `json:"display_name"`
	Description    string `json:"description"`
	// The counts are present only for a caller who may see them; see
	// academic.StructureCounts.
	LevelCount               *int  `json:"level_count,omitempty"`
	LevelCountIsLowerBound   *bool `json:"level_count_is_lower_bound,omitempty"`
	ClassCount               *int  `json:"class_count,omitempty"`
	ClassCountIsLowerBound   *bool `json:"class_count_is_lower_bound,omitempty"`
	StudentCount             *int  `json:"student_count,omitempty"`
	StudentCountIsLowerBound *bool `json:"student_count_is_lower_bound,omitempty"`
}

type createProgrammeRequest struct {
	Kind        string `json:"kind,omitempty"`
	Name        string `json:"name"`
	DisplayName string `json:"display_name"`
	Description string `json:"description"`
}

type updateProgrammeRequest struct {
	ExpectedRevision route.Optional[int64]  `json:"expected_revision"`
	Kind             route.Optional[string] `json:"kind"`
	Name             route.Optional[string] `json:"name"`
	DisplayName      route.Optional[string] `json:"display_name"`
	Description      route.Optional[string] `json:"description"`
}

type programmeHandlers struct {
	app *academic.Service
}

func programmeResource(app *academic.Service) route.Resource {
	h := programmeHandlers{app: app}
	collection := "/academic-units/{academic_unit_id}/programmes"
	member := "/programmes/{programme_id}"
	return route.NewResource(
		"programmes",
		route.Endpoint{
			Method: http.MethodGet,
			Path:   collection,
			Auth:   route.AuthPrincipalRequired,
			Errors: academicReadErrorCodes("request.invalid", "resource.not_found"),
			Handle: h.list,
		},
		route.Endpoint{
			Method: http.MethodPost,
			Path:   collection,
			Auth:   route.AuthPrincipalRequired,
			Errors: academicMutationErrorCodes("request.invalid", "resource.not_found", "programme.invalid", "programme.conflict"),
			Handle: h.create,
		},
		route.Endpoint{
			Method: http.MethodGet,
			Path:   member,
			Auth:   route.AuthPrincipalRequired,
			Errors: academicReadErrorCodes("request.invalid", "resource.not_found"),
			Handle: h.get,
		},
		route.Endpoint{
			Method: http.MethodPatch,
			Path:   member,
			Auth:   route.AuthPrincipalRequired,
			Errors: academicMutationErrorCodes("request.invalid", "resource.not_found", "programme.invalid", "programme.conflict"),
			Handle: h.patch,
		},
		route.Endpoint{
			Method: http.MethodDelete,
			Path:   member,
			Auth:   route.AuthPrincipalRequired,
			Errors: academicMutationErrorCodes("request.invalid", "resource.not_found", "programme.conflict"),
			Handle: h.archive,
		},
	)
}

func (h programmeHandlers) list(request route.Request) (route.Result, error) {
	unitID, err := route.PathID[model.AcademicUnitID](request, "academic_unit_id")
	if err != nil {
		return route.Result{}, err
	}

	pageQuery, err := academicNamePageQuery(request)
	if err != nil {
		return route.Result{}, err
	}

	page, err := h.app.ListProgrammes(
		request.Context,
		request.Invocation(),
		academic.ListProgrammesQuery{AcademicUnitID: unitID, Query: request.HTTP.URL.Query().Get("q"), NameQuery: pageQuery},
	)
	if err != nil {
		return route.Result{}, err
	}

	present := func(programme *model.Programme) programmeResponse {
		return countedProgrammeResponse(programme, page.StructureCounts)
	}
	return academicNameCountedResult(page.Counted, pageQuery, present, func(programme *model.Programme) (string, string) {
		return programme.Name, programme.ID.String()
	})
}

func (h programmeHandlers) create(request route.Request) (route.Result, error) {
	unitID, err := route.PathID[model.AcademicUnitID](request, "academic_unit_id")
	if err != nil {
		return route.Result{}, err
	}

	var body createProgrammeRequest
	if err = request.DecodeJSON(&body); err != nil {
		return route.Result{}, err
	}

	saved, err := h.app.CreateProgramme(
		request.Context,
		request.Invocation(),
		academic.CreateProgrammeCommand{
			AcademicUnitID: unitID, Kind: model.ProgrammeKind(body.Kind),
			Name: body.Name, DisplayName: body.DisplayName, Description: body.Description,
		},
	)
	if err != nil {
		return route.Result{}, err
	}

	return route.JSON(http.StatusCreated, programmeResponseFromModel(saved)), nil
}

func (h programmeHandlers) get(request route.Request) (route.Result, error) {
	id, err := route.PathID[model.ProgrammeID](request, "programme_id")
	if err != nil {
		return route.Result{}, err
	}

	view, err := h.app.GetProgramme(
		request.Context,
		request.Invocation(),
		id,
	)
	if err != nil {
		return route.Result{}, err
	}

	return route.JSON(http.StatusOK, countedProgrammeResponse(view.Programme, view.StructureCounts)), nil
}

func (h programmeHandlers) patch(request route.Request) (route.Result, error) {
	id, err := route.PathID[model.ProgrammeID](request, "programme_id")
	if err != nil {
		return route.Result{}, err
	}

	var body updateProgrammeRequest
	if err = request.DecodeJSON(&body); err != nil {
		return route.Result{}, err
	}
	if validateErr := validateAcademicRevision(body.ExpectedRevision); validateErr != nil {
		return route.Result{}, validateErr
	}

	programme, err := h.app.UpdateProgramme(
		request.Context,
		request.Invocation(),
		academic.UpdateProgrammeCommand{
			ExpectedRevision: body.ExpectedRevision.ValuePointer(),
			ID:               id,
			Kind:             (*model.ProgrammeKind)(body.Kind.ValuePointer()),
			Name:             body.Name.ValuePointer(),
			DisplayName:      body.DisplayName.ValuePointer(),
			Description:      body.Description.ValuePointer(),
		},
	)
	if err != nil {
		return route.Result{}, err
	}

	return route.JSON(http.StatusOK, programmeResponseFromModel(programme)), nil
}

func (h programmeHandlers) archive(request route.Request) (route.Result, error) {
	id, err := route.PathID[model.ProgrammeID](request, "programme_id")
	if err != nil {
		return route.Result{}, err
	}

	expectedRevision, err := academicRevision(request)
	if err != nil {
		return route.Result{}, err
	}

	err = h.app.ArchiveProgramme(
		request.Context,
		request.Invocation(),
		academic.ArchiveProgrammeCommand{ExpectedRevision: expectedRevision, ID: id},
	)
	if err != nil {
		return route.Result{}, err
	}

	return route.NoContent(), nil
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
		Kind:           string(programme.Kind),
		Name:           programme.Name,
		DisplayName:    programme.DisplayName,
		Description:    programme.Description,
	}
}

func countedProgrammeResponse(programme *model.Programme, counts academic.StructureCounts) programmeResponse {
	response := programmeResponseFromModel(programme)
	id := programme.ID.String()
	response.LevelCount, response.LevelCountIsLowerBound = cappedCount(counts.Levels, id)
	response.ClassCount, response.ClassCountIsLowerBound = cappedCount(counts.Classes, id)
	response.StudentCount, response.StudentCountIsLowerBound = cappedCount(counts.Students, id)
	return response
}
