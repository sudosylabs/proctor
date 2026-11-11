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
	"time"

	"github.com/sudosylabs/proctor/server/app/administration"
	"github.com/sudosylabs/proctor/server/app/invocation"
	"github.com/sudosylabs/proctor/server/httpapi/route"
	"github.com/sudosylabs/proctor/server/model"
)

type RetentionApplication interface {
	ListRetentionNotices(context.Context, invocation.Invocation, model.RetentionRetirementID, int) (*administration.RetentionNoticePage, error)
	GetRetentionControl(context.Context, invocation.Invocation) (*model.RetentionControl, error)
	GetRetentionPreview(context.Context, invocation.Invocation, model.RetentionPreviewID) (*model.RetentionPreview, error)
	CreateRetentionPreview(
		context.Context,
		invocation.Invocation,
		administration.CreateRetentionPreviewCommand,
	) (*model.RetentionPreview, error)
	ChangeRetentionControl(
		context.Context,
		invocation.Invocation,
		administration.ChangeRetentionControlCommand,
	) (*model.RetentionControl, error)
	ListRetentionRecords(context.Context, invocation.Invocation, model.SubmissionID, int) (*administration.RetentionRecordPage, error)
}

type retentionHandlers struct{ app RetentionApplication }

func retentionResource(app RetentionApplication) route.Resource {
	h := retentionHandlers{app: app}
	mutationErrors := operatorMutationErrorCodes(
		"request.invalid",
		"retention.invalid",
		"retention.conflict",
		"retention.unavailable",
		"retention_policy.unavailable",
		"retention_policy.revision_conflict",
		"resource.not_found",
		"idempotency.key_required",
		"idempotency.key_invalid",
		"idempotency.conflict",
		"idempotency.in_progress",
	)
	preview := route.Endpoint{
		Method:      http.MethodPost,
		Path:        "/retention/previews",
		Auth:        route.AuthSessionRequired,
		Idempotency: route.IdempotencyRequired,
		Errors:      mutationErrors,
		Handle:      h.preview,
	}
	control := route.Endpoint{
		Method:      http.MethodPut,
		Path:        "/retention/control",
		Auth:        route.AuthStrongRecentSessionRequired,
		Idempotency: route.IdempotencyRequired,
		Errors:      mutationErrors,
		Handle:      h.changeControl,
	}
	return route.NewResource(
		"retention",
		route.Endpoint{
			Method: http.MethodGet,
			Path:   "/retention/control",
			Auth:   route.AuthSessionRequired,
			Errors: []string{
				"authorization.denied",
				"authorization.request.invalid",
				"authorization.unavailable",
				"retention.invalid",
				"retention.unavailable",
				"retention_policy.unavailable",
				"resource.not_found",
				"retention_policy.revision_conflict",
			},
			Handle: h.getControl,
		},
		control,
		preview,
		route.Endpoint{
			Method: http.MethodGet,
			Path:   "/retention/previews/{retention_preview_id}",
			Auth:   route.AuthSessionRequired,
			Errors: []string{
				"authorization.denied",
				"authorization.request.invalid",
				"authorization.unavailable",
				"request.invalid",
				"retention.invalid",
				"retention.unavailable",
				"retention_policy.unavailable",
				"resource.not_found",
				"retention_policy.revision_conflict",
			},
			Handle: h.getPreview,
		},
		route.Endpoint{
			Method: http.MethodGet,
			Path:   "/retention/records",
			Auth:   route.AuthSessionRequired,
			Errors: []string{
				"authorization.denied",
				"authorization.request.invalid",
				"authorization.unavailable",
				"request.invalid",
				"retention.invalid",
				"retention.unavailable",
				"retention_policy.unavailable",
				"resource.not_found",
				"retention_policy.revision_conflict",
			},
			Handle: h.records,
		},
		route.Endpoint{
			Method: http.MethodGet,
			Path:   "/users/me/retention-notices",
			Auth:   route.AuthSessionRequired,
			Errors: []string{
				"authorization.denied",
				"request.invalid",
				"retention.invalid",
				"retention.unavailable",
				"retention_policy.revision_conflict",
				"retention_policy.unavailable",
			},
			Handle: h.notices,
		},
	)
}

