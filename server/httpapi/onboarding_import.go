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
	"errors"
	"io"
	"net/http"

	"github.com/sudosylabs/proctor/server/app/apperror"
	"github.com/sudosylabs/proctor/server/app/invocation"
	"github.com/sudosylabs/proctor/server/app/onboarding"
	"github.com/sudosylabs/proctor/server/app/paging"
	"github.com/sudosylabs/proctor/server/httpapi/route"
	"github.com/sudosylabs/proctor/server/model"
	"github.com/sudosylabs/proctor/server/store"
)

// OnboardingImportApplication is the application port its handlers call; it
// uploads, commits, cancels and reports onboarding imports.
type OnboardingImportApplication interface {
	UploadOnboardingImport(
		context.Context,
		invocation.Invocation,
		onboarding.UploadOnboardingImportCommand,
	) (onboarding.OnboardingImportView, error)
	GetOnboardingImport(
		context.Context,
		invocation.Invocation,
		model.OnboardingImportID,
	) (onboarding.OnboardingImportView, []onboarding.OnboardingImportRowView, error)
	ListOnboardingImports(
		context.Context,
		invocation.Invocation,
		onboarding.ListOnboardingImportsQuery,
	) (paging.Page[onboarding.OnboardingImportView], error)
	CommitOnboardingImport(
		context.Context,
		invocation.Invocation,
		onboarding.CommitOnboardingImportCommand,
	) (onboarding.OnboardingImportView, error)
	CancelOnboardingImport(context.Context, invocation.Invocation, model.OnboardingImportID) (onboarding.OnboardingImportView, error)
	OnboardingImportReport(context.Context, invocation.Invocation, model.OnboardingImportID, io.Writer) error
}

type onboardingImportHandlers struct{ app OnboardingImportApplication }

type onboardingImportCommitRequest struct {
	ExpectedRevision int64  `json:"expected_revision"`
	PreviewDigest    string `json:"preview_digest"`
	Policy           string `json:"policy"`
}

type onboardingImportResponse struct {
	ID                  string `json:"id"`
	Mode                string `json:"mode"`
	State               string `json:"state"`
	ScopeType           string `json:"scope_type"`
	ScopeID             string `json:"scope_id"`
	RoleID              string `json:"role_id,omitempty"`
	SourcePeriodID      string `json:"source_period_id,omitempty"`
	SourceClassID       string `json:"source_class_id,omitempty"`
	DestinationPeriodID string `json:"destination_period_id,omitempty"`
	DestinationClassID  string `json:"destination_class_id,omitempty"`
	// SourceClassName and DestinationClassName are present only on student
	// progression reads and listings.
	SourceClassName      string                        `json:"source_class_name,omitempty"`
	DestinationClassName string                        `json:"destination_class_name,omitempty"`
	EffectiveAt          string                        `json:"effective_at,omitempty"`
	PreviewDigest        string                        `json:"preview_digest,omitempty"`
	IgnoredHeaders       []string                      `json:"ignored_headers,omitempty"`
	Rows                 []onboardingImportRowResponse `json:"rows,omitempty"`
	TotalRows            int                           `json:"total_rows"`
	ValidRows            int                           `json:"valid_rows"`
	InvalidRows          int                           `json:"invalid_rows"`
	SucceededRows        int                           `json:"succeeded_rows"`
	NoOpRows             int                           `json:"no_op_rows"`
	FailedRows           int                           `json:"failed_rows"`
	SkippedRows          int                           `json:"skipped_rows"`
	CommitPolicy         string                        `json:"commit_policy,omitempty"`
	ParseJobID           string                        `json:"parse_job_id"`
	ExecutionJobID       string                        `json:"execution_job_id,omitempty"`
	CreatedAt            string                        `json:"created_at"`
	UpdatedAt            string                        `json:"updated_at"`
	ExpiresAt            string                        `json:"expires_at"`
	Revision             int64                         `json:"revision"`
	FailureCode          string                        `json:"failure_code,omitempty"`
	// FileName is the uploaded file's base name, omitted when none was given.
	FileName string `json:"file_name,omitempty"`
}

// onboardingImportCursor is the keyset of the last listed import by creation
// instant, then identity, in the read order it records.
type onboardingImportCursor struct {
	CreatedAt string `json:"created_at"`
	ID        string `json:"id"`
	Order     string `json:"order"`
}

