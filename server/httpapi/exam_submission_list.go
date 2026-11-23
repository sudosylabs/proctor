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

	"github.com/sudosylabs/proctor/server/app/exam/review"
	"github.com/sudosylabs/proctor/server/httpapi/route"
	"github.com/sudosylabs/proctor/server/model"
	"github.com/sudosylabs/proctor/server/store"
)

const submissionListCursorKind = "submission_list"

type submissionListCursor struct {
	Kind         string `json:"kind"`
	Sort         string `json:"sort"`
	SubmittedAt  string `json:"submitted_at,omitempty"`
	SubmissionID string `json:"submission_id"`
}

func submissionListCursorSpec(sort store.SubmissionListSort) route.CursorSpec[submissionListCursor] {
	return route.CursorSpec[submissionListCursor]{
		Label:                "Submission list",
		MaximumEncodedLength: route.DefaultMaximumCursorLength,
		Members:              []string{"kind", "sort", "submitted_at", "submission_id"},
		Validate: func(cursor submissionListCursor) error {
			if cursor.Kind != submissionListCursorKind || cursor.Sort != string(sort) ||
				!model.SubmissionID(cursor.SubmissionID).IsValid() {
				return errors.New("invalid Submission list keyset")
			}
			if sort == store.SubmissionListByName {
				if cursor.SubmittedAt != "" {
					return errors.New("invalid Submission list keyset")
				}
				return nil
			}
			if _, err := model.ParseRFC3339(cursor.SubmittedAt); err != nil {
				return errors.New("invalid Submission list keyset")
			}
			return nil
		},
	}
}

// submissionListReviewResponse is a row's review, present only for a reader
// who may see reviews. state is none until a manager opens one.
type submissionListReviewResponse struct {
	ID                string             `json:"id,omitempty"`
	State             string             `json:"state"`
	ReleaseState      string             `json:"release_state"`
	Revision          int64              `json:"revision,omitempty"`
	FinalizedAt       string             `json:"finalized_at,omitempty"`
	FinalizedByUserID string             `json:"finalized_by_user_id,omitempty"`
	FinalizedBy       *personRefResponse `json:"finalized_by,omitempty"`
	ReleasedAt        string             `json:"released_at,omitempty"`
}

type submissionListItemResponse struct {
	SubmissionID             string                        `json:"submission_id"`
	Exam                     *examRefResponse              `json:"exam,omitempty"`
	ExamID                   string                        `json:"exam_id"`
	ExamSittingID            string                        `json:"exam_sitting_id"`
	ExamAttemptID            string                        `json:"exam_attempt_id"`
	Candidate                personRefResponse             `json:"candidate"`
	SubmittedAt              string                        `json:"submitted_at"`
	ReceiptCode              string                        `json:"receipt_code"`
	FlagCount                int64                         `json:"flag_count"`
	UnresolvedIntegrityCount int64                         `json:"unresolved_integrity_count"`
	IntegrityState           string                        `json:"integrity_state"`
	Review                   *submissionListReviewResponse `json:"review,omitempty"`
}

// submissionListCountsResponse counts the whole list, whatever the filter.
// reviewed and released appear only to a reader who may see reviews.
type submissionListCountsResponse struct {
	Total              int64  `json:"total"`
	Reviewed           *int64 `json:"reviewed,omitempty"`
	Released           *int64 `json:"released,omitempty"`
	FlaggedSubmissions int64  `json:"flagged_submissions"`
	Flags              int64  `json:"flags"`
}

type submissionListResponse struct {
	Counts     *submissionListCountsResponse `json:"counts,omitempty"`
	Items      []submissionListItemResponse  `json:"items"`
	NextCursor string                        `json:"next_cursor,omitempty"`
}

func (h examIntegrityReviewHandlers) listSittingSubmissions(request route.Request) (route.Result, error) {
	examID, err := route.PathID[model.ExamID](request, "exam_id")
	if err != nil {
		return route.Result{}, err
	}
	sittingID, err := route.PathID[model.ExamSittingID](request, "exam_sitting_id")
	if err != nil {
		return route.Result{}, err
	}
	query, err := submissionListQuery(request.HTTP.URL.Query())
	if err != nil {
		return route.Result{}, route.InvalidRequest(err)
	}
	query.ExamID, query.SittingID = examID, sittingID
	page, err := h.app.ListSittingSubmissions(request.Context, request.Invocation(), query)
	if err != nil {
		return route.Result{}, err
	}
	return h.submissionListResult(request, query.Sort, page, false)
}

