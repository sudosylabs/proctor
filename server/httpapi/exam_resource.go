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
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"

	"github.com/sudosylabs/proctor/server/app/exam/resource"
	"github.com/sudosylabs/proctor/server/app/invocation"
	"github.com/sudosylabs/proctor/server/httpapi/route"
	"github.com/sudosylabs/proctor/server/model"
	"github.com/sudosylabs/proctor/server/store"
)

const examResourceUploadBodyLimit = model.ExamResourceMaximumBytes + 64*1024
const examResourceMetadataLimit = 32 * 1024

type ExamResourceApplication interface {
	CreateExamResource(context.Context, invocation.Invocation, resource.CreateCommand) (store.ExamResourceRecord, error)
	ReplaceExamResourceContent(context.Context, invocation.Invocation, resource.ReplaceContentCommand) (store.ExamResourceRecord, error)
	EditExamResourceMetadata(context.Context, invocation.Invocation, resource.EditMetadataCommand) (store.ExamResourceRecord, error)
	ReorderExamResources(context.Context, invocation.Invocation, resource.ReorderCommand) ([]store.ExamResourceRecord, error)
	RemoveExamResource(context.Context, invocation.Invocation, resource.RemoveCommand) (store.ExamResourceRecord, error)
	ListExamResources(context.Context, invocation.Invocation, model.ExamID) ([]store.ExamResourceRecord, error)
	OpenExamResource(context.Context, invocation.Invocation, model.ExamID, model.ExamResourceID) (resource.Opened, error)
}

// examResourceHandlers serves the Exam Resource routes: the files attached to
// an Exam draft. The Exam routes themselves are examHandlers.
type examResourceHandlers struct{ app ExamResourceApplication }
type examResourceUploadMetadata struct {
	ExpectedDraftRevision int64  `json:"expected_revision"`
	DisplayName           string `json:"display_name"`
	DescriptionMarkdown   string `json:"description_markdown"`
	MediaType             string `json:"media_type"`
	Size                  *int64 `json:"size"`
	SHA256                string `json:"sha256"`
}
type examResourceContentReplacementMetadata struct {
	ExpectedDraftRevision int64  `json:"expected_revision"`
	MediaType             string `json:"media_type"`
	Size                  *int64 `json:"size"`
	SHA256                string `json:"sha256"`
}
type editExamResourceMetadataRequest struct {
	ExpectedDraftRevision int64   `json:"expected_revision"`
	DisplayName           *string `json:"display_name"`
	DescriptionMarkdown   *string `json:"description_markdown"`
}
type reorderExamResourcesRequest struct {
	ExpectedDraftRevision int64    `json:"expected_revision"`
	ResourceIDs           []string `json:"resource_ids"`
}
type removeExamResourceRequest struct {
	ExpectedDraftRevision int64 `json:"expected_revision"`
}
type examResourceResponse struct {
	ID                  string `json:"id"`
	DisplayName         string `json:"display_name"`
	DescriptionMarkdown string `json:"description_markdown"`
	Position            int    `json:"position"`
	ContentRevisionID   string `json:"content_revision_id"`
	MediaType           string `json:"media_type"`
	Size                int64  `json:"size"`
	SHA256              string `json:"sha256"`
	UpdatedAt           string `json:"updated_at"`
	DraftRevision       int64  `json:"draft_revision"`
}

