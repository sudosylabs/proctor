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
	"net/url"
	"slices"
	"strconv"

	"github.com/sudosylabs/proctor/server/app/exam/sitting"
	"github.com/sudosylabs/proctor/server/httpapi/route"
	"github.com/sudosylabs/proctor/server/model"
)

const sittingAcrossCursorKind = "exam_sitting_across"

type sittingAcrossCursor struct {
	Kind             string `json:"kind"`
	ScheduledStartAt string `json:"scheduled_start_at"`
	SittingID        string `json:"exam_sitting_id"`
}

func sittingAcrossCursorSpec() route.CursorSpec[sittingAcrossCursor] {
	return route.CursorSpec[sittingAcrossCursor]{
		Label:                "Exam Sitting list",
		MaximumEncodedLength: route.DefaultMaximumCursorLength,
		Members:              []string{"kind", "scheduled_start_at", "exam_sitting_id"},
		Validate: func(cursor sittingAcrossCursor) error {
			if _, err := model.ParseRFC3339(cursor.ScheduledStartAt); err != nil ||
				cursor.Kind != sittingAcrossCursorKind || !model.ExamSittingID(cursor.SittingID).IsValid() {
				return errors.New("invalid Exam Sitting list keyset")
			}
			return nil
		},
	}
}

type sittingAcrossClassResponse struct {
	ID          string `json:"id"`
	DisplayName string `json:"display_name"`
}

// sittingAcrossItemResponse is one sitting of the list across exams. An open
// or paused sitting carries its candidate board's counts and how many of its
// candidates need attention: suspended, failing a check or with unresolved
// integrity attention.
type sittingAcrossItemResponse struct {
	ID                  string                                `json:"id"`
	Exam                submissionListExamResponse            `json:"exam"`
	AcademicUnitID      string                                `json:"academic_unit_id"`
	Class               sittingAcrossClassResponse            `json:"class"`
	State               model.ExamSittingState                `json:"state"`
	Revision            int64                                 `json:"revision"`
	ScheduledStartAt    string                                `json:"scheduled_start_at"`
	ScheduledEndAt      string                                `json:"scheduled_end_at"`
	Counts              *sittingCandidateStatusCountsResponse `json:"counts,omitempty"`
	NeedsAttentionCount *int64                                `json:"needs_attention_count,omitempty"`
}

type sittingAcrossListResponse struct {
	ServerTime string                      `json:"server_time"`
	Items      []sittingAcrossItemResponse `json:"items"`
	NextCursor string                      `json:"next_cursor,omitempty"`
}

func examSittingAcrossResource(app *sitting.Service) route.Resource {
	h := examSittingHandlers{app: app}
	return route.NewResource("exam-sittings-across-exams",
		route.Endpoint{
			Method: http.MethodGet,
			Path:   "/exam-sittings",
			Auth:   route.AuthPrincipalRequired,
			Errors: []string{
				"authorization.request.invalid",
				"authorization.unavailable",
				"request.invalid",
				"resource.not_found",
				"exam.sitting.invalid",
				"exam.sitting.unavailable",
			},
			Handle: h.listAcrossExams,
		},
	)
}

func (h examSittingHandlers) listAcrossExams(request route.Request) (route.Result, error) {
	query, err := sittingAcrossQuery(request.HTTP.URL.Query())
	if err != nil {
		return route.Result{}, route.InvalidRequest(err)
	}
	page, err := h.app.ListSittingsAcrossExams(request.Context, request.Invocation(), query)
	if err != nil {
		return route.Result{}, err
	}
	response := sittingAcrossListResponse{ServerTime: model.FormatRFC3339(page.ServerTime),
		Items: make([]sittingAcrossItemResponse, 0, len(page.Items))}
	for _, item := range page.Items {
		row := sittingAcrossItemResponse{
			ID: item.SittingID.String(), Exam: submissionListExamResponse{ID: item.ExamID.String(), Title: item.ExamTitle},
			AcademicUnitID: item.AcademicUnitID.String(),
			Class:          sittingAcrossClassResponse{ID: item.ClassID.String(), DisplayName: item.ClassDisplayName},
			State:          item.State, Revision: item.Revision,
			ScheduledStartAt: model.FormatRFC3339(item.ScheduledStartAt), ScheduledEndAt: model.FormatRFC3339(item.ScheduledEndAt),
			Counts: sittingCandidateStatusCountsFrom(item.Counts),
		}
		if item.Counts != nil {
			needs := item.Counts.NeedsAttention
			row.NeedsAttentionCount = &needs
		}
		response.Items = append(response.Items, row)
	}
	if page.HasMore {
		if len(page.Items) == 0 {
			return route.Result{}, route.ErrUnpresentable
		}
		last := page.Items[len(page.Items)-1]
		response.NextCursor, err = route.EncodeCursor(sittingAcrossCursor{Kind: sittingAcrossCursorKind,
			ScheduledStartAt: model.FormatRFC3339(last.ScheduledStartAt), SittingID: last.SittingID.String()}, sittingAcrossCursorSpec())
		if err != nil {
			return route.Result{}, route.Unpresentable(err)
		}
	}
	return route.JSON(http.StatusOK, response).WithHeaders(noStoreHeaders()), nil
}

func sittingAcrossQuery(values url.Values) (sitting.AcrossExamsQuery, error) {
	query := sitting.AcrossExamsQuery{Limit: 50}
	for _, raw := range values["state"] {
		state := model.ExamSittingState(raw)
		if !state.IsValid() || slices.Contains(query.States, state) {
			return query, errors.New("invalid state")
		}
		query.States = append(query.States, state)
	}
	if raw := values.Get("starts_before"); raw != "" {
		at, err := model.ParseRFC3339(raw)
		if err != nil {
			return query, errors.New("invalid starts_before")
		}
		query.StartsBefore = at
	}
	if raw := values.Get("ends_after"); raw != "" {
		at, err := model.ParseRFC3339(raw)
		if err != nil {
			return query, errors.New("invalid ends_after")
		}
		query.EndsAfter = at
	}
	if raw := values.Get("academic_unit_id"); raw != "" {
		unitID, err := model.ParseAcademicUnitID(raw)
		if err != nil {
			return query, errors.New("invalid academic_unit_id")
		}
		query.AcademicUnitID = unitID
	}
	if raw := values.Get("limit"); raw != "" {
		limit, err := strconv.Atoi(raw)
		if err != nil || limit < 1 || limit > 200 {
			return query, errors.New("limit must be between 1 and 200")
		}
		query.Limit = limit
	}
	if raw := values.Get("cursor"); raw != "" {
		cursor, err := route.DecodeCursor(raw, sittingAcrossCursorSpec())
		if err != nil {
			return query, err
		}
		query.AfterScheduledStartAt, _ = model.ParseRFC3339(cursor.ScheduledStartAt)
		query.AfterSittingID = model.ExamSittingID(cursor.SittingID)
	}
	return query, nil
}
