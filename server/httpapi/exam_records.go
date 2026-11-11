// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package httpapi

import (
	"context"
	"errors"
	"net/http"

	"github.com/sudosylabs/proctor/server/app/exam"
	"github.com/sudosylabs/proctor/server/app/invocation"
	"github.com/sudosylabs/proctor/server/httpapi/route"
	"github.com/sudosylabs/proctor/server/model"
	"github.com/sudosylabs/proctor/server/store"
)

// ExamRecordsApplication is the application port its handlers call; it
// completes a Sitting's records, waives Submission reviews and places or
// releases retention holds.
type ExamRecordsApplication interface {
	GetExamSittingRecords(
		context.Context,
		invocation.Invocation,
		model.ExamID,
		model.ExamSittingID,
	) (*store.ExamRecordsCompletionSnapshot, error)
	GetSubmissionReviewWaiver(context.Context, invocation.Invocation, model.RetentionHoldScope) (*model.SubmissionReviewWaiver, error)
	CompleteExamSittingRecords(
		context.Context,
		invocation.Invocation,
		exam.CompleteRecordsCommand,
	) (*model.ExamSittingRecordsCompletion, error)
	WaiveSubmissionReview(context.Context, invocation.Invocation, exam.WaiveReviewCommand) (*model.SubmissionReviewWaiver, error)
	ListRetentionHolds(context.Context, invocation.Invocation, store.ExamRecordsHoldListOptions) (*store.ExamRecordsHoldPage, error)
	CreateRetentionHold(context.Context, invocation.Invocation, exam.CreateRetentionHoldCommand) (*model.RetentionHold, error)
	ReleaseRetentionHold(context.Context, invocation.Invocation, exam.ReleaseRetentionHoldCommand) (*model.RetentionHold, error)
}

type examRecordsHandlers struct{ app ExamRecordsApplication }

func examRecordsResource(app ExamRecordsApplication) route.Resource {
	h := examRecordsHandlers{app: app}
	completion := "/exams/{exam_id}/sittings/{exam_sitting_id}/records-completion"
	waiver := "/exams/{exam_id}/sittings/{exam_sitting_id}/submissions/{submission_id}/review-waiver"
	holds := "/exams/{exam_id}/retention-holds"
	release := "/exams/{exam_id}/retention-holds/{retention_hold_id}/release"
	completeRoute := route.Endpoint{
		Method:      http.MethodPost,
		Path:        completion,
		Auth:        route.AuthSessionRequired,
		Idempotency: route.IdempotencyRequired,
		Errors: []string{
			"authorization.request.invalid",
			"authorization.unavailable",
			"audit.unavailable",
			"request.invalid",
			"resource.not_found",
			"exam.invalid",
			"exam.conflict",
			"exam.unavailable",
			"idempotency.key_required",
			"idempotency.key_invalid",
			"idempotency.conflict",
			"idempotency.in_progress",
		},
		Handle: h.complete,
	}
	waiveRoute := route.Endpoint{
		Method:      http.MethodPost,
		Path:        waiver,
		Auth:        route.AuthSessionRequired,
		Idempotency: route.IdempotencyRequired,
		Errors: []string{
			"authorization.request.invalid",
			"authorization.unavailable",
			"audit.unavailable",
			"request.invalid",
			"resource.not_found",
			"exam.invalid",
			"exam.conflict",
			"exam.unavailable",
			"idempotency.key_required",
			"idempotency.key_invalid",
			"idempotency.conflict",
			"idempotency.in_progress",
		},
		Handle: h.waive,
	}
	createRoute := route.Endpoint{
		Method:      http.MethodPost,
		Path:        holds,
		Auth:        route.AuthSessionRequired,
		Idempotency: route.IdempotencyRequired,
		Errors: []string{
			"authorization.request.invalid",
			"authorization.unavailable",
			"audit.unavailable",
			"request.invalid",
			"resource.not_found",
			"exam.invalid",
			"exam.conflict",
			"exam.unavailable",
			"idempotency.key_required",
			"idempotency.key_invalid",
			"idempotency.conflict",
			"idempotency.in_progress",
		},
		Handle: h.createHold,
	}
	releaseRoute := route.Endpoint{
		Method:      http.MethodPost,
		Path:        release,
		Auth:        route.AuthStrongRecentSessionRequired,
		Idempotency: route.IdempotencyRequired,
		Errors: []string{
			"authorization.request.invalid",
			"authorization.unavailable",
			"audit.unavailable",
			"request.invalid",
			"resource.not_found",
			"exam.invalid",
			"exam.conflict",
			"exam.unavailable",
			"idempotency.key_required",
			"idempotency.key_invalid",
			"idempotency.conflict",
			"idempotency.in_progress",
		},
		Handle: h.releaseHold,
	}
	return route.NewResource(
		"exam-records",
		route.Endpoint{
			Method: http.MethodGet,
			Path:   completion,
			Auth:   route.AuthSessionRequired,
			Errors: []string{
				"authorization.request.invalid",
				"authorization.unavailable",
				"request.invalid",
				"resource.not_found",
				"exam.invalid",
				"exam.unavailable",
			},
			Handle: h.getCompletion,
		},
		completeRoute,
		route.Endpoint{
			Method: http.MethodGet,
			Path:   waiver,
			Auth:   route.AuthSessionRequired,
			Errors: []string{
				"authorization.request.invalid",
				"authorization.unavailable",
				"request.invalid",
				"resource.not_found",
				"exam.invalid",
				"exam.unavailable",
			},
			Handle: h.getWaiver,
		},
		waiveRoute,
		route.Endpoint{
			Method: http.MethodGet,
			Path:   holds,
			Auth:   route.AuthSessionRequired,
			Errors: []string{
				"authorization.request.invalid",
				"authorization.unavailable",
				"request.invalid",
				"resource.not_found",
				"exam.invalid",
				"exam.unavailable",
			},
			Handle: h.listHolds,
		},
		createRoute,
		releaseRoute,
	)
}