type retentionPreviewRequest struct {
	ExpectedPolicyRevision int64 `json:"expected_revision"`
}
type retentionControlRequest struct {
	ExpectedRevision       int64  `json:"expected_revision"`
	ExpectedPolicyRevision int64  `json:"expected_policy_revision"`
	PreviewID              string `json:"preview_id,omitempty"`
	State                  string `json:"state"`
}
type retentionControlResponse struct {
	Revision               int64  `json:"revision"`
	State                  string `json:"state"`
	ApprovedPolicyRevision *int64 `json:"approved_policy_revision,omitempty"`
	ApprovedPreviewID      string `json:"approved_preview_id,omitempty"`
	UpdatedAt              string `json:"updated_at"`
}
type retentionPreviewCountsResponse struct {
	Total            int64 `json:"total"`
	Eligible         int64 `json:"eligible"`
	AwaitingDeadline int64 `json:"awaiting_deadline"`
	Incomplete       int64 `json:"incomplete"`
	Held             int64 `json:"held"`
	Unconfigured     int64 `json:"unconfigured"`
	SupportingWork   int64 `json:"supporting_work"`
	ExportProtected  int64 `json:"export_protected"`
	Retired          int64 `json:"retired"`
}
type retentionExpiryCountsResponse struct {
	Total            int64 `json:"total"`
	Eligible         int64 `json:"eligible"`
	AwaitingDeadline int64 `json:"awaiting_deadline"`
	Unconfigured     int64 `json:"unconfigured"`
	Unfinished       int64 `json:"unfinished"`
	Referenced       int64 `json:"referenced"`
	Held             int64 `json:"held"`
	PurgePending     int64 `json:"purge_pending"`
	SourceProtected  int64 `json:"source_protected"`
}

type retentionPreviewResponse struct {
	Audit               retentionExpiryCountsResponse  `json:"audit"`
	Receipts            retentionExpiryCountsResponse  `json:"receipts"`
	ID                  string                         `json:"id"`
	PolicyRevision      int64                          `json:"policy_revision"`
	CreatedAt           string                         `json:"created_at"`
	ExpiresAt           string                         `json:"expires_at"`
	Work                retentionPreviewCountsResponse `json:"work"`
	Integrity           retentionPreviewCountsResponse `json:"integrity"`
	BrowserActivity     retentionPreviewCountsResponse `json:"browser_activity"`
	SecurityOperational retentionPreviewCountsResponse `json:"security_operational"`
}
type retirementResponse struct {
	ID                 string  `json:"id"`
	State              string  `json:"state"`
	PolicyRevision     int64   `json:"policy_revision"`
	ControlRevision    int64   `json:"control_revision"`
	CompletionRevision int64   `json:"completion_revision"`
	ScheduledAt        string  `json:"scheduled_at"`
	RetireAfter        string  `json:"retire_after"`
	RetiredAt          *string `json:"retired_at,omitempty"`
	PurgePending       int64   `json:"purge_pending"`
	PurgeVerified      int64   `json:"purge_verified"`
}
type retentionRecordResponse struct {
	ExamID                 string              `json:"exam_id"`
	SittingID              string              `json:"exam_sitting_id"`
	SubmissionID           string              `json:"submission_id"`
	Category               string              `json:"category"`
	CompletionRevision     int64               `json:"completion_revision"`
	CompletedAt            *string             `json:"completed_at,omitempty"`
	CompletionCurrent      bool                `json:"completion_current"`
	HasIntegrity           bool                `json:"has_integrity"`
	Held                   bool                `json:"held"`
	ExportProtectedUntil   *string             `json:"export_protected_until,omitempty"`
	RetiredAt              *string             `json:"retired_at,omitempty"`
	SharedPublishedObjects int64               `json:"shared_published_objects"`
	Blocker                string              `json:"blocker"`
	EligibleAt             *string             `json:"eligible_at,omitempty"`
	Retirement             *retirementResponse `json:"retirement,omitempty"`
}
type retentionRecordsResponse struct {
	PolicyRevision int64                     `json:"policy_revision"`
	AsOf           string                    `json:"as_of"`
	Items          []retentionRecordResponse `json:"items"`
	NextCursor     string                    `json:"next_cursor,omitempty"`
}

