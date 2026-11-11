// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/sudosylabs/proctor/server/app/exam/sitting"
	"github.com/sudosylabs/proctor/server/app/invocation"
	"github.com/sudosylabs/proctor/server/httpapi/route"
	"github.com/sudosylabs/proctor/server/model"
	"github.com/sudosylabs/proctor/server/store"
)

// ExamSittingApplication is the application port its handlers call; it
// schedules Sittings and pauses, resumes, extends, closes or cancels them.
type ExamSittingApplication interface {
	ScheduleExamSitting(context.Context, invocation.Invocation, sitting.ScheduleCommand) (store.ExamSittingSnapshot, error)
	GetExamSitting(context.Context, invocation.Invocation, model.ExamID, model.ExamSittingID) (store.ExamSittingSnapshot, error)
	ListExamSittings(context.Context, invocation.Invocation, sitting.ListQuery) (sitting.Page, error)
	UpdateExamSittingSchedule(context.Context, invocation.Invocation, sitting.UpdateScheduleCommand) (store.ExamSittingSnapshot, error)
	CancelExamSitting(context.Context, invocation.Invocation, sitting.CancelCommand) (store.ExamSittingSnapshot, error)
	PauseExamSitting(context.Context, invocation.Invocation, sitting.ManagerTransitionCommand) (store.ExamSittingSnapshot, error)
	ResumeExamSitting(context.Context, invocation.Invocation, sitting.ManagerTransitionCommand) (store.ExamSittingSnapshot, error)
	ExtendExamSitting(context.Context, invocation.Invocation, sitting.ExtendCommand) (store.ExamSittingSnapshot, error)
	CloseExamSitting(context.Context, invocation.Invocation, sitting.ManagerTransitionCommand) (store.ExamSittingSnapshot, error)
	ListExamSittingNoShows(context.Context, invocation.Invocation, sitting.ListNoShowsQuery) (sitting.NoShowPage, error)
}

type scheduleExamSittingRequest struct {
	ExamRevisionID   string `json:"exam_revision_id"`
	ClassID          string `json:"class_id"`
	ScheduledStartAt string `json:"scheduled_start_at"`
	ScheduledEndAt   string `json:"scheduled_end_at"`
}

type updateExamSittingScheduleRequest struct {
	ExpectedRevision int64                  `json:"expected_revision"`
	ExamRevisionID   route.Optional[string] `json:"exam_revision_id"`
	ClassID          route.Optional[string] `json:"class_id"`
	ScheduledStartAt route.Optional[string] `json:"scheduled_start_at"`
	ScheduledEndAt   route.Optional[string] `json:"scheduled_end_at"`
}

type cancelExamSittingRequest struct {
	ExpectedRevision int64  `json:"expected_revision"`
	Reason           string `json:"reason"`
}

type examSittingManagerTransitionRequest struct {
	ExpectedRevision int64  `json:"expected_revision"`
	Reason           string `json:"reason"`
}

type extendExamSittingRequest struct {
	ExpectedRevision int64  `json:"expected_revision"`
	ScheduledEndAt   string `json:"scheduled_end_at"`
	Reason           string `json:"reason"`
}

func (b *scheduleExamSittingRequest) UnmarshalJSON(encoded []byte) error {
	type wire scheduleExamSittingRequest
	var decoded wire
	if err := decodeDuplicateFreeExamSittingObject(encoded, &decoded); err != nil {
		return err
	}
	*b = scheduleExamSittingRequest(decoded)
	return nil
}

func (b *updateExamSittingScheduleRequest) UnmarshalJSON(encoded []byte) error {
	type wire updateExamSittingScheduleRequest
	var decoded wire
	if err := decodeDuplicateFreeExamSittingObject(encoded, &decoded); err != nil {
		return err
	}
	*b = updateExamSittingScheduleRequest(decoded)
	return nil
}

func (b *cancelExamSittingRequest) UnmarshalJSON(encoded []byte) error {
	type wire cancelExamSittingRequest
	var decoded wire
	if err := decodeDuplicateFreeExamSittingObject(encoded, &decoded); err != nil {
		return err
	}
	*b = cancelExamSittingRequest(decoded)
	return nil
}

func (b *examSittingManagerTransitionRequest) UnmarshalJSON(encoded []byte) error {
	type wire examSittingManagerTransitionRequest
	var decoded wire
	if err := decodeDuplicateFreeExamSittingObject(encoded, &decoded); err != nil {
		return err
	}
	*b = examSittingManagerTransitionRequest(decoded)
	return nil
}

