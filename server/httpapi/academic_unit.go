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

type academicUnitResponse struct {
	Revision      int64  `json:"revision"`
	ID            string `json:"id"`
	CreatedAt     string `json:"created_at"`
	UpdatedAt     string `json:"updated_at"`
	ArchivedAt    string `json:"archived_at,omitempty"`
	InstitutionID string `json:"institution_id"`
	ParentID      string `json:"parent_id,omitempty"`
	Name          string `json:"name"`
	DisplayName   string `json:"display_name"`
	Description   string `json:"description"`
}

type createAcademicUnitRequest struct {
	Name        string `json:"name"`
	DisplayName string `json:"display_name"`
	Description string `json:"description"`
}

type updateAcademicUnitRequest struct {
	ExpectedRevision route.Optional[int64] `json:"expected_revision"`
	ParentID         *string               `json:"parent_id"`
	Name             *string               `json:"name"`
	DisplayName      *string               `json:"display_name"`
	Description      *string               `json:"description"`
}

type academicUnitHandlers struct {
	app AcademicUnitApplication
}

func academicUnitResource(app AcademicUnitApplication) route.Resource {
	h := academicUnitHandlers{app: app}
	collection := "/academic-units"
	member := "/academic-units/{academic_unit_id}"
	children := "/academic-units/{academic_unit_id}/children"
	return route.NewResource(
		"academic-units",
		route.Endpoint{
			Method: http.MethodGet,
			Path:   collection,
			Auth:   route.AuthPrincipalRequired,
			Errors: []string{
				"authorization.denied",
				"authorization.request.invalid",
				"authorization.unavailable",
				"request.invalid",
				"administration.unavailable",
				"resource.not_found",
			},
			Handle: h.listRoot,
		},
		route.Endpoint{
			Method:      http.MethodPost,
			Path:        collection,
			Auth:        route.AuthPrincipalRequired,
			Idempotency: route.IdempotencyOptional,
			Errors: []string{
				"authorization.denied",
				"authorization.request.invalid",
				"authorization.unavailable",
				"audit.unavailable",
				"request.invalid",
				"academic_unit.invalid",
				"academic_unit.conflict",
				"idempotency.key_invalid",
				"idempotency.conflict",
				"idempotency.in_progress",
				"administration.unavailable",
				"resource.not_found",
			},
			Handle: h.createRoot,
		},
		route.Endpoint{
			Method: http.MethodGet,
			Path:   member,
			Auth:   route.AuthPrincipalRequired,
			Errors: []string{
				"authorization.denied",
				"authorization.request.invalid",
				"authorization.unavailable",
				"resource.not_found",
				"administration.unavailable",
				"request.invalid",
			},
			Handle: h.get,
		},
		route.Endpoint{
			Method: http.MethodPatch,
			Path:   member,
			Auth:   route.AuthPrincipalRequired,
			Errors: academicMutationErrorCodes("request.invalid", "resource.not_found", "academic_unit.invalid", "academic_unit.conflict"),
			Handle: h.patch,
		},
		route.Endpoint{
			Method: http.MethodDelete,
			Path:   member,
			Auth:   route.AuthPrincipalRequired,
			Errors: academicMutationErrorCodes("request.invalid", "resource.not_found", "academic_unit.conflict"),
			Handle: h.archive,
		},
		route.Endpoint{
			Method: http.MethodGet,
			Path:   children,
			Auth:   route.AuthPrincipalRequired,
			Errors: []string{
				"authorization.denied",
				"authorization.request.invalid",
				"authorization.unavailable",
				"resource.not_found",
				"administration.unavailable",
				"request.invalid",
			},
			Handle: h.listChildren,
		},
		route.Endpoint{
			Method: http.MethodPost,
			Path:   children,
			Auth:   route.AuthPrincipalRequired,
			Errors: academicMutationErrorCodes("request.invalid", "resource.not_found", "academic_unit.invalid", "academic_unit.conflict"),
			Handle: h.createChild,
		},
	)
}

func (h academicUnitHandlers) listRoot(request route.Request) (route.Result, error) {
	pageQuery, err := academicNamePageQuery(request)
	if err != nil {
		return route.Result{}, err
	}
	var page paging.Page[*model.AcademicUnit]
	if query := request.HTTP.URL.Query().Get("q"); query != "" {
		page, err = h.app.SearchAcademicUnits(
			request.Context,
			request.Invocation(),
			academic.SearchAcademicUnitsQuery{Query: query, NameQuery: pageQuery},
		)
	} else {
		page, err = h.app.ListAcademicUnits(
			request.Context, request.Invocation(), academic.ListAcademicUnitsQuery{NameQuery: pageQuery},
		)
	}
	if err != nil {
		return route.Result{}, err
	}
	return academicNameListResult(page, academicUnitResponseFromModel, func(unit *model.AcademicUnit) (string, string) {
		return unit.Name, unit.ID.String()
	})
}

func (h academicUnitHandlers) createRoot(request route.Request) (route.Result, error) {
	var body createAcademicUnitRequest
	if err := request.DecodeJSON(&body); err != nil {
		return route.Result{}, err
	}

	saved, err := h.app.CreateAcademicUnit(
		request.Context,
		request.Invocation(),
		academic.CreateAcademicUnitCommand{
			Name:           body.Name,
			DisplayName:    body.DisplayName,
			Description:    body.Description,
			IdempotencyKey: request.IdempotencyKey,
		},
	)
	if err != nil {
		return route.Result{}, err
	}

	return route.JSON(http.StatusCreated, academicUnitResponseFromModel(saved)), nil
}

