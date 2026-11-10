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

type classResponse struct {
	Revision         int64  `json:"revision"`
	ID               string `json:"id"`
	CreatedAt        string `json:"created_at"`
	UpdatedAt        string `json:"updated_at"`
	ArchivedAt       string `json:"archived_at,omitempty"`
	ProgrammeLevelID string `json:"programme_level_id"`
	AcademicPeriodID string `json:"academic_period_id"`
	Name             string `json:"name"`
	DisplayName      string `json:"display_name"`
	Description      string `json:"description"`
}
type createClassRequest struct {
	AcademicPeriodID string `json:"academic_period_id"`
	Name             string `json:"name"`
	DisplayName      string `json:"display_name"`
	Description      string `json:"description"`
}
type updateClassRequest struct {
	ExpectedRevision Optional[int64]  `json:"expected_revision"`
	ProgrammeLevelID Optional[string] `json:"programme_level_id"`
	AcademicPeriodID Optional[string] `json:"academic_period_id"`
	Name             Optional[string] `json:"name"`
	DisplayName      Optional[string] `json:"display_name"`
	Description      Optional[string] `json:"description"`
}

type classHandlers struct {
	app ClassApplication
}

func classResource(app ClassApplication) apiResource {
	h := classHandlers{app: app}
	return newResource(
		"classes",
		endpoint{
			Method: http.MethodGet,
			Path:   "/academic-units/{academic_unit_id}/classes",
			Auth:   AuthPrincipalRequired,
			Errors: classReadErrorCodes(),
			Handle: h.search,
		},
		endpoint{
			Method: http.MethodGet,
			Path:   "/programme-levels/{programme_level_id}/classes",
			Auth:   AuthPrincipalRequired,
			Errors: classReadErrorCodes(),
			Handle: h.list,
		},
		endpoint{
			Method: http.MethodPost,
			Path:   "/programme-levels/{programme_level_id}/classes",
			Auth:   AuthPrincipalRequired,
			Errors: classMutationErrorCodes("class.invalid", "class.conflict"),
			Handle: h.create,
		},
		endpoint{
			Method: http.MethodGet,
			Path:   "/classes/{class_id}",
			Auth:   AuthPrincipalRequired,
			Errors: classReadErrorCodes(),
			Handle: h.get,
		},
		endpoint{
			Method: http.MethodPatch,
			Path:   "/classes/{class_id}",
			Auth:   AuthPrincipalRequired,
			Errors: classMutationErrorCodes("class.invalid", "class.conflict"),
			Handle: h.patch,
		},
		endpoint{
			Method: http.MethodDelete,
			Path:   "/classes/{class_id}",
			Auth:   AuthPrincipalRequired,
			Errors: classMutationErrorCodes("class.conflict"),
			Handle: h.archive,
		},
	)
}

func classReadErrorCodes() []string {
	return []string{
		"authorization.denied",
		"authorization.request.invalid",
		"authorization.unavailable",
		"request.invalid",
		"resource.not_found",
		"administration.unavailable",
	}
}

func classMutationErrorCodes(specific ...string) []string {
	codes := []string{
		"authorization.denied",
		"authorization.request.invalid",
		"authorization.unavailable",
		"audit.unavailable",
		"request.invalid",
		"resource.not_found",
	}
	codes = append(codes, specific...)
	return append(codes, "administration.unavailable")
}

func (h classHandlers) search(request operationRequest) (operationResult, error) {
	academicUnitID, err := pathID[model.AcademicUnitID](request, "academic_unit_id")
	if err != nil {
		return operationResult{}, err
	}
	pageQuery, err := request.academicNamePageQuery()
	if err != nil {
		return operationResult{}, err
	}
	page, err := h.app.SearchClasses(
		request.context,
		request.invocation(),
		academic.SearchClassesQuery{
			AcademicUnitID: academicUnitID,
			Query:          request.http.URL.Query().Get("q"),
			NameQuery:      pageQuery,
		},
	)
	if err != nil {
		return operationResult{}, err
	}
	return academicNameListResult(page, classResponseFromModel, func(class *model.Class) (string, string) {
		return class.Name, class.ID.String()
	})
}

func (h classHandlers) list(request operationRequest) (operationResult, error) {
	programmeLevelID, err := pathID[model.ProgrammeLevelID](request, "programme_level_id")
	if err != nil {
		return operationResult{}, err
	}
	pageQuery, err := request.academicNamePageQuery()
	if err != nil {
		return operationResult{}, err
	}
	page, err := h.app.ListClasses(
		request.context,
		request.invocation(),
		academic.ListClassesQuery{ProgrammeLevelID: programmeLevelID, NameQuery: pageQuery},
	)
	if err != nil {
		return operationResult{}, err
	}
	return academicNameListResult(page, classResponseFromModel, func(class *model.Class) (string, string) {
		return class.Name, class.ID.String()
	})
}