func (b *extendExamSittingRequest) UnmarshalJSON(encoded []byte) error {
	type wire extendExamSittingRequest
	var decoded wire
	if err := decodeDuplicateFreeExamSittingObject(encoded, &decoded); err != nil {
		return err
	}
	*b = extendExamSittingRequest(decoded)
	return nil
}

func decodeDuplicateFreeExamSittingObject(encoded []byte, target any) error {
	if !utf8.Valid(encoded) {
		return errors.New("Exam Sitting request must be valid UTF-8")
	}

	decoder := json.NewDecoder(bytes.NewReader(encoded))
	token, err := decoder.Token()
	if err != nil || token != json.Delim('{') {
		return errors.New("Exam Sitting request must be a JSON object")
	}

	seen := make(map[string]struct{})
	for decoder.More() {
		keyToken, tokenErr := decoder.Token()
		if tokenErr != nil {
			return tokenErr
		}

		key, ok := keyToken.(string)
		if !ok {
			return errors.New("Exam Sitting request member is invalid")
		}
		if _, duplicate := seen[key]; duplicate {
			return errors.New("Exam Sitting request contains a duplicate member")
		}

		seen[key] = struct{}{}
		var value json.RawMessage
		if tokenErr = decoder.Decode(&value); tokenErr != nil {
			return tokenErr
		}
	}

	if _, err = decoder.Token(); err != nil {
		return err
	}
	if _, err = decoder.Token(); err != io.EOF {
		return errors.New("Exam Sitting request contains trailing JSON")
	}

	strict := json.NewDecoder(bytes.NewReader(encoded))
	strict.DisallowUnknownFields()
	return strict.Decode(target)
}

type examSittingResponse struct {
	ID               string `json:"id"`
	ExamID           string `json:"exam_id"`
	ExamRevisionID   string `json:"exam_revision_id"`
	ClassID          string `json:"class_id"`
	ScheduledStartAt string `json:"scheduled_start_at"`
	ScheduledEndAt   string `json:"scheduled_end_at"`
	State            string `json:"state"`
	CreatedAt        string `json:"created_at"`
	UpdatedAt        string `json:"updated_at"`
	OpenedAt         string `json:"opened_at,omitempty"`
	PausedAt         string `json:"paused_at,omitempty"`
	ClosingAt        string `json:"closing_at,omitempty"`
	ClosedAt         string `json:"closed_at,omitempty"`
	CanceledAt       string `json:"canceled_at,omitempty"`
	ReasonCode       string `json:"reason_code,omitempty"`
	Revision         int64  `json:"revision"`
}

type examSittingListResponse struct {
	Items      []examSittingResponse `json:"items"`
	NextCursor string                `json:"next_cursor,omitempty"`
}

type examSittingNoShowResponse struct {
	CandidateUserID string `json:"candidate_user_id"`
}

type examSittingNoShowListResponse struct {
	Items      []examSittingNoShowResponse `json:"items"`
	NextCursor string                      `json:"next_cursor,omitempty"`
}

type examSittingNoShowCursorWire struct {
	CandidateUserID string `json:"candidate_user_id"`
}

type examSittingCursor struct {
	StartAt time.Time
	ID      model.ExamSittingID
}

type examSittingCursorWire struct {
	StartAt string `json:"start_at"`
	ID      string `json:"id"`
}

type examSittingHandlers struct{ app ExamSittingApplication }

