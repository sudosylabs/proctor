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

	"github.com/sudosylabs/proctor/server/app/apperror"
	"github.com/sudosylabs/proctor/server/app/invocation"
	"github.com/sudosylabs/proctor/server/app/onboarding"
	"github.com/sudosylabs/proctor/server/app/paging"
	"github.com/sudosylabs/proctor/server/app/user"
	"github.com/sudosylabs/proctor/server/httpapi/route"
	"github.com/sudosylabs/proctor/server/model"
	"github.com/sudosylabs/proctor/server/store"
)

// AcademicAdministrationPreviewApplication is the application port its
// handlers call; it previews, reads, commits and cancels one academic
// administration operation over the Users a directory filter matches.
type AcademicAdministrationPreviewApplication interface {
	CreateAcademicAdministrationPreview(
		context.Context,
		invocation.Invocation,
		onboarding.CreateAcademicAdministrationPreviewCommand,
	) (onboarding.AcademicAdministrationPreviewView, error)
	GetAcademicAdministrationPreview(
		context.Context,
		invocation.Invocation,
		model.OnboardingImportID,
	) (onboarding.AcademicAdministrationPreviewView, error)
	ListAcademicAdministrationPreviewRows(
		context.Context,
		invocation.Invocation,
		onboarding.ListAcademicAdministrationPreviewRowsQuery,
	) (paging.Page[store.AcademicAdministrationPreviewRow], error)
	CommitAcademicAdministrationPreview(
		context.Context,
		invocation.Invocation,
		onboarding.CommitAcademicAdministrationPreviewCommand,
	) (onboarding.AcademicAdministrationPreviewView, error)
	CancelAcademicAdministrationPreview(
		context.Context,
		invocation.Invocation,
		model.OnboardingImportID,
	) (onboarding.AcademicAdministrationPreviewView, error)
}

type academicAdministrationPreviewHandlers struct {
	app AcademicAdministrationPreviewApplication
}

// academicAdministrationPreviewFilterBody is the User directory filter of a
// preview, spelled as the directory's query parameters are.
type academicAdministrationPreviewFilterBody struct {
	Q                string   `json:"q,omitempty"`
	Affiliation      []string `json:"affiliation,omitempty"`
	AcademicUnitID   string   `json:"academic_unit_id,omitempty"`
	RoleID           []string `json:"role_id,omitempty"`
	State            string   `json:"state,omitempty"`
	NeverSignedIn    bool     `json:"never_signed_in,omitempty"`
	LastActiveAfter  string   `json:"last_active_after,omitempty"`
	LastActiveBefore string   `json:"last_active_before,omitempty"`
}

type academicAdministrationPreviewRequest struct {
	Operation       string                                  `json:"operation"`
	ScopeType       string                                  `json:"scope_type"`
	ScopeID         string                                  `json:"scope_id"`
	Filter          academicAdministrationPreviewFilterBody `json:"filter"`
	RoleID          string                                  `json:"role_id,omitempty"`
	AffiliationKind string                                  `json:"affiliation_kind,omitempty"`
	StartAt         string                                  `json:"start_at,omitempty"`
	EndAt           string                                  `json:"end_at,omitempty"`
}

type academicAdministrationPreviewCommitRequest struct {
	ExpectedRevision int64  `json:"expected_revision"`
	PreviewDigest    string `json:"preview_digest"`
}

type academicAdministrationPreviewSkipResponse struct {
	Reason string `json:"reason"`
	Rows   int    `json:"rows"`
}

// academicAdministrationPreviewSummaryResponse counts a ready preview's rows
// by class, and its skipped rows by reason.
type academicAdministrationPreviewSummaryResponse struct {
	WillChange  int                                         `json:"will_change"`
	NoOp        int                                         `json:"no_op"`
	Skipped     int                                         `json:"skipped"`
	SkipReasons []academicAdministrationPreviewSkipResponse `json:"skip_reasons"`
}

