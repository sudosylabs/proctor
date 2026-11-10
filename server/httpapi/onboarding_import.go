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
	"github.com/sudosylabs/proctor/server/model"
	"github.com/sudosylabs/proctor/server/store"
)

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

// onboardingImportCursor is the keyset of the last listed import: newest
// first by creation instant, then identity.
type onboardingImportCursor struct {
	CreatedAt string `json:"created_at"`
	ID        string `json:"id"`
}

func onboardingImportCursorSpec() opaqueCursorSpec[onboardingImportCursor] {
	return opaqueCursorSpec[onboardingImportCursor]{
		label:                "onboarding import",
		maximumEncodedLength: defaultOpaqueCursorMaximumEncodedLength,
		members:              []string{"created_at", "id"},
		validate: func(cursor onboardingImportCursor) error {
			createdAt, err := model.ParseRFC3339(cursor.CreatedAt)
			if err != nil || createdAt.IsZero() || !model.OnboardingImportID(cursor.ID).IsValid() {
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

func onboardingImportResource(app OnboardingImportApplication) apiResource {
	h := onboardingImportHandlers{app: app}
	return newResource("onboarding-imports",
		endpoint{
			Method:       http.MethodPost,
			Path:         "/onboarding-imports",
			Auth:         AuthPrincipalRequired,
			Idempotency:  IdempotencyNone,
			MaxBodyBytes: store.OnboardingImportMaximumBytes,
			ProtocolName: "onboarding-import-csv",
			ProtocolKind: RouteProtocolStreamingUpload,
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
		endpoint{
			Method: http.MethodGet,
			Path:   "/onboarding-imports",
			Auth:   AuthPrincipalRequired,
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
		endpoint{
			Method: http.MethodGet,
			Path:   "/onboarding-imports/{onboarding_import_id}",
			Auth:   AuthPrincipalRequired,
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
		endpoint{
			Method:      http.MethodPost,
			Path:        "/onboarding-imports/{onboarding_import_id}/commit",
			Auth:        AuthPrincipalRequired,
			Idempotency: IdempotencyRequired,
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
		endpoint{
			Method: http.MethodPost,
			Path:   "/onboarding-imports/{onboarding_import_id}/cancel",
			Auth:   AuthPrincipalRequired,
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
		endpoint{
			Method:       http.MethodGet,
			Path:         "/onboarding-imports/{onboarding_import_id}/report",
			Auth:         AuthPrincipalRequired,
			ProtocolName: "onboarding-import-report",
			ProtocolKind: RouteProtocolBinaryDownload,
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

func (h onboardingImportHandlers) upload(request operationRequest) (protocolResult, error) {
	query := request.http.URL.Query()
	command := onboarding.UploadOnboardingImportCommand{
		Mode:      model.OnboardingImportMode(query.Get("mode")),
		ScopeType: model.RoleScopeType(query.Get("scope_type")),
		ScopeID:   query.Get("scope_id"),
		RoleID:    model.RoleID(query.Get("role_id")),
		Body:      request.http.Body,
	}
	if names, given := query["file_name"]; given {
		if len(names) != 1 {
			return protocolResult{}, apperror.Invalid("request.invalid", "file_name")
		}

		command.FileName = &names[0]
	}

	view, err := h.app.UploadOnboardingImport(request.context, request.invocation(), command)
	if err != nil {
		return protocolResult{}, err
	}

	return streamingUploadProtocolResult(http.StatusAccepted, onboardingImportResponseFromView(view, nil)).withHeaders(noStoreHeaders()), nil
}

func (h onboardingImportHandlers) list(request operationRequest) (operationResult, error) {
	page, err := request.pageRequest()
	if err != nil {
		return operationResult{}, err
	}

	values := request.http.URL.Query()
	for key := range values {
		if key != "state" && key != "limit" && key != "cursor" {
			return operationResult{}, invalidRequestError(errors.New("unknown onboarding import query parameter"))
		}
	}

	query := onboarding.ListOnboardingImportsQuery{Limit: page.Limit}
	for _, raw := range values["state"] {
		if raw == "" {
			return operationResult{}, invalidRequestError(errors.New("state must be non-empty"))
		}

		query.States = append(query.States, model.OnboardingImportState(raw))
	}

	cursor, continued, err := decodePageCursor(page, onboardingImportCursorSpec())
	if err != nil {
		return operationResult{}, err
	}

	if continued {
		query.BeforeCreatedAt, _ = model.ParseRFC3339(cursor.CreatedAt)
		query.BeforeID = model.OnboardingImportID(cursor.ID)
	}

	result, err := h.app.ListOnboardingImports(request.context, request.invocation(), query)
	if err != nil {
		return operationResult{}, err
	}
	if len(result.Items) > query.Limit {
		return operationResult{}, errUnpresentableResult
	}

	response, err := newListResponse(result.Items, result.HasMore, func(view onboarding.OnboardingImportView) onboardingImportResponse {
		return onboardingImportResponseFromView(view, nil)
	}, func(last onboarding.OnboardingImportView) (string, error) {
		return encodeOpaqueCursor(
			onboardingImportCursor{CreatedAt: model.FormatRFC3339(last.CreatedAt), ID: last.ID.String()},
			onboardingImportCursorSpec(),
		)
	})
	if err != nil {
		return operationResult{}, err
	}

	return jsonResult(http.StatusOK, response).withHeaders(noStoreHeaders()), nil
}

func (h onboardingImportHandlers) get(request operationRequest) (operationResult, error) {
	id, err := pathID[model.OnboardingImportID](request, "onboarding_import_id")
	if err != nil {
		return operationResult{}, err
	}
	view, rows, err := h.app.GetOnboardingImport(request.context, request.invocation(), id)
	if err != nil {
		return operationResult{}, err
	}
	return jsonResult(http.StatusOK, onboardingImportResponseFromView(view, rows)).withHeaders(noStoreHeaders()), nil
}

func (h onboardingImportHandlers) commit(request operationRequest) (operationResult, error) {
	id, err := pathID[model.OnboardingImportID](request, "onboarding_import_id")
	if err != nil {
		return operationResult{}, err
	}

	var body onboardingImportCommitRequest
	if err = request.decodeJSON(&body); err != nil {
		return operationResult{}, invalidRequestError(err)
	}

	view, err := h.app.CommitOnboardingImport(request.context, request.invocation(), onboarding.CommitOnboardingImportCommand{
		ID:               id,
		ExpectedRevision: body.ExpectedRevision,
		PreviewDigest:    body.PreviewDigest,
		Policy:           model.OnboardingImportCommitPolicy(body.Policy),
		IdempotencyKey:   request.idempotencyKey,
	})
	if err != nil {
		return operationResult{}, err
	}

	return jsonResult(http.StatusAccepted, onboardingImportResponseFromView(view, nil)).withHeaders(noStoreHeaders()), nil
}

func (h onboardingImportHandlers) cancel(request operationRequest) (operationResult, error) {
	id, err := pathID[model.OnboardingImportID](request, "onboarding_import_id")
	if err != nil {
		return operationResult{}, err
	}
	view, err := h.app.CancelOnboardingImport(request.context, request.invocation(), id)
	if err != nil {
		return operationResult{}, err
	}
	return jsonResult(http.StatusOK, onboardingImportResponseFromView(view, nil)).withHeaders(noStoreHeaders()), nil
}

func (h onboardingImportHandlers) report(request operationRequest) (protocolResult, error) {
	id, err := pathID[model.OnboardingImportID](request, "onboarding_import_id")
	if err != nil {
		return protocolResult{}, err
	}
	var body bytes.Buffer
	if err = h.app.OnboardingImportReport(request.context, request.invocation(), id, &body); err != nil {
		return protocolResult{}, err
	}
	headers := noStoreHeaders()
	headers.Set("Content-Type", "text/csv; charset=utf-8")
	headers.Set("X-Content-Type-Options", "nosniff")
	return binaryDownloadProtocolResult(io.NopCloser(bytes.NewReader(body.Bytes())), int64(body.Len())).withHeaders(headers), nil
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