func onboardingImportCursorSpec() route.CursorSpec[onboardingImportCursor] {
	return route.CursorSpec[onboardingImportCursor]{
		Label:                "onboarding import",
		MaximumEncodedLength: route.DefaultMaximumCursorLength,
		Members:              []string{"created_at", "id", "order"},
		Validate: func(cursor onboardingImportCursor) error {
			createdAt, err := model.ParseRFC3339(cursor.CreatedAt)
			if err != nil || createdAt.IsZero() || !model.OnboardingImportID(cursor.ID).IsValid() || !validListDirection(cursor.Order) {
				return errors.New("invalid onboarding import keyset")
			}

			return nil
		},
	}
}

type onboardingImportRowResponse struct {
	RowNumber    int    `json:"row"`
	Reference    string `json:"reference"`
	Operation    string `json:"operation"`
	Status       string `json:"status"`
	PreviewCode  string `json:"preview_code,omitempty"`
	PublicCode   string `json:"public_code,omitempty"`
	InvitationID string `json:"invitation_id,omitempty"`
	ResourceID   string `json:"resource_id,omitempty"`
}

func onboardingImportResource(app OnboardingImportApplication) route.Resource {
	h := onboardingImportHandlers{app: app}
	return route.NewResource("onboarding-imports",
		route.Endpoint{
			Method:       http.MethodPost,
			Path:         "/onboarding-imports",
			Auth:         route.AuthPrincipalRequired,
			Idempotency:  route.IdempotencyNone,
			MaxBodyBytes: store.OnboardingImportMaximumBytes,
			ProtocolName: "onboarding-import-csv",
			ProtocolKind: route.ProtocolStreamingUpload,
			Errors: []string{
				"authorization.denied",
				"authorization.request.invalid",
				"authorization.unavailable",
				"audit.unavailable",
				"request.invalid",
				"resource.not_found",
				"onboarding_import.conflict",
				"onboarding_import.file_invalid",
				"onboarding_import.unavailable",
				"administration.unavailable",
				"authentication.mfa.unavailable",
				"invitation.role_not_delegable",
				"invitation.unavailable",
				"student_progression.conflict",
				"student_progression.unavailable",
			},
			Protocol: h.upload,
		},
		route.Endpoint{
			Method: http.MethodGet,
			Path:   "/onboarding-imports",
			Auth:   route.AuthPrincipalRequired,
			Errors: []string{
				"authorization.denied",
				"authorization.request.invalid",
				"authorization.unavailable",
				"request.invalid",
				"resource.not_found",
				"onboarding_import.unavailable",
				"invitation.unavailable",
			},
			Handle: h.list,
		},
		route.Endpoint{
			Method: http.MethodGet,
			Path:   "/onboarding-imports/{onboarding_import_id}",
			Auth:   route.AuthPrincipalRequired,
			Errors: []string{
				"authorization.denied",
				"authorization.request.invalid",
				"authorization.unavailable",
				"request.invalid",
				"resource.not_found",
				"onboarding_import.unavailable",
				"invitation.unavailable",
			},
			Handle: h.get,
		},
		route.Endpoint{
			Method:      http.MethodPost,
			Path:        "/onboarding-imports/{onboarding_import_id}/commit",
			Auth:        route.AuthPrincipalRequired,
			Idempotency: route.IdempotencyRequired,
			Errors: []string{
				"authorization.denied",
				"authorization.request.invalid",
				"authorization.unavailable",
				"audit.unavailable",
				"request.invalid",
				"resource.not_found",
				"onboarding_import.conflict",
				"onboarding_import.file_invalid",
				"onboarding_import.unavailable",
				"administration.unavailable",
				"idempotency.key_required",
				"idempotency.key_invalid",
				"authentication.mfa.unavailable",
				"invitation.unavailable",
				"student_progression.conflict",
				"student_progression.unavailable",
			},
			Handle: h.commit,
		},
		route.Endpoint{
			Method: http.MethodPost,
			Path:   "/onboarding-imports/{onboarding_import_id}/cancel",
			Auth:   route.AuthPrincipalRequired,
			Errors: []string{
				"authorization.denied",
				"authorization.request.invalid",
				"authorization.unavailable",
				"audit.unavailable",
				"request.invalid",
				"resource.not_found",
				"onboarding_import.conflict",
				"onboarding_import.file_invalid",
				"onboarding_import.unavailable",
				"invitation.unavailable",
				"student_progression.conflict",
				"student_progression.unavailable",
			},
			Handle: h.cancel,
		},
		route.Endpoint{
			Method:       http.MethodGet,
			Path:         "/onboarding-imports/{onboarding_import_id}/report",
			Auth:         route.AuthPrincipalRequired,
			ProtocolName: "onboarding-import-report",
			ProtocolKind: route.ProtocolBinaryDownload,
			Errors: []string{
				"authorization.denied",
				"authorization.request.invalid",
				"authorization.unavailable",
				"request.invalid",
				"resource.not_found",
				"onboarding_import.conflict",
				"onboarding_import.unavailable",
				"invitation.unavailable",
			},
			Protocol: h.report,
		},
	)
}