// examResourceResource is the HTTP resource of the Exam Resource entity, hence
// the doubled word; examResource is the Exam's.
func examResourceResource(app ExamResourceApplication) route.Resource {
	h := examResourceHandlers{app: app}
	collection := "/exams/{exam_id}/draft/resources"
	member := "/exams/{exam_id}/draft/resources/{exam_resource_id}"
	order := "/exams/{exam_id}/draft/resources/order"
	content := "/exams/{exam_id}/draft/resources/{exam_resource_id}/content"
	return route.NewResource(
		"exam-resources",
		route.Endpoint{
			Method: http.MethodGet,
			Path:   collection,
			Auth:   route.AuthPrincipalRequired,
			Errors: []string{
				"authorization.request.invalid",
				"authorization.unavailable",
				"request.invalid",
				"resource.not_found",
				"exam.resource.invalid",
				"exam.resource.unavailable",
			},
			Handle: h.list,
		},
		route.Endpoint{
			Method:       http.MethodPost,
			Path:         collection,
			Auth:         route.AuthPrincipalRequired,
			Idempotency:  route.IdempotencyRequired,
			MaxBodyBytes: examResourceUploadBodyLimit,
			ProtocolName: "exam-resource-upload",
			ProtocolKind: route.ProtocolStreamingUpload,
			Errors: []string{
				"authorization.request.invalid",
				"authorization.unavailable",
				"audit.unavailable",
				"request.invalid",
				"resource.not_found",
				"exam.archived",
				"exam.draft.revision_conflict",
				"exam.resource.invalid",
				"exam.resource.conflict",
				"exam.resource.unavailable",
				"idempotency.key_required",
				"idempotency.key_invalid",
				"idempotency.conflict",
				"idempotency.in_progress",
				"service.busy",
				"exam.resource.content_invalid",
				"exam.resource.limit",
				"exam.resource.upload_invalid",
				"exam.resource.revision_conflict",
			},
			Protocol: h.create,
		},
		route.Endpoint{
			Method:      http.MethodPatch,
			Path:        member,
			Auth:        route.AuthPrincipalRequired,
			Idempotency: route.IdempotencyRequired,
			Errors: []string{
				"authorization.request.invalid",
				"authorization.unavailable",
				"audit.unavailable",
				"request.invalid",
				"resource.not_found",
				"exam.archived",
				"exam.draft.revision_conflict",
				"exam.resource.invalid",
				"exam.resource.conflict",
				"exam.resource.unavailable",
				"idempotency.key_required",
				"idempotency.key_invalid",
				"idempotency.conflict",
				"idempotency.in_progress",
				"exam.resource.no_changes",
				"exam.resource.revision_conflict",
			},
			Handle: h.editMetadata,
		},
		route.Endpoint{
			Method:      http.MethodPut,
			Path:        order,
			Auth:        route.AuthPrincipalRequired,
			Idempotency: route.IdempotencyRequired,
			Errors: []string{
				"authorization.request.invalid",
				"authorization.unavailable",
				"audit.unavailable",
				"request.invalid",
				"resource.not_found",
				"exam.archived",
				"exam.draft.revision_conflict",
				"exam.resource.invalid",
				"exam.resource.conflict",
				"exam.resource.unavailable",
				"idempotency.key_required",
				"idempotency.key_invalid",
				"idempotency.conflict",
				"idempotency.in_progress",
				"exam.resource.no_changes",
				"exam.resource.order_invalid",
			},
			Handle: h.reorder,
		},
		route.Endpoint{
			Method:       http.MethodPut,
			Path:         content,
			Auth:         route.AuthPrincipalRequired,
			Idempotency:  route.IdempotencyRequired,
			MaxBodyBytes: examResourceUploadBodyLimit,
			ProtocolName: "exam-resource-content-replacement",
			ProtocolKind: route.ProtocolStreamingUpload,
			Errors: []string{
				"authorization.request.invalid",
				"authorization.unavailable",
				"audit.unavailable",
				"request.invalid",
				"resource.not_found",
				"exam.archived",
				"exam.draft.revision_conflict",
				"exam.resource.invalid",
				"exam.resource.conflict",
				"exam.resource.unavailable",
				"idempotency.key_required",
				"idempotency.key_invalid",
				"idempotency.conflict",
				"idempotency.in_progress",
				"service.busy",
				"exam.resource.content_invalid",
				"exam.resource.upload_invalid",
				"exam.resource.revision_conflict",
			},
			Protocol: h.replaceContent,
		},
		route.Endpoint{
			Method:      http.MethodDelete,
			Path:        member,
			Auth:        route.AuthPrincipalRequired,
			Idempotency: route.IdempotencyRequired,
			Errors: []string{
				"authorization.request.invalid",
				"authorization.unavailable",
				"audit.unavailable",
				"request.invalid",
				"resource.not_found",
				"exam.archived",
				"exam.draft.revision_conflict",
				"exam.resource.invalid",
				"exam.resource.conflict",
				"exam.resource.unavailable",
				"idempotency.key_required",
				"idempotency.key_invalid",
				"idempotency.conflict",
				"idempotency.in_progress",
				"exam.resource.revision_conflict",
			},
			Handle: h.remove,
		},
		route.Endpoint{
			Method:       http.MethodGet,
			Path:         content,
			Auth:         route.AuthPrincipalRequired,
			ProtocolName: "exam-resource-protected-content",
			ProtocolKind: route.ProtocolBinaryDownload,
			Errors: []string{
				"authorization.request.invalid",
				"authorization.unavailable",
				"request.invalid",
				"resource.not_found",
				"exam.resource.invalid",
				"exam.resource.unavailable",
			},
			Protocol: h.open,
		},
	)
}

