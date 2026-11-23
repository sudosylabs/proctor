// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package httpapi

import (
	"net/http"

	"github.com/sudosylabs/proctor/server/app/exam"
	"github.com/sudosylabs/proctor/server/app/people"
	"github.com/sudosylabs/proctor/server/httpapi/route"
	"github.com/sudosylabs/proctor/server/model"
)

type copyExamRequest struct {
	Title          string `json:"title"`
	AcademicUnitID string `json:"academic_unit_id"`
}

type examCopyHandlers struct {
	app    *exam.Copier
	people *people.Resolver
}

func examCopyResource(app *exam.Copier, resolver *people.Resolver) route.Resource {
	h := examCopyHandlers{app: app, people: resolver}
	return route.NewResource(
		"exam-copies",
		route.Endpoint{
			Method:      http.MethodPost,
			Path:        "/exams/{exam_id}/copy",
			Auth:        route.AuthPrincipalRequired,
			Idempotency: route.IdempotencyRequired,
			Errors: []string{
				"authorization.request.invalid",
				"authorization.unavailable",
				"audit.unavailable",
				"request.invalid",
				"resource.not_found",
				"exam.invalid",
				"exam.conflict",
				"exam.revision.capacity_exceeded",
				"exam.unavailable",
				"idempotency.key_required",
				"idempotency.key_invalid",
				"idempotency.conflict",
				"idempotency.in_progress",
			},
			Handle: h.copy,
		},
	)
}

func (h examCopyHandlers) copy(request route.Request) (route.Result, error) {
	sourceID, err := route.PathID[model.ExamID](request, "exam_id")
	if err != nil {
		return route.Result{}, err
	}
	var body copyExamRequest
	if err = request.DecodeJSON(&body); err != nil {
		return route.Result{}, err
	}
	unitID, err := model.ParseAcademicUnitID(body.AcademicUnitID)
	if err != nil {
		return route.Result{}, route.InvalidRequest(err)
	}

	view, err := h.app.CopyExam(request.Context, request.Invocation(), exam.CopyCommand{
		SourceExamID: sourceID, Title: body.Title, AcademicUnitID: unitID, IdempotencyKey: request.IdempotencyKey,
	})
	if err != nil {
		return route.Result{}, err
	}

	refs, err := namePeople(request, h.people, view.Exam.CreatorUserID, view.Exam.OwnerUserID, view.Draft.UpdatedByUserID)
	if err != nil {
		return route.Result{}, err
	}
	response := examResponseFromView(view)
	response.Exam = response.Exam.named(refs)
	response.Draft.UpdatedBy = personRef(refs, view.Draft.UpdatedByUserID)
	return route.JSON(http.StatusCreated, response), nil
}
