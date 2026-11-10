// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package httpapi

import (
	"net/http"
	"strconv"

	"github.com/sudosylabs/proctor/server/app/exam/attempt"
	"github.com/sudosylabs/proctor/server/model"
)

func nativeDeliveryResource(app ExamAttemptApplication) apiResource {
	h := examAttemptHandlers{app: app}
	return newResource(
		"native-delivery",
		endpoint{
			Method:       http.MethodPost,
			Path:         "/exam-attempts/{exam_attempt_id}/security-batches",
			Auth:         AuthSessionRequired,
			Idempotency:  IdempotencyRequired,
			MaxBodyBytes: 262144,
			Errors: []string{
				"audit.unavailable",
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
				"exam.delivery.batch_conflict",
				"exam.delivery.replay_window_exceeded",
				"exam.delivery.pending_capacity",
				"exam.delivery.append_rate_limited",
			},
			Handle: h.appendNativeDelivery,
		},
		endpoint{
			Method: http.MethodGet,
			Path:   "/exam-attempts/{exam_attempt_id}/security-streams/{stream_id}",
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
			Handle: h.nativeDeliveryStatus,
		},
		endpoint{
			Method: http.MethodGet,
			Path:   "/exam-attempts/{exam_attempt_id}/security-streams/{stream_id}/receipts/{batch_sequence:sequence}",
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
			Handle: h.nativeDeliveryReceipt,
		},
		endpoint{
			Method:       http.MethodPost,
			Path:         "/exam-attempts/{exam_attempt_id}/security-streams/{stream_id}/gaps",
			Auth:         AuthSessionRequired,
			Idempotency:  IdempotencyRequired,
			MaxBodyBytes: 8192,
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
			Handle: h.declareNativeDeliveryGaps,
		},
		endpoint{
			Method:       http.MethodPost,
			Path:         "/exam-attempts/{exam_attempt_id}/security-streams/{stream_id}/seal",
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
			Handle: h.sealNativeDelivery,
		},
		endpoint{
			Method:       http.MethodPost,
			Path:         "/exam-attempts/{exam_attempt_id}/security-streams/{stream_id}/summary",
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
			Handle: h.updateNativeDeliverySummary,
		},
	)
}