func examSittingResource(app ExamSittingApplication) route.Resource {
	h := examSittingHandlers{app: app}
	collection := "/exams/{exam_id}/sittings"
	member := "/exams/{exam_id}/sittings/{exam_sitting_id}"
	cancel := "/exams/{exam_id}/sittings/{exam_sitting_id}/cancel"
	pause := "/exams/{exam_id}/sittings/{exam_sitting_id}/pause"
	resume := "/exams/{exam_id}/sittings/{exam_sitting_id}/resume"
	extend := "/exams/{exam_id}/sittings/{exam_sitting_id}/extend"
	closeSitting := "/exams/{exam_id}/sittings/{exam_sitting_id}/close"
	noShows := "/exams/{exam_id}/sittings/{exam_sitting_id}/no-shows"
	return route.NewResource(
		"exam-sittings",
		route.Endpoint{
			Method:      http.MethodPost,
			Path:        collection,
			Auth:        route.AuthPrincipalRequired,
			Idempotency: route.IdempotencyRequired,
			Errors: []string{
				"authorization.request.invalid",
				"authorization.unavailable",
				"audit.unavailable",
				"request.invalid",
				"resource.not_found",
				"exam.sitting.invalid",
				"exam.sitting.conflict",
				"exam.sitting.unavailable",
				"exam.archived",
				"exam.sitting.class_ineligible",
				"exam.sitting.schedule_outside_period",
				"exam.sitting.schedule_not_future",
				"idempotency.key_required",
				"idempotency.key_invalid",
				"idempotency.conflict",
				"idempotency.in_progress",
			},
			Handle: h.schedule,
		},
		route.Endpoint{
			Method: http.MethodGet,
			Path:   collection,
			Auth:   route.AuthPrincipalRequired,
			Errors: []string{
				"authorization.request.invalid",
				"authorization.unavailable",
				"request.invalid",
				"resource.not_found",
				"exam.sitting.invalid",
				"exam.sitting.unavailable",
			},
			Handle: h.list,
		},
		route.Endpoint{
			Method: http.MethodGet,
			Path:   member,
			Auth:   route.AuthPrincipalRequired,
			Errors: []string{
				"authorization.request.invalid",
				"authorization.unavailable",
				"request.invalid",
				"resource.not_found",
				"exam.sitting.invalid",
				"exam.sitting.unavailable",
			},
			Handle: h.get,
		},
		route.Endpoint{
			Method:      http.MethodPatch,
			Path:        member,
			Auth:        route.AuthPrincipalRequired,
			Idempotency: route.IdempotencyRequired,
			Errors: []string{
				"authorization.request.invalid",
				"authorization.unavailable",
				"audit.unavailable",
				"request.invalid",
				"resource.not_found",
				"exam.sitting.invalid",
				"exam.sitting.conflict",
				"exam.sitting.unavailable",
				"exam.archived",
				"exam.sitting.revision_conflict",
				"exam.sitting.no_changes",
				"exam.sitting.state_conflict",
				"exam.sitting.class_ineligible",
				"exam.sitting.schedule_outside_period",
				"exam.sitting.schedule_not_future",
				"idempotency.key_required",
				"idempotency.key_invalid",
				"idempotency.conflict",
				"idempotency.in_progress",
			},
			Handle: h.updateSchedule,
		},
		route.Endpoint{
			Method:      http.MethodPost,
			Path:        cancel,
			Auth:        route.AuthPrincipalRequired,
			Idempotency: route.IdempotencyRequired,
			Errors: []string{
				"authorization.request.invalid",
				"authorization.unavailable",
				"audit.unavailable",
				"request.invalid",
				"resource.not_found",
				"exam.sitting.invalid",
				"exam.sitting.conflict",
				"exam.sitting.unavailable",
				"exam.archived",
				"exam.sitting.revision_conflict",
				"exam.sitting.state_conflict",
				"idempotency.key_required",
				"idempotency.key_invalid",
				"idempotency.conflict",
				"idempotency.in_progress",
			},
			Handle: h.cancel,
		},
		route.Endpoint{
			Method:      http.MethodPost,
			Path:        pause,
			Auth:        route.AuthPrincipalRequired,
			Idempotency: route.IdempotencyRequired,
			Errors: []string{
				"authorization.request.invalid",
				"authorization.unavailable",
				"audit.unavailable",
				"request.invalid",
				"resource.not_found",
				"exam.sitting.invalid",
				"exam.sitting.conflict",
				"exam.sitting.unavailable",
				"exam.sitting.revision_conflict",
				"exam.sitting.state_conflict",
				"exam.sitting.deadline_reached",
				"idempotency.key_required",
				"idempotency.key_invalid",
				"idempotency.conflict",
				"idempotency.in_progress",
				"exam.archived",
			},
			Handle: h.pause,
		},
		route.Endpoint{
			Method:      http.MethodPost,
			Path:        resume,
			Auth:        route.AuthPrincipalRequired,
			Idempotency: route.IdempotencyRequired,
			Errors: []string{
				"authorization.request.invalid",
				"authorization.unavailable",
				"audit.unavailable",
				"request.invalid",
				"resource.not_found",
				"exam.sitting.invalid",
				"exam.sitting.conflict",
				"exam.sitting.unavailable",
				"exam.archived",
				"exam.sitting.revision_conflict",
				"exam.sitting.state_conflict",
				"exam.sitting.deadline_reached",
				"idempotency.key_required",
				"idempotency.key_invalid",
				"idempotency.conflict",
				"idempotency.in_progress",
			},
			Handle: h.resume,
		},
		route.Endpoint{
			Method:      http.MethodPost,
			Path:        extend,
			Auth:        route.AuthPrincipalRequired,
			Idempotency: route.IdempotencyRequired,
			Errors: []string{
				"authorization.request.invalid",
				"authorization.unavailable",
				"audit.unavailable",
				"request.invalid",
				"resource.not_found",
				"exam.sitting.invalid",
				"exam.sitting.conflict",
				"exam.sitting.unavailable",
				"exam.archived",
				"exam.sitting.revision_conflict",
				"exam.sitting.state_conflict",
				"exam.sitting.deadline_reached",
				"exam.sitting.extension_not_later",
				"exam.sitting.class_ineligible",
				"exam.sitting.schedule_outside_period",
				"idempotency.key_required",
				"idempotency.key_invalid",
				"idempotency.conflict",
				"idempotency.in_progress",
			},
			Handle: h.extend,
		},
		route.Endpoint{
			Method:      http.MethodPost,
			Path:        closeSitting,
			Auth:        route.AuthPrincipalRequired,
			Idempotency: route.IdempotencyRequired,
			Errors: []string{
				"authorization.request.invalid",
				"authorization.unavailable",
				"audit.unavailable",
				"request.invalid",
				"resource.not_found",
				"exam.sitting.invalid",
				"exam.sitting.conflict",
				"exam.sitting.unavailable",
				"exam.sitting.revision_conflict",
				"exam.sitting.state_conflict",
				"exam.sitting.deadline_reached",
				"idempotency.key_required",
				"idempotency.key_invalid",
				"idempotency.conflict",
				"idempotency.in_progress",
				"exam.archived",
			},
			Handle: h.close,
		},
		route.Endpoint{
			Method: http.MethodGet,
			Path:   noShows,
			Auth:   route.AuthPrincipalRequired,
			Errors: []string{
				"authorization.request.invalid",
				"authorization.unavailable",
				"request.invalid",
				"resource.not_found",
				"exam.sitting.invalid",
				"exam.sitting.unavailable",
				"exam.sitting.state_conflict",
			},
			Handle: h.listNoShows,
		},
	)
}

