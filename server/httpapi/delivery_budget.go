// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------
package httpapi

import (
	"net/http"

	"github.com/sudosylabs/proctor/server/app/exam/attempt"
	"github.com/sudosylabs/proctor/server/httpapi/route"
	"github.com/sudosylabs/proctor/server/model"
)

func deliveryBudgetResource(app ExamAttemptApplication) route.Resource {
	h := examAttemptHandlers{app: app}
	return route.NewResource(
		"delivery-budget",
		route.Endpoint{
			Method: http.MethodGet,
			Path:   "/exam-attempts/{exam_attempt_id}/delivery-limits",
			Auth:   route.AuthSessionRequired,
			Errors: []string{
				"exam.delivery.control_rate_limited",
				"request.invalid",
				"resource.not_found",
				"exam.attempt.invalid",
				"exam.attempt.registered_desktop_required",
				"exam.attempt.connection_closed",
				"exam.attempt.connection_lost",
				"exam.attempt.sitting_not_open",
				"exam.attempt.unavailable",
				"exam.delivery.upload_expired",
				"exam.delivery.declaration_conflict",
				"exam.delivery.sequence_limit",
				"exam.delivery.detail_budget_exhausted",
				"exam.delivery.metadata_capacity",
				"exam.delivery.summary_rate_limited",
				"idempotency.conflict",
				"idempotency.in_progress",
			},
			Handle: h.deliveryBudget,
		},
		route.Endpoint{
			Method:       http.MethodPost,
			Path:         "/exam-attempts/{exam_attempt_id}/delivery-limits/stop-details",
			Auth:         route.AuthSessionRequired,
			Idempotency:  route.IdempotencyRequired,
			MaxBodyBytes: 2048,
			Errors: []string{
				"audit.unavailable",
				"exam.delivery.control_rate_limited",
				"request.invalid",
				"resource.not_found",
				"exam.attempt.invalid",
				"exam.attempt.registered_desktop_required",
				"exam.attempt.connection_closed",
				"exam.attempt.connection_lost",
				"exam.attempt.sitting_not_open",
				"exam.attempt.unavailable",
				"exam.delivery.upload_expired",
				"exam.delivery.declaration_conflict",
				"exam.delivery.sequence_limit",
				"exam.delivery.detail_budget_exhausted",
				"exam.delivery.metadata_capacity",
				"exam.delivery.summary_rate_limited",
				"idempotency.conflict",
				"idempotency.in_progress",
				"idempotency.key_required",
				"idempotency.key_invalid",
			},
			Handle: h.stopDeliveryDetails,
		},
	)
}
func (h examAttemptHandlers) deliveryBudget(request route.Request) (route.Result, error) {
	query, err := browserSourceQuery(request, true)
	if err != nil {
		return route.Result{}, err
	}

	value, err := h.app.DeliveryBudget(
		request.Context,
		request.Invocation(),
		attempt.DeliveryBudgetQuery{Access: query.Access, ParticipationID: query.ParticipationID},
	)
	if err != nil {
		return route.Result{}, err
	}
	if value == nil || value.Validate() != nil {
		return route.Result{}, route.ErrUnpresentable
	}

	return route.JSON(http.StatusOK, value).WithHeaders(noStoreHeaders()), nil
}
func (h examAttemptHandlers) stopDeliveryDetails(request route.Request) (route.Result, error) {
	attemptID, err := route.PathID[model.ExamAttemptID](request, "exam_attempt_id")
	if err != nil {
		return route.Result{}, err
	}

	headers, err := candidateAttemptAccessHeaders(request.HTTP)
	if err != nil {
		return route.Result{}, route.InvalidRequest(err)
	}

	var body model.StopDeliveryDetails
	if err = request.DecodeJSON(&body); err != nil {
		return route.Result{}, err
	}
	if body.Validate() != nil {
		return route.Result{}, route.InvalidRequest(nil)
	}

	value, err := h.app.StopDeliveryDetails(request.Context, request.Invocation(), attempt.StopDeliveryDetailsCommand{
		Access: attempt.CandidateAccess{
			AttemptID:            attemptID,
			ConnectionID:         headers.ConnectionID,
			ContinuityCredential: headers.ContinuityCredential,
		},
		Request:        body,
		IdempotencyKey: request.IdempotencyKey,
	})
	if err != nil {
		return route.Result{}, err
	}
	if value == nil || value.Validate(body.Family) != nil {
		return route.Result{}, route.ErrUnpresentable
	}

	return route.JSON(http.StatusOK, value).WithHeaders(noStoreHeaders()), nil
}