// nativeDeliveryQuery builds the query for one delivery stream. streamID names
// a delivery stream, Participation, or source session chosen by the Store, so
// it stays an untyped opaque identifier.
func nativeDeliveryQuery(request operationRequest, streamID string) (attempt.NativeDeliveryQuery, error) {
	attemptID, err := pathID[model.ExamAttemptID](request, "exam_attempt_id")
	if err != nil {
		return attempt.NativeDeliveryQuery{}, err
	}

	if !model.IsValidOpaqueID(streamID) {
		return attempt.NativeDeliveryQuery{}, invalidRequestError(nil)
	}

	query := attempt.NativeDeliveryQuery{Access: attempt.CandidateAccess{AttemptID: attemptID}, StreamID: streamID}
	// Omission does not select historical authority. The Store decides from the
	// recorded source closure, and rejects omission for a live source.
	if len(request.http.Header.Values(candidateAttemptCredentialHeader)) > 0 ||
		len(request.http.Header.Values(candidateAttemptConnectionHeader)) > 0 {
		headers, err := candidateAttemptAccessHeaders(request.http)
		if err != nil {
			return query, invalidRequestError(err)
		}

		query.Access.ConnectionID = headers.ConnectionID
		query.Access.ContinuityCredential = headers.ContinuityCredential
	}

	return query, nil
}
func (h examAttemptHandlers) nativeDeliveryStatus(request operationRequest) (operationResult, error) {
	query, err := nativeDeliveryQuery(request, request.params["stream_id"])
	if err != nil {
		return operationResult{}, err
	}
	value, err := h.app.NativeDeliveryStatus(request.context, request.invocation(), query)
	if err != nil {
		return operationResult{}, err
	}
	return nativeDeliveryStatusResult(value)
}
func (h examAttemptHandlers) nativeDeliveryReceipt(request operationRequest) (operationResult, error) {
	query, err := nativeDeliveryQuery(request, request.params["stream_id"])
	if err != nil {
		return operationResult{}, err
	}
	sequence, err := strconv.ParseInt(request.params["batch_sequence"], 10, 64)
	if err != nil || sequence < 1 || sequence > model.NativeParticipationPositionLimit {
		return operationResult{}, invalidRequestError(err)
	}
	value, err := h.app.NativeDeliveryReceipt(request.context, request.invocation(), query, sequence)
	if err != nil {
		return operationResult{}, err
	}
	if value == nil || value.Validate() != nil {
		return operationResult{}, errUnpresentableResult
	}
	return jsonResult(http.StatusOK, value).withHeaders(noStoreHeaders()), nil
}
func (h examAttemptHandlers) declareNativeDeliveryGaps(request operationRequest) (operationResult, error) {
	query, err := nativeDeliveryQuery(request, request.params["stream_id"])
	if err != nil {
		return operationResult{}, err
	}

	var body model.DeclareDeliveryGaps
	if err = request.decodeJSON(&body); err != nil {
		return operationResult{}, err
	}

	value, err := h.app.DeclareNativeDeliveryGaps(request.context, request.invocation(), attempt.NativeDeliveryGapsCommand{
		Query:          query,
		Declaration:    body,
		IdempotencyKey: request.idempotencyKey,
	})
	if err != nil {
		return operationResult{}, err
	}

	return jsonResult(http.StatusOK, value).withHeaders(noStoreHeaders()), nil
}
func (h examAttemptHandlers) sealNativeDelivery(request operationRequest) (operationResult, error) {
	query, err := nativeDeliveryQuery(request, request.params["stream_id"])
	if err != nil {
		return operationResult{}, err
	}

	var body model.FinalDeliveryDeclaration
	if err = request.decodeJSON(&body); err != nil {
		return operationResult{}, err
	}

	value, err := h.app.SealNativeDelivery(request.context, request.invocation(), attempt.NativeDeliveryFinalCommand{
		Query:          query,
		Declaration:    body,
		IdempotencyKey: request.idempotencyKey,
	})
	if err != nil {
		return operationResult{}, err
	}

	return nativeDeliveryStatusResult(value)
}
func (h examAttemptHandlers) updateNativeDeliverySummary(request operationRequest) (operationResult, error) {
	query, err := nativeDeliveryQuery(request, request.params["stream_id"])
	if err != nil {
		return operationResult{}, err
	}

	var body model.UnretainedDeliverySummary
	if err = request.decodeJSON(&body); err != nil {
		return operationResult{}, err
	}

	value, err := h.app.UpdateNativeDeliverySummary(request.context, request.invocation(), attempt.NativeDeliverySummaryCommand{
		Query:          query,
		Summary:        body,
		IdempotencyKey: request.idempotencyKey,
	})
	if err != nil {
		return operationResult{}, err
	}

	return nativeDeliveryStatusResult(value)
}
func nativeDeliveryStatusResult(value *model.NativeSecurityStreamStatus) (operationResult, error) {
	if value == nil || value.Validate() != nil {
		return operationResult{}, errUnpresentableResult
	}
	return jsonResult(http.StatusOK, value).withHeaders(noStoreHeaders()), nil
}

func (h examAttemptHandlers) appendNativeDelivery(request operationRequest) (operationResult, error) {
	var body model.NativeSecurityBatch
	if err := request.decodeJSON(&body); err != nil {
		return operationResult{}, err
	}

	// The stream selector is in the closed batch envelope on this operation.
	query, err := nativeDeliveryQuery(request, body.StreamID)
	if err != nil {
		return operationResult{}, err
	}

	value, err := h.app.AppendNativeDelivery(request.context, request.invocation(), attempt.NativeDeliveryAppendCommand{
		Query:          query,
		Batch:          body,
		IdempotencyKey: request.idempotencyKey,
	})
	if err != nil {
		return operationResult{}, err
	}
	if value == nil || value.Receipt.Validate() != nil {
		return operationResult{}, errUnpresentableResult
	}

	return jsonResult(
		http.StatusOK,
		nativeDeliveryAcknowledgementResponse{Receipt: value.Receipt, NativeDeliveryProgress: value.NativeDeliveryProgress},
	).withHeaders(noStoreHeaders()), nil
}

// nativeDeliveryAcknowledgementResponse acknowledges one native security batch
// with its receipt and the stream's delivery progress.
type nativeDeliveryAcknowledgementResponse struct {
	Receipt model.NativeBatchReceipt `json:"receipt"`
	model.NativeDeliveryProgress
}
