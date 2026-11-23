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

	"github.com/sudosylabs/proctor/server/app/administration"
	"github.com/sudosylabs/proctor/server/httpapi/route"
	"github.com/sudosylabs/proctor/server/model"
)

func auditExportEndpoints(h auditHandlers) []route.Endpoint {
	read := []string{
		"authorization.denied",
		"authorization.request.invalid",
		"authorization.unavailable",
		"audit.unavailable",
		"audit.export.not_found",
		"audit.export.unavailable",
		"request.invalid",
		"resource.not_found",
	}
	return []route.Endpoint{
		{
			Method:      http.MethodPost,
			Path:        "/audits/exports",
			Auth:        route.AuthPrincipalRequired,
			Idempotency: route.IdempotencyRequired,
			Errors: []string{
				"authorization.denied",
				"authorization.request.invalid",
				"authorization.unavailable",
				"audit.internal",
				"audit.query.invalid",
				"audit.unavailable",
				"audit.export.limit",
				"audit.export.not_found",
				"audit.export.policy_unconfigured",
				"audit.export.unavailable",
				"idempotency.key_required",
				"idempotency.key_invalid",
				"idempotency.conflict",
				"idempotency.in_progress",
				"request.invalid",
				"resource.not_found",
			},
			Handle: h.createExport,
		},
		{
			Method: http.MethodGet,
			Path:   "/audits/exports/{audit_export_id}",
			Auth:   route.AuthPrincipalRequired,
			Errors: read,
			Handle: h.getExport,
		},
		{
			Method:       http.MethodGet,
			Path:         "/audits/exports/{audit_export_id}/download",
			Auth:         route.AuthPrincipalRequired,
			ProtocolName: "audit-export-download",
			ProtocolKind: route.ProtocolBinaryDownload,
			Errors:       append(append([]string(nil), read...), "audit.export.expired", "audit.export.not_ready"),
			Protocol:     h.downloadExport,
		},
	}
}

// createAuditExportRequest carries the listing's filters under the listing's
// query names, and the file format.
type createAuditExportRequest struct {
	Format        model.AuditExportFormat `json:"format"`
	ActorID       string                  `json:"actor_id,omitempty"`
	SubjectUserID string                  `json:"subject_user_id,omitempty"`
	Actions       []string                `json:"action,omitempty"`
	Kind          string                  `json:"kind,omitempty"`
	Status        string                  `json:"status,omitempty"`
	ResourceType  string                  `json:"resource_type,omitempty"`
	ResourceID    string                  `json:"resource_id,omitempty"`
	CreatedAfter  string                  `json:"created_after,omitempty"`
	CreatedBefore string                  `json:"created_before,omitempty"`
	Order         string                  `json:"order,omitempty"`
}

type auditExportResponse struct {
	ID         string                  `json:"id"`
	Format     model.AuditExportFormat `json:"format"`
	Redacted   bool                    `json:"redacted"`
	State      model.ExamExportState   `json:"state"`
	JobID      string                  `json:"job_id"`
	EventCount int                     `json:"event_count"`
	CreatedAt  string                  `json:"created_at"`
	ExpiresAt  string                  `json:"expires_at"`
	SizeBytes  *int64                  `json:"size_bytes,omitempty"`
	SHA256     string                  `json:"sha256,omitempty"`
	ReadyAt    string                  `json:"ready_at,omitempty"`
}

func auditExportResponseFromModel(e *model.AuditExport) auditExportResponse {
	r := auditExportResponse{
		ID:         e.ID.String(),
		Format:     e.Format,
		Redacted:   e.Redacted,
		State:      e.State,
		JobID:      e.JobID.String(),
		EventCount: e.EventCount,
		CreatedAt:  model.FormatRFC3339(e.CreatedAt),
		ExpiresAt:  model.FormatRFC3339(e.ExpiresAt),
	}
	if e.State == model.ExamExportReady {
		size := e.SizeBytes
		r.SizeBytes, r.SHA256 = &size, e.SHA256
		r.ReadyAt = model.FormatRFC3339(e.ReadyAt.Time)
	}
	return r
}

