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
	"slices"

	"github.com/sudosylabs/proctor/server/app/exam"
	"github.com/sudosylabs/proctor/server/app/invocation"
	"github.com/sudosylabs/proctor/server/httpapi/route"
	"github.com/sudosylabs/proctor/server/model"
)

// ExamExportsApplication is the application port its handlers call; it
// requests an Exam export and reads or downloads it.
type ExamExportsApplication interface {
	CreateExamExport(context.Context, invocation.Invocation, exam.CreateExportCommand) (*model.ExamExport, error)
	GetExamExport(context.Context, invocation.Invocation, exam.ExportQuery) (*model.ExamExport, error)
	OpenExamExport(context.Context, invocation.Invocation, exam.ExportQuery) (*exam.ExportDownload, error)
}

// examExportsHandlers serves the Exam export routes of one scope: a whole
// Sitting, or one Submission when submission is set.
type examExportsHandlers struct {
	app        ExamExportsApplication
	submission bool
}

func examExportsResource(app ExamExportsApplication) route.Resource {
	var routes []route.Endpoint
	for _, submission := range []bool{false, true} {
		h := examExportsHandlers{app: app, submission: submission}
		base := "/exams/{exam_id}/sittings/{exam_sitting_id}"
		if submission {
			base += "/submissions/{submission_id}"
		}

		collection := base + "/exports"
		member := base + "/exports/{exam_export_id}"
		content := base + "/exports/{exam_export_id}/content"
		create := route.Endpoint{
			Method:      http.MethodPost,
			Path:        collection,
			Auth:        route.AuthSessionRequired,
			Idempotency: route.IdempotencyRequired,
			Errors: []string{
				"authorization.request.invalid",
				"authorization.unavailable",
				"audit.unavailable",
				"request.invalid",
				"resource.not_found",
				"exam.invalid",
				"exam.unavailable",
				"exam.export.not_found",
				"exam.export.conflict",
				"exam.export.unavailable",
				"exam.export.policy_unconfigured",
				"exam.export.limit",
				"exam.export.source_retired",
				"exam.export.scope_changed",
				"idempotency.key_required",
				"idempotency.key_invalid",
				"idempotency.conflict",
				"idempotency.in_progress",
			},
			Handle: h.create,
		}
		protocol := "sitting-export-content"
		if submission {
			protocol = "submission-export-content"
		}

		routes = append(
			routes,
			create,
			route.Endpoint{
				Method: http.MethodGet,
				Path:   member,
				Auth:   route.AuthSessionRequired,
				Errors: []string{
					"authorization.request.invalid",
					"authorization.unavailable",
					"request.invalid",
					"resource.not_found",
					"exam.invalid",
					"exam.unavailable",
					"exam.export.not_found",
					"exam.export.conflict",
					"exam.export.unavailable",
				},
				Handle: h.get,
			},
			route.Endpoint{
				Method:       http.MethodGet,
				Path:         content,
				Auth:         route.AuthSessionRequired,
				ProtocolName: protocol,
				ProtocolKind: route.ProtocolBinaryDownload,
				Errors: []string{
					"authorization.request.invalid",
					"authorization.unavailable",
					"request.invalid",
					"resource.not_found",
					"exam.invalid",
					"exam.unavailable",
					"exam.export.not_found",
					"exam.export.conflict",
					"exam.export.unavailable",
					"exam.export.expired",
					"exam.export.not_ready",
				},
				Protocol: h.open,
			},
		)
	}

	return route.NewResource("exam-exports", routes...)
}

type createExamExportRequest struct {
	Categories []model.RetentionCategory `json:"categories"`
}

func (b *createExamExportRequest) UnmarshalJSON(data []byte) error {
	type wire createExamExportRequest
	var value wire
	if err := decodeDuplicateFreeExamIntegrityReviewObject(data, &value); err != nil {
		return err
	}
	*b = createExamExportRequest(value)
	return nil
}

type examExportResponse struct {
	ID                   string                    `json:"id"`
	ExamID               string                    `json:"exam_id"`
	SittingID            string                    `json:"exam_sitting_id"`
	SubmissionID         string                    `json:"submission_id,omitempty"`
	Categories           []model.RetentionCategory `json:"categories"`
	State                model.ExamExportState     `json:"state"`
	PolicyRevision       int64                     `json:"policy_revision"`
	CreatedAt            string                    `json:"created_at"`
	ExpiresAt            string                    `json:"expires_at"`
	ConstructionDeadline string                    `json:"construction_deadline"`
	SubmissionCount      int                       `json:"submission_count"`
	FileCount            int                       `json:"file_count"`
	SourceBytes          int64                     `json:"source_bytes"`
	ArchiveSizeBytes     int64                     `json:"archive_size_bytes,omitempty"`
	ArchiveSHA256        string                    `json:"archive_sha256,omitempty"`
	ReadyAt              string                    `json:"ready_at,omitempty"`
}

