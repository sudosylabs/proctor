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

type academicPeriodResponse struct {
	Revision    int64  `json:"revision"`
	ID          string `json:"id"`
	CreatedAt   string `json:"created_at"`
	UpdatedAt   string `json:"updated_at"`
	ArchivedAt  string `json:"archived_at,omitempty"`
	OwnerType   string `json:"owner_type"`
	OwnerID     string `json:"owner_id"`
	Name        string `json:"name"`
	DisplayName string `json:"display_name"`
	Description string `json:"description"`
	StartAt     string `json:"start_at"`
	EndAt       string `json:"end_at"`
}

type createAcademicPeriodRequest struct {
	OwnerType   string `json:"owner_type"`
	OwnerID     string `json:"owner_id"`
	Name        string `json:"name"`
	DisplayName string `json:"display_name"`
	Description string `json:"description"`
	StartAt     string `json:"start_at"`
	EndAt       string `json:"end_at"`
}

type updateAcademicPeriodRequest struct {
	ExpectedRevision route.Optional[int64]  `json:"expected_revision"`
	Name             route.Optional[string] `json:"name"`
	DisplayName      route.Optional[string] `json:"display_name"`
	Description      route.Optional[string] `json:"description"`
	StartAt          route.Optional[string] `json:"start_at"`
	EndAt            route.Optional[string] `json:"end_at"`
}

type academicPeriodHandlers struct {
	app AcademicPeriodApplication
}

func academicPeriodResource(app AcademicPeriodApplication) route.Resource {
	h := academicPeriodHandlers{app: app}
	collection := "/academic-periods"
	member := "/academic-periods/{academic_period_id}"
	return route.NewResource(
		"academic-periods",
		route.Endpoint{
			Method: http.MethodGet,
			Path:   collection,
			Auth:   route.AuthPrincipalRequired,
			Errors: academicReadErrorCodes("request.invalid", "resource.not_found"),
			Handle: h.list,
		},
		route.Endpoint{
			Method:      http.MethodPost,
			Path:        collection,
			Auth:        route.AuthPrincipalRequired,
			Idempotency: route.IdempotencyOptional,
			Errors: academicMutationErrorCodes(
				"request.invalid",
				"resource.not_found",
				"academic_period.invalid",
				"academic_period.conflict",
				"idempotency.key_invalid",
				"idempotency.conflict",
				"idempotency.in_progress",
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
				"academic_period.invalid",
				"academic_period.conflict",
			),
			Handle: h.patch,
		},
		route.Endpoint{
			Method: http.MethodDelete,
			Path:   member,
			Auth:   route.AuthPrincipalRequired,
			Errors: academicMutationErrorCodes(
				"request.invalid", "resource.not_found", "academic_period.conflict",
			),
			Handle: h.archive,
		},
	)
}

func (h academicPeriodHandlers) list(request route.Request) (route.Result, error) {
	requested, err := request.Page()
	if err != nil {
		return route.Result{}, err
	}

	cursor, present, err := route.DecodePageCursor(requested, academicPeriodCursorSpec())
	if err != nil {
		return route.Result{}, err
	}

	query := academic.ListAcademicPeriodsQuery{Query: request.HTTP.URL.Query().Get("q"), Limit: requested.Limit}
	if present {
		query.AfterStartAt, query.AfterName, query.AfterID = academicPeriodCursorStart(cursor), cursor.Name, model.AcademicPeriodID(cursor.ID)
	}

	page, err := h.app.ListAcademicPeriods(request.Context, request.Invocation(), query)
	if err != nil {
		return route.Result{}, err
	}

	response, err := route.NewList(
		page.Items,
		page.HasMore,
		academicPeriodResponseFromModel,
		func(last *model.AcademicPeriod) (string, error) {
			return route.EncodeCursor(
				academicPeriodCursor{StartAt: model.FormatRFC3339(last.StartsAt), Name: last.Name, ID: last.ID.String()},
				academicPeriodCursorSpec(),
			)
		},
	)
	if err != nil {
		return route.Result{}, err
	}

	return route.JSON(http.StatusOK, response), nil
}

func (h academicPeriodHandlers) create(request route.Request) (route.Result, error) {
	var body createAcademicPeriodRequest
	if err := request.DecodeJSON(&body); err != nil {
		return route.Result{}, err
	}

	startAt, err := route.WireTimeMillis("start_at", body.StartAt)
	if err != nil {
		return route.Result{}, err
	}

	endAt, err := route.WireTimeMillis("end_at", body.EndAt)
	if err != nil {
		return route.Result{}, err
	}

	period, err := h.app.CreateAcademicPeriod(
		request.Context,
		request.Invocation(),
		academic.CreateAcademicPeriodCommand{
			OwnerType:      body.OwnerType,
			OwnerID:        body.OwnerID,
			Name:           body.Name,
			DisplayName:    body.DisplayName,
			Description:    body.Description,
			StartAt:        startAt,
			EndAt:          endAt,
			IdempotencyKey: request.IdempotencyKey,
		},
	)
	if err != nil {
		return route.Result{}, err
	}

	return route.JSON(http.StatusCreated, academicPeriodResponseFromModel(period)), nil
}

