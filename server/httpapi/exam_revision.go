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

	"github.com/sudosylabs/proctor/server/app/exam"
	"github.com/sudosylabs/proctor/server/app/invocation"
	"github.com/sudosylabs/proctor/server/httpapi/route"
	"github.com/sudosylabs/proctor/server/model"
	"github.com/sudosylabs/proctor/server/store"
)

// ExamRevisionApplication is the application port its handlers call; it
// publishes Exam Revisions and reads their summaries, never authored content.
type ExamRevisionApplication interface {
	PublishExamRevision(context.Context, invocation.Invocation, exam.PublishRevisionCommand) (store.ExamRevisionSummary, error)
	GetExamRevision(context.Context, invocation.Invocation, model.ExamID, model.ExamRevisionID) (store.ExamRevisionSummary, error)
	ListExamRevisions(context.Context, invocation.Invocation, exam.ListRevisionsQuery) (exam.RevisionPage, error)
}

type publishExamRevisionRequest struct {
	ExpectedDraftRevision int64 `json:"expected_revision"`
}

type examRevisionResponse struct {
	ID                         string                     `json:"id"`
	ExamID                     string                     `json:"exam_id"`
	Number                     int64                      `json:"number"`
	SourceDraftRevision        int64                      `json:"source_draft_revision"`
	Title                      string                     `json:"title"`
	PolicySchemaVersion        int                        `json:"policy_schema_version"`
	PolicyDigest               string                     `json:"policy_digest"`
	Capacity                   examCapacityPolicyResponse `json:"capacity"`
	StarterWorkspaceDigest     string                     `json:"starter_workspace_digest"`
	ContentDigest              string                     `json:"content_digest"`
	ResourceCount              int                        `json:"resource_count"`
	StarterWorkspaceEntryCount int                        `json:"starter_workspace_entry_count"`
	StarterWorkspaceTotalBytes int64                      `json:"starter_workspace_total_bytes"`
	PublishedByUserID          string                     `json:"published_by_user_id"`
	PublishedAt                string                     `json:"published_at"`
	BaseRevisionID             string                     `json:"base_revision_id,omitempty"`
	PublicationKind            string                     `json:"publication_kind"`
}

type examRevisionListResponse struct {
	Items      []examRevisionResponse `json:"items"`
	NextCursor string                 `json:"next_cursor,omitempty"`
}

type examRevisionCursor struct {
	Number     int64  `json:"number"`
	RevisionID string `json:"revision_id"`
}

type examRevisionHandlers struct{ app ExamRevisionApplication }

func examRevisionResource(app ExamRevisionApplication) route.Resource {
	h := examRevisionHandlers{app: app}
	collection := "/exams/{exam_id}/revisions"
	member := "/exams/{exam_id}/revisions/{exam_revision_id}"
	return route.NewResource(
		"exam-revisions",
		route.Endpoint{
			Method:      http.MethodPost,
			Path:        collection,
			Auth:        route.AuthPrincipalRequired,
			Idempotency: route.IdempotencyRequired,
			Errors: []string{
				"authorization.request.invalid",
				"authorization.unavailable",
				"audit.unavailable",
				"request.invalid",
				"resource.not_found",
				"exam.invalid",
				"exam.archived",
				"exam.conflict",
				"exam.draft.revision_conflict",
				"exam.revision.no_changes",
				"exam.revision.capacity_exceeded",
				"exam.unavailable",
				"idempotency.key_required",
				"idempotency.key_invalid",
				"idempotency.conflict",
				"idempotency.in_progress",
			},
			Handle: h.publish,
		},
		route.Endpoint{
			Method: http.MethodGet,
			Path:   collection,
			Auth:   route.AuthPrincipalRequired,
			Errors: []string{
				"authorization.request.invalid",
				"authorization.unavailable",
				"request.invalid",
				"resource.not_found",
				"exam.invalid",
				"exam.unavailable",
			},
			Handle: h.list,
		},
		route.Endpoint{
			Method: http.MethodGet,
			Path:   member,
			Auth:   route.AuthPrincipalRequired,
			Errors: []string{
				"authorization.request.invalid",
				"authorization.unavailable",
				"request.invalid",
				"resource.not_found",
				"exam.invalid",
				"exam.unavailable",
			},
			Handle: h.get,
		},
	)
}

func (h examRevisionHandlers) publish(request route.Request) (route.Result, error) {
	examID, err := examRevisionExamID(request)
	if err != nil {
		return route.Result{}, err
	}

	var body publishExamRevisionRequest
	if err = request.DecodeJSON(&body); err != nil {
		return route.Result{}, err
	}
	if body.ExpectedDraftRevision < 1 {
		return route.Result{}, route.InvalidRequest(errors.New("must be positive"))
	}

	summary, err := h.app.PublishExamRevision(request.Context, request.Invocation(), exam.PublishRevisionCommand{
		ExamID:                examID,
		ExpectedDraftRevision: body.ExpectedDraftRevision,
		IdempotencyKey:        request.IdempotencyKey,
	})
	if err != nil {
		return route.Result{}, err
	}

	return route.JSON(http.StatusCreated, examRevisionResponseFromSummary(summary)), nil
}