type completeExamRecordsRequest struct {
	ExpectedRevision             route.Optional[int64] `json:"expected_revision"`
	AcknowledgedEvidenceRevision route.Optional[int64] `json:"acknowledged_evidence_revision"`
}
type waiveSubmissionReviewRequest struct {
	ExpectedDeliveryInventoryRevision route.Optional[int64] `json:"expected_delivery_inventory_revision,omitempty"`
	ExpectedRevision                  route.Optional[int64] `json:"expected_revision"`
	ExpectedReviewRevision            route.Optional[int64] `json:"expected_review_revision"`
	ExpectedDiscrepancyCount          route.Optional[int64] `json:"expected_discrepancy_count"`
	ReasonCode                        string                `json:"reason_code"`
	PrivateReason                     string                `json:"private_reason"`
}
type createRetentionHoldRequest struct {
	SittingID     route.Optional[string] `json:"exam_sitting_id"`
	SubmissionID  route.Optional[string] `json:"submission_id"`
	ReasonCode    string                 `json:"reason_code"`
	PrivateReason string                 `json:"private_reason"`
}
type releaseRetentionHoldRequest struct {
	SittingID        route.Optional[string] `json:"exam_sitting_id"`
	SubmissionID     route.Optional[string] `json:"submission_id"`
	ExpectedRevision route.Optional[int64]  `json:"expected_revision"`
	ReasonCode       string                 `json:"reason_code"`
	PrivateReason    string                 `json:"private_reason"`
}

