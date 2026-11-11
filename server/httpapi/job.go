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
	"strconv"

	"github.com/sudosylabs/proctor/server/app/apperror"
	"github.com/sudosylabs/proctor/server/app/invocation"
	"github.com/sudosylabs/proctor/server/app/job"
	"github.com/sudosylabs/proctor/server/app/paging"
	"github.com/sudosylabs/proctor/server/httpapi/route"
	"github.com/sudosylabs/proctor/server/model"
)

const defaultJobPageSize = 50

type JobOperationsApplication interface {
	ListJobs(context.Context, invocation.Invocation, job.ListQuery) (job.Page, error)
	GetJob(context.Context, invocation.Invocation, model.JobID) (job.View, error)
	ListJobAttempts(context.Context, invocation.Invocation, job.AttemptListQuery) (job.AttemptPage, error)
	CancelJob(context.Context, invocation.Invocation, job.CancelCommand) (job.View, error)
	RetryJob(context.Context, invocation.Invocation, job.RetryCommand) (job.View, error)
}

type jobProgressResponse struct {
	Current int64  `json:"current"`
	Total   int64  `json:"total"`
	Stage   string `json:"stage"`
}
type jobResponse struct {
	ID              string               `json:"id"`
	Type            model.JobType        `json:"type"`
	Status          model.JobStatus      `json:"status"`
	CreatedAt       string               `json:"created_at"`
	UpdatedAt       string               `json:"updated_at"`
	AvailableAt     string               `json:"available_at"`
	StartedAt       string               `json:"started_at,omitempty"`
	CompletedAt     string               `json:"completed_at,omitempty"`
	PublicErrorCode string               `json:"public_error_code,omitempty"`
	AttemptCount    int                  `json:"attempt_count"`
	MaximumAttempts int                  `json:"maximum_attempts"`
	Progress        *jobProgressResponse `json:"progress,omitempty"`
	Revision        int64                `json:"revision"`
	// CreatedByUserID and CreatedByDisplayName name the operator who started
	// the Job; both are omitted for recurring and system work.
	CreatedByUserID      string `json:"created_by_user_id,omitempty"`
	CreatedByDisplayName string `json:"created_by_display_name,omitempty"`
	Subject              string `json:"subject,omitempty"`
}

// jobListResponse is the collection envelope; the first page also carries the
// total (see route.CountedList).
type jobListResponse struct {
	Items             []jobResponse `json:"items"`
	NextCursor        string        `json:"next_cursor,omitempty"`
	Total             *int          `json:"total,omitempty"`
	TotalIsLowerBound *bool         `json:"total_is_lower_bound,omitempty"`
}
type jobAttemptResponse struct {
	ID              string                 `json:"id"`
	Number          int                    `json:"number"`
	Status          model.JobAttemptStatus `json:"status"`
	StartedAt       string                 `json:"started_at"`
	HeartbeatAt     string                 `json:"heartbeat_at"`
	LeaseExpiresAt  string                 `json:"lease_expires_at"`
	CompletedAt     string                 `json:"completed_at,omitempty"`
	PublicErrorCode string                 `json:"public_error_code,omitempty"`
}
type jobAttemptListResponse struct {
	Items      []jobAttemptResponse `json:"items"`
	NextCursor string               `json:"next_cursor,omitempty"`
}
type jobCursor struct {
	CreatedAt string `json:"created_at"`
	ID        string `json:"id"`
}
type jobAttemptCursor struct {
	Number int `json:"number"`
}

type jobHandlers struct {
	app JobOperationsApplication
}

func jobResource(application JobOperationsApplication) route.Resource {
	h := jobHandlers{app: application}
	return route.NewResource(
		"jobs",
		route.Endpoint{
			Method: http.MethodGet,
			Path:   "/jobs",
			Auth:   route.AuthPrincipalRequired,
			Errors: []string{
				"authorization.denied",
				"authorization.request.invalid",
				"authorization.unavailable",
				"job.query.invalid",
				"job.unavailable",
				"request.invalid",
				"resource.not_found",
			},
			Handle: h.list,
		},
		route.Endpoint{
			Method: http.MethodGet,
			Path:   "/jobs/{job_id}",
			Auth:   route.AuthPrincipalRequired,
			Errors: []string{
				"authorization.denied",
				"authorization.request.invalid",
				"authorization.unavailable",
				"resource.not_found",
				"job.unavailable",
				"request.invalid",
			},
			Handle: h.get,
		},
		route.Endpoint{
			Method: http.MethodGet,
			Path:   "/jobs/{job_id}/attempts",
			Auth:   route.AuthPrincipalRequired,
			Errors: []string{
				"authorization.denied",
				"authorization.request.invalid",
				"authorization.unavailable",
				"job.query.invalid",
				"request.invalid",
				"resource.not_found",
				"job.unavailable",
			},
			Handle: h.listAttempts,
		},
		route.Endpoint{
			Method: http.MethodPost,
			Path:   "/jobs/{job_id}/cancel",
			Auth:   route.AuthPrincipalRequired,
			Errors: []string{
				"authorization.denied",
				"authorization.request.invalid",
				"authorization.unavailable",
				"audit.unavailable",
				"resource.not_found",
				"job.cancel.unsupported",
				"job.conflict",
				"job.unavailable",
				"request.invalid",
			},
			Handle: h.cancel,
		},
		route.Endpoint{
			Method: http.MethodPost,
			Path:   "/jobs/{job_id}/retry",
			Auth:   route.AuthPrincipalRequired,
			Errors: []string{
				"authorization.denied",
				"authorization.request.invalid",
				"authorization.unavailable",
				"audit.unavailable",
				"resource.not_found",
				"job.retry.unsupported",
				"job.conflict",
				"job.unavailable",
				"request.invalid",
			},
			Handle: h.retry,
		},
	)
}