func (h examResourceHandlers) list(request route.Request) (route.Result, error) {
	examID, err := examResourceExamID(request)
	if err != nil {
		return route.Result{}, err
	}

	items, err := h.app.ListExamResources(request.Context, request.Invocation(), examID)
	if err != nil {
		return route.Result{}, err
	}

	// The Exam Capacity Policy bounds the Draft's Resources, so the complete
	// ordered set is paged by position.
	response, err := route.BoundedList(
		request,
		"Exam Resource",
		items,
		func(item store.ExamResourceRecord) route.BoundedKey {
			return route.BoundedKey{Name: fmt.Sprintf("%08d", item.Resource.Position), ID: item.Resource.ID.String()}
		},
		examResourceResponseFromRecord,
	)
	if err != nil {
		return route.Result{}, err
	}

	return route.JSON(http.StatusOK, response), nil
}
func (h examResourceHandlers) create(request route.Request) (route.ProtocolResult, error) {
	examID, err := examResourceExamID(request)
	if err != nil {
		return route.ProtocolResult{}, err
	}

	var metadata examResourceUploadMetadata
	content, err := decodeExamResourceMultipart(request.HTTP, &metadata)
	if err != nil {
		return route.ProtocolResult{}, route.InvalidRequest(err)
	}
	if metadata.Size == nil {
		return route.ProtocolResult{}, route.InvalidRequest(errors.New("size is required"))
	}

	result, err := h.app.CreateExamResource(request.Context, request.Invocation(), resource.CreateCommand{
		ExamID:                examID,
		ExpectedDraftRevision: metadata.ExpectedDraftRevision,
		DisplayName:           metadata.DisplayName,
		DescriptionMarkdown:   metadata.DescriptionMarkdown,
		MediaType:             model.ExamResourceMediaType(metadata.MediaType),
		Body:                  content,
		Size:                  *metadata.Size,
		ExpectedSHA256:        metadata.SHA256,
		IdempotencyKey:        request.IdempotencyKey,
	})
	if err != nil {
		return route.ProtocolResult{}, err
	}

	return route.StreamingUpload(http.StatusCreated, examResourceResponseFromRecord(result)), nil
}
func (h examResourceHandlers) replaceContent(request route.Request) (route.ProtocolResult, error) {
	examID, resourceID, err := examResourceIDs(request)
	if err != nil {
		return route.ProtocolResult{}, err
	}

	var metadata examResourceContentReplacementMetadata
	content, err := decodeExamResourceMultipart(request.HTTP, &metadata)
	if err != nil {
		return route.ProtocolResult{}, route.InvalidRequest(err)
	}
	if metadata.Size == nil {
		return route.ProtocolResult{}, route.InvalidRequest(errors.New("size is required"))
	}

	result, err := h.app.ReplaceExamResourceContent(request.Context, request.Invocation(), resource.ReplaceContentCommand{
		ExamID:                examID,
		ResourceID:            resourceID,
		ExpectedDraftRevision: metadata.ExpectedDraftRevision,
		MediaType:             model.ExamResourceMediaType(metadata.MediaType),
		Body:                  content,
		Size:                  *metadata.Size,
		ExpectedSHA256:        metadata.SHA256,
		IdempotencyKey:        request.IdempotencyKey,
	})
	if err != nil {
		return route.ProtocolResult{}, err
	}

	return route.StreamingUpload(http.StatusOK, examResourceResponseFromRecord(result)), nil
}
func (h examResourceHandlers) editMetadata(request route.Request) (route.Result, error) {
	examID, resourceID, err := examResourceIDs(request)
	if err != nil {
		return route.Result{}, err
	}

	var body editExamResourceMetadataRequest
	if err = request.DecodeJSON(&body); err != nil {
		return route.Result{}, err
	}
	if body.DisplayName == nil && body.DescriptionMarkdown == nil {
		return route.Result{}, route.InvalidRequest(errors.New("at least one metadata field is required"))
	}

	result, err := h.app.EditExamResourceMetadata(request.Context, request.Invocation(), resource.EditMetadataCommand{
		ExamID:                examID,
		ResourceID:            resourceID,
		ExpectedDraftRevision: body.ExpectedDraftRevision,
		DisplayName:           body.DisplayName,
		DescriptionMarkdown:   body.DescriptionMarkdown,
		IdempotencyKey:        request.IdempotencyKey,
	})
	if err != nil {
		return route.Result{}, err
	}

	return route.JSON(http.StatusOK, examResourceResponseFromRecord(result)), nil
}
func (h examResourceHandlers) reorder(request route.Request) (route.Result, error) {
	examID, err := examResourceExamID(request)
	if err != nil {
		return route.Result{}, err
	}

	var body reorderExamResourcesRequest
	if err = request.DecodeJSON(&body); err != nil {
		return route.Result{}, err
	}

	ids := make([]model.ExamResourceID, len(body.ResourceIDs))
	for i, raw := range body.ResourceIDs {
		id, parseErr := model.ParseExamResourceID(raw)
		if parseErr != nil {
			return route.Result{}, route.InvalidRequest(parseErr)
		}

		ids[i] = id
	}

	items, err := h.app.ReorderExamResources(request.Context, request.Invocation(), resource.ReorderCommand{
		ExamID:                examID,
		ExpectedDraftRevision: body.ExpectedDraftRevision,
		ResourceIDs:           ids,
		IdempotencyKey:        request.IdempotencyKey,
	})
	if err != nil {
		return route.Result{}, err
	}

	// The new order is the complete Resource set, presented in the
	// collection envelope without a continuation.
	response, err := route.NewList(items, false, examResourceResponseFromRecord, nil)
	if err != nil {
		return route.Result{}, err
	}

	return route.JSON(http.StatusOK, response), nil
}
func (h examResourceHandlers) remove(request route.Request) (route.Result, error) {
	examID, resourceID, err := examResourceIDs(request)
	if err != nil {
		return route.Result{}, err
	}

	var body removeExamResourceRequest
	if err = request.DecodeJSON(&body); err != nil {
		return route.Result{}, err
	}

	_, err = h.app.RemoveExamResource(request.Context, request.Invocation(), resource.RemoveCommand{
		ExamID:                examID,
		ResourceID:            resourceID,
		ExpectedDraftRevision: body.ExpectedDraftRevision,
		IdempotencyKey:        request.IdempotencyKey,
	})
	if err != nil {
		return route.Result{}, err
	}

	return route.NoContent(), nil
}
func (h examResourceHandlers) open(request route.Request) (route.ProtocolResult, error) {
	examID, resourceID, err := examResourceIDs(request)
	if err != nil {
		return route.ProtocolResult{}, err
	}

	opened, err := h.app.OpenExamResource(request.Context, request.Invocation(), examID, resourceID)
	if err != nil {
		return route.ProtocolResult{}, err
	}
	if opened.Body == nil || opened.Record.Resource == nil || opened.Record.Rendition == nil {
		return route.ProtocolResult{}, errors.New("exam resource application returned incomplete content")
	}

	etag := `"` + opened.Record.Rendition.SHA256 + `"`
	headers := http.Header{"Content-Type": {opened.Record.Rendition.MediaType}, "Cache-Control": {"private, max-age=300"}, "ETag": {etag}}
	if request.HTTP.Header.Get("If-None-Match") == etag {
		_ = opened.Body.Close()
		return route.NotModified(opened.Record.Rendition.Size).WithHeaders(headers), nil
	}

	return route.BinaryDownload(opened.Body, opened.Record.Rendition.Size).WithHeaders(headers), nil
}