type academicAdministrationPreviewResponse struct {
	ID              string                                        `json:"id"`
	State           string                                        `json:"state"`
	Operation       string                                        `json:"operation"`
	ScopeType       string                                        `json:"scope_type"`
	ScopeID         string                                        `json:"scope_id"`
	Filter          academicAdministrationPreviewFilterBody       `json:"filter"`
	RoleID          string                                        `json:"role_id,omitempty"`
	AffiliationKind string                                        `json:"affiliation_kind,omitempty"`
	StartAt         string                                        `json:"start_at,omitempty"`
	EndAt           string                                        `json:"end_at,omitempty"`
	PreviewDigest   string                                        `json:"preview_digest,omitempty"`
	Summary         *academicAdministrationPreviewSummaryResponse `json:"summary,omitempty"`
	TotalRows       int                                           `json:"total_rows"`
	SucceededRows   int                                           `json:"succeeded_rows"`
	NoOpRows        int                                           `json:"no_op_rows"`
	FailedRows      int                                           `json:"failed_rows"`
	SkippedRows     int                                           `json:"skipped_rows"`
	PreviewJobID    string                                        `json:"preview_job_id"`
	ExecutionJobID  string                                        `json:"execution_job_id,omitempty"`
	FailureCode     string                                        `json:"failure_code,omitempty"`
	CreatedAt       string                                        `json:"created_at"`
	UpdatedAt       string                                        `json:"updated_at"`
	// PreviewExpiresAt is when a ready preview stops admitting its commit.
	PreviewExpiresAt string `json:"preview_expires_at,omitempty"`
	ExpiresAt        string `json:"expires_at"`
	Revision         int64  `json:"revision"`
}

type academicAdministrationPreviewRowResponse struct {
	RowNumber   int    `json:"row"`
	UserID      string `json:"user_id"`
	Username    string `json:"username"`
	DisplayName string `json:"display_name"`
	Group       string `json:"group"`
	Reason      string `json:"reason,omitempty"`
	Status      string `json:"status"`
	PublicCode  string `json:"public_code,omitempty"`
	ResourceID  string `json:"resource_id,omitempty"`
}

// academicAdministrationPreviewRowCursor is the last listed row and the
// group and reason it was listed in; a cursor is refused for another group.
type academicAdministrationPreviewRowCursor struct {
	Row    int    `json:"row"`
	Group  string `json:"group"`
	Reason string `json:"reason"`
}

func academicAdministrationPreviewRowCursorSpec() route.CursorSpec[academicAdministrationPreviewRowCursor] {
	return route.CursorSpec[academicAdministrationPreviewRowCursor]{
		Label:                "academic administration preview row",
		MaximumEncodedLength: route.DefaultMaximumCursorLength,
		Members:              []string{"row", "group", "reason"},
		Validate: func(cursor academicAdministrationPreviewRowCursor) error {
			if cursor.Row < 1 || !store.AcademicAdministrationPreviewClass(cursor.Group).IsValid() || len(cursor.Reason) > 128 {
				return errors.New("invalid academic administration preview row keyset")
			}
			return nil
		},
	}
}

// academicAdministrationPreviewReadErrors are the codes reading a preview
// or its rows returns beyond the kernel codes.
var academicAdministrationPreviewReadErrors = []string{
	"authorization.denied",
	"authorization.request.invalid",
	"authorization.unavailable",
	"request.invalid",
	"resource.not_found",
	"onboarding_import.unavailable",
	"invitation.unavailable",
}