func (r createAuditExportRequest) query() (administration.ListAuditEventsQuery, error) {
	query := administration.ListAuditEventsQuery{
		ActorID:       model.UserID(r.ActorID),
		SubjectUserID: model.UserID(r.SubjectUserID),
		Actions:       r.Actions,
		Kind:          model.AuditKind(r.Kind),
		Status:        model.AuditStatus(r.Status),
	}
	switch r.Order {
	case "", listDescending:
	case listAscending:
		query.Ascending = true
	default:
		return query, errAuditQuery("order")
	}
	var err error
	if r.CreatedAfter != "" {
		if query.Since, err = model.ParseRFC3339(r.CreatedAfter); err != nil {
			return query, errAuditQuery("created_after")
		}
	}
	if r.CreatedBefore != "" {
		if query.Until, err = model.ParseRFC3339(r.CreatedBefore); err != nil {
			return query, errAuditQuery("created_before")
		}
	}
	if (r.ResourceType == "") != (r.ResourceID == "") {
		return query, errInvalidAuditResource
	}
	if r.ResourceType != "" {
		resource := model.Resource{Type: model.ResourceType(r.ResourceType), ID: r.ResourceID}
		if resource.Validate() != nil {
			return query, errInvalidAuditResource
		}
		query.Resource = &resource
	}
	return query, nil
}

func (h auditHandlers) createExport(request route.Request) (route.Result, error) {
	var body createAuditExportRequest
	if err := request.DecodeJSON(&body); err != nil {
		return route.Result{}, err
	}
	if !body.Format.IsValid() {
		return route.Result{}, route.InvalidRequest(errors.New("format must be csv or jsonl"))
	}
	query, err := body.query()
	if err != nil {
		return route.Result{}, route.InvalidRequest(err)
	}
	export, err := h.app.CreateAuditExport(request.Context, request.Invocation(), administration.CreateAuditExportCommand{
		Query: query, Format: body.Format, IdempotencyKey: request.IdempotencyKey,
	})
	if err != nil {
		return route.Result{}, err
	}
	if export.Validate() != nil {
		return route.Result{}, route.ErrUnpresentable
	}
	return route.JSON(http.StatusAccepted, auditExportResponseFromModel(export)).WithHeaders(privateNoStoreHeaders()), nil
}

func (h auditHandlers) getExport(request route.Request) (route.Result, error) {
	id, err := route.PathID[model.AuditExportID](request, "audit_export_id")
	if err != nil {
		return route.Result{}, err
	}
	export, err := h.app.GetAuditExport(request.Context, request.Invocation(), id)
	if err != nil {
		return route.Result{}, err
	}
	if export.Validate() != nil || export.ID != id {
		return route.Result{}, route.ErrUnpresentable
	}
	return route.JSON(http.StatusOK, auditExportResponseFromModel(export)).WithHeaders(privateNoStoreHeaders()), nil
}

func (h auditHandlers) downloadExport(request route.Request) (route.ProtocolResult, error) {
	id, err := route.PathID[model.AuditExportID](request, "audit_export_id")
	if err != nil {
		return route.ProtocolResult{}, err
	}
	opened, err := h.app.OpenAuditExport(request.Context, request.Invocation(), id)
	if err != nil {
		return route.ProtocolResult{}, err
	}
	if opened == nil {
		return route.ProtocolResult{}, route.ErrUnpresentable
	}
	if opened.Export.Validate() != nil || opened.Export.State != model.ExamExportReady || opened.Export.ID != id || opened.Body == nil {
		if opened.Body != nil {
			_ = opened.Body.Close()
		}
		return route.ProtocolResult{}, route.ErrUnpresentable
	}

	headers := privateNoStoreHeaders()
	extension, contentType := "csv", "text/csv; charset=utf-8"
	if opened.Export.Format == model.AuditExportJSONL {
		extension, contentType = "jsonl", "application/jsonl"
	}
	headers.Set("Content-Type", contentType)
	headers.Set("Content-Disposition", `attachment; filename="audit-export-`+id.String()+`.`+extension+`"`)
	headers.Set("X-Content-Type-Options", "nosniff")
	headers.Set("ETag", `"`+opened.Export.SHA256+`"`)
	return route.BinaryDownload(opened.Body, opened.Export.SizeBytes).WithHeaders(headers), nil
}
