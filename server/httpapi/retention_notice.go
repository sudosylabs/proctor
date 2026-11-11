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

	"github.com/sudosylabs/proctor/server/httpapi/route"
	"github.com/sudosylabs/proctor/server/model"
)

type retentionNoticeResponse struct {
	RetirementID      string  `json:"retirement_id"`
	ExamID            string  `json:"exam_id"`
	SittingID         string  `json:"exam_sitting_id"`
	SubmissionID      string  `json:"submission_id"`
	ExamTitle         string  `json:"exam_title"`
	SittingStartAt    string  `json:"sitting_start_at"`
	SittingEndAt      string  `json:"sitting_end_at"`
	Category          string  `json:"category"`
	State             string  `json:"state"`
	CreatedAt         string  `json:"created_at"`
	RetireAfter       string  `json:"retire_after"`
	CanceledAt        *string `json:"canceled_at,omitempty"`
	DeliveryState     string  `json:"delivery_state"`
	DeliveryErrorCode string  `json:"delivery_error_code,omitempty"`
}
type retentionNoticesResponse struct {
	Items      []retentionNoticeResponse `json:"items"`
	NextCursor string                    `json:"next_cursor,omitempty"`
}
type retentionNoticeCursor struct {
	After string `json:"after_retirement_id"`
}

func retentionNoticeCursorSpec() route.CursorSpec[retentionNoticeCursor] {
	return route.CursorSpec[retentionNoticeCursor]{
		Label:                "retention-notices",
		MaximumEncodedLength: 256,
		Members:              []string{"after_retirement_id"},
		Validate: func(c retentionNoticeCursor) error {
			if !model.RetentionRetirementID(c.After).IsValid() {
				return errors.New("invalid retention notice cursor")
			}
			return nil
		},
	}
}
func (h retentionHandlers) notices(request route.Request) (route.Result, error) {
	values := request.HTTP.URL.Query()
	for key, items := range values {
		if len(items) != 1 || (key != "limit" && key != "cursor") {
			return route.Result{}, route.InvalidRequest(nil)
		}
	}

	limit, err := request.QueryLimit()
	if err != nil {
		return route.Result{}, err
	}

	var after model.RetentionRetirementID
	if raw := values.Get("cursor"); raw != "" {
		cursor, cursorErr := route.DecodeCursor(raw, retentionNoticeCursorSpec())
		if cursorErr != nil {
			return route.Result{}, route.InvalidRequest(cursorErr)
		}

		after = model.RetentionRetirementID(cursor.After)
	}

	page, err := h.app.ListRetentionNotices(request.Context, request.Invocation(), after, limit)
	if err != nil {
		return route.Result{}, err
	}
	if page == nil || len(page.Items) > limit || (page.HasMore && len(page.Items) == 0) {
		return route.Result{}, route.ErrUnpresentable
	}

	response := retentionNoticesResponse{Items: make([]retentionNoticeResponse, 0, len(page.Items))}
	previous := after
	for _, n := range page.Items {
		if !n.RetirementID.IsValid() || n.RetirementID <= previous || n.RecipientUserID != request.Invocation().Principal().UserID {
			return route.Result{}, route.ErrUnpresentable
		}

		response.Items = append(response.Items, retentionNoticeResponse{
			RetirementID:      n.RetirementID.String(),
			ExamID:            n.Scope.ExamID.String(),
			SittingID:         n.Scope.SittingID.String(),
			SubmissionID:      n.Scope.SubmissionID.String(),
			ExamTitle:         n.ExamTitle,
			SittingStartAt:    retentionTime(n.SittingStartAt),
			SittingEndAt:      retentionTime(n.SittingEndAt),
			Category:          string(n.Category),
			State:             string(n.State),
			CreatedAt:         retentionTime(n.CreatedAt),
			RetireAfter:       retentionTime(n.RetireAfter),
			CanceledAt:        retentionOptionalTime(n.CanceledAt),
			DeliveryState:     n.DeliveryState,
			DeliveryErrorCode: n.DeliveryErrorCode,
		})
		previous = n.RetirementID
	}

	if page.HasMore {
		response.NextCursor, err = route.EncodeCursor(retentionNoticeCursor{After: previous.String()}, retentionNoticeCursorSpec())
		if err != nil {
			return route.Result{}, route.ErrUnpresentable
		}
	}

	return route.JSON(http.StatusOK, response).WithHeaders(privateNoStoreHeaders()), nil
}