func (h examSittingHandlers) listNoShows(request route.Request) (route.Result, error) {
	examID, sittingID, err := examSittingIDs(request)
	if err != nil {
		return route.Result{}, err
	}

	query := sitting.ListNoShowsQuery{ExamID: examID, SittingID: sittingID, Limit: 50}
	values := request.HTTP.URL.Query()
	if raw := values.Get("limit"); raw != "" {
		query.Limit, err = strconv.Atoi(raw)
		if err != nil || query.Limit < 1 || query.Limit > 200 {
			return route.Result{}, route.InvalidRequest(errors.New("must be between 1 and 200"))
		}
	}

	if raw := values.Get("cursor"); raw != "" {
		query.AfterCandidateUserID, err = decodeExamSittingNoShowCursor(raw)
		if err != nil {
			return route.Result{}, route.InvalidRequest(err)
		}
	}

	page, err := h.app.ListExamSittingNoShows(request.Context, request.Invocation(), query)
	if err != nil {
		return route.Result{}, err
	}

	response := examSittingNoShowListResponse{Items: make([]examSittingNoShowResponse, 0, len(page.Items))}
	for _, item := range page.Items {
		response.Items = append(response.Items, examSittingNoShowResponse{CandidateUserID: item.CandidateUserID.String()})
	}

	if page.HasMore && len(page.Items) == 0 {
		return route.Result{}, route.Unpresentable(errors.New("no-show page made no progress"))
	}

	if page.HasMore {
		response.NextCursor, err = encodeExamSittingNoShowCursor(page.Items[len(page.Items)-1].CandidateUserID)
		if err != nil {
			return route.Result{}, route.Unpresentable(err)
		}
	}

	return route.JSON(http.StatusOK, response), nil
}

func encodeExamSittingNoShowCursor(id model.UserID) (string, error) {
	return route.EncodeCursor(examSittingNoShowCursorWire{CandidateUserID: id.String()}, examSittingNoShowCursorSpec())
}