func (h onboardingImportHandlers) upload(request route.Request) (route.ProtocolResult, error) {
	query := request.HTTP.URL.Query()
	command := onboarding.UploadOnboardingImportCommand{
		Mode:      model.OnboardingImportMode(query.Get("mode")),
		ScopeType: model.RoleScopeType(query.Get("scope_type")),
		ScopeID:   query.Get("scope_id"),
		RoleID:    model.RoleID(query.Get("role_id")),
		Body:      request.HTTP.Body,
	}
	if names, given := query["file_name"]; given {
		if len(names) != 1 {
			return route.ProtocolResult{}, apperror.Invalid("request.invalid", "file_name")
		}

		command.FileName = &names[0]
	}

	view, err := h.app.UploadOnboardingImport(request.Context, request.Invocation(), command)
	if err != nil {
		return route.ProtocolResult{}, err
	}

	return route.StreamingUpload(http.StatusAccepted, onboardingImportResponseFromView(view, nil)).WithHeaders(noStoreHeaders()), nil
}

func (h onboardingImportHandlers) list(request route.Request) (route.Result, error) {
	page, err := request.Page()
	if err != nil {
		return route.Result{}, err
	}

	values := request.HTTP.URL.Query()
	for key := range values {
		if key != "state" && key != "order" && key != "limit" && key != "cursor" {
			return route.Result{}, route.InvalidRequest(errors.New("unknown onboarding import query parameter"))
		}
	}

	order, err := parseListDirection(values, "order", listDescending)
	if err != nil {
		return route.Result{}, err
	}

	query := onboarding.ListOnboardingImportsQuery{Limit: page.Limit, Ascending: order == listAscending}
	for _, raw := range values["state"] {
		if raw == "" {
			return route.Result{}, route.InvalidRequest(errors.New("state must be non-empty"))
		}

		query.States = append(query.States, model.OnboardingImportState(raw))
	}

	cursor, continued, err := route.DecodePageCursor(page, onboardingImportCursorSpec())
	if err != nil {
		return route.Result{}, err
	}

	if continued {
		if cursor.Order != order {
			return route.Result{}, route.InvalidRequest(errors.New("cursor belongs to another order"))
		}
		query.BeforeCreatedAt, _ = model.ParseRFC3339(cursor.CreatedAt)
		query.BeforeID = model.OnboardingImportID(cursor.ID)
	}

	result, err := h.app.ListOnboardingImports(request.Context, request.Invocation(), query)
	if err != nil {
		return route.Result{}, err
	}
	if len(result.Items) > query.Limit {
		return route.Result{}, route.ErrUnpresentable
	}

	response, err := route.NewList(result.Items, result.HasMore, func(view onboarding.OnboardingImportView) onboardingImportResponse {
		return onboardingImportResponseFromView(view, nil)
	}, func(last onboarding.OnboardingImportView) (string, error) {
		return route.EncodeCursor(
			onboardingImportCursor{CreatedAt: model.FormatRFC3339(last.CreatedAt), ID: last.ID.String(), Order: order},
			onboardingImportCursorSpec(),
		)
	})
	if err != nil {
		return route.Result{}, err
	}

	return route.JSON(http.StatusOK, response).WithHeaders(noStoreHeaders()), nil
}

func (h onboardingImportHandlers) get(request route.Request) (route.Result, error) {
	id, err := route.PathID[model.OnboardingImportID](request, "onboarding_import_id")
	if err != nil {
		return route.Result{}, err
	}
	view, rows, err := h.app.GetOnboardingImport(request.Context, request.Invocation(), id)
	if err != nil {
		return route.Result{}, err
	}
	return route.JSON(http.StatusOK, onboardingImportResponseFromView(view, rows)).WithHeaders(noStoreHeaders()), nil
}

