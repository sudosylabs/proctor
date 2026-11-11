// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package httpapi

import (
	"errors"
	"net/http"
	"time"

	"github.com/sudosylabs/proctor/server/app/exam/attempt"
	"github.com/sudosylabs/proctor/server/httpapi/route"
	"github.com/sudosylabs/proctor/server/model"
)

func browserDeliveryResource(app ExamAttemptApplication) route.Resource {
	h := examAttemptHandlers{app: app}
	return route.NewResource(
		"browser-delivery",
		route.Endpoint{
			Method: http.MethodGet,
			Path:   "/exam-attempts/{exam_attempt_id}/browser-activity/sources",
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
				"exam.delivery.declaration_conflict",
				"exam.delivery.sequence_limit",
				"exam.delivery.detail_budget_exhausted",
				"exam.delivery.metadata_capacity",
				"exam.delivery.summary_rate_limited",
				"idempotency.conflict",
				"idempotency.in_progress",
			},
			Handle: h.browserSourceList,
		},
		route.Endpoint{
			Method: http.MethodGet,
			Path:   "/exam-attempts/{exam_attempt_id}/browser-activity/sources/{source_session_id:uuid}",
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
				"exam.delivery.declaration_conflict",
				"exam.delivery.sequence_limit",
				"exam.delivery.detail_budget_exhausted",
				"exam.delivery.metadata_capacity",
				"exam.delivery.summary_rate_limited",
				"idempotency.conflict",
				"idempotency.in_progress",
			},
			Handle: h.browserSourceStatus,
		},
		route.Endpoint{
			Method: http.MethodGet,
			Path:   "/exam-attempts/{exam_attempt_id}/browser-activity/sources/{source_session_id:uuid}/receipts",
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
			Handle: h.browserDeliveryReceipts,
		},
		route.Endpoint{
			Method:       http.MethodPost,
			Path:         "/exam-attempts/{exam_attempt_id}/browser-activity/sources/{source_session_id:uuid}/gaps",
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
			Handle: h.declareBrowserDeliveryGaps,
		},
		route.Endpoint{
			Method:       http.MethodPost,
			Path:         "/exam-attempts/{exam_attempt_id}/browser-activity/sources/{source_session_id:uuid}/seal",
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
			Handle: h.sealBrowserDelivery,
		},
		route.Endpoint{
			Method:       http.MethodPost,
			Path:         "/exam-attempts/{exam_attempt_id}/browser-activity/sources/{source_session_id:uuid}/summary",
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
			Handle: h.updateBrowserDeliverySummary,
		},
		route.Endpoint{
			Method:       http.MethodPost,
			Path:         "/exam-attempts/{exam_attempt_id}/browser-activity/sources/{source_session_id:uuid}/events",
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
				"exam.attempt.browser_activity_conflict",
			},
			Handle: h.appendHistoricalBrowserDelivery,
		},
	)
}