func (h examIntegrityReviewHandlers) listSubmissions(request route.Request) (route.Result, error) {
	query, err := submissionListQuery(request.HTTP.URL.Query())
	if err != nil {
		return route.Result{}, route.InvalidRequest(err)
	}
	page, err := h.app.ListSubmissions(request.Context, request.Invocation(), query)
	if err != nil {
		return route.Result{}, err
	}
	return h.submissionListResult(request, query.Sort, page, true)
}

func (h examIntegrityReviewHandlers) submissionListResult(
	request route.Request,
	sort store.SubmissionListSort,
	page review.SubmissionListPage,
	acrossExams bool,
) (route.Result, error) {
	ids := make([]model.UserID, 0, 2*len(page.Items))
	for _, item := range page.Items {
		if !item.Candidate.UserID.IsValid() {
			return route.Result{}, route.ErrUnpresentable
		}
		ids = append(ids, item.Candidate.UserID)
		if item.Review != nil {
			ids = append(ids, item.Review.FinalizedByUserID)
		}
	}
	refs, err := namePeople(request, h.people, ids...)
	if err != nil {
		return route.Result{}, err
	}

	response := submissionListResponse{Items: make([]submissionListItemResponse, 0, len(page.Items))}
	if counts := page.Counts; counts != nil {
		response.Counts = &submissionListCountsResponse{Total: counts.Total, Reviewed: counts.Reviewed,
			Released: counts.Released, FlaggedSubmissions: counts.FlaggedSubmissions, Flags: counts.Flags}
	}
	for _, item := range page.Items {
		row := submissionListItemResponse{
			SubmissionID: item.SubmissionID.String(), ExamID: item.ExamID.String(),
			ExamSittingID: item.SittingID.String(), ExamAttemptID: item.AttemptID.String(),
			Candidate:   *personRef(refs, item.Candidate.UserID),
			SubmittedAt: model.FormatRFC3339(item.SubmittedAt), ReceiptCode: item.ReceiptCode.Display(),
			FlagCount: item.FlagCount, UnresolvedIntegrityCount: item.UnresolvedIntegrityCount,
			IntegrityState: string(item.IntegrityState),
		}
		if acrossExams {
			row.Exam = &examRefResponse{ID: item.ExamID.String(), DisplayName: item.ExamTitle}
		}
		if item.ReviewVisible {
			row.Review = &submissionListReviewResponse{State: string(store.SubmissionListReviewNone),
				ReleaseState: string(model.SubmissionReviewWithheld)}
			if value := item.Review; value != nil {
				row.Review = &submissionListReviewResponse{
					ID: value.ID.String(), State: string(value.State), ReleaseState: string(value.ReleaseState),
					Revision: value.Revision, FinalizedByUserID: value.FinalizedByUserID.String(),
					FinalizedBy: personRef(refs, value.FinalizedByUserID),
				}
				if value.FinalizedAt.Valid {
					row.Review.FinalizedAt = model.FormatRFC3339(value.FinalizedAt.Time)
				}
				if value.ReleasedAt.Valid {
					row.Review.ReleasedAt = model.FormatRFC3339(value.ReleasedAt.Time)
				}
			}
		}
		response.Items = append(response.Items, row)
	}

	if page.HasMore {
		if len(page.Items) == 0 {
			return route.Result{}, route.ErrUnpresentable
		}
		last := page.Items[len(page.Items)-1]
		cursor := submissionListCursor{Kind: submissionListCursorKind, Sort: string(sort), SubmissionID: last.SubmissionID.String()}
		if sort == store.SubmissionListBySubmitted {
			cursor.SubmittedAt = model.FormatRFC3339(last.SubmittedAt)
		}
		if response.NextCursor, err = route.EncodeCursor(cursor, submissionListCursorSpec(sort)); err != nil {
			return route.Result{}, route.Unpresentable(err)
		}
	}
	return route.JSON(http.StatusOK, response).WithHeaders(noStoreHeaders()), nil
}