func (h jobHandlers) list(request route.Request) (route.Result, error) {
	query, err := listJobsQuery(request.HTTP)
	if err != nil {
		return route.Result{}, apperror.Invalid("request.invalid", "query")
	}
	page, appErr := h.app.ListJobs(request.Context, request.Invocation(), query)
	if appErr != nil {
		return route.Result{}, appErr
	}
	response := jobListResponse{Items: make([]jobResponse, 0, len(page.Items))}
	for _, item := range page.Items {
		response.Items = append(response.Items, jobResponseFromApplication(item))
	}
	if page.HasMore && len(page.Items) == 0 {
		return route.Result{}, route.Unpresentable(errors.New("job page made no progress"))
	}
	if page.HasMore {
		last := page.Items[len(page.Items)-1]
		response.NextCursor, err = encodeJobCursor(jobCursor{CreatedAt: model.FormatRFC3339(last.CreatedAt), ID: last.ID.String()})
		if err != nil {
			return route.Result{}, route.Unpresentable(err)
		}
	}
	total := paging.Total{Counted: page.Counted, Total: page.Total, TotalIsLowerBound: page.TotalIsLowerBound}
	if response.Total, response.TotalIsLowerBound, err = route.PageTotal(total, !query.BeforeID.IsZero()); err != nil {
		return route.Result{}, err
	}
	return route.JSON(http.StatusOK, response), nil
}
func (h jobHandlers) get(request route.Request) (route.Result, error) {
	jobID, err := route.PathID[model.JobID](request, "job_id")
	if err != nil {
		return route.Result{}, err
	}
	view, err := h.app.GetJob(request.Context, request.Invocation(), jobID)
	if err != nil {
		return route.Result{}, err
	}
	return route.JSON(http.StatusOK, jobResponseFromApplication(view)), nil
}
func (h jobHandlers) listAttempts(request route.Request) (route.Result, error) {
	jobID, err := route.PathID[model.JobID](request, "job_id")
	if err != nil {
		return route.Result{}, err
	}

	limit, before, err := attemptPagination(request.HTTP)
	if err != nil {
		return route.Result{}, apperror.Invalid("request.invalid", "query")
	}

	page, appErr := h.app.ListJobAttempts(request.Context, request.Invocation(), job.AttemptListQuery{
		JobID:        jobID,
		BeforeNumber: before,
		Limit:        limit,
	})
	if appErr != nil {
		return route.Result{}, appErr
	}

	response := jobAttemptListResponse{Items: make([]jobAttemptResponse, 0, len(page.Items))}
	for _, item := range page.Items {
		response.Items = append(response.Items, jobAttemptResponseFromApplication(item))
	}

	if page.HasMore && len(page.Items) == 0 {
		return route.Result{}, route.Unpresentable(errors.New("job attempt page made no progress"))
	}

	if page.HasMore {
		response.NextCursor, err = encodeJobAttemptCursor(page.Items[len(page.Items)-1].Number)
		if err != nil {
			return route.Result{}, route.Unpresentable(err)
		}
	}

	return route.JSON(http.StatusOK, response), nil
}
func (h jobHandlers) cancel(request route.Request) (route.Result, error) {
	jobID, err := route.PathID[model.JobID](request, "job_id")
	if err != nil {
		return route.Result{}, err
	}
	view, err := h.app.CancelJob(request.Context, request.Invocation(), job.CancelCommand{ID: jobID})
	if err != nil {
		return route.Result{}, err
	}
	return route.JSON(http.StatusOK, jobResponseFromApplication(view)), nil
}
func (h jobHandlers) retry(request route.Request) (route.Result, error) {
	jobID, err := route.PathID[model.JobID](request, "job_id")
	if err != nil {
		return route.Result{}, err
	}
	view, err := h.app.RetryJob(request.Context, request.Invocation(), job.RetryCommand{ID: jobID})
	if err != nil {
		return route.Result{}, err
	}
	return route.JSON(http.StatusOK, jobResponseFromApplication(view)), nil
}
func listJobsQuery(r *http.Request) (job.ListQuery, error) {
	limit := defaultJobPageSize
	if raw := r.URL.Query().Get("limit"); raw != "" {
		value, err := strconv.Atoi(raw)
		if err != nil || value < 1 || value > 200 {
			return job.ListQuery{}, err
		}
		limit = value
	}
	query := job.ListQuery{Limit: limit}
	if raw := r.URL.Query().Get("cursor"); raw != "" {
		cursor, err := decodeJobCursor(raw)
		if err != nil {
			return query, err
		}
		query.BeforeCreatedAt, _ = model.ParseRFC3339(cursor.CreatedAt)
		query.BeforeID = model.JobID(cursor.ID)
	}
	for _, raw := range r.URL.Query()["status"] {
		query.Statuses = append(query.Statuses, model.JobStatus(raw))
	}
	return query, nil
}
func attemptPagination(r *http.Request) (int, int, error) {
	limit := defaultJobPageSize
	var err error
	if raw := r.URL.Query().Get("limit"); raw != "" {
		limit, err = strconv.Atoi(raw)
		if err != nil || limit < 1 || limit > 200 {
			return 0, 0, err
		}
	}
	before := 0
	if raw := r.URL.Query().Get("cursor"); raw != "" {
		before, err = decodeJobAttemptCursor(raw)
		if err != nil {
			return 0, 0, err
		}
	}
	return limit, before, nil
}
func encodeJobAttemptCursor(number int) (string, error) {
	return route.EncodeCursor(jobAttemptCursor{Number: number}, jobAttemptCursorSpec())
}
func decodeJobAttemptCursor(raw string) (int, error) {
	cursor, err := route.DecodeCursor(raw, jobAttemptCursorSpec())
	return cursor.Number, err
}
func encodeJobCursor(cursor jobCursor) (string, error) {
	return route.EncodeCursor(cursor, jobCursorSpec())
}
func decodeJobCursor(raw string) (jobCursor, error) {
	return route.DecodeCursor(raw, jobCursorSpec())
}
func jobCursorSpec() route.CursorSpec[jobCursor] {
	return route.CursorSpec[jobCursor]{
		Label:                "job",
		MaximumEncodedLength: route.DefaultMaximumCursorLength,
		Members:              []string{"created_at", "id"},
		Validate: func(cursor jobCursor) error {
			createdAt, err := model.ParseRFC3339(cursor.CreatedAt)
			if err != nil || createdAt.IsZero() || !model.JobID(cursor.ID).IsValid() {
				return errors.New("invalid job keyset")
			}

			return nil
		},
	}
}
func jobAttemptCursorSpec() route.CursorSpec[jobAttemptCursor] {
	return route.CursorSpec[jobAttemptCursor]{
		Label:                "job attempt",
		MaximumEncodedLength: route.DefaultMaximumCursorLength,
		Members:              []string{"number"},
		Validate: func(cursor jobAttemptCursor) error {
			if cursor.Number <= 0 {
				return errors.New("invalid job attempt keyset")
			}
			return nil
		},
	}
}
func jobResponseFromApplication(view job.View) jobResponse {
	response := jobResponse{
		ID:                   view.ID.String(),
		Type:                 view.Type,
		Status:               view.Status,
		CreatedAt:            model.FormatRFC3339(view.CreatedAt),
		UpdatedAt:            model.FormatRFC3339(view.UpdatedAt),
		AvailableAt:          model.FormatRFC3339(view.AvailableAt),
		PublicErrorCode:      view.PublicErrorCode,
		AttemptCount:         view.AttemptCount,
		MaximumAttempts:      view.MaximumAttempts,
		Revision:             view.Revision,
		CreatedByUserID:      view.CreatedByUserID.String(),
		CreatedByDisplayName: view.CreatedByDisplayName,
		Subject:              view.Subject,
	}
	if view.StartedAt.Valid {
		response.StartedAt = model.FormatRFC3339(view.StartedAt.Time)
	}
	if view.CompletedAt.Valid {
		response.CompletedAt = model.FormatRFC3339(view.CompletedAt.Time)
	}
	if view.Progress != nil {
		response.Progress = &jobProgressResponse{Current: view.Progress.Current, Total: view.Progress.Total, Stage: view.Progress.Stage}
	}

	return response
}
func jobAttemptResponseFromApplication(view job.AttemptView) jobAttemptResponse {
	response := jobAttemptResponse{
		ID:              view.ID.String(),
		Number:          view.Number,
		Status:          view.Status,
		StartedAt:       model.FormatRFC3339(view.StartedAt),
		HeartbeatAt:     model.FormatRFC3339(view.HeartbeatAt),
		LeaseExpiresAt:  model.FormatRFC3339(view.LeaseExpiresAt),
		PublicErrorCode: view.PublicErrorCode,
	}
	if view.CompletedAt.Valid {
		response.CompletedAt = model.FormatRFC3339(view.CompletedAt.Time)
	}

	return response
}