func (b *completeExamRecordsRequest) UnmarshalJSON(data []byte) error {
	type wire completeExamRecordsRequest
	var value wire
	if err := decodeDuplicateFreeExamIntegrityReviewObject(data, &value); err != nil {
		return err
	}
	*b = completeExamRecordsRequest(value)
	return nil
}
func (b *waiveSubmissionReviewRequest) UnmarshalJSON(data []byte) error {
	type wire waiveSubmissionReviewRequest
	var value wire
	if err := decodeDuplicateFreeExamIntegrityReviewObject(data, &value); err != nil {
		return err
	}
	*b = waiveSubmissionReviewRequest(value)
	return nil
}
func (b *createRetentionHoldRequest) UnmarshalJSON(data []byte) error {
	type wire createRetentionHoldRequest
	var value wire
	if err := decodeDuplicateFreeExamIntegrityReviewObject(data, &value); err != nil {
		return err
	}
	*b = createRetentionHoldRequest(value)
	return nil
}
func (b *releaseRetentionHoldRequest) UnmarshalJSON(data []byte) error {
	type wire releaseRetentionHoldRequest
	var value wire
	if err := decodeDuplicateFreeExamIntegrityReviewObject(data, &value); err != nil {
		return err
	}
	*b = releaseRetentionHoldRequest(value)
	return nil
}

type examRecordsCompletionResponse struct {
	SittingID                 string `json:"exam_sitting_id"`
	Revision                  int64  `json:"revision"`
	EvidenceRevision          int64  `json:"evidence_revision"`
	CompletedEvidenceRevision int64  `json:"completed_evidence_revision"`
	Current                   bool   `json:"current"`
	CompletedAt               string `json:"completed_at,omitempty"`
	CompletedByUserID         string `json:"completed_by_user_id,omitempty"`
	StaleAt                   string `json:"stale_at,omitempty"`
}
type examRecordsSnapshotResponse struct {
	Completion      examRecordsCompletionResponse `json:"completion"`
	SittingState    string                        `json:"sitting_state"`
	SubmissionCount int64                         `json:"submission_count"`
	PendingReviews  int64                         `json:"pending_reviews"`
}
type submissionReviewWaiverResponse struct {
	DeliveryInventoryRevision int64  `json:"delivery_inventory_revision"`
	InventoryInvalidated      bool   `json:"inventory_invalidated"`
	SubmissionID              string `json:"submission_id"`
	Revision                  int64  `json:"revision"`
	ReviewRevision            int64  `json:"review_revision"`
	DiscrepancyCount          int64  `json:"discrepancy_count"`
	ActorUserID               string `json:"actor_user_id"`
	RecordedAt                string `json:"recorded_at"`
	ReasonCode                string `json:"reason_code"`
	PrivateReason             string `json:"private_reason"`
}
type submissionReviewWaiverEnvelope struct {
	Waiver *submissionReviewWaiverResponse `json:"waiver"`
}
type retentionHoldResponse struct {
	ID                              string `json:"id"`
	ExamID                          string `json:"exam_id"`
	SittingID                       string `json:"exam_sitting_id,omitempty"`
	SubmissionID                    string `json:"submission_id,omitempty"`
	Revision                        int64  `json:"revision"`
	CreatedAt                       string `json:"created_at"`
	CreatedByUserID                 string `json:"created_by_user_id"`
	ReasonCode                      string `json:"reason_code"`
	PrivateReason                   string `json:"private_reason"`
	ReleasedAt                      string `json:"released_at,omitempty"`
	ReleasedByUserID                string `json:"released_by_user_id,omitempty"`
	ReleaseReasonCode               string `json:"release_reason_code,omitempty"`
	ReleasePrivateReason            string `json:"release_private_reason,omitempty"`
	WorkRetiredSubmissionCount      int64  `json:"work_retired_submission_count"`
	IntegrityRetiredSubmissionCount int64  `json:"integrity_retired_submission_count"`
}
type retentionHoldPageResponse struct {
	Items      []retentionHoldResponse `json:"items"`
	NextCursor string                  `json:"next_cursor,omitempty"`
}