func decodeExamSittingNoShowCursor(raw string) (model.UserID, error) {
	wire, err := route.DecodeCursor(raw, examSittingNoShowCursorSpec())
	if err != nil {
		return "", err
	}
	id, err := model.ParseUserID(wire.CandidateUserID)
	if err != nil {
		return "", errors.New("invalid no-show cursor")
	}
	return id, nil
}

func examSittingNoShowCursorSpec() route.CursorSpec[examSittingNoShowCursorWire] {
	return route.CursorSpec[examSittingNoShowCursorWire]{
		Label:                "no-show",
		MaximumEncodedLength: 342,
		Members:              []string{"candidate_user_id"},
		Validate: func(cursor examSittingNoShowCursorWire) error {
			if !model.UserID(cursor.CandidateUserID).IsValid() {
				return errors.New("invalid no-show keyset")
			}
			return nil
		},
	}
}

func (h examSittingHandlers) schedule(request route.Request) (route.Result, error) {
	examID, err := examSittingExamID(request)
	if err != nil {
		return route.Result{}, err
	}

	var body scheduleExamSittingRequest
	if err = request.DecodeJSON(&body); err != nil {
		return route.Result{}, err
	}

	revisionID, err := model.ParseExamRevisionID(body.ExamRevisionID)
	if err != nil {
		return route.Result{}, route.InvalidRequest(err)
	}

	classID, err := model.ParseClassID(body.ClassID)
	if err != nil {
		return route.Result{}, route.InvalidRequest(err)
	}

	startAt, err := parseExamSittingTime("scheduled_start_at", body.ScheduledStartAt)
	if err != nil {
		return route.Result{}, err
	}

	endAt, err := parseExamSittingTime("scheduled_end_at", body.ScheduledEndAt)
	if err != nil {
		return route.Result{}, err
	}
	if !startAt.Before(endAt) {
		return route.Result{}, route.InvalidRequest(errors.New("must be a nonempty interval"))
	}

	view, err := h.app.ScheduleExamSitting(request.Context, request.Invocation(), sitting.ScheduleCommand{
		ExamID:           examID,
		ExamRevisionID:   revisionID,
		ClassID:          classID,
		ScheduledStartAt: startAt,
		ScheduledEndAt:   endAt,
		IdempotencyKey:   request.IdempotencyKey,
	})
	if err != nil {
		return route.Result{}, err
	}

	return route.JSON(http.StatusCreated, examSittingResponseFromView(view)), nil
}

func (h examSittingHandlers) get(request route.Request) (route.Result, error) {
	examID, sittingID, err := examSittingIDs(request)
	if err != nil {
		return route.Result{}, err
	}
	view, err := h.app.GetExamSitting(request.Context, request.Invocation(), examID, sittingID)
	if err != nil {
		return route.Result{}, err
	}
	return route.JSON(http.StatusOK, examSittingResponseFromView(view)), nil
}

func (h examSittingHandlers) list(request route.Request) (route.Result, error) {
	examID, err := examSittingExamID(request)
	if err != nil {
		return route.Result{}, err
	}

	query := sitting.ListQuery{ExamID: examID, Limit: 50}
	values := request.HTTP.URL.Query()
	if raw := values.Get("class_id"); raw != "" {
		query.ClassID, err = model.ParseClassID(raw)
		if err != nil {
			return route.Result{}, route.InvalidRequest(err)
		}
	}

	query.States, err = parseExamSittingStates(values["state"])
	if err != nil {
		return route.Result{}, err
	}

	endsAfter, startsBefore := values.Get("ends_after"), values.Get("starts_before")
	if (endsAfter == "") != (startsBefore == "") {
		return route.Result{}, route.InvalidRequest(errors.New("ends_after and starts_before must be paired"))
	}

	if endsAfter != "" {
		query.OverlapStartAt, err = parseExamSittingTime("ends_after", endsAfter)
		if err != nil {
			return route.Result{}, err
		}

		query.OverlapEndAt, err = parseExamSittingTime("starts_before", startsBefore)
		if err != nil {
			return route.Result{}, err
		}
		if !query.OverlapStartAt.Before(query.OverlapEndAt) {
			return route.Result{}, route.InvalidRequest(errors.New("must be a nonempty interval"))
		}
	}

	if raw := values.Get("limit"); raw != "" {
		query.Limit, err = strconv.Atoi(raw)
		if err != nil || query.Limit < 1 || query.Limit > 200 {
			return route.Result{}, route.InvalidRequest(errors.New("must be between 1 and 200"))
		}
	}

	if raw := values.Get("cursor"); raw != "" {
		cursor, cursorErr := decodeExamSittingCursor(raw)
		if cursorErr != nil {
			return route.Result{}, route.InvalidRequest(cursorErr)
		}

		query.BeforeScheduledStartAt, query.BeforeSittingID = cursor.StartAt, cursor.ID
	}

	page, err := h.app.ListExamSittings(request.Context, request.Invocation(), query)
	if err != nil {
		return route.Result{}, err
	}

	response := examSittingListResponse{Items: make([]examSittingResponse, 0, len(page.Items))}
	for _, view := range page.Items {
		response.Items = append(response.Items, examSittingResponseFromView(view))
	}

	if page.HasMore && len(page.Items) == 0 {
		return route.Result{}, route.Unpresentable(errors.New("sitting page made no progress"))
	}

	if page.HasMore {
		last := page.Items[len(page.Items)-1].Sitting
		if last == nil {
			return route.Result{}, route.ErrUnpresentable
		}

		response.NextCursor, err = encodeExamSittingCursor(examSittingCursor{StartAt: last.ScheduledStartAt, ID: last.ID})
		if err != nil {
			return route.Result{}, route.Unpresentable(err)
		}
	}

	return route.JSON(http.StatusOK, response), nil
}

