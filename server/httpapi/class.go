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

type classResourceModule struct {
	classes ClassApplication
}

func classResource(classes ClassApplication) apiResource {
	module := classResourceModule{classes: classes}
	return newResource(
		"classes",
		principalRoute(
			http.MethodGet,
			path("/academic-units/{academic_unit_id}/classes"),
			classReadErrorCodes(),
			module.search,
		),
		principalRoute(
			http.MethodGet,
			path("/programme-levels/{programme_level_id}/classes"),
			classReadErrorCodes(),
			module.list,
		),
		principalRoute(
			http.MethodPost,
			path("/programme-levels/{programme_level_id}/classes"),
			classMutationErrorCodes("class.invalid", "class.conflict"),
			module.create,
		),
		principalRoute(
			http.MethodGet,
			path("/classes/{class_id}"),
			classReadErrorCodes(),
			module.get,
		),
		principalRoute(
			http.MethodPatch,
			path("/classes/{class_id}"),
			classMutationErrorCodes("class.invalid", "class.conflict"),
			module.patch,
		),
		principalRoute(
			http.MethodDelete,
			path("/classes/{class_id}"),
			classMutationErrorCodes("class.conflict"),
			module.archive,
		),
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

func (m classResourceModule) search(request operationRequest) (operationResult, error) {
	academicUnitID, err := request.params.RequireAcademicUnitID()
	if err != nil {
		return operationResult{}, err
	}
	pageQuery, err := request.academicNamePageQuery()
	if err != nil {
		return operationResult{}, err
	}
	page, err := m.classes.SearchClasses(
		request.context,
		request.invocation(),
		academic.SearchClassesQuery{
			AcademicUnitID: academicUnitID,
			Query:          request.request.URL.Query().Get("q"),
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

func (m classResourceModule) list(request operationRequest) (operationResult, error) {
	programmeLevelID, err := request.params.RequireProgrammeLevelID()
	if err != nil {
		return operationResult{}, err
	}
	pageQuery, err := request.academicNamePageQuery()
	if err != nil {
		return operationResult{}, err
	}
	page, err := m.classes.ListClasses(
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

func (m classResourceModule) create(request operationRequest) (operationResult, error) {
	programmeLevelID, err := request.params.RequireProgrammeLevelID()
	if err != nil {
		return operationResult{}, err
	}
	var body createClassRequest
	if err = request.decodeJSON(&body); err != nil {
		return operationResult{}, err
	}
	class, err := m.classes.CreateClass(
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

func (m classResourceModule) get(request operationRequest) (operationResult, error) {
	id, err := request.params.RequireClassID()
	if err != nil {
		return operationResult{}, err
	}
	class, err := m.classes.GetClass(
		request.context,
		request.invocation(),
		academic.GetClassQuery{ID: id},
	)
	if err != nil {
		return operationResult{}, err
	}
	return jsonResult(http.StatusOK, classResponseFromModel(class)), nil
}

func (m classResourceModule) patch(request operationRequest) (operationResult, error) {
	id, err := request.params.RequireClassID()
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
	class, err := m.classes.UpdateClass(
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

func (m classResourceModule) archive(request operationRequest) (operationResult, error) {
	id, err := request.params.RequireClassID()
	if err != nil {
		return operationResult{}, err
	}
	expectedRevision, err := request.academicRevision()
	if err != nil {
		return operationResult{}, err
	}
	if err := m.classes.ArchiveClass(
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