func (h examRecordsHandlers) getCompletion(request route.Request) (route.Result, error) {
	examID, sittingID, err := examSittingIDs(request)
	if err != nil {
		return route.Result{}, err
	}

	value, err := h.app.GetExamSittingRecords(request.Context, request.Invocation(), examID, sittingID)
	if err != nil {
		return route.Result{}, err
	}
	if value == nil {
		return route.Result{}, route.ErrUnpresentable
	}

	return route.JSON(http.StatusOK, examRecordsSnapshotResponse{
		Completion:      recordsCompletionResponse(value.Completion),
		SittingState:    string(value.SittingState),
		SubmissionCount: value.SubmissionCount,
		PendingReviews:  value.PendingReviews,
	}).WithHeaders(privateNoStoreHeaders()), nil
}
func (h examRecordsHandlers) complete(request route.Request) (route.Result, error) {
	examID, sittingID, err := examSittingIDs(request)
	if err != nil {
		return route.Result{}, err
	}

	var body completeExamRecordsRequest
	if err = request.DecodeJSON(&body); err != nil {
		return route.Result{}, err
	}

	revision, err := requiredRecordsInteger("expected_revision", body.ExpectedRevision, 1)
	if err != nil {
		return route.Result{}, err
	}

	evidence, err := requiredRecordsInteger("acknowledged_evidence_revision", body.AcknowledgedEvidenceRevision, 0)
	if err != nil {
		return route.Result{}, err
	}

	value, err := h.app.CompleteExamSittingRecords(request.Context, request.Invocation(), exam.CompleteRecordsCommand{
		Scope:                        model.RetentionHoldScope{ExamID: examID, SittingID: sittingID},
		ExpectedRevision:             revision,
		AcknowledgedEvidenceRevision: evidence,
		IdempotencyKey:               request.IdempotencyKey,
	})
	if err != nil {
		return route.Result{}, err
	}
	if value == nil {
		return route.Result{}, route.ErrUnpresentable
	}

	return route.JSON(http.StatusOK, recordsCompletionResponse(*value)).WithHeaders(privateNoStoreHeaders()), nil
}
func (h examRecordsHandlers) getWaiver(request route.Request) (route.Result, error) {
	scope, err := recordsSubmissionScope(request)
	if err != nil {
		return route.Result{}, err
	}

	value, err := h.app.GetSubmissionReviewWaiver(request.Context, request.Invocation(), scope)
	if err != nil {
		return route.Result{}, err
	}

	return route.JSON(
		http.StatusOK,
		submissionReviewWaiverEnvelope{Waiver: recordsWaiverResponse(value)},
	).WithHeaders(privateNoStoreHeaders()), nil
}
func (h examRecordsHandlers) waive(request route.Request) (route.Result, error) {
	scope, err := recordsSubmissionScope(request)
	if err != nil {
		return route.Result{}, err
	}

	var body waiveSubmissionReviewRequest
	if err = request.DecodeJSON(&body); err != nil {
		return route.Result{}, err
	}

	revision, err := requiredRecordsInteger("expected_revision", body.ExpectedRevision, 0)
	if err != nil {
		return route.Result{}, err
	}

	reviewRevision, err := requiredRecordsInteger("expected_review_revision", body.ExpectedReviewRevision, 0)
	if err != nil {
		return route.Result{}, err
	}

	discrepancies, err := requiredRecordsInteger("expected_discrepancy_count", body.ExpectedDiscrepancyCount, 0)
	if err != nil {
		return route.Result{}, err
	}
	if err = model.ValidateRecordsReason(body.ReasonCode, body.PrivateReason); err != nil {
		return route.Result{}, route.InvalidRequest(err)
	}

	var deliveryRevision int64
	if body.ExpectedDeliveryInventoryRevision.IsSet() {
		deliveryRevision, err = requiredRecordsInteger("expected_delivery_inventory_revision", body.ExpectedDeliveryInventoryRevision, 0)
		if err != nil {
			return route.Result{}, err
		}
	}

	value, err := h.app.WaiveSubmissionReview(request.Context, request.Invocation(), exam.WaiveReviewCommand{
		Scope:                             scope,
		ExpectedRevision:                  revision,
		ExpectedDeliveryInventoryRevision: deliveryRevision,
		ExpectedReviewRevision:            reviewRevision,
		ExpectedDiscrepancyCount:          discrepancies,
		ReasonCode:                        body.ReasonCode,
		PrivateReason:                     body.PrivateReason,
		IdempotencyKey:                    request.IdempotencyKey,
	})
	if err != nil {
		return route.Result{}, err
	}
	if value == nil {
		return route.Result{}, route.ErrUnpresentable
	}

	return route.JSON(
		http.StatusOK,
		submissionReviewWaiverEnvelope{Waiver: recordsWaiverResponse(value)},
	).WithHeaders(privateNoStoreHeaders()), nil
}
func (h examRecordsHandlers) createHold(request route.Request) (route.Result, error) {
	examID, err := examSittingExamID(request)
	if err != nil {
		return route.Result{}, err
	}

	var body createRetentionHoldRequest
	if err = request.DecodeJSON(&body); err != nil {
		return route.Result{}, err
	}

	scope, err := recordsOptionalScope(examID, body.SittingID, body.SubmissionID)
	if err != nil {
		return route.Result{}, err
	}
	if err = model.ValidateRecordsReason(body.ReasonCode, body.PrivateReason); err != nil {
		return route.Result{}, route.InvalidRequest(err)
	}

	value, err := h.app.CreateRetentionHold(request.Context, request.Invocation(), exam.CreateRetentionHoldCommand{
		Scope:          scope,
		ReasonCode:     body.ReasonCode,
		PrivateReason:  body.PrivateReason,
		IdempotencyKey: request.IdempotencyKey,
	})
	if err != nil {
		return route.Result{}, err
	}
	if value == nil {
		return route.Result{}, route.ErrUnpresentable
	}

	return route.JSON(http.StatusCreated, recordsHoldResponse(*value)).WithHeaders(privateNoStoreHeaders()), nil
}
func (h examRecordsHandlers) releaseHold(request route.Request) (route.Result, error) {
	examID, err := examSittingExamID(request)
	if err != nil {
		return route.Result{}, err
	}

	holdID, err := route.PathID[model.RetentionHoldID](request, "retention_hold_id")
	if err != nil {
		return route.Result{}, err
	}

	var body releaseRetentionHoldRequest
	if err = request.DecodeJSON(&body); err != nil {
		return route.Result{}, err
	}

	scope, err := recordsOptionalScope(examID, body.SittingID, body.SubmissionID)
	if err != nil {
		return route.Result{}, err
	}

	revision, err := requiredRecordsInteger("expected_revision", body.ExpectedRevision, 1)
	if err != nil {
		return route.Result{}, err
	}
	if err = model.ValidateRecordsReason(body.ReasonCode, body.PrivateReason); err != nil {
		return route.Result{}, route.InvalidRequest(err)
	}

	value, err := h.app.ReleaseRetentionHold(request.Context, request.Invocation(), exam.ReleaseRetentionHoldCommand{
		Scope:            scope,
		HoldID:           holdID,
		ExpectedRevision: revision,
		ReasonCode:       body.ReasonCode,
		PrivateReason:    body.PrivateReason,
		IdempotencyKey:   request.IdempotencyKey,
	})
	if err != nil {
		return route.Result{}, err
	}
	if value == nil {
		return route.Result{}, route.ErrUnpresentable
	}

	return route.JSON(http.StatusOK, recordsHoldResponse(*value)).WithHeaders(privateNoStoreHeaders()), nil
}