func (h retentionHandlers) getControl(request route.Request) (route.Result, error) {
	c, err := h.app.GetRetentionControl(request.Context, request.Invocation())
	if err != nil {
		return route.Result{}, err
	}
	return route.JSON(http.StatusOK, retentionControlDTO(c)).WithHeaders(privateNoStoreHeaders()), nil
}
func (h retentionHandlers) changeControl(request route.Request) (route.Result, error) {
	var body retentionControlRequest
	if err := request.DecodeJSON(&body); err != nil {
		return route.Result{}, err
	}

	c, err := h.app.ChangeRetentionControl(request.Context, request.Invocation(), administration.ChangeRetentionControlCommand{
		ExpectedRevision:       body.ExpectedRevision,
		ExpectedPolicyRevision: body.ExpectedPolicyRevision,
		PreviewID:              model.RetentionPreviewID(body.PreviewID),
		State:                  model.RetentionControlState(body.State),
		IdempotencyKey:         request.IdempotencyKey,
	})
	if err != nil {
		return route.Result{}, err
	}

	return route.JSON(http.StatusOK, retentionControlDTO(c)).WithHeaders(privateNoStoreHeaders()), nil
}
func (h retentionHandlers) preview(request route.Request) (route.Result, error) {
	var body retentionPreviewRequest
	if err := request.DecodeJSON(&body); err != nil {
		return route.Result{}, err
	}

	p, err := h.app.CreateRetentionPreview(
		request.Context,
		request.Invocation(),
		administration.CreateRetentionPreviewCommand{ExpectedPolicyRevision: body.ExpectedPolicyRevision, IdempotencyKey: request.IdempotencyKey},
	)
	if err != nil {
		return route.Result{}, err
	}

	return route.JSON(http.StatusCreated, retentionPreviewDTO(p)).WithHeaders(privateNoStoreHeaders()), nil
}
func (h retentionHandlers) getPreview(request route.Request) (route.Result, error) {
	id, err := model.ParseRetentionPreviewID(request.Params["retention_preview_id"])
	if err != nil {
		return route.Result{}, route.InvalidRequest(err)
	}
	p, err := h.app.GetRetentionPreview(request.Context, request.Invocation(), id)
	if err != nil {
		return route.Result{}, err
	}
	return route.JSON(http.StatusOK, retentionPreviewDTO(p)).WithHeaders(privateNoStoreHeaders()), nil
}
func (h retentionHandlers) records(request route.Request) (route.Result, error) {
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

	var after model.SubmissionID
	if raw := values.Get("cursor"); raw != "" {
		cursor, cursorErr := route.DecodeCursor(raw, retentionCursorSpec())
		if cursorErr != nil {
			return route.Result{}, route.InvalidRequest(cursorErr)
		}

		after = model.SubmissionID(cursor.After)
	}

	page, err := h.app.ListRetentionRecords(request.Context, request.Invocation(), after, limit)
	if err != nil {
		return route.Result{}, err
	}
	if page == nil || len(page.Items) > limit*2 || (page.HasMore && len(page.Items) == 0) {
		return route.Result{}, route.ErrUnpresentable
	}

	response := retentionRecordsResponse{
		PolicyRevision: page.PolicyRevision,
		AsOf:           retentionTime(page.AsOf),
		Items:          make([]retentionRecordResponse, 0, len(page.Items)),
	}
	for _, item := range page.Items {
		r := item.Record
		value := retentionRecordResponse{
			ExamID:                 r.Scope.ExamID.String(),
			SittingID:              r.Scope.SittingID.String(),
			SubmissionID:           r.Scope.SubmissionID.String(),
			Category:               string(r.Category),
			CompletionRevision:     r.CompletionRevision,
			CompletedAt:            retentionOptionalTime(r.CompletedAt),
			CompletionCurrent:      r.CompletionCurrent,
			HasIntegrity:           r.HasIntegrity,
			Held:                   r.Held,
			ExportProtectedUntil:   retentionOptionalTime(r.ExportProtectedUntil),
			RetiredAt:              retentionOptionalTime(r.RetiredAt),
			SharedPublishedObjects: r.SharedPublishedObjects,
			Blocker:                string(item.Eligibility.Blocker),
			EligibleAt:             retentionOptionalTime(item.Eligibility.EligibleAt),
		}
		if t := item.Retirement; t != nil {
			value.Retirement = &retirementResponse{
				ID:                 t.ID.String(),
				State:              string(t.State),
				PolicyRevision:     t.PolicyRevision,
				ControlRevision:    t.ControlRevision,
				CompletionRevision: t.CompletionRevision,
				ScheduledAt:        retentionTime(t.ScheduledAt),
				RetireAfter:        retentionTime(t.RetireAfter),
				RetiredAt:          retentionOptionalTime(t.RetiredAt),
				PurgePending:       t.PurgePending,
				PurgeVerified:      t.PurgeVerified,
			}
		}

		response.Items = append(response.Items, value)
	}

	if page.HasMore {
		response.NextCursor, err = route.EncodeCursor(
			retentionCursor{After: page.Items[len(page.Items)-1].Record.Scope.SubmissionID.String()},
			retentionCursorSpec(),
		)
		if err != nil {
			return route.Result{}, route.Unpresentable(err)
		}
	}

	return route.JSON(http.StatusOK, response).WithHeaders(privateNoStoreHeaders()), nil
}

