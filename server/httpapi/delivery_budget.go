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
	"github.com/sudosylabs/proctor/server/model"
)

func deliveryBudgetResource(app ExamAttemptApplication) apiResource {
	h := examAttemptHandlers{app: app}
	return newResource(
		"delivery-budget",
		endpoint{
			Method: http.MethodGet,
			Path:   "/exam-attempts/{exam_attempt_id}/delivery-limits",
			Auth:   AuthSessionRequired,
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
		endpoint{
			Method:       http.MethodPost,
			Path:         "/exam-attempts/{exam_attempt_id}/delivery-limits/stop-details",
			Auth:         AuthSessionRequired,
			Idempotency:  IdempotencyRequired,
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
func (h examAttemptHandlers) deliveryBudget(request operationRequest) (operationResult, error) {
	query, err := browserSourceQuery(request, true)
	if err != nil {
		return operationResult{}, err
	}

	value, err := h.app.DeliveryBudget(
		request.context,
		request.invocation(),
		attempt.DeliveryBudgetQuery{Access: query.Access, ParticipationID: query.ParticipationID},
	)
	if err != nil {
		return operationResult{}, err
	}
	if value == nil || value.Validate() != nil {
		return operationResult{}, errUnpresentableResult
	}

	return jsonResult(http.StatusOK, value).withHeaders(noStoreHeaders()), nil
}
func (h examAttemptHandlers) stopDeliveryDetails(request operationRequest) (operationResult, error) {
	attemptID, err := pathID[model.ExamAttemptID](request, "exam_attempt_id")
	if err != nil {
		return operationResult{}, err
	}

	headers, err := candidateAttemptAccessHeaders(request.http)
	if err != nil {
		return operationResult{}, invalidRequestError(err)
	}

	var body model.StopDeliveryDetails
	if err = request.decodeJSON(&body); err != nil {
		return operationResult{}, err
	}
	if body.Validate() != nil {
		return operationResult{}, invalidRequestError(nil)
	}

	value, err := h.app.StopDeliveryDetails(request.context, request.invocation(), attempt.StopDeliveryDetailsCommand{
		Access: attempt.CandidateAccess{
			AttemptID:            attemptID,
			ConnectionID:         headers.ConnectionID,
			ContinuityCredential: headers.ContinuityCredential,
		},
		Request:        body,
		IdempotencyKey: request.idempotencyKey,
	})
	if err != nil {
		return operationResult{}, err
	}
	if value == nil || value.Validate(body.Family) != nil {
		return operationResult{}, errUnpresentableResult
	}

	return jsonResult(http.StatusOK, value).withHeaders(noStoreHeaders()), nil
}