func (h examRecordsHandlers) listHolds(request route.Request) (route.Result, error) {
	examID, err := examSittingExamID(request)
	if err != nil {
		return route.Result{}, err
	}

	values := request.HTTP.URL.Query()
	for key, items := range values {
		if len(items) != 1 {
			return route.Result{}, route.InvalidRequest(nil)
		}

		switch key {
		case "exam_sitting_id", "submission_id", "include_released", "limit", "cursor":
		default:
			return route.Result{}, route.InvalidRequest(nil)
		}
	}

	scope := model.RetentionHoldScope{
		ExamID:       examID,
		SittingID:    model.ExamSittingID(values.Get("exam_sitting_id")),
		SubmissionID: model.SubmissionID(values.Get("submission_id")),
	}
	if err = scope.Validate(); err != nil {
		return route.Result{}, route.InvalidRequest(err)
	}

	includeReleased := false
	if raw, exists := values["include_released"]; exists {
		if raw[0] != "true" && raw[0] != "false" {
			return route.Result{}, route.InvalidRequest(nil)
		}

		includeReleased = raw[0] == "true"
	}

	limit, err := request.QueryLimit()
	if err != nil {
		return route.Result{}, err
	}

	query := store.ExamRecordsHoldListOptions{Scope: scope, Limit: limit, IncludeReleased: includeReleased}
	if raw := values.Get("cursor"); raw != "" {
		cursor, decodeErr := route.DecodeCursor(raw, recordsHoldCursorSpec(scope, includeReleased))
		if decodeErr != nil {
			return route.Result{}, route.InvalidRequest(decodeErr)
		}

		query.After = model.RetentionHoldID(cursor.AfterID)
	}

	page, err := h.app.ListRetentionHolds(request.Context, request.Invocation(), query)
	if err != nil {
		return route.Result{}, err
	}
	if page == nil || len(page.Holds) > limit || page.HasMore && len(page.Holds) == 0 {
		return route.Result{}, route.ErrUnpresentable
	}

	response := retentionHoldPageResponse{Items: make([]retentionHoldResponse, 0, len(page.Holds))}
	for _, hold := range page.Holds {
		response.Items = append(response.Items, recordsHoldResponse(hold))
	}

	if page.HasMore {
		response.NextCursor, err = route.EncodeCursor(
			recordsHoldCursor{
				ExamID:          scope.ExamID.String(),
				SittingID:       scope.SittingID.String(),
				SubmissionID:    scope.SubmissionID.String(),
				IncludeReleased: includeReleased,
				AfterID:         page.Holds[len(page.Holds)-1].ID.String(),
			},
			recordsHoldCursorSpec(scope, includeReleased),
		)
		if err != nil {
			return route.Result{}, route.Unpresentable(err)
		}
	}

	return route.JSON(http.StatusOK, response).WithHeaders(privateNoStoreHeaders()), nil
}