func (h examSittingHandlers) updateSchedule(request route.Request) (route.Result, error) {
	examID, sittingID, err := examSittingIDs(request)
	if err != nil {
		return route.Result{}, err
	}

	var body updateExamSittingScheduleRequest
	if err = request.DecodeJSON(&body); err != nil {
		return route.Result{}, err
	}
	if body.ExpectedRevision < 1 {
		return route.Result{}, route.InvalidRequest(errors.New("must be positive"))
	}
	if noExamSittingScheduleChange(body) {
		return route.Result{}, route.InvalidRequest(errors.New("at least one change is required"))
	}

	command := sitting.UpdateScheduleCommand{
		ExamID:           examID,
		SittingID:        sittingID,
		ExpectedRevision: body.ExpectedRevision,
		IdempotencyKey:   request.IdempotencyKey,
	}
	if body.ExamRevisionID.IsSet() {
		if body.ExamRevisionID.IsNull() {
			return route.Result{}, route.InvalidRequest(errors.New("must not be null"))
		}

		parsed, parseErr := model.ParseExamRevisionID(*body.ExamRevisionID.ValuePointer())
		if parseErr != nil {
			return route.Result{}, route.InvalidRequest(parseErr)
		}

		command.ExamRevisionID = &parsed
	}

	if body.ClassID.IsSet() {
		if body.ClassID.IsNull() {
			return route.Result{}, route.InvalidRequest(errors.New("must not be null"))
		}

		parsed, parseErr := model.ParseClassID(*body.ClassID.ValuePointer())
		if parseErr != nil {
			return route.Result{}, route.InvalidRequest(parseErr)
		}

		command.ClassID = &parsed
	}

	command.ScheduledStartAt, err = parseOptionalExamSittingTime("scheduled_start_at", body.ScheduledStartAt)
	if err != nil {
		return route.Result{}, err
	}

	command.ScheduledEndAt, err = parseOptionalExamSittingTime("scheduled_end_at", body.ScheduledEndAt)
	if err != nil {
		return route.Result{}, err
	}

	view, err := h.app.UpdateExamSittingSchedule(request.Context, request.Invocation(), command)
	if err != nil {
		return route.Result{}, err
	}

	return route.JSON(http.StatusOK, examSittingResponseFromView(view)), nil
}

func (h examSittingHandlers) cancel(request route.Request) (route.Result, error) {
	examID, sittingID, err := examSittingIDs(request)
	if err != nil {
		return route.Result{}, err
	}

	var body cancelExamSittingRequest
	if err = request.DecodeJSON(&body); err != nil {
		return route.Result{}, err
	}
	if body.ExpectedRevision < 1 {
		return route.Result{}, route.InvalidRequest(errors.New("must be positive"))
	}

	if !validExamSittingManagerReason(body.Reason) {
		return route.Result{}, route.InvalidRequest(
			errors.New("must be trimmed valid UTF-8 between 1 and 1000 characters and at most 4000 bytes"),
		)
	}

	view, err := h.app.CancelExamSitting(request.Context, request.Invocation(), sitting.CancelCommand{
		ExamID:           examID,
		SittingID:        sittingID,
		ExpectedRevision: body.ExpectedRevision,
		PrivateReason:    body.Reason,
		IdempotencyKey:   request.IdempotencyKey,
	})
	if err != nil {
		return route.Result{}, err
	}

	return route.JSON(http.StatusOK, examSittingResponseFromView(view)), nil
}

