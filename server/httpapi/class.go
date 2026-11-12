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
	"github.com/sudosylabs/proctor/server/store"
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

// classListItemResponse is one Class of a Class listing with its current
// student count, present only for a caller who may view the Classes'
// members. The count stops at 10,000 and then is a lower bound.
type classListItemResponse struct {
	classResponse
	StudentCount             *int  `json:"student_count,omitempty"`
	StudentCountIsLowerBound *bool `json:"student_count_is_lower_bound,omitempty"`
}

type createClassRequest struct {
	AcademicPeriodID string `json:"academic_period_id"`
	Name             string `json:"name"`
	DisplayName      string `json:"display_name"`
	Description      string `json:"description"`
}
type updateClassRequest struct {
	ExpectedRevision route.Optional[int64]  `json:"expected_revision"`
	ProgrammeLevelID route.Optional[string] `json:"programme_level_id"`
	AcademicPeriodID route.Optional[string] `json:"academic_period_id"`
	Name             route.Optional[string] `json:"name"`
	DisplayName      route.Optional[string] `json:"display_name"`
	Description      route.Optional[string] `json:"description"`
}

type classHandlers struct {
	app ClassApplication
}

func classResource(app ClassApplication) route.Resource {
	h := classHandlers{app: app}
	return route.NewResource(
		"classes",
		route.Endpoint{
			Method: http.MethodGet,
			Path:   "/academic-units/{academic_unit_id}/classes",
			Auth:   route.AuthPrincipalRequired,
			Errors: classReadErrorCodes(),
			Handle: h.search,
		},
		route.Endpoint{
			Method: http.MethodGet,
			Path:   "/programme-levels/{programme_level_id}/classes",
			Auth:   route.AuthPrincipalRequired,
			Errors: classReadErrorCodes(),
			Handle: h.list,
		},
		route.Endpoint{
			Method: http.MethodPost,
			Path:   "/programme-levels/{programme_level_id}/classes",
			Auth:   route.AuthPrincipalRequired,
			Errors: classMutationErrorCodes("class.invalid", "class.conflict"),
			Handle: h.create,
		},
		route.Endpoint{
			Method: http.MethodGet,
			Path:   "/classes/{class_id}",
			Auth:   route.AuthPrincipalRequired,
			Errors: classReadErrorCodes(),
			Handle: h.get,
		},
		route.Endpoint{
			Method: http.MethodPatch,
			Path:   "/classes/{class_id}",
			Auth:   route.AuthPrincipalRequired,
			Errors: classMutationErrorCodes("class.invalid", "class.conflict"),
			Handle: h.patch,
		},
		route.Endpoint{
			Method: http.MethodDelete,
			Path:   "/classes/{class_id}",
			Auth:   route.AuthPrincipalRequired,
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

func (h classHandlers) search(request route.Request) (route.Result, error) {
	academicUnitID, err := route.PathID[model.AcademicUnitID](request, "academic_unit_id")
	if err != nil {
		return route.Result{}, err
	}
	pageQuery, err := academicNameDirectedPageQuery(request)
	if err != nil {
		return route.Result{}, err
	}
	page, err := h.app.SearchClasses(
		request.Context,
		request.Invocation(),
		academic.SearchClassesQuery{
			AcademicUnitID: academicUnitID,
			Query:          request.HTTP.URL.Query().Get("q"),
			NameQuery:      pageQuery,
		},
	)
	if err != nil {
		return route.Result{}, err
	}
	return classListResult(page, pageQuery)
}

func (h classHandlers) list(request route.Request) (route.Result, error) {
	programmeLevelID, err := route.PathID[model.ProgrammeLevelID](request, "programme_level_id")
	if err != nil {
		return route.Result{}, err
	}
	pageQuery, err := academicNameDirectedPageQuery(request)
	if err != nil {
		return route.Result{}, err
	}
	page, err := h.app.ListClasses(
		request.Context,
		request.Invocation(),
		academic.ListClassesQuery{ProgrammeLevelID: programmeLevelID, NameQuery: pageQuery},
	)
	if err != nil {
		return route.Result{}, err
	}
	return classListResult(page, pageQuery)
}

// classListResult presents one name-ordered Class page with its first-page
// total and, when the application read them, each Class's student count.
func classListResult(page academic.ClassPage, query paging.NameQuery) (route.Result, error) {
	present := func(class *model.Class) classListItemResponse {
		item := classListItemResponse{classResponse: classResponseFromModel(class)}
		if page.StudentCounts != nil {
			count := page.StudentCounts[class.ID]
			lowerBound := count >= store.MaximumListCount
			item.StudentCount, item.StudentCountIsLowerBound = &count, &lowerBound
		}
		return item
	}
	return academicNameCountedResult(page.Counted, query, present, func(class *model.Class) (string, string) {
		return class.Name, class.ID.String()
	})
}

func (h classHandlers) create(request route.Request) (route.Result, error) {
	programmeLevelID, err := route.PathID[model.ProgrammeLevelID](request, "programme_level_id")
	if err != nil {
		return route.Result{}, err
	}
	var body createClassRequest
	if err = request.DecodeJSON(&body); err != nil {
		return route.Result{}, err
	}
	class, err := h.app.CreateClass(
		request.Context,
		request.Invocation(),
		academic.CreateClassCommand{
			ProgrammeLevelID: programmeLevelID,
			AcademicPeriodID: model.AcademicPeriodID(body.AcademicPeriodID),
			Name:             body.Name,
			DisplayName:      body.DisplayName,
			Description:      body.Description,
		},
	)
	if err != nil {
		return route.Result{}, err
	}
	return route.JSON(http.StatusCreated, classResponseFromModel(class)), nil
}

func (h classHandlers) get(request route.Request) (route.Result, error) {
	id, err := route.PathID[model.ClassID](request, "class_id")
	if err != nil {
		return route.Result{}, err
	}
	class, err := h.app.GetClass(
		request.Context,
		request.Invocation(),
		id,
	)
	if err != nil {
		return route.Result{}, err
	}
	return route.JSON(http.StatusOK, classResponseFromModel(class)), nil
}

func (h classHandlers) patch(request route.Request) (route.Result, error) {
	id, err := route.PathID[model.ClassID](request, "class_id")
	if err != nil {
		return route.Result{}, err
	}
	var body updateClassRequest
	if err = request.DecodeJSON(&body); err != nil {
		return route.Result{}, err
	}
	if validateErr := validateAcademicRevision(body.ExpectedRevision); validateErr != nil {
		return route.Result{}, validateErr
	}
	class, err := h.app.UpdateClass(
		request.Context,
		request.Invocation(),
		academic.UpdateClassCommand{ExpectedRevision: body.ExpectedRevision.ValuePointer(),
			ID:               id,
			ProgrammeLevelID: route.OptionalID[model.ProgrammeLevelID](body.ProgrammeLevelID.ValuePointer()),
			AcademicPeriodID: route.OptionalID[model.AcademicPeriodID](body.AcademicPeriodID.ValuePointer()),
			Name:             body.Name.ValuePointer(),
			DisplayName:      body.DisplayName.ValuePointer(),
			Description:      body.Description.ValuePointer(),
		},
	)
	if err != nil {
		return route.Result{}, err
	}
	return route.JSON(http.StatusOK, classResponseFromModel(class)), nil
}

func (h classHandlers) archive(request route.Request) (route.Result, error) {
	id, err := route.PathID[model.ClassID](request, "class_id")
	if err != nil {
		return route.Result{}, err
	}
	expectedRevision, err := academicRevision(request)
	if err != nil {
		return route.Result{}, err
	}
	if err := h.app.ArchiveClass(
		request.Context,
		request.Invocation(),
		academic.ArchiveClassCommand{ExpectedRevision: expectedRevision, ID: id},
	); err != nil {
		return route.Result{}, err
	}
	return route.NoContent(), nil
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
	GetClass(context.Context, invocation.Invocation, model.ClassID) (*model.Class, error)
	ListClasses(context.Context, invocation.Invocation, academic.ListClassesQuery) (academic.ClassPage, error)
	SearchClasses(context.Context, invocation.Invocation, academic.SearchClassesQuery) (academic.ClassPage, error)
	CreateClass(context.Context, invocation.Invocation, academic.CreateClassCommand) (*model.Class, error)
	UpdateClass(context.Context, invocation.Invocation, academic.UpdateClassCommand) (*model.Class, error)
	ArchiveClass(context.Context, invocation.Invocation, academic.ArchiveClassCommand) error
}