func exportResponse(e *model.ExamExport) examExportResponse {
	r := examExportResponse{
		ID:                   e.ID.String(),
		ExamID:               e.Scope.ExamID.String(),
		SittingID:            e.Scope.SittingID.String(),
		SubmissionID:         e.Scope.SubmissionID.String(),
		Categories:           slices.Clone(e.Categories),
		State:                e.State,
		PolicyRevision:       e.PolicyRevision,
		CreatedAt:            model.FormatRFC3339(e.CreatedAt),
		ExpiresAt:            model.FormatRFC3339(e.ExpiresAt),
		ConstructionDeadline: model.FormatRFC3339(e.SourceExpiresAt),
		SubmissionCount:      e.SubmissionCount,
		FileCount:            e.FileCount,
		SourceBytes:          e.SourceBytes,
		ArchiveSizeBytes:     e.ArchiveSizeBytes,
		ArchiveSHA256:        e.ArchiveSHA256,
	}
	if e.ReadyAt.Valid {
		r.ReadyAt = model.FormatRFC3339(e.ReadyAt.Time)
	}

	return r
}

func (h examExportsHandlers) scope(request route.Request) (model.RetentionHoldScope, error) {
	if h.submission {
		return recordsSubmissionScope(request)
	}
	examID, sittingID, err := examSittingIDs(request)
	return model.RetentionHoldScope{ExamID: examID, SittingID: sittingID}, err
}

func (h examExportsHandlers) query(request route.Request) (exam.ExportQuery, error) {
	scope, err := h.scope(request)
	if err != nil {
		return exam.ExportQuery{}, err
	}
	id, err := route.PathID[model.ExamExportID](request, "exam_export_id")
	if err != nil {
		return exam.ExportQuery{}, err
	}
	return exam.ExportQuery{Scope: scope, ExportID: id}, nil
}

func (h examExportsHandlers) create(request route.Request) (route.Result, error) {
	scope, err := h.scope(request)
	if err != nil {
		return route.Result{}, err
	}

	var body createExamExportRequest
	if err = request.DecodeJSON(&body); err != nil {
		return route.Result{}, err
	}

	categories := slices.Clone(body.Categories)
	if len(categories) == 2 && categories[0] == model.RetentionCategoryIntegrity && categories[1] == model.RetentionCategoryWork {
		categories[0], categories[1] = categories[1], categories[0]
	}
	if !model.ValidExamExportCategories(categories) {
		return route.Result{}, route.InvalidRequest(errors.New("select distinct work and/or integrity categories"))
	}

	value, err := h.app.CreateExamExport(request.Context, request.Invocation(), exam.CreateExportCommand{
		Scope:          scope,
		Categories:     categories,
		IdempotencyKey: request.IdempotencyKey,
	})
	if err != nil {
		return route.Result{}, err
	}
	if value.Validate() != nil || value.Scope != scope {
		return route.Result{}, route.ErrUnpresentable
	}

	return route.JSON(http.StatusAccepted, exportResponse(value)).WithHeaders(privateNoStoreHeaders()), nil
}

func (h examExportsHandlers) get(request route.Request) (route.Result, error) {
	query, err := h.query(request)
	if err != nil {
		return route.Result{}, err
	}
	value, err := h.app.GetExamExport(request.Context, request.Invocation(), query)
	if err != nil {
		return route.Result{}, err
	}
	if value.Validate() != nil || value.ID != query.ExportID || value.Scope != query.Scope {
		return route.Result{}, route.ErrUnpresentable
	}
	return route.JSON(http.StatusOK, exportResponse(value)).WithHeaders(privateNoStoreHeaders()), nil
}

func (h examExportsHandlers) open(request route.Request) (route.ProtocolResult, error) {
	query, err := h.query(request)
	if err != nil {
		return route.ProtocolResult{}, err
	}

	opened, err := h.app.OpenExamExport(request.Context, request.Invocation(), query)
	if err != nil {
		return route.ProtocolResult{}, err
	}
	if opened == nil {
		return route.ProtocolResult{}, route.ErrUnpresentable
	}

	if opened.Export.Validate() != nil ||
		opened.Export.State != model.ExamExportReady ||
		opened.Export.ID != query.ExportID ||
		opened.Export.Scope != query.Scope ||
		opened.Body == nil {
		if opened.Body != nil {
			_ = opened.Body.Close()
		}
		return route.ProtocolResult{}, route.ErrUnpresentable
	}

	headers := privateNoStoreHeaders()
	headers.Set("Content-Type", "application/zip")
	headers.Set("Content-Disposition", `attachment; filename="exam-export-`+query.ExportID.String()+`.zip"`)
	headers.Set("ETag", `"`+opened.Export.ArchiveSHA256+`"`)
	return route.BinaryDownload(opened.Body, opened.Export.ArchiveSizeBytes).WithHeaders(headers), nil
}