func (h classHandlers) create(request operationRequest) (operationResult, error) {
	programmeLevelID, err := pathID[model.ProgrammeLevelID](request, "programme_level_id")
	if err != nil {
		return operationResult{}, err
	}
	var body createClassRequest
	if err = request.decodeJSON(&body); err != nil {
		return operationResult{}, err
	}
	class, err := h.app.CreateClass(
		request.context,
		request.invocation(),
		academic.CreateClassCommand{
			ProgrammeLevelID: programmeLevelID,
			AcademicPeriodID: model.AcademicPeriodID(body.AcademicPeriodID),
			Name:             body.Name,
			DisplayName:      body.DisplayName,
			Description:      body.Description,
		},
	)
	if err != nil {
		return operationResult{}, err
	}
	return jsonResult(http.StatusCreated, classResponseFromModel(class)), nil
}

func (h classHandlers) get(request operationRequest) (operationResult, error) {
	id, err := pathID[model.ClassID](request, "class_id")
	if err != nil {
		return operationResult{}, err
	}
	class, err := h.app.GetClass(
		request.context,
		request.invocation(),
		academic.GetClassQuery{ID: id},
	)
	if err != nil {
		return operationResult{}, err
	}
	return jsonResult(http.StatusOK, classResponseFromModel(class)), nil
}

func (h classHandlers) patch(request operationRequest) (operationResult, error) {
	id, err := pathID[model.ClassID](request, "class_id")
	if err != nil {
		return operationResult{}, err
	}
	var body updateClassRequest
	if err = request.decodeJSON(&body); err != nil {
		return operationResult{}, err
	}
	if err := validateAcademicRevision(body.ExpectedRevision); err != nil {
		return operationResult{}, err
	}
	class, err := h.app.UpdateClass(
		request.context,
		request.invocation(),
		academic.UpdateClassCommand{ExpectedRevision: body.ExpectedRevision.ValuePointer(),
			ID:               id,
			ProgrammeLevelID: optionalIdentifier[model.ProgrammeLevelID](body.ProgrammeLevelID.ValuePointer()),
			AcademicPeriodID: optionalIdentifier[model.AcademicPeriodID](body.AcademicPeriodID.ValuePointer()),
			Name:             body.Name.ValuePointer(),
			DisplayName:      body.DisplayName.ValuePointer(),
			Description:      body.Description.ValuePointer(),
		},
	)
	if err != nil {
		return operationResult{}, err
	}
	return jsonResult(http.StatusOK, classResponseFromModel(class)), nil
}

func (h classHandlers) archive(request operationRequest) (operationResult, error) {
	id, err := pathID[model.ClassID](request, "class_id")
	if err != nil {
		return operationResult{}, err
	}
	expectedRevision, err := request.academicRevision()
	if err != nil {
		return operationResult{}, err
	}
	if err := h.app.ArchiveClass(
		request.context,
		request.invocation(),
		academic.ArchiveClassCommand{ExpectedRevision: expectedRevision, ID: id},
	); err != nil {
		return operationResult{}, err
	}
	return noContentResult(), nil
}

func classResponseFromModel(class *model.Class) classResponse {
	if class == nil {
		return classResponse{}
	}
	return classResponse{
		Revision:         class.Revision,
		ID:               class.ID.String(),
		CreatedAt:        model.FormatRFC3339(class.CreatedAt),
		UpdatedAt:        model.FormatRFC3339(class.UpdatedAt),
		ArchivedAt:       class.ArchivedAt.FormatRFC3339(),
		ProgrammeLevelID: class.ProgrammeLevelID.String(),
		AcademicPeriodID: class.AcademicPeriodID.String(),
		Name:             class.Name,
		DisplayName:      class.DisplayName,
		Description:      class.Description,
	}
}

// ClassApplication is the application port its handlers call; it reads and changes Classes.
type ClassApplication interface {
	GetClass(context.Context, invocation.Invocation, academic.GetClassQuery) (*model.Class, error)
	ListClasses(context.Context, invocation.Invocation, academic.ListClassesQuery) (paging.Page[*model.Class], error)
	SearchClasses(context.Context, invocation.Invocation, academic.SearchClassesQuery) (paging.Page[*model.Class], error)
	CreateClass(context.Context, invocation.Invocation, academic.CreateClassCommand) (*model.Class, error)
	UpdateClass(context.Context, invocation.Invocation, academic.UpdateClassCommand) (*model.Class, error)
	ArchiveClass(context.Context, invocation.Invocation, academic.ArchiveClassCommand) error
}