func (h academicUnitHandlers) get(request route.Request) (route.Result, error) {
	id, err := route.PathID[model.AcademicUnitID](request, "academic_unit_id")
	if err != nil {
		return route.Result{}, err
	}
	unit, err := h.app.GetAcademicUnit(
		request.Context,
		request.Invocation(),
		id,
	)
	if err != nil {
		return route.Result{}, err
	}
	return route.JSON(http.StatusOK, academicUnitResponseFromModel(unit)), nil
}

func (h academicUnitHandlers) patch(request route.Request) (route.Result, error) {
	id, err := route.PathID[model.AcademicUnitID](request, "academic_unit_id")
	if err != nil {
		return route.Result{}, err
	}

	var body updateAcademicUnitRequest
	if err = request.DecodeJSON(&body); err != nil {
		return route.Result{}, err
	}
	if err := validateAcademicRevision(body.ExpectedRevision); err != nil {
		return route.Result{}, err
	}

	unit, err := h.app.UpdateAcademicUnit(
		request.Context,
		request.Invocation(),
		academic.UpdateAcademicUnitCommand{
			ExpectedRevision: body.ExpectedRevision.ValuePointer(),
			ID:               id,
			ParentID:         route.OptionalID[model.AcademicUnitID](body.ParentID),
			Name:             body.Name,
			DisplayName:      body.DisplayName,
			Description:      body.Description,
		},
	)
	if err != nil {
		return route.Result{}, err
	}

	return route.JSON(http.StatusOK, academicUnitResponseFromModel(unit)), nil
}

func (h academicUnitHandlers) archive(request route.Request) (route.Result, error) {
	id, err := route.PathID[model.AcademicUnitID](request, "academic_unit_id")
	if err != nil {
		return route.Result{}, err
	}
	expectedRevision, err := academicRevision(request)
	if err != nil {
		return route.Result{}, err
	}
	err = h.app.ArchiveAcademicUnit(
		request.Context,
		request.Invocation(),
		academic.ArchiveAcademicUnitCommand{ExpectedRevision: expectedRevision, ID: id},
	)
	if err != nil {
		return route.Result{}, err
	}
	return route.NoContent(), nil
}

func (h academicUnitHandlers) listChildren(request route.Request) (route.Result, error) {
	id, err := route.PathID[model.AcademicUnitID](request, "academic_unit_id")
	if err != nil {
		return route.Result{}, err
	}
	pageQuery, err := academicNamePageQuery(request)
	if err != nil {
		return route.Result{}, err
	}
	page, err := h.app.ListAcademicUnits(
		request.Context,
		request.Invocation(),
		academic.ListAcademicUnitsQuery{ParentID: id, NameQuery: pageQuery},
	)
	if err != nil {
		return route.Result{}, err
	}
	return academicNameListResult(page, academicUnitResponseFromModel, func(unit *model.AcademicUnit) (string, string) {
		return unit.Name, unit.ID.String()
	})
}

func (h academicUnitHandlers) createChild(request route.Request) (route.Result, error) {
	id, err := route.PathID[model.AcademicUnitID](request, "academic_unit_id")
	if err != nil {
		return route.Result{}, err
	}

	var body createAcademicUnitRequest
	if err = request.DecodeJSON(&body); err != nil {
		return route.Result{}, err
	}

	saved, err := h.app.CreateAcademicUnit(
		request.Context,
		request.Invocation(),
		academic.CreateAcademicUnitCommand{
			ParentID:    id,
			Name:        body.Name,
			DisplayName: body.DisplayName,
			Description: body.Description,
		},
	)
	if err != nil {
		return route.Result{}, err
	}

	return route.JSON(http.StatusCreated, academicUnitResponseFromModel(saved)), nil
}

func academicUnitResponseFromModel(unit *model.AcademicUnit) academicUnitResponse {
	if unit == nil {
		return academicUnitResponse{}
	}
	return academicUnitResponse{
		Revision:      unit.Revision,
		ID:            unit.ID.String(),
		CreatedAt:     model.FormatRFC3339(unit.CreatedAt),
		UpdatedAt:     model.FormatRFC3339(unit.UpdatedAt),
		ArchivedAt:    unit.ArchivedAt.FormatRFC3339(),
		InstitutionID: unit.InstitutionID.String(),
		ParentID:      unit.ParentID.String(),
		Name:          unit.Name,
		DisplayName:   unit.DisplayName,
		Description:   unit.Description,
	}
}

// AcademicUnitApplication is the application port its handlers call; it reads and changes Academic Units.
type AcademicUnitApplication interface {
	GetAcademicUnit(context.Context, invocation.Invocation, model.AcademicUnitID) (*model.AcademicUnit, error)
	ListAcademicUnits(context.Context, invocation.Invocation, academic.ListAcademicUnitsQuery) (paging.Page[*model.AcademicUnit], error)
	SearchAcademicUnits(context.Context, invocation.Invocation, academic.SearchAcademicUnitsQuery) (paging.Page[*model.AcademicUnit], error)
	CreateAcademicUnit(context.Context, invocation.Invocation, academic.CreateAcademicUnitCommand) (*model.AcademicUnit, error)
	UpdateAcademicUnit(context.Context, invocation.Invocation, academic.UpdateAcademicUnitCommand) (*model.AcademicUnit, error)
	ArchiveAcademicUnit(context.Context, invocation.Invocation, academic.ArchiveAcademicUnitCommand) error
}