func (h examRevisionHandlers) get(request route.Request) (route.Result, error) {
	examID, revisionID, err := examRevisionIDs(request)
	if err != nil {
		return route.Result{}, err
	}

	summary, err := h.app.GetExamRevision(request.Context, request.Invocation(), examID, revisionID)
	if err != nil {
		return route.Result{}, err
	}

	return route.JSON(http.StatusOK, examRevisionResponseFromSummary(summary)), nil
}

func (h examRevisionHandlers) list(request route.Request) (route.Result, error) {
	examID, err := examRevisionExamID(request)
	if err != nil {
		return route.Result{}, err
	}

	query := exam.ListRevisionsQuery{ExamID: examID, Limit: 50}
	values := request.HTTP.URL.Query()
	if raw := values.Get("limit"); raw != "" {
		query.Limit, err = strconv.Atoi(raw)
		if err != nil || query.Limit < 1 || query.Limit > 200 {
			return route.Result{}, route.InvalidRequest(errors.New("must be between 1 and 200"))
		}
	}

	if raw := values.Get("cursor"); raw != "" {
		cursor, decodeErr := decodeExamRevisionCursor(raw)
		if decodeErr != nil {
			return route.Result{}, route.InvalidRequest(decodeErr)
		}

		query.BeforeNumber = cursor.Number
		query.BeforeRevisionID = model.ExamRevisionID(cursor.RevisionID)
	}

	page, err := h.app.ListExamRevisions(request.Context, request.Invocation(), query)
	if err != nil {
		return route.Result{}, err
	}

	response := examRevisionListResponse{Items: make([]examRevisionResponse, 0, len(page.Items))}
	for _, item := range page.Items {
		response.Items = append(response.Items, examRevisionResponseFromSummary(item))
	}

	if page.HasMore && len(page.Items) == 0 {
		return route.Result{}, route.Unpresentable(errors.New("revision page made no progress"))
	}

	if page.HasMore {
		last := page.Items[len(page.Items)-1]
		response.NextCursor, err = encodeExamRevisionCursor(examRevisionCursor{Number: last.Number, RevisionID: last.ID.String()})
		if err != nil {
			return route.Result{}, route.Unpresentable(err)
		}
	}

	return route.JSON(http.StatusOK, response), nil
}

func examRevisionExamID(request route.Request) (model.ExamID, error) {
	id, err := route.PathID[model.ExamID](request, "exam_id")
	if err != nil {
		return "", err
	}
	return id, nil
}

func examRevisionIDs(request route.Request) (model.ExamID, model.ExamRevisionID, error) {
	examID, err := examRevisionExamID(request)
	if err != nil {
		return "", "", err
	}
	revisionID, err := route.PathID[model.ExamRevisionID](request, "exam_revision_id")
	if err != nil {
		return "", "", err
	}
	return examID, revisionID, nil
}

func examRevisionResponseFromSummary(summary store.ExamRevisionSummary) examRevisionResponse {
	return examRevisionResponse{
		ID:                         summary.ID.String(),
		ExamID:                     summary.ExamID.String(),
		Number:                     summary.Number,
		SourceDraftRevision:        summary.SourceDraftRevision,
		Title:                      summary.Title,
		PolicySchemaVersion:        summary.PolicySchemaVersion,
		PolicyDigest:               summary.PolicyDigest,
		Capacity:                   examCapacityPolicyResponseFromModel(summary.Capacity),
		StarterWorkspaceDigest:     summary.StarterWorkspaceDigest,
		ContentDigest:              summary.ContentDigest,
		ResourceCount:              summary.ResourceCount,
		StarterWorkspaceEntryCount: summary.StarterWorkspaceEntries,
		StarterWorkspaceTotalBytes: summary.StarterWorkspaceBytes,
		PublishedByUserID:          summary.PublishedByUserID.String(),
		PublishedAt:                model.FormatRFC3339(summary.PublishedAt),
		BaseRevisionID:             summary.BaseRevisionID.String(),
		PublicationKind:            string(summary.Kind),
	}
}

func encodeExamRevisionCursor(cursor examRevisionCursor) (string, error) {
	return route.EncodeCursor(cursor, examRevisionCursorSpec())
}

func decodeExamRevisionCursor(raw string) (examRevisionCursor, error) {
	return route.DecodeCursor(raw, examRevisionCursorSpec())
}

func examRevisionCursorSpec() route.CursorSpec[examRevisionCursor] {
	return route.CursorSpec[examRevisionCursor]{
		Label:                "Exam Revision",
		MaximumEncodedLength: route.DefaultMaximumCursorLength,
		Members:              []string{"number", "revision_id"},
		Validate: func(cursor examRevisionCursor) error {
			if cursor.Number < 1 || !model.ExamRevisionID(cursor.RevisionID).IsValid() {
				return errors.New("invalid Exam Revision keyset")
			}
			return nil
		},
	}
}