func requiredRecordsInteger(field string, value route.Optional[int64], minimum int64) (int64, error) {
	p := value.ValuePointer()
	if p == nil || *p < minimum {
		return 0, route.InvalidRequest(nil)
	}
	return *p, nil
}
func recordsSubmissionScope(request route.Request) (model.RetentionHoldScope, error) {
	examID, sittingID, err := examSittingIDs(request)
	if err != nil {
		return model.RetentionHoldScope{}, err
	}
	id, err := route.PathID[model.SubmissionID](request, "submission_id")
	if err != nil {
		return model.RetentionHoldScope{}, err
	}
	return model.RetentionHoldScope{ExamID: examID, SittingID: sittingID, SubmissionID: id}, nil
}
func recordsOptionalScope(examID model.ExamID, sitting, submission route.Optional[string]) (model.RetentionHoldScope, error) {
	scope := model.RetentionHoldScope{ExamID: examID}
	if sitting.IsSet() {
		if p := sitting.ValuePointer(); p == nil || *p == "" {
			return scope, route.InvalidRequest(nil)
		} else {
			scope.SittingID = model.ExamSittingID(*p)
		}
	}
	if submission.IsSet() {
		if p := submission.ValuePointer(); p == nil || *p == "" {
			return scope, route.InvalidRequest(nil)
		} else {
			scope.SubmissionID = model.SubmissionID(*p)
		}
	}
	if err := scope.Validate(); err != nil {
		return scope, route.InvalidRequest(err)
	}
	return scope, nil
}