type examSittingManagerCommandCall func(context.Context, invocation.Invocation, sitting.ManagerTransitionCommand) (store.ExamSittingSnapshot, error)

func (h examSittingHandlers) pause(request route.Request) (route.Result, error) {
	return h.managerTransition(request, "pauseExamSitting", h.app.PauseExamSitting)
}

func (h examSittingHandlers) resume(request route.Request) (route.Result, error) {
	return h.managerTransition(request, "resumeExamSitting", h.app.ResumeExamSitting)
}

func (h examSittingHandlers) close(request route.Request) (route.Result, error) {
	return h.managerTransition(request, "closeExamSitting", h.app.CloseExamSitting)
}

func (h examSittingHandlers) managerTransition(
	request route.Request,
	operation string,
	run examSittingManagerCommandCall,
) (route.Result, error) {
	examID, sittingID, err := examSittingIDs(request)
	if err != nil {
		return route.Result{}, err
	}

	var body examSittingManagerTransitionRequest
	if err = request.DecodeJSON(&body); err != nil {
		return route.Result{}, err
	}
	if body.ExpectedRevision < 1 {
		return route.Result{}, route.InvalidRequest(errors.New("must be positive"))
	}

	if !validExamSittingManagerReason(body.Reason) {
		return route.Result{}, route.InvalidRequest(
			errors.New("must be trimmed valid UTF-8 between 1 and 1000 characters and at most 4000 bytes"),
		)
	}

	view, err := run(request.Context, request.Invocation(), sitting.ManagerTransitionCommand{
		ExamID:           examID,
		SittingID:        sittingID,
		ExpectedRevision: body.ExpectedRevision,
		PrivateReason:    body.Reason,
		IdempotencyKey:   request.IdempotencyKey,
	})
	if err != nil {
		return route.Result{}, err
	}

	return route.JSON(http.StatusOK, examSittingResponseFromView(view)), nil
}

func (h examSittingHandlers) extend(request route.Request) (route.Result, error) {
	examID, sittingID, err := examSittingIDs(request)
	if err != nil {
		return route.Result{}, err
	}

	var body extendExamSittingRequest
	if err = request.DecodeJSON(&body); err != nil {
		return route.Result{}, err
	}
	if body.ExpectedRevision < 1 {
		return route.Result{}, route.InvalidRequest(errors.New("must be positive"))
	}

	endAt, err := parseExamSittingTime("scheduled_end_at", body.ScheduledEndAt)
	if err != nil {
		return route.Result{}, err
	}

	if !validExamSittingManagerReason(body.Reason) {
		return route.Result{}, route.InvalidRequest(
			errors.New("must be trimmed valid UTF-8 between 1 and 1000 characters and at most 4000 bytes"),
		)
	}

	view, err := h.app.ExtendExamSitting(request.Context, request.Invocation(), sitting.ExtendCommand{
		ExamID:           examID,
		SittingID:        sittingID,
		ExpectedRevision: body.ExpectedRevision,
		ScheduledEndAt:   endAt,
		PrivateReason:    body.Reason,
		IdempotencyKey:   request.IdempotencyKey,
	})
	if err != nil {
		return route.Result{}, err
	}

	return route.JSON(http.StatusOK, examSittingResponseFromView(view)), nil
}

func validExamSittingManagerReason(reason string) bool {
	return utf8.ValidString(reason) &&
		reason == strings.TrimSpace(reason) &&
		utf8.RuneCountInString(reason) >= 1 &&
		utf8.RuneCountInString(reason) <= 1000 &&
		len(reason) <= 4000
}

func examSittingExamID(request route.Request) (model.ExamID, error) {
	id, err := route.PathID[model.ExamID](request, "exam_id")
	if err != nil {
		return "", err
	}
	return id, nil
}

func examSittingIDs(request route.Request) (model.ExamID, model.ExamSittingID, error) {
	examID, err := examSittingExamID(request)
	if err != nil {
		return "", "", err
	}
	sittingID, err := route.PathID[model.ExamSittingID](request, "exam_sitting_id")
	if err != nil {
		return "", "", err
	}
	return examID, sittingID, nil
}