func browserSourceQuery(request route.Request, list bool) (attempt.BrowserSourceQuery, error) {
	attemptID, err := route.PathID[model.ExamAttemptID](request, "exam_attempt_id")
	if err != nil {
		return attempt.BrowserSourceQuery{}, err
	}

	query := attempt.BrowserSourceQuery{
		Access:          attempt.CandidateAccess{AttemptID: attemptID},
		SourceSessionID: model.BrowserSourceSessionID(request.Params["source_session_id"]),
	}
	if list {
		values := request.HTTP.URL.Query()["participation_id"]
		if len(values) != 1 || !model.AttemptParticipationID(values[0]).IsValid() {
			return query, route.InvalidRequest(nil)
		}

		query.ParticipationID = model.AttemptParticipationID(values[0])
	} else if !query.SourceSessionID.IsValid() {
		return query, route.InvalidRequest(nil)
	}

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
func (h examAttemptHandlers) browserSourceStatus(request route.Request) (route.Result, error) {
	query, err := browserSourceQuery(request, false)
	if err != nil {
		return route.Result{}, err
	}
	value, err := h.app.BrowserSourceStatus(request.Context, request.Invocation(), query)
	if err != nil {
		return route.Result{}, err
	}
	if value == nil || value.Validate() != nil {
		return route.Result{}, route.ErrUnpresentable
	}
	return route.JSON(http.StatusOK, value).WithHeaders(noStoreHeaders()), nil
}
func (h examAttemptHandlers) browserSourceList(request route.Request) (route.Result, error) {
	query, err := browserSourceQuery(request, true)
	if err != nil {
		return route.Result{}, err
	}

	values, err := h.app.BrowserSourceList(request.Context, request.Invocation(), query)
	if err != nil {
		return route.Result{}, err
	}
	if values == nil || len(values) > model.BrowserSourceMaximumPerParticipation {
		return route.Result{}, route.ErrUnpresentable
	}

	for _, value := range values {
		if value.Validate() != nil {
			return route.Result{}, route.ErrUnpresentable
		}
	}

	// A Participation holds at most BrowserSourceMaximumPerParticipation
	// sources, so the complete set is paged by source identity.
	response, err := route.BoundedList(
		request,
		"Browser source",
		values,
		func(value model.BrowserSourceStatus) route.BoundedKey {
			return route.BoundedKey{Name: string(value.SourceSessionID), ID: string(value.SourceSessionID)}
		},
		func(value model.BrowserSourceStatus) model.BrowserSourceStatus { return value },
	)
	if err != nil {
		return route.Result{}, err
	}

	return route.JSON(http.StatusOK, response).WithHeaders(noStoreHeaders()), nil
}

func (h examAttemptHandlers) browserDeliveryReceipts(request route.Request) (route.Result, error) {
	query, err := browserSourceQuery(request, false)
	if err != nil {
		return route.Result{}, err
	}

	page, err := request.Page()
	if err != nil {
		return route.Result{}, err
	}
	if !request.HTTP.URL.Query().Has("limit") {
		page.Limit = browserReceiptPageLimit
	}
	if page.Limit > browserReceiptPageLimit {
		return route.Result{}, route.InvalidRequest(errors.New("limit must be between 1 and 64"))
	}

	cursor, continued, err := route.DecodePageCursor(page, browserReceiptCursorSpec())
	if err != nil {
		return route.Result{}, err
	}

	first := int64(1)
	if continued {
		first = cursor.Sequence
	}

	value, err := h.app.BrowserDeliveryReceipts(request.Context, request.Invocation(), query, first, page.Limit)
	if err != nil {
		return route.Result{}, err
	}
	if value == nil {
		return route.Result{}, route.ErrUnpresentable
	}

	response := route.List[model.BrowserEventReceipt]{Items: value.Receipts}
	if value.NextSequence != nil {
		if response.NextCursor, err = route.EncodeCursor(
			browserReceiptCursor{Sequence: *value.NextSequence},
			browserReceiptCursorSpec(),
		); err != nil {
			return route.Result{}, route.Unpresentable(err)
		}
	}

	return route.JSON(http.StatusOK, response).WithHeaders(noStoreHeaders()), nil
}

// browserReceiptPageLimit is the largest and default receipt page; the
// receipts of one page fit the 16 KiB response bound.
const browserReceiptPageLimit = 64

// browserReceiptCursor continues the receipt listing at the first retained
// sequence of the next page.
type browserReceiptCursor struct {
	Sequence int64 `json:"sequence"`
}

func browserReceiptCursorSpec() route.CursorSpec[browserReceiptCursor] {
	return route.CursorSpec[browserReceiptCursor]{
		Label:                "Browser receipt",
		MaximumEncodedLength: route.DefaultMaximumCursorLength,
		Members:              []string{"sequence"},
		Validate: func(cursor browserReceiptCursor) error {
			if cursor.Sequence < 1 || cursor.Sequence > model.BrowserParticipationPositionLimit {
				return errors.New("invalid Browser receipt keyset")
			}
			return nil
		},
	}
}
func (h examAttemptHandlers) declareBrowserDeliveryGaps(request route.Request) (route.Result, error) {
	query, err := browserSourceQuery(request, false)
	if err != nil {
		return route.Result{}, err
	}

	var body model.DeclareDeliveryGaps
	if err = request.DecodeJSON(&body); err != nil {
		return route.Result{}, err
	}

	value, err := h.app.DeclareBrowserDeliveryGaps(request.Context, request.Invocation(), attempt.BrowserDeliveryGapsCommand{
		Query:          query,
		Declaration:    body,
		IdempotencyKey: request.IdempotencyKey,
	})
	if err != nil {
		return route.Result{}, err
	}
	if value == nil || value.Status.Validate() != nil {
		return route.Result{}, route.ErrUnpresentable
	}

	return route.JSON(
		http.StatusOK,
		browserDeliveryGapResponse{Receipt: value.Receipt, Status: value.Status},
	).WithHeaders(noStoreHeaders()), nil
}

// browserDeliveryGapResponse acknowledges a Browser delivery gap declaration
// with its receipt and the resulting source status.
type browserDeliveryGapResponse struct {
	Receipt model.DeliveryGapReceipt  `json:"receipt"`
	Status  model.BrowserSourceStatus `json:"status"`
}

func (h examAttemptHandlers) sealBrowserDelivery(request route.Request) (route.Result, error) {
	query, err := browserSourceQuery(request, false)
	if err != nil {
		return route.Result{}, err
	}

	var body model.FinalDeliveryDeclaration
	if err = request.DecodeJSON(&body); err != nil {
		return route.Result{}, err
	}

	value, err := h.app.SealBrowserDelivery(request.Context, request.Invocation(), attempt.BrowserDeliveryFinalCommand{
		Query:          query,
		Declaration:    body,
		IdempotencyKey: request.IdempotencyKey,
	})
	if err != nil {
		return route.Result{}, err
	}
	if value == nil || value.Validate() != nil {
		return route.Result{}, route.ErrUnpresentable
	}

	return route.JSON(http.StatusOK, value).WithHeaders(noStoreHeaders()), nil
}
func (h examAttemptHandlers) updateBrowserDeliverySummary(request route.Request) (route.Result, error) {
	query, err := browserSourceQuery(request, false)
	if err != nil {
		return route.Result{}, err
	}

	var body model.UnretainedDeliverySummary
	if err = request.DecodeJSON(&body); err != nil {
		return route.Result{}, err
	}

	value, err := h.app.UpdateBrowserDeliverySummary(request.Context, request.Invocation(), attempt.BrowserDeliverySummaryCommand{
		Query:          query,
		Summary:        body,
		IdempotencyKey: request.IdempotencyKey,
	})
	if err != nil {
		return route.Result{}, err
	}
	if value == nil || value.Status.Validate() != nil || value.Summary.Validate() != nil {
		return route.Result{}, route.ErrUnpresentable
	}

	return route.JSON(http.StatusOK, value).WithHeaders(noStoreHeaders()), nil
}
func (h examAttemptHandlers) appendHistoricalBrowserDelivery(request route.Request) (route.Result, error) {
	query, err := browserSourceQuery(request, false)
	if err != nil {
		return route.Result{}, err
	}
	if query.Access.ConnectionID != "" || query.Access.ContinuityCredential != "" {
		return route.Result{}, route.InvalidRequest(nil)
	}

	var body model.BrowserActivityBatch
	if err = request.DecodeJSON(&body); err != nil {
		return route.Result{}, err
	}

	value, err := h.app.AppendHistoricalBrowserDelivery(request.Context, request.Invocation(), attempt.BrowserDeliveryAppendCommand{
		Query:          query,
		Batch:          body,
		IdempotencyKey: request.IdempotencyKey,
	})
	if err != nil {
		return route.Result{}, err
	}
	if value == nil {
		return route.Result{}, route.ErrUnpresentable
	}

	progress := model.BrowserDeliveryProgress{
		HighestContiguous:      value.HighestContiguous,
		SettledThrough:         value.SettledThrough,
		HighestSeen:            value.HighestSeen,
		AllocatedThrough:       value.AllocatedThrough,
		TerminalMissingThrough: value.TerminalMissingThrough,
		MissingRanges:          []model.SequenceRange{},
		MissingRangesTruncated: value.MissingRangesTruncated,
	}
	for _, r := range value.MissingRanges {
		progress.MissingRanges = append(progress.MissingRanges, model.SequenceRange{First: r.First, Last: r.Last})
	}

	if progress.Validate() != nil {
		return route.Result{}, route.ErrUnpresentable
	}

	return route.JSON(http.StatusOK, browserDeliveryAcknowledgementResponse{
		BrowserDeliveryProgress: progress,
		SourceSessionID:         value.SourceSessionID,
		Receipts:                value.Receipts,
		ServerTime:              value.ServerTime,
	}).WithHeaders(noStoreHeaders()), nil
}

type browserDeliveryAcknowledgementResponse struct {
	model.BrowserDeliveryProgress
	SourceSessionID model.BrowserSourceSessionID `json:"source_session_id"`
	Receipts        []model.BrowserEventReceipt  `json:"receipts"`
	ServerTime      time.Time                    `json:"server_time"`
}