func academicAdministrationPreviewResource(app AcademicAdministrationPreviewApplication) route.Resource {
	h := academicAdministrationPreviewHandlers{app: app}
	return route.NewResource("academic-administration-previews",
		route.Endpoint{
			Method: http.MethodPost,
			Path:   "/academic-administration-previews",
			Auth:   route.AuthStrongRecentSessionRequired,
			Errors: []string{
				"authorization.denied",
				"authorization.request.invalid",
				"authorization.unavailable",
				"audit.unavailable",
				"request.invalid",
				"resource.not_found",
				"administration.unavailable",
				"authentication.mfa.unavailable",
				"invitation.unavailable",
				"onboarding_import.file_invalid",
				"onboarding_import.unavailable",
				"student_progression.conflict",
				"student_progression.unavailable",
			},
			Handle: h.create,
		},
		route.Endpoint{
			Method: http.MethodGet,
			Path:   "/academic-administration-previews/{academic_administration_preview_id}",
			Auth:   route.AuthSessionRequired,
			Errors: academicAdministrationPreviewReadErrors,
			Handle: h.get,
		},
		route.Endpoint{
			Method: http.MethodGet,
			Path:   "/academic-administration-previews/{academic_administration_preview_id}/rows",
			Auth:   route.AuthSessionRequired,
			Errors: academicAdministrationPreviewReadErrors,
			Handle: h.rows,
		},
		route.Endpoint{
			Method:      http.MethodPost,
			Path:        "/academic-administration-previews/{academic_administration_preview_id}/commit",
			Auth:        route.AuthStrongRecentSessionRequired,
			Idempotency: route.IdempotencyRequired,
			Errors: []string{
				"authorization.denied",
				"authorization.request.invalid",
				"authorization.unavailable",
				"audit.unavailable",
				"request.invalid",
				"resource.not_found",
				"academic_administration_preview.conflict",
				"academic_administration_preview.expired",
				"administration.unavailable",
				"authentication.mfa.unavailable",
				"idempotency.key_required",
				"idempotency.key_invalid",
				"invitation.unavailable",
				"onboarding_import.file_invalid",
				"onboarding_import.unavailable",
				"student_progression.conflict",
				"student_progression.unavailable",
			},
			Handle: h.commit,
		},
		route.Endpoint{
			Method: http.MethodPost,
			Path:   "/academic-administration-previews/{academic_administration_preview_id}/cancel",
			Auth:   route.AuthSessionRequired,
			Errors: []string{
				"authorization.denied",
				"authorization.request.invalid",
				"authorization.unavailable",
				"audit.unavailable",
				"request.invalid",
				"resource.not_found",
				"invitation.unavailable",
				"onboarding_import.conflict",
				"onboarding_import.file_invalid",
				"onboarding_import.unavailable",
				"student_progression.conflict",
				"student_progression.unavailable",
			},
			Handle: h.cancel,
		},
	)
}

func (h academicAdministrationPreviewHandlers) create(request route.Request) (route.Result, error) {
	var body academicAdministrationPreviewRequest
	if err := request.DecodeJSON(&body); err != nil {
		return route.Result{}, route.InvalidRequest(err)
	}

	command := onboarding.CreateAcademicAdministrationPreviewCommand{
		Operation:       onboarding.AcademicAdministrationBatchOperation(body.Operation),
		ScopeType:       model.RoleScopeType(body.ScopeType),
		ScopeID:         body.ScopeID,
		RoleID:          model.RoleID(body.RoleID),
		AffiliationKind: model.AffiliationKind(body.AffiliationKind),
		Filter: onboarding.AcademicAdministrationUserFilter{
			Query:          body.Filter.Q,
			AcademicUnitID: model.AcademicUnitID(body.Filter.AcademicUnitID),
			State:          user.AccountState(body.Filter.State),
			NeverSignedIn:  body.Filter.NeverSignedIn,
		},
	}
	for _, kind := range body.Filter.Affiliation {
		command.Filter.Affiliations = append(command.Filter.Affiliations, model.AffiliationKind(kind))
	}
	for _, id := range body.Filter.RoleID {
		command.Filter.RoleIDs = append(command.Filter.RoleIDs, model.RoleID(id))
	}
	for _, instant := range []struct {
		field  string
		raw    string
		target *model.OptionalTime
	}{
		{"start_at", body.StartAt, &command.StartAt},
		{"end_at", body.EndAt, &command.EndAt},
		{"last_active_after", body.Filter.LastActiveAfter, nil},
		{"last_active_before", body.Filter.LastActiveBefore, nil},
	} {
		if instant.raw == "" {
			continue
		}
		at, err := model.ParseRFC3339(instant.raw)
		if err != nil || at.IsZero() {
			return route.Result{}, apperror.Invalid("request.invalid", instant.field)
		}
		switch {
		case instant.target != nil:
			*instant.target = model.OptionalTimeFrom(at)
		case instant.field == "last_active_after":
			command.Filter.LastActiveAfter = at
		default:
			command.Filter.LastActiveBefore = at
		}
	}

	view, err := h.app.CreateAcademicAdministrationPreview(request.Context, request.Invocation(), command)
	if err != nil {
		return route.Result{}, err
	}

	return route.JSON(http.StatusAccepted, academicAdministrationPreviewResponseFromView(view)).WithHeaders(noStoreHeaders()), nil
}