func noExamSittingScheduleChange(body updateExamSittingScheduleRequest) bool {
	return !body.ExamRevisionID.IsSet() && !body.ClassID.IsSet() && !body.ScheduledStartAt.IsSet() && !body.ScheduledEndAt.IsSet()
}

func parseOptionalExamSittingTime(field string, value route.Optional[string]) (*time.Time, error) {
	if !value.IsSet() {
		return nil, nil
	}
	if value.IsNull() {
		return nil, route.InvalidRequest(errors.New("must not be null"))
	}
	parsed, err := parseExamSittingTime(field, *value.ValuePointer())
	if err != nil {
		return nil, err
	}
	return &parsed, nil
}

func parseExamSittingTime(field, raw string) (time.Time, error) {
	parsed, err := model.ParseRFC3339(raw)
	if err != nil || parsed.IsZero() {
		return time.Time{}, route.InvalidRequest(errors.New("must be an RFC 3339 instant"))
	}
	return model.TimeUTC(parsed), nil
}

func parseExamSittingStates(raw []string) ([]model.ExamSittingState, error) {
	states := make([]model.ExamSittingState, 0, len(raw))
	seen := make(map[model.ExamSittingState]struct{}, len(raw))
	for _, value := range raw {
		state := model.ExamSittingState(value)
		if !state.IsValid() {
			return nil, route.InvalidRequest(errors.New("must be a valid Exam Sitting state"))
		}
		if _, duplicate := seen[state]; duplicate {
			continue
		}
		seen[state] = struct{}{}
		states = append(states, state)
		if len(states) > 6 {
			return nil, route.InvalidRequest(errors.New("must contain at most six values"))
		}
	}
	return states, nil
}

func examSittingResponseFromView(view store.ExamSittingSnapshot) examSittingResponse {
	if view.Sitting == nil {
		return examSittingResponse{}
	}

	record := view.Sitting
	return examSittingResponse{
		ID:               record.ID.String(),
		ExamID:           record.ExamID.String(),
		ExamRevisionID:   record.ExamRevisionID.String(),
		ClassID:          record.ClassID.String(),
		ScheduledStartAt: model.FormatRFC3339(record.ScheduledStartAt),
		ScheduledEndAt:   model.FormatRFC3339(record.ScheduledEndAt),
		State:            string(record.State),
		CreatedAt:        model.FormatRFC3339(record.CreatedAt),
		UpdatedAt:        model.FormatRFC3339(record.UpdatedAt),
		OpenedAt:         record.OpenedAt.FormatRFC3339(),
		PausedAt:         record.PausedAt.FormatRFC3339(),
		ClosingAt:        record.ClosingAt.FormatRFC3339(),
		ClosedAt:         record.ClosedAt.FormatRFC3339(),
		CanceledAt:       record.CanceledAt.FormatRFC3339(),
		ReasonCode:       string(record.ReasonCode),
		Revision:         record.Revision,
	}
}

func encodeExamSittingCursor(cursor examSittingCursor) (string, error) {
	wire := examSittingCursorWire{StartAt: model.FormatRFC3339(cursor.StartAt), ID: cursor.ID.String()}
	return route.EncodeCursor(wire, examSittingCursorSpec())
}

func decodeExamSittingCursor(raw string) (examSittingCursor, error) {
	wire, err := route.DecodeCursor(raw, examSittingCursorSpec())
	if err != nil {
		return examSittingCursor{}, err
	}
	startAt, _ := model.ParseRFC3339(wire.StartAt)
	id, _ := model.ParseExamSittingID(wire.ID)
	return examSittingCursor{StartAt: model.TimeUTC(startAt), ID: id}, nil
}

func examSittingCursorSpec() route.CursorSpec[examSittingCursorWire] {
	return route.CursorSpec[examSittingCursorWire]{
		Label:                "Exam Sitting",
		MaximumEncodedLength: route.DefaultMaximumCursorLength,
		Members:              []string{"start_at", "id"},
		Validate: func(cursor examSittingCursorWire) error {
			startAt, err := model.ParseRFC3339(cursor.StartAt)
			if err != nil || startAt.IsZero() || !model.ExamSittingID(cursor.ID).IsValid() {
				return errors.New("invalid Exam Sitting keyset")
			}

			return nil
		},
	}
}