func (h onboardingImportHandlers) commit(request route.Request) (route.Result, error) {
	id, err := route.PathID[model.OnboardingImportID](request, "onboarding_import_id")
	if err != nil {
		return route.Result{}, err
	}

	var body onboardingImportCommitRequest
	if err = request.DecodeJSON(&body); err != nil {
		return route.Result{}, route.InvalidRequest(err)
	}

	view, err := h.app.CommitOnboardingImport(request.Context, request.Invocation(), onboarding.CommitOnboardingImportCommand{
		ID:               id,
		ExpectedRevision: body.ExpectedRevision,
		PreviewDigest:    body.PreviewDigest,
		Policy:           model.OnboardingImportCommitPolicy(body.Policy),
		IdempotencyKey:   request.IdempotencyKey,
	})
	if err != nil {
		return route.Result{}, err
	}

	return route.JSON(http.StatusAccepted, onboardingImportResponseFromView(view, nil)).WithHeaders(noStoreHeaders()), nil
}

func (h onboardingImportHandlers) cancel(request route.Request) (route.Result, error) {
	id, err := route.PathID[model.OnboardingImportID](request, "onboarding_import_id")
	if err != nil {
		return route.Result{}, err
	}
	view, err := h.app.CancelOnboardingImport(request.Context, request.Invocation(), id)
	if err != nil {
		return route.Result{}, err
	}
	return route.JSON(http.StatusOK, onboardingImportResponseFromView(view, nil)).WithHeaders(noStoreHeaders()), nil
}

func (h onboardingImportHandlers) report(request route.Request) (route.ProtocolResult, error) {
	id, err := route.PathID[model.OnboardingImportID](request, "onboarding_import_id")
	if err != nil {
		return route.ProtocolResult{}, err
	}
	var body bytes.Buffer
	if err = h.app.OnboardingImportReport(request.Context, request.Invocation(), id, &body); err != nil {
		return route.ProtocolResult{}, err
	}
	headers := noStoreHeaders()
	headers.Set("Content-Type", "text/csv; charset=utf-8")
	headers.Set("X-Content-Type-Options", "nosniff")
	return route.BinaryDownload(io.NopCloser(bytes.NewReader(body.Bytes())), int64(body.Len())).WithHeaders(headers), nil
}

func onboardingImportResponseFromView(
	view onboarding.OnboardingImportView,
	rows []onboarding.OnboardingImportRowView,
) onboardingImportResponse {
	response := onboardingImportResponse{
		ID:                   view.ID.String(),
		Mode:                 string(view.Mode),
		State:                string(view.State),
		ScopeType:            string(view.ScopeType),
		ScopeID:              view.ScopeID,
		RoleID:               view.RoleID.String(),
		SourcePeriodID:       view.SourcePeriodID.String(),
		SourceClassID:        view.SourceClassID.String(),
		DestinationPeriodID:  view.DestinationPeriodID.String(),
		DestinationClassID:   view.DestinationClassID.String(),
		SourceClassName:      view.SourceClassName,
		DestinationClassName: view.DestinationClassName,
		PreviewDigest:        view.PreviewDigest,
		IgnoredHeaders:       append([]string(nil), view.IgnoredHeaders...),
		TotalRows:            view.TotalRows,
		ValidRows:            view.ValidRows,
		InvalidRows:          view.InvalidRows,
		SucceededRows:        view.SucceededRows,
		NoOpRows:             view.NoOpRows,
		FailedRows:           view.FailedRows,
		SkippedRows:          view.SkippedRows,
		CommitPolicy:         string(view.CommitPolicy),
		ParseJobID:           view.ParseJobID.String(),
		ExecutionJobID:       view.ExecutionJobID.String(),
		CreatedAt:            model.FormatRFC3339(view.CreatedAt),
		UpdatedAt:            model.FormatRFC3339(view.UpdatedAt),
		ExpiresAt:            model.FormatRFC3339(view.ExpiresAt),
		Revision:             view.Revision,
		FailureCode:          view.FailureCode,
		FileName:             view.FileName,
	}
	if !view.EffectiveAt.IsZero() {
		response.EffectiveAt = model.FormatRFC3339(view.EffectiveAt)
	}

	if rows != nil {
		response.Rows = make([]onboardingImportRowResponse, 0, len(rows))
		for _, row := range rows {
			response.Rows = append(response.Rows, onboardingImportRowResponse{
				RowNumber:    row.RowNumber,
				Reference:    row.Reference,
				Operation:    row.Operation,
				Status:       string(row.Status),
				PreviewCode:  row.PreviewCode,
				PublicCode:   row.PublicCode,
				InvitationID: row.InvitationID.String(),
				ResourceID:   row.ResourceID,
			})
		}
	}

	return response
}