func examResourceExamID(request route.Request) (model.ExamID, error) {
	id, err := route.PathID[model.ExamID](request, "exam_id")
	if err != nil {
		return "", err
	}
	return id, nil
}
func examResourceIDs(request route.Request) (model.ExamID, model.ExamResourceID, error) {
	examID, err := examResourceExamID(request)
	if err != nil {
		return "", "", err
	}
	resourceID, err := route.PathID[model.ExamResourceID](request, "exam_resource_id")
	if err != nil {
		return "", "", err
	}
	return examID, resourceID, nil
}
func examResourceResponseFromRecord(record store.ExamResourceRecord) examResourceResponse {
	r, x := record.Resource, record.Rendition
	if r == nil || x == nil {
		return examResourceResponse{}
	}

	return examResourceResponse{
		ID:                  r.ID.String(),
		DisplayName:         r.DisplayName,
		DescriptionMarkdown: r.DescriptionMarkdown,
		Position:            r.Position,
		ContentRevisionID:   r.SelectedFileRevisionID.String(),
		MediaType:           x.MediaType,
		Size:                x.Size,
		SHA256:              x.SHA256,
		UpdatedAt:           model.TimeUTC(r.UpdatedAt).Format("2006-01-02T15:04:05.999999999Z07:00"),
		DraftRevision:       record.DraftRevision,
	}
}