type retentionCursor struct {
	After string `json:"after_submission_id"`
}

func retentionCursorSpec() route.CursorSpec[retentionCursor] {
	return route.CursorSpec[retentionCursor]{
		Label:                "retention",
		MaximumEncodedLength: 256,
		Members:              []string{"after_submission_id"},
		Validate: func(c retentionCursor) error {
			if !model.SubmissionID(c.After).IsValid() {
				return errors.New("invalid retention cursor")
			}
			return nil
		},
	}
}
func retentionTime(at time.Time) string { return model.FormatRFC3339(at) }
func retentionOptionalTime(at model.OptionalTime) *string {
	if !at.Valid {
		return nil
	}
	value := retentionTime(at.Time)
	return &value
}
func retentionControlDTO(c *model.RetentionControl) retentionControlResponse {
	if c == nil {
		return retentionControlResponse{}
	}
	value := retentionControlResponse{Revision: c.Revision, State: string(c.State), UpdatedAt: retentionTime(c.UpdatedAt)}
	if c.ApprovedPolicyRevision > 0 {
		value.ApprovedPolicyRevision = &c.ApprovedPolicyRevision
		value.ApprovedPreviewID = c.ApprovedPreviewID.String()
	}
	return value
}
func retentionPreviewCountsDTO(c model.RetentionPreviewCounts) retentionPreviewCountsResponse {
	return retentionPreviewCountsResponse{
		Total:            c.Total,
		Eligible:         c.Eligible,
		AwaitingDeadline: c.AwaitingDeadline,
		Incomplete:       c.Incomplete,
		Held:             c.Held,
		Unconfigured:     c.Unconfigured,
		SupportingWork:   c.SupportingWork,
		ExportProtected:  c.ExportProtected,
		Retired:          c.Retired,
	}
}
func retentionPreviewDTO(p *model.RetentionPreview) retentionPreviewResponse {
	if p == nil {
		return retentionPreviewResponse{}
	}
	return retentionPreviewResponse{
		Audit:               retentionExpiryCountsDTO(p.Audit),
		Receipts:            retentionExpiryCountsDTO(p.Receipts),
		ID:                  p.ID.String(),
		PolicyRevision:      p.PolicyRevision,
		CreatedAt:           retentionTime(p.CreatedAt),
		ExpiresAt:           retentionTime(p.ExpiresAt),
		Work:                retentionPreviewCountsDTO(p.Work),
		Integrity:           retentionPreviewCountsDTO(p.Integrity),
		BrowserActivity:     retentionPreviewCountsDTO(p.BrowserActivity),
		SecurityOperational: retentionPreviewCountsDTO(p.SecurityOperational),
	}
}

func retentionExpiryCountsDTO(c model.RetentionExpiryCounts) retentionExpiryCountsResponse {
	return retentionExpiryCountsResponse{
		Total:            c.Total,
		Eligible:         c.Eligible,
		AwaitingDeadline: c.AwaitingDeadline,
		Unconfigured:     c.Unconfigured,
		Unfinished:       c.Unfinished,
		Referenced:       c.Referenced,
		Held:             c.Held,
		PurgePending:     c.PurgePending,
		SourceProtected:  c.SourceProtected,
	}
}