func recordsCompletionResponse(value model.ExamSittingRecordsCompletion) examRecordsCompletionResponse {
	out := examRecordsCompletionResponse{
		SittingID:                 value.SittingID.String(),
		Revision:                  value.Revision,
		EvidenceRevision:          value.EvidenceRevision,
		CompletedEvidenceRevision: value.CompletedEvidenceRevision,
		Current:                   value.IsCurrent(),
		CompletedByUserID:         value.CompletedByUserID.String(),
	}
	if value.CompletedAt.Valid {
		out.CompletedAt = model.FormatRFC3339(value.CompletedAt.Time)
	}
	if value.StaleAt.Valid {
		out.StaleAt = model.FormatRFC3339(value.StaleAt.Time)
	}

	return out
}
func recordsWaiverResponse(value *model.SubmissionReviewWaiver) *submissionReviewWaiverResponse {
	if value == nil {
		return nil
	}
	return &submissionReviewWaiverResponse{
		DeliveryInventoryRevision: value.DeliveryInventoryRevision,
		InventoryInvalidated:      value.InventoryInvalidated,
		SubmissionID:              value.SubmissionID.String(),
		Revision:                  value.Revision,
		ReviewRevision:            value.ReviewRevision,
		DiscrepancyCount:          value.DiscrepancyCount,
		ActorUserID:               value.ActorUserID.String(),
		RecordedAt:                model.FormatRFC3339(value.RecordedAt),
		ReasonCode:                value.ReasonCode,
		PrivateReason:             value.PrivateReason,
	}
}
func recordsHoldResponse(value model.RetentionHold) retentionHoldResponse {
	out := retentionHoldResponse{
		ID:                              value.ID.String(),
		ExamID:                          value.Scope.ExamID.String(),
		SittingID:                       value.Scope.SittingID.String(),
		SubmissionID:                    value.Scope.SubmissionID.String(),
		Revision:                        value.Revision,
		CreatedAt:                       model.FormatRFC3339(value.CreatedAt),
		CreatedByUserID:                 value.CreatedByUserID.String(),
		ReasonCode:                      value.ReasonCode,
		PrivateReason:                   value.PrivateReason,
		ReleasedByUserID:                value.ReleasedByUserID.String(),
		ReleaseReasonCode:               value.ReleaseReasonCode,
		ReleasePrivateReason:            value.ReleasePrivateReason,
		WorkRetiredSubmissionCount:      value.WorkRetiredSubmissionCount,
		IntegrityRetiredSubmissionCount: value.IntegrityRetiredSubmissionCount,
	}
	if value.ReleasedAt.Valid {
		out.ReleasedAt = model.FormatRFC3339(value.ReleasedAt.Time)
	}

	return out
}

type recordsHoldCursor struct {
	ExamID          string `json:"exam_id"`
	SittingID       string `json:"exam_sitting_id"`
	SubmissionID    string `json:"submission_id"`
	IncludeReleased bool   `json:"include_released"`
	AfterID         string `json:"after_id"`
}

func recordsHoldCursorSpec(scope model.RetentionHoldScope, includeReleased bool) route.CursorSpec[recordsHoldCursor] {
	return route.CursorSpec[recordsHoldCursor]{
		Label:                "retention holds",
		MaximumEncodedLength: 512,
		Members:              []string{"exam_id", "exam_sitting_id", "submission_id", "include_released", "after_id"},
		Validate: func(c recordsHoldCursor) error {
			if scope.Validate() != nil ||
				c.ExamID != scope.ExamID.String() ||
				c.SittingID != scope.SittingID.String() ||
				c.SubmissionID != scope.SubmissionID.String() ||
				c.IncludeReleased != includeReleased ||
				!model.RetentionHoldID(c.AfterID).IsValid() {
				return errors.New("retention hold cursor scope changed")
			}

			return nil
		},
	}
}