func decodeExamResourceMultipart[T any](request *http.Request, target *T) (io.Reader, error) {
	if request == nil || target == nil {
		return nil, errors.New("request is required")
	}

	reader, err := request.MultipartReader()
	if err != nil {
		return nil, errors.New("multipart/form-data is required")
	}

	metadata, err := reader.NextPart()
	if err != nil {
		return nil, errors.New("metadata part is required")
	}

	defer metadata.Close()
	if metadata.FormName() != "metadata" || metadata.FileName() != "" {
		return nil, errors.New("metadata must be the first non-file part")
	}

	raw, err := io.ReadAll(io.LimitReader(metadata, examResourceMetadataLimit+1))
	if err != nil || len(raw) > examResourceMetadataLimit {
		return nil, errors.New("metadata is invalid")
	}
	if err = rejectDuplicateTopLevelJSONMembers(raw); err != nil {
		return nil, errors.New("metadata is invalid")
	}

	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err = decoder.Decode(target); err != nil {
		return nil, errors.New("metadata is invalid")
	}

	var trailing any
	if err = decoder.Decode(&trailing); err != io.EOF {
		return nil, errors.New("metadata is invalid")
	}

	content, err := reader.NextPart()
	if err != nil {
		return nil, errors.New("content part is required")
	}

	if content.FormName() != "content" {
		_ = content.Close()
		return nil, errors.New("content must be the second part")
	}

	return &exactMultipartContent{part: content, reader: reader}, nil
}

func rejectDuplicateTopLevelJSONMembers(raw []byte) error {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	token, err := decoder.Token()
	if err != nil || token != json.Delim('{') {
		return errors.New("metadata must be a JSON object")
	}

	seen := make(map[string]struct{})
	for decoder.More() {
		name, tokenErr := decoder.Token()
		if tokenErr != nil {
			return tokenErr
		}

		member, ok := name.(string)
		if !ok {
			return errors.New("metadata member name is invalid")
		}
		if _, exists := seen[member]; exists {
			return errors.New("metadata contains a duplicate member")
		}

		seen[member] = struct{}{}
		var value json.RawMessage
		if tokenErr = decoder.Decode(&value); tokenErr != nil {
			return tokenErr
		}
	}

	if token, err = decoder.Token(); err != nil || token != json.Delim('}') {
		return errors.New("metadata object is incomplete")
	}

	var trailing json.RawMessage
	if err = decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return errors.New("metadata contains trailing data")
	}

	return nil
}

type exactMultipartContent struct {
	part   *multipart.Part
	reader *multipart.Reader
	done   bool
}

func (r *exactMultipartContent) Read(buffer []byte) (int, error) {
	if r.done {
		return 0, io.EOF
	}
	n, err := r.part.Read(buffer)
	if !errors.Is(err, io.EOF) {
		return n, err
	}
	_ = r.part.Close()
	next, nextErr := r.reader.NextPart()
	if nextErr == nil {
		_ = next.Close()
		r.done = true
		return n, errors.New("unexpected multipart part")
	}
	if !errors.Is(nextErr, io.EOF) {
		r.done = true
		return n, nextErr
	}
	r.done = true
	return n, io.EOF
}