func (h academicAdministrationPreviewHandlers) get(request route.Request) (route.Result, error) {
	id, err := route.PathID[model.OnboardingImportID](request, "academic_administration_preview_id")
	if err != nil {
		return route.Result{}, err
	}
	view, err := h.app.GetAcademicAdministrationPreview(request.Context, request.Invocation(), id)
	if err != nil {
		return route.Result{}, err
	}
	return route.JSON(http.StatusOK, academicAdministrationPreviewResponseFromView(view)).WithHeaders(noStoreHeaders()), nil
}

func (h academicAdministrationPreviewHandlers) rows(request route.Request) (route.Result, error) {
	id, err := route.PathID[model.OnboardingImportID](request, "academic_administration_preview_id")
	if err != nil {
		return route.Result{}, err
	}
	page, err := request.Page()
	if err != nil {
		return route.Result{}, err
	}

	values := request.HTTP.URL.Query()
	for key := range values {
		if key != "group" && key != "reason" && key != "limit" && key != "cursor" {
			return route.Result{}, route.InvalidRequest(errors.New("unknown academic administration preview row query parameter"))
		}
	}
	single := func(name string, required bool) (string, error) {
		raw, present := values[name]
		if !present && !required {
			return "", nil
		}
		if len(raw) != 1 || raw[0] == "" {
			return "", apperror.Invalid("request.invalid", name)
		}
		return raw[0], nil
	}
	group, err := single("group", true)
	if err != nil {
		return route.Result{}, err
	}
	reason, err := single("reason", false)
	if err != nil {
		return route.Result{}, err
	}

	query := onboarding.ListAcademicAdministrationPreviewRowsQuery{
		ID:     id,
		Class:  store.AcademicAdministrationPreviewClass(group),
		Reason: reason,
		Limit:  page.Limit,
	}
	cursor, continued, err := route.DecodePageCursor(page, academicAdministrationPreviewRowCursorSpec())
	if err != nil {
		return route.Result{}, err
	}
	if continued {
		if cursor.Group != group || cursor.Reason != reason {
			return route.Result{}, route.InvalidRequest(errors.New("cursor belongs to another group"))
		}
		query.AfterRowNumber = cursor.Row
	}

	result, err := h.app.ListAcademicAdministrationPreviewRows(request.Context, request.Invocation(), query)
	if err != nil {
		return route.Result{}, err
	}
	if len(result.Items) > query.Limit {
		return route.Result{}, route.ErrUnpresentable
	}

	envelope, err := route.NewList(result.Items, result.HasMore, academicAdministrationPreviewRowResponseFromStore,
		func(last store.AcademicAdministrationPreviewRow) (string, error) {
			return route.EncodeCursor(
				academicAdministrationPreviewRowCursor{Row: last.RowNumber, Group: group, Reason: reason},
				academicAdministrationPreviewRowCursorSpec(),
			)
		})
	if err != nil {
		return route.Result{}, err
	}

	return route.JSON(http.StatusOK, envelope).WithHeaders(noStoreHeaders()), nil
}

func (h academicAdministrationPreviewHandlers) commit(request route.Request) (route.Result, error) {
	id, err := route.PathID[model.OnboardingImportID](request, "academic_administration_preview_id")
	if err != nil {
		return route.Result{}, err
	}

	var body academicAdministrationPreviewCommitRequest
	if err = request.DecodeJSON(&body); err != nil {
		return route.Result{}, route.InvalidRequest(err)
	}

	view, err := h.app.CommitAcademicAdministrationPreview(request.Context, request.Invocation(), onboarding.CommitAcademicAdministrationPreviewCommand{
		ID:               id,
		ExpectedRevision: body.ExpectedRevision,
		PreviewDigest:    body.PreviewDigest,
		IdempotencyKey:   request.IdempotencyKey,
	})
	if err != nil {
		return route.Result{}, err
	}

	return route.JSON(http.StatusAccepted, academicAdministrationPreviewResponseFromView(view)).WithHeaders(noStoreHeaders()), nil
}

