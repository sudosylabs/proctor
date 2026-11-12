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
	"github.com/sudosylabs/proctor/server/httpapi/route"
	"github.com/sudosylabs/proctor/server/model"
)

type programmeLevelResponse struct {
	Revision    int64  `json:"revision"`
	ID          string `json:"id"`
	CreatedAt   string `json:"created_at"`
	UpdatedAt   string `json:"updated_at"`
	ArchivedAt  string `json:"archived_at,omitempty"`
	ProgrammeID string `json:"programme_id"`
	Name        string `json:"name"`
	DisplayName string `json:"display_name"`
	Description string `json:"description"`
}

type createProgrammeLevelRequest struct {
	Name        string `json:"name"`
	DisplayName string `json:"display_name"`
	Description string `json:"description"`
}

type updateProgrammeLevelRequest struct {
	ExpectedRevision route.Optional[int64]  `json:"expected_revision"`
	Name             route.Optional[string] `json:"name"`
	DisplayName      route.Optional[string] `json:"display_name"`
	Description      route.Optional[string] `json:"description"`
}

type programmeLevelHandlers struct {
	app ProgrammeLevelApplication
}

func programmeLevelResource(app ProgrammeLevelApplication) route.Resource {
	h := programmeLevelHandlers{app: app}
	collection := "/programmes/{programme_id}/levels"
	member := "/programme-levels/{programme_level_id}"
	return route.NewResource(
		"programme-levels",
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
			Errors: academicMutationErrorCodes(
				"request.invalid",
				"resource.not_found",
				"programme_level.invalid",
				"programme_level.conflict",
			),
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
			Errors: academicMutationErrorCodes(
				"request.invalid",
				"resource.not_found",
				"programme_level.invalid",
				"programme_level.conflict",
			),
			Handle: h.patch,
		},
		route.Endpoint{
			Method: http.MethodDelete,
			Path:   member,
			Auth:   route.AuthPrincipalRequired,
			Errors: academicMutationErrorCodes(
				"request.invalid", "resource.not_found", "programme_level.conflict",
			),
			Handle: h.archive,
		},
	)
}

func (h programmeLevelHandlers) list(request route.Request) (route.Result, error) {
	programmeID, err := route.PathID[model.ProgrammeID](request, "programme_id")
	if err != nil {
		return route.Result{}, err
	}
	pageQuery, err := academicNamePageQuery(request)
	if err != nil {
		return route.Result{}, err
	}
	page, err := h.app.ListProgrammeLevels(
		request.Context,
		request.Invocation(),
		academic.ListProgrammeLevelsQuery{
			ProgrammeID: programmeID,
			Query:       request.HTTP.URL.Query().Get("q"),
			NameQuery:   pageQuery,
		},
	)
	if err != nil {
		return route.Result{}, err
	}
	return academicNameCountedResult(page, pageQuery, programmeLevelResponseFromModel, func(level *model.ProgrammeLevel) (string, string) {
		return level.Name, level.ID.String()
	})
}

func (h programmeLevelHandlers) create(request route.Request) (route.Result, error) {
	programmeID, err := route.PathID[model.ProgrammeID](request, "programme_id")
	if err != nil {
		return route.Result{}, err
	}
	var body createProgrammeLevelRequest
	if err = request.DecodeJSON(&body); err != nil {
		return route.Result{}, err
	}
	saved, err := h.app.CreateProgrammeLevel(
		request.Context,
		request.Invocation(),
		academic.CreateProgrammeLevelCommand{
			ProgrammeID: programmeID,
			Name:        body.Name,
			DisplayName: body.DisplayName,
			Description: body.Description,
		},
	)
	if err != nil {
		return route.Result{}, err
	}
	return route.JSON(http.StatusCreated, programmeLevelResponseFromModel(saved)), nil
}

func (h programmeLevelHandlers) get(request route.Request) (route.Result, error) {
	id, err := route.PathID[model.ProgrammeLevelID](request, "programme_level_id")
	if err != nil {
		return route.Result{}, err
	}
	level, err := h.app.GetProgrammeLevel(
		request.Context, request.Invocation(), id,
	)
	if err != nil {
		return route.Result{}, err
	}
	return route.JSON(http.StatusOK, programmeLevelResponseFromModel(level)), nil
}

func (h programmeLevelHandlers) patch(request route.Request) (route.Result, error) {
	id, err := route.PathID[model.ProgrammeLevelID](request, "programme_level_id")
	if err != nil {
		return route.Result{}, err
	}
	var body updateProgrammeLevelRequest
	if err = request.DecodeJSON(&body); err != nil {
		return route.Result{}, err
	}
	if validateErr := validateAcademicRevision(body.ExpectedRevision); validateErr != nil {
		return route.Result{}, validateErr
	}
	level, err := h.app.UpdateProgrammeLevel(
		request.Context,
		request.Invocation(),
		academic.UpdateProgrammeLevelCommand{ExpectedRevision: body.ExpectedRevision.ValuePointer(),
			ID:          id,
			Name:        body.Name.ValuePointer(),
			DisplayName: body.DisplayName.ValuePointer(),
			Description: body.Description.ValuePointer(),
		},
	)
	if err != nil {
		return route.Result{}, err
	}
	return route.JSON(http.StatusOK, programmeLevelResponseFromModel(level)), nil
}

func (h programmeLevelHandlers) archive(request route.Request) (route.Result, error) {
	id, err := route.PathID[model.ProgrammeLevelID](request, "programme_level_id")
	if err != nil {
		return route.Result{}, err
	}
	expectedRevision, err := academicRevision(request)
	if err != nil {
		return route.Result{}, err
	}
	err = h.app.ArchiveProgrammeLevel(
		request.Context, request.Invocation(), academic.ArchiveProgrammeLevelCommand{ExpectedRevision: expectedRevision, ID: id},
	)
	if err != nil {
		return route.Result{}, err
	}
	return route.NoContent(), nil
}

func programmeLevelResponseFromModel(level *model.ProgrammeLevel) programmeLevelResponse {
	if level == nil {
		return programmeLevelResponse{}
	}
	return programmeLevelResponse{
		Revision:    level.Revision,
		ID:          level.ID.String(),
		CreatedAt:   model.FormatRFC3339(level.CreatedAt),
		UpdatedAt:   model.FormatRFC3339(level.UpdatedAt),
		ArchivedAt:  level.ArchivedAt.FormatRFC3339(),
		ProgrammeID: level.ProgrammeID.String(),
		Name:        level.Name,
		DisplayName: level.DisplayName,
		Description: level.Description,
	}
}

// ProgrammeLevelApplication is the application port its handlers call; it reads and changes Programme Levels.
type ProgrammeLevelApplication interface {
	GetProgrammeLevel(context.Context, invocation.Invocation, model.ProgrammeLevelID) (*model.ProgrammeLevel, error)
	ListProgrammeLevels(context.Context, invocation.Invocation, academic.ListProgrammeLevelsQuery) (paging.Counted[*model.ProgrammeLevel], error)
	CreateProgrammeLevel(context.Context, invocation.Invocation, academic.CreateProgrammeLevelCommand) (*model.ProgrammeLevel, error)
	UpdateProgrammeLevel(context.Context, invocation.Invocation, academic.UpdateProgrammeLevelCommand) (*model.ProgrammeLevel, error)
	ArchiveProgrammeLevel(context.Context, invocation.Invocation, academic.ArchiveProgrammeLevelCommand) error
}