func (h academicPeriodHandlers) get(request route.Request) (route.Result, error) {
	id, err := route.PathID[model.AcademicPeriodID](request, "academic_period_id")
	if err != nil {
		return route.Result{}, err
	}
	period, err := h.app.GetAcademicPeriod(
		request.Context, request.Invocation(), id,
	)
	if err != nil {
		return route.Result{}, err
	}
	return route.JSON(http.StatusOK, academicPeriodResponseFromModel(period)), nil
}

func (h academicPeriodHandlers) patch(request route.Request) (route.Result, error) {
	id, err := route.PathID[model.AcademicPeriodID](request, "academic_period_id")
	if err != nil {
		return route.Result{}, err
	}

	var body updateAcademicPeriodRequest
	if err = request.DecodeJSON(&body); err != nil {
		return route.Result{}, err
	}
	if err := validateAcademicRevision(body.ExpectedRevision); err != nil {
		return route.Result{}, err
	}

	startAt, err := route.OptionalWireTimeMillis("start_at", body.StartAt)
	if err != nil {
		return route.Result{}, err
	}

	endAt, err := route.OptionalWireTimeMillis("end_at", body.EndAt)
	if err != nil {
		return route.Result{}, err
	}

	period, err := h.app.UpdateAcademicPeriod(
		request.Context,
		request.Invocation(),
		academic.UpdateAcademicPeriodCommand{
			ExpectedRevision: body.ExpectedRevision.ValuePointer(),
			ID:               id,
			Name:             body.Name.ValuePointer(),
			DisplayName:      body.DisplayName.ValuePointer(),
			Description:      body.Description.ValuePointer(),
			StartAt:          startAt,
			EndAt:            endAt,
		},
	)
	if err != nil {
		return route.Result{}, err
	}

	return route.JSON(http.StatusOK, academicPeriodResponseFromModel(period)), nil
}

func (h academicPeriodHandlers) archive(request route.Request) (route.Result, error) {
	id, err := route.PathID[model.AcademicPeriodID](request, "academic_period_id")
	if err != nil {
		return route.Result{}, err
	}
	expectedRevision, err := academicRevision(request)
	if err != nil {
		return route.Result{}, err
	}
	if err := h.app.ArchiveAcademicPeriod(
		request.Context, request.Invocation(), academic.ArchiveAcademicPeriodCommand{ExpectedRevision: expectedRevision, ID: id},
	); err != nil {
		return route.Result{}, err
	}
	return route.NoContent(), nil
}

func academicPeriodResponseFromModel(period *model.AcademicPeriod) academicPeriodResponse {
	if period == nil {
		return academicPeriodResponse{}
	}
	return academicPeriodResponse{
		Revision:    period.Revision,
		ID:          period.ID.String(),
		CreatedAt:   model.FormatRFC3339(period.CreatedAt),
		UpdatedAt:   model.FormatRFC3339(period.UpdatedAt),
		ArchivedAt:  period.ArchivedAt.FormatRFC3339(),
		OwnerType:   string(period.Owner.Type()),
		OwnerID:     period.Owner.ID(),
		Name:        period.Name,
		DisplayName: period.DisplayName,
		Description: period.Description,
		StartAt:     model.FormatRFC3339(period.StartsAt),
		EndAt:       model.FormatRFC3339(period.EndsAt),
	}
}

// AcademicPeriodApplication is the application port its handlers call; it reads and changes Academic Periods.
type AcademicPeriodApplication interface {
	GetAcademicPeriod(context.Context, invocation.Invocation, model.AcademicPeriodID) (*model.AcademicPeriod, error)
	ListAcademicPeriods(context.Context, invocation.Invocation, academic.ListAcademicPeriodsQuery) (paging.Page[*model.AcademicPeriod], error)
	CreateAcademicPeriod(context.Context, invocation.Invocation, academic.CreateAcademicPeriodCommand) (*model.AcademicPeriod, error)
	UpdateAcademicPeriod(context.Context, invocation.Invocation, academic.UpdateAcademicPeriodCommand) (*model.AcademicPeriod, error)
	ArchiveAcademicPeriod(context.Context, invocation.Invocation, academic.ArchiveAcademicPeriodCommand) error
}
