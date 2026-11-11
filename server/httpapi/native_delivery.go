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
	"github.com/sudosylabs/proctor/server/httpapi/route"
	"github.com/sudosylabs/proctor/server/model"
)

func nativeDeliveryResource(app ExamAttemptApplication) route.Resource {
	h := examAttemptHandlers{app: app}
	return route.NewResource(
		"native-delivery",
		route.Endpoint{
			Method:       http.MethodPost,
			Path:         "/exam-attempts/{exam_attempt_id}/security-batches",
			Auth:         route.AuthSessionRequired,
			Idempotency:  route.IdempotencyRequired,
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
		route.Endpoint{
			Method: http.MethodGet,
			Path:   "/exam-attempts/{exam_attempt_id}/security-streams/{stream_id}",
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
			Handle: h.nativeDeliveryStatus,
		},
		route.Endpoint{
			Method: http.MethodGet,
			Path:   "/exam-attempts/{exam_attempt_id}/security-streams/{stream_id}/receipts/{batch_sequence:sequence}",
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
			Handle: h.nativeDeliveryReceipt,
		},
		route.Endpoint{
			Method:       http.MethodPost,
			Path:         "/exam-attempts/{exam_attempt_id}/security-streams/{stream_id}/gaps",
			Auth:         route.AuthSessionRequired,
			Idempotency:  route.IdempotencyRequired,
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
		route.Endpoint{
			Method:       http.MethodPost,
			Path:         "/exam-attempts/{exam_attempt_id}/security-streams/{stream_id}/seal",
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
			Handle: h.sealNativeDelivery,
		},
		route.Endpoint{
			Method:       http.MethodPost,
			Path:         "/exam-attempts/{exam_attempt_id}/security-streams/{stream_id}/summary",
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
			Handle: h.updateNativeDeliverySummary,
		},
	)
}

// nativeDeliveryQuery builds the query for one delivery stream. streamID names
// a delivery stream, Participation, or source session chosen by the Store, so
// it stays an untyped opaque identifier.
func nativeDeliveryQuery(request route.Request, streamID string) (attempt.NativeDeliveryQuery, error) {
	attemptID, err := route.PathID[model.ExamAttemptID](request, "exam_attempt_id")
	if err != nil {
		return attempt.NativeDeliveryQuery{}, err
	}

	if !model.IsValidOpaqueID(streamID) {
		return attempt.NativeDeliveryQuery{}, route.InvalidRequest(nil)
	}

	query := attempt.NativeDeliveryQuery{Access: attempt.CandidateAccess{AttemptID: attemptID}, StreamID: streamID}
	// Omission does not select historical authority. The Store decides from the
	// recorded source closure, and rejects omission for a live source.
	if len(request.HTTP.Header.Values(candidateAttemptCredentialHeader)) > 0 ||
		len(request.HTTP.Header.Values(candidateAttemptConnectionHeader)) > 0 {
		headers, err := candidateAttemptAccessHeaders(request.HTTP)
		if err != nil {
			return query, route.InvalidRequest(err)
		}

		query.Access.ConnectionID = headers.ConnectionID
		query.Access.ContinuityCredential = headers.ContinuityCredential
	}

	return query, nil
}
func (h examAttemptHandlers) nativeDeliveryStatus(request route.Request) (route.Result, error) {
	query, err := nativeDeliveryQuery(request, request.Params["stream_id"])
	if err != nil {
		return route.Result{}, err
	}
	value, err := h.app.NativeDeliveryStatus(request.Context, request.Invocation(), query)
	if err != nil {
		return route.Result{}, err
	}
	return nativeDeliveryStatusResult(value)
}
func (h examAttemptHandlers) nativeDeliveryReceipt(request route.Request) (route.Result, error) {
	query, err := nativeDeliveryQuery(request, request.Params["stream_id"])
	if err != nil {
		return route.Result{}, err
	}
	sequence, err := strconv.ParseInt(request.Params["batch_sequence"], 10, 64)
	if err != nil || sequence < 1 || sequence > model.NativeParticipationPositionLimit {
		return route.Result{}, route.InvalidRequest(err)
	}
	value, err := h.app.NativeDeliveryReceipt(request.Context, request.Invocation(), query, sequence)
	if err != nil {
		return route.Result{}, err
	}
	if value == nil || value.Validate() != nil {
		return route.Result{}, route.ErrUnpresentable
	}
	return route.JSON(http.StatusOK, value).WithHeaders(noStoreHeaders()), nil
}
func (h examAttemptHandlers) declareNativeDeliveryGaps(request route.Request) (route.Result, error) {
	query, err := nativeDeliveryQuery(request, request.Params["stream_id"])
	if err != nil {
		return route.Result{}, err
	}

	var body model.DeclareDeliveryGaps
	if err = request.DecodeJSON(&body); err != nil {
		return route.Result{}, err
	}

	value, err := h.app.DeclareNativeDeliveryGaps(request.Context, request.Invocation(), attempt.NativeDeliveryGapsCommand{
		Query:          query,
		Declaration:    body,
		IdempotencyKey: request.IdempotencyKey,
	})
	if err != nil {
		return route.Result{}, err
	}

	return route.JSON(http.StatusOK, value).WithHeaders(noStoreHeaders()), nil
}
func (h examAttemptHandlers) sealNativeDelivery(request route.Request) (route.Result, error) {
	query, err := nativeDeliveryQuery(request, request.Params["stream_id"])
	if err != nil {
		return route.Result{}, err
	}

	var body model.FinalDeliveryDeclaration
	if err = request.DecodeJSON(&body); err != nil {
		return route.Result{}, err
	}

	value, err := h.app.SealNativeDelivery(request.Context, request.Invocation(), attempt.NativeDeliveryFinalCommand{
		Query:          query,
		Declaration:    body,
		IdempotencyKey: request.IdempotencyKey,
	})
	if err != nil {
		return route.Result{}, err
	}

	return nativeDeliveryStatusResult(value)
}
func (h examAttemptHandlers) updateNativeDeliverySummary(request route.Request) (route.Result, error) {
	query, err := nativeDeliveryQuery(request, request.Params["stream_id"])
	if err != nil {
		return route.Result{}, err
	}

	var body model.UnretainedDeliverySummary
	if err = request.DecodeJSON(&body); err != nil {
		return route.Result{}, err
	}

	value, err := h.app.UpdateNativeDeliverySummary(request.Context, request.Invocation(), attempt.NativeDeliverySummaryCommand{
		Query:          query,
		Summary:        body,
		IdempotencyKey: request.IdempotencyKey,
	})
	if err != nil {
		return route.Result{}, err
	}

	return nativeDeliveryStatusResult(value)
}
func nativeDeliveryStatusResult(value *model.NativeSecurityStreamStatus) (route.Result, error) {
	if value == nil || value.Validate() != nil {
		return route.Result{}, route.ErrUnpresentable
	}
	return route.JSON(http.StatusOK, value).WithHeaders(noStoreHeaders()), nil
}

func (h examAttemptHandlers) appendNativeDelivery(request route.Request) (route.Result, error) {
	var body model.NativeSecurityBatch
	if err := request.DecodeJSON(&body); err != nil {
		return route.Result{}, err
	}

	// The stream selector is in the closed batch envelope on this operation.
	query, err := nativeDeliveryQuery(request, body.StreamID)
	if err != nil {
		return route.Result{}, err
	}

	value, err := h.app.AppendNativeDelivery(request.Context, request.Invocation(), attempt.NativeDeliveryAppendCommand{
		Query:          query,
		Batch:          body,
		IdempotencyKey: request.IdempotencyKey,
	})
	if err != nil {
		return route.Result{}, err
	}
	if value == nil || value.Receipt.Validate() != nil {
		return route.Result{}, route.ErrUnpresentable
	}

	return route.JSON(
		http.StatusOK,
		nativeDeliveryAcknowledgementResponse{Receipt: value.Receipt, NativeDeliveryProgress: value.NativeDeliveryProgress},
	).WithHeaders(noStoreHeaders()), nil
}

// nativeDeliveryAcknowledgementResponse acknowledges one native security batch
// with its receipt and the stream's delivery progress.
type nativeDeliveryAcknowledgementResponse struct {
	Receipt model.NativeBatchReceipt `json:"receipt"`
	model.NativeDeliveryProgress
}