func (h academicAdministrationPreviewHandlers) cancel(request route.Request) (route.Result, error) {
	id, err := route.PathID[model.OnboardingImportID](request, "academic_administration_preview_id")
	if err != nil {
		return route.Result{}, err
	}
	view, err := h.app.CancelAcademicAdministrationPreview(request.Context, request.Invocation(), id)
	if err != nil {
		return route.Result{}, err
	}
	return route.JSON(http.StatusOK, academicAdministrationPreviewResponseFromView(view)).WithHeaders(noStoreHeaders()), nil
}

func optionalRFC3339(value model.OptionalTime) string {
	if !value.Valid {
		return ""
	}
	return model.FormatRFC3339(value.Time)
}

func academicAdministrationPreviewResponseFromView(view onboarding.AcademicAdministrationPreviewView) academicAdministrationPreviewResponse {
	value, filter := view.Import, view.Filter
	response := academicAdministrationPreviewResponse{
		ID:              value.ID.String(),
		State:           string(value.State),
		Operation:       filter.Operation,
		ScopeType:       string(value.ScopeType),
		ScopeID:         value.ScopeID,
		RoleID:          filter.RoleID.String(),
		AffiliationKind: string(filter.AffiliationKind),
		StartAt:         optionalRFC3339(filter.StartAt),
		EndAt:           optionalRFC3339(filter.EndAt),
		PreviewDigest:   value.PreviewDigest,
		TotalRows:       value.TotalRows,
		SucceededRows:   value.SucceededRows,
		NoOpRows:        value.NoOpRows,
		FailedRows:      value.FailedRows,
		SkippedRows:     value.SkippedRows,
		PreviewJobID:    value.ParseJobID.String(),
		ExecutionJobID:  value.ExecutionJobID.String(),
		FailureCode:     value.FailureCode,
		CreatedAt:       model.FormatRFC3339(value.CreatedAt),
		UpdatedAt:       model.FormatRFC3339(value.UpdatedAt),
		ExpiresAt:       model.FormatRFC3339(value.ExpiresAt),
		Revision:        value.Revision,
		Filter: academicAdministrationPreviewFilterBody{
			Q:              filter.Query,
			AcademicUnitID: filter.AcademicUnitID.String(),
			State:          filter.State,
			NeverSignedIn:  filter.NeverSignedIn,
		},
		PreviewExpiresAt: optionalRFC3339(view.PreviewExpiresAt),
	}
	for _, kind := range filter.Affiliations {
		response.Filter.Affiliation = append(response.Filter.Affiliation, string(kind))
	}
	for _, id := range filter.RoleIDs {
		response.Filter.RoleID = append(response.Filter.RoleID, id.String())
	}
	if !filter.LastActiveAfter.IsZero() {
		response.Filter.LastActiveAfter = model.FormatRFC3339(filter.LastActiveAfter)
	}
	if !filter.LastActiveBefore.IsZero() {
		response.Filter.LastActiveBefore = model.FormatRFC3339(filter.LastActiveBefore)
	}

	if view.Groups != nil {
		summary := &academicAdministrationPreviewSummaryResponse{SkipReasons: []academicAdministrationPreviewSkipResponse{}}
		for _, group := range view.Groups {
			switch group.Class {
			case store.AcademicAdministrationPreviewWillChange:
				summary.WillChange += group.Rows
			case store.AcademicAdministrationPreviewNoOp:
				summary.NoOp += group.Rows
			case store.AcademicAdministrationPreviewSkipped:
				summary.Skipped += group.Rows
				summary.SkipReasons = append(summary.SkipReasons, academicAdministrationPreviewSkipResponse{Reason: group.Reason, Rows: group.Rows})
			}
		}
		response.Summary = summary
	}
	return response
}

func academicAdministrationPreviewRowResponseFromStore(row store.AcademicAdministrationPreviewRow) academicAdministrationPreviewRowResponse {
	return academicAdministrationPreviewRowResponse{
		RowNumber:   row.RowNumber,
		UserID:      row.UserID.String(),
		Username:    row.Username,
		DisplayName: row.DisplayName,
		Group:       string(row.Class),
		Reason:      row.Reason,
		Status:      string(row.Status),
		PublicCode:  row.PublicCode,
		ResourceID:  row.ResourceID,
	}
}