// submissionListQuery reads the list's filters, sort and page. review_state
// and release_state may repeat; a value may appear once.
func submissionListQuery(values url.Values) (review.SubmissionListQuery, error) {
	query := review.SubmissionListQuery{Sort: store.SubmissionListBySubmitted, Limit: 50}
	if raw := values.Get("sort"); raw != "" {
		query.Sort = store.SubmissionListSort(raw)
		if query.Sort != store.SubmissionListBySubmitted && query.Sort != store.SubmissionListByName {
			return query, errors.New("sort must be submitted or name")
		}
	}
	for _, raw := range values["review_state"] {
		state := store.SubmissionListReviewState(raw)
		if !state.IsValid() || slices.Contains(query.Filter.ReviewStates, state) {
			return query, errors.New("invalid review_state")
		}
		query.Filter.ReviewStates = append(query.Filter.ReviewStates, state)
	}
	for _, raw := range values["release_state"] {
		state := model.SubmissionReviewReleaseState(raw)
		if state != model.SubmissionReviewWithheld && state != model.SubmissionReviewReleased ||
			slices.Contains(query.Filter.ReleaseStates, state) {
			return query, errors.New("invalid release_state")
		}
		query.Filter.ReleaseStates = append(query.Filter.ReleaseStates, state)
	}
	if raw := values.Get("has_flags"); raw != "" {
		value, err := strconv.ParseBool(raw)
		if err != nil || raw != "true" && raw != "false" {
			return query, errors.New("has_flags must be true or false")
		}
		query.Filter.HasFlags = &value
	}
	if raw := values.Get("limit"); raw != "" {
		limit, err := strconv.Atoi(raw)
		if err != nil || limit < 1 || limit > store.SubmissionListMaximum {
			return query, errors.New("limit must be between 1 and 200")
		}
		query.Limit = limit
	}
	if raw := values.Get("cursor"); raw != "" {
		cursor, err := route.DecodeCursor(raw, submissionListCursorSpec(query.Sort))
		if err != nil {
			return query, err
		}
		after := &store.SubmissionListCursor{SubmissionID: model.SubmissionID(cursor.SubmissionID)}
		if cursor.SubmittedAt != "" {
			after.SubmittedAt, _ = model.ParseRFC3339(cursor.SubmittedAt)
		}
		query.After = after
	}
	return query, nil
}

// releaseSittingResultsRequest names the Submissions to release, or asks
// for every finalized, withheld one with all_finalized.
type releaseSittingResultsRequest struct {
	SubmissionIDs []string `json:"submission_ids,omitempty"`
	AllFinalized  bool     `json:"all_finalized,omitempty"`
}

type resultReleaseItemResponse struct {
	SubmissionID string `json:"submission_id"`
	Outcome      string `json:"outcome"`
	ErrorCode    string `json:"error_code,omitempty"`
}

type resultReleaseBatchResponse struct {
	Items []resultReleaseItemResponse `json:"items"`
}

func (h examIntegrityReviewHandlers) releaseSittingResults(request route.Request) (route.Result, error) {
	examID, err := route.PathID[model.ExamID](request, "exam_id")
	if err != nil {
		return route.Result{}, err
	}
	sittingID, err := route.PathID[model.ExamSittingID](request, "exam_sitting_id")
	if err != nil {
		return route.Result{}, err
	}
	var body releaseSittingResultsRequest
	if err = request.DecodeJSON(&body); err != nil {
		return route.Result{}, err
	}
	if body.AllFinalized == (len(body.SubmissionIDs) > 0) || len(body.SubmissionIDs) > store.SubmissionListMaximum {
		return route.Result{}, route.InvalidRequest(errors.New("give submission_ids or all_finalized, not both"))
	}
	ids := make([]model.SubmissionID, 0, len(body.SubmissionIDs))
	for _, raw := range body.SubmissionIDs {
		id, parseErr := model.ParseSubmissionID(raw)
		if parseErr != nil || slices.Contains(ids, id) {
			return route.Result{}, route.InvalidRequest(errors.New("submission_ids must be distinct Submission IDs"))
		}
		ids = append(ids, id)
	}
	result, err := h.app.ReleaseSittingResults(request.Context, request.Invocation(), review.ResultReleaseBatchCommand{
		ExamID: examID, SittingID: sittingID, SubmissionIDs: ids, AllFinalized: body.AllFinalized,
		IdempotencyKey: request.IdempotencyKey,
	})
	if err != nil {
		return route.Result{}, err
	}
	response := resultReleaseBatchResponse{Items: make([]resultReleaseItemResponse, 0, len(result.Items))}
	for _, item := range result.Items {
		response.Items = append(response.Items, resultReleaseItemResponse{
			SubmissionID: item.SubmissionID.String(), Outcome: string(item.Outcome), ErrorCode: item.ErrorCode,
		})
	}
	return route.JSON(http.StatusOK, response).WithHeaders(noStoreHeaders()), nil
}
