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

	"github.com/sudosylabs/proctor/server/app/exam/workspace"
	"github.com/sudosylabs/proctor/server/app/invocation"
	"github.com/sudosylabs/proctor/server/model"
	"github.com/sudosylabs/proctor/server/store"
)

const examStarterWorkspaceUploadBodyLimit = model.StarterWorkspaceMaximumFileBytes + 64*1024

type ExamStarterWorkspaceApplication interface {
	ListExamStarterWorkspace(context.Context, invocation.Invocation, workspace.ListQuery) ([]store.ExamStarterWorkspaceItem, error)
	OpenExamStarterWorkspaceFile(context.Context, invocation.Invocation, workspace.OpenFileQuery) (workspace.OpenedFile, error)
	CreateExamStarterWorkspaceDirectory(context.Context, invocation.Invocation, workspace.CreateDirectoryCommand) (workspace.Result, error)
	CreateExamStarterWorkspaceFile(context.Context, invocation.Invocation, workspace.CreateFileCommand) (workspace.Result, error)
	MoveExamStarterWorkspaceEntry(context.Context, invocation.Invocation, workspace.MoveEntryCommand) (workspace.Result, error)
	ReplaceExamStarterWorkspaceFile(context.Context, invocation.Invocation, workspace.ReplaceFileCommand) (workspace.Result, error)
	RemoveExamStarterWorkspaceEntry(context.Context, invocation.Invocation, workspace.RemoveEntryCommand) (workspace.Result, error)
}

type examStarterWorkspaceHandlers struct {
	app ExamStarterWorkspaceApplication
}

type createExamStarterWorkspaceDirectoryRequest struct {
	ExpectedDraftRevision int64  `json:"expected_revision"`
	Path                  string `json:"path"`
}
type examStarterWorkspaceFileUploadMetadata struct {
	ExpectedDraftRevision int64  `json:"expected_revision"`
	Path                  string `json:"path"`
	MediaType             string `json:"media_type"`
	Size                  *int64 `json:"size"`
	SHA256                string `json:"sha256"`
}
type examStarterWorkspaceFileReplacementMetadata struct {
	ExpectedDraftRevision  int64  `json:"expected_revision"`
	ExpectedContentVersion string `json:"expected_content_version"`
	MediaType              string `json:"media_type"`
	Size                   *int64 `json:"size"`
	SHA256                 string `json:"sha256"`
}
type moveExamStarterWorkspaceEntryRequest struct {
	ExpectedDraftRevision int64  `json:"expected_revision"`
	Path                  string `json:"path"`
}
type removeExamStarterWorkspaceEntryRequest struct {
	ExpectedDraftRevision int64 `json:"expected_revision"`
	Recursive             bool  `json:"recursive,omitempty"`
}
type examStarterWorkspaceEntryResponse struct {
	ID             string `json:"id"`
	Kind           string `json:"kind"`
	Path           string `json:"path"`
	ContentVersion string `json:"content_version,omitempty"`
	MediaType      string `json:"media_type,omitempty"`
	Size           *int64 `json:"size,omitempty"`
	SHA256         string `json:"sha256,omitempty"`
	CreatedAt      string `json:"created_at"`
	UpdatedAt      string `json:"updated_at"`
	DraftRevision  int64  `json:"draft_revision,omitempty"`
}
type examStarterWorkspaceListItemResponse struct {
	ID             string `json:"id"`
	Kind           string `json:"kind"`
	Path           string `json:"path"`
	ContentVersion string `json:"content_version,omitempty"`
	MediaType      string `json:"media_type,omitempty"`
	Size           *int64 `json:"size,omitempty"`
	SHA256         string `json:"sha256,omitempty"`
	CreatedAt      string `json:"created_at"`
	UpdatedAt      string `json:"updated_at"`
}

func examStarterWorkspaceResource(app ExamStarterWorkspaceApplication) apiResource {
	h := examStarterWorkspaceHandlers{app: app}
	collection := "/exams/{exam_id}/draft/starter-workspace"
	directories := "/exams/{exam_id}/draft/starter-workspace/directories"
	files := "/exams/{exam_id}/draft/starter-workspace/files"
	entry := "/exams/{exam_id}/draft/starter-workspace/entries/{starter_workspace_entry_id}"
	fileContent := "/exams/{exam_id}/draft/starter-workspace/files/{starter_workspace_entry_id}/content"
	return newResource("exam-starter-workspace",
		endpoint{
			Method: http.MethodGet,
			Path:   collection,
			Auth:   AuthPrincipalRequired,
			Errors: []string{
				"authorization.request.invalid",
				"authorization.unavailable",
				"request.invalid",
				"resource.not_found",
				"exam.starter_workspace.invalid",
				"exam.starter_workspace.unavailable",
			},
			Handle: h.list,
		},
		endpoint{
			Method:      http.MethodPost,
			Path:        directories,
			Auth:        AuthPrincipalRequired,
			Idempotency: IdempotencyRequired,
			Errors: []string{
				"authorization.request.invalid",
				"authorization.unavailable",
				"audit.unavailable",
				"request.invalid",
				"resource.not_found",
				"exam.archived",
				"exam.draft.revision_conflict",
				"exam.starter_workspace.invalid",
				"exam.starter_workspace.conflict",
				"exam.starter_workspace.unavailable",
				"idempotency.key_required",
				"idempotency.key_invalid",
				"idempotency.conflict",
				"idempotency.in_progress",
				"exam.starter_workspace.path_conflict",
				"exam.starter_workspace.parent_not_found",
				"exam.starter_workspace.entry_limit",
			},
			Handle: h.createDirectory,
		},
		endpoint{
			Method:       http.MethodPost,
			Path:         files,
			Auth:         AuthPrincipalRequired,
			Idempotency:  IdempotencyRequired,
			MaxBodyBytes: examStarterWorkspaceUploadBodyLimit,
			ProtocolName: "exam-starter-workspace-file-upload",
			ProtocolKind: RouteProtocolStreamingUpload,
			Errors: []string{
				"authorization.request.invalid",
				"authorization.unavailable",
				"audit.unavailable",
				"request.invalid",
				"resource.not_found",
				"exam.archived",
				"exam.draft.revision_conflict",
				"exam.starter_workspace.invalid",
				"exam.starter_workspace.conflict",
				"exam.starter_workspace.unavailable",
				"idempotency.key_required",
				"idempotency.key_invalid",
				"idempotency.conflict",
				"idempotency.in_progress",
				"exam.starter_workspace.path_conflict",
				"exam.starter_workspace.parent_not_found",
				"exam.starter_workspace.entry_limit",
				"exam.starter_workspace.total_size_limit",
				"exam.starter_workspace.upload_expired",
				"exam.starter_workspace.object_conflict",
			},
			Protocol: h.createFile,
		},
		endpoint{
			Method:      http.MethodPatch,
			Path:        entry,
			Auth:        AuthPrincipalRequired,
			Idempotency: IdempotencyRequired,
			Errors: []string{
				"authorization.request.invalid",
				"authorization.unavailable",
				"audit.unavailable",
				"request.invalid",
				"resource.not_found",
				"exam.archived",
				"exam.draft.revision_conflict",
				"exam.starter_workspace.invalid",
				"exam.starter_workspace.conflict",
				"exam.starter_workspace.unavailable",
				"idempotency.key_required",
				"idempotency.key_invalid",
				"idempotency.conflict",
				"idempotency.in_progress",
				"exam.starter_workspace.path_conflict",
				"exam.starter_workspace.parent_not_found",
				"exam.starter_workspace.no_changes",
				"exam.starter_workspace.move_conflict",
			},
			Handle: h.moveEntry,
		},
		endpoint{
			Method:       http.MethodPut,
			Path:         fileContent,
			Auth:         AuthPrincipalRequired,
			Idempotency:  IdempotencyRequired,
			MaxBodyBytes: examStarterWorkspaceUploadBodyLimit,
			ProtocolName: "exam-starter-workspace-file-replacement",
			ProtocolKind: RouteProtocolStreamingUpload,
			Errors: []string{
				"authorization.request.invalid",
				"authorization.unavailable",
				"audit.unavailable",
				"request.invalid",
				"resource.not_found",
				"exam.archived",
				"exam.draft.revision_conflict",
				"exam.starter_workspace.invalid",
				"exam.starter_workspace.conflict",
				"exam.starter_workspace.unavailable",
				"idempotency.key_required",
				"idempotency.key_invalid",
				"idempotency.conflict",
				"idempotency.in_progress",
				"exam.starter_workspace.total_size_limit",
				"exam.starter_workspace.upload_expired",
				"exam.starter_workspace.object_conflict",
				"exam.starter_workspace.content_conflict",
				"exam.starter_workspace.entry_kind",
			},
			Protocol: h.replaceFile,
		},
		endpoint{
			Method:      http.MethodDelete,
			Path:        entry,
			Auth:        AuthPrincipalRequired,
			Idempotency: IdempotencyRequired,
			Errors: []string{
				"authorization.request.invalid",
				"authorization.unavailable",
				"audit.unavailable",
				"request.invalid",
				"resource.not_found",
				"exam.archived",
				"exam.draft.revision_conflict",
				"exam.starter_workspace.invalid",
				"exam.starter_workspace.conflict",
				"exam.starter_workspace.unavailable",
				"idempotency.key_required",
				"idempotency.key_invalid",
				"idempotency.conflict",
				"idempotency.in_progress",
				"exam.starter_workspace.directory_not_empty",
			},
			Handle: h.removeEntry,
		},
		endpoint{
			Method:       http.MethodGet,
			Path:         fileContent,
			Auth:         AuthPrincipalRequired,
			ProtocolName: "exam-starter-workspace-protected-content",
			ProtocolKind: RouteProtocolBinaryDownload,
			Errors: []string{
				"authorization.request.invalid",
				"authorization.unavailable",
				"request.invalid",
				"resource.not_found",
				"exam.starter_workspace.invalid",
				"exam.starter_workspace.unavailable",
			},
			Protocol: h.openFile,
		},
	)
}

func (h examStarterWorkspaceHandlers) list(request operationRequest) (operationResult, error) {
	examID, err := examStarterWorkspaceExamID(request)
	if err != nil {
		return operationResult{}, err
	}

	items, err := h.app.ListExamStarterWorkspace(request.context, request.invocation(), workspace.ListQuery{ExamID: examID})
	if err != nil {
		return operationResult{}, err
	}

	// The Exam Capacity Policy bounds the Starter Workspace, so the complete
	// set is paged in canonical path order.
	response, err := boundedListResult(
		request,
		"Starter Workspace",
		items,
		func(item store.ExamStarterWorkspaceItem) boundedPageKey {
			return boundedPageKey{Name: item.Entry.Path, ID: item.Entry.ID.String()}
		},
		examStarterWorkspaceListItem,
	)
	if err != nil {
		return operationResult{}, err
	}

	return jsonResult(http.StatusOK, response), nil
}

func (h examStarterWorkspaceHandlers) createDirectory(request operationRequest) (operationResult, error) {
	examID, err := examStarterWorkspaceExamID(request)
	if err != nil {
		return operationResult{}, err
	}

	var body createExamStarterWorkspaceDirectoryRequest
	if err = request.decodeJSON(&body); err != nil {
		return operationResult{}, err
	}

	result, err := h.app.CreateExamStarterWorkspaceDirectory(request.context, request.invocation(), workspace.CreateDirectoryCommand{
		ExamID:                examID,
		ExpectedDraftRevision: body.ExpectedDraftRevision,
		Path:                  body.Path,
		IdempotencyKey:        request.idempotencyKey,
	})
	if err != nil {
		return operationResult{}, err
	}

	return jsonResult(http.StatusCreated, examStarterWorkspaceResultResponse(result)), nil
}

func (h examStarterWorkspaceHandlers) createFile(request operationRequest) (protocolResult, error) {
	examID, err := examStarterWorkspaceExamID(request)
	if err != nil {
		return protocolResult{}, err
	}

	var metadata examStarterWorkspaceFileUploadMetadata
	content, err := decodeExamResourceMultipart(request.http, &metadata)
	if err != nil {
		return protocolResult{}, invalidRequestError(err)
	}
	if metadata.Size == nil {
		return protocolResult{}, invalidRequestError(errors.New("metadata size is required"))
	}

	result, err := h.app.CreateExamStarterWorkspaceFile(request.context, request.invocation(), workspace.CreateFileCommand{
		ExamID:                examID,
		ExpectedDraftRevision: metadata.ExpectedDraftRevision,
		Path:                  metadata.Path,
		MediaType:             metadata.MediaType,
		ExpectedSHA256:        metadata.SHA256,
		Body:                  content,
		Size:                  *metadata.Size,
		IdempotencyKey:        request.idempotencyKey,
	})
	if err != nil {
		return protocolResult{}, err
	}

	return streamingUploadProtocolResult(http.StatusCreated, examStarterWorkspaceResultResponse(result)), nil
}

func (h examStarterWorkspaceHandlers) moveEntry(request operationRequest) (operationResult, error) {
	examID, entryID, err := examStarterWorkspaceIDs(request)
	if err != nil {
		return operationResult{}, err
	}

	var body moveExamStarterWorkspaceEntryRequest
	if err = request.decodeJSON(&body); err != nil {
		return operationResult{}, err
	}

	result, err := h.app.MoveExamStarterWorkspaceEntry(request.context, request.invocation(), workspace.MoveEntryCommand{
		ExamID:                examID,
		EntryID:               entryID,
		ExpectedDraftRevision: body.ExpectedDraftRevision,
		Path:                  body.Path,
		IdempotencyKey:        request.idempotencyKey,
	})
	if err != nil {
		return operationResult{}, err
	}

	return jsonResult(http.StatusOK, examStarterWorkspaceResultResponse(result)), nil
}

func (h examStarterWorkspaceHandlers) replaceFile(request operationRequest) (protocolResult, error) {
	examID, entryID, err := examStarterWorkspaceIDs(request)
	if err != nil {
		return protocolResult{}, err
	}

	var metadata examStarterWorkspaceFileReplacementMetadata
	content, err := decodeExamResourceMultipart(request.http, &metadata)
	if err != nil {
		return protocolResult{}, invalidRequestError(err)
	}
	if metadata.Size == nil {
		return protocolResult{}, invalidRequestError(errors.New("metadata size is required"))
	}

	expectedContentVersion, err := model.ParseWorkspaceContentVersion(metadata.ExpectedContentVersion)
	if err != nil {
		return protocolResult{}, invalidRequestError(err)
	}

	result, err := h.app.ReplaceExamStarterWorkspaceFile(request.context, request.invocation(), workspace.ReplaceFileCommand{
		ExamID:                 examID,
		EntryID:                entryID,
		ExpectedDraftRevision:  metadata.ExpectedDraftRevision,
		ExpectedContentVersion: expectedContentVersion,
		MediaType:              metadata.MediaType,
		ExpectedSHA256:         metadata.SHA256,
		Body:                   content,
		Size:                   *metadata.Size,
		IdempotencyKey:         request.idempotencyKey,
	})
	if err != nil {
		return protocolResult{}, err
	}

	return streamingUploadProtocolResult(http.StatusOK, examStarterWorkspaceResultResponse(result)), nil
}

func (h examStarterWorkspaceHandlers) removeEntry(request operationRequest) (operationResult, error) {
	examID, entryID, err := examStarterWorkspaceIDs(request)
	if err != nil {
		return operationResult{}, err
	}

	var body removeExamStarterWorkspaceEntryRequest
	if err = request.decodeJSON(&body); err != nil {
		return operationResult{}, err
	}

	_, err = h.app.RemoveExamStarterWorkspaceEntry(request.context, request.invocation(), workspace.RemoveEntryCommand{
		ExamID:                examID,
		EntryID:               entryID,
		ExpectedDraftRevision: body.ExpectedDraftRevision,
		Recursive:             body.Recursive,
		IdempotencyKey:        request.idempotencyKey,
	})
	if err != nil {
		return operationResult{}, err
	}

	return noContentResult(), nil
}

func (h examStarterWorkspaceHandlers) openFile(request operationRequest) (protocolResult, error) {
	examID, entryID, err := examStarterWorkspaceIDs(request)
	if err != nil {
		return protocolResult{}, err
	}

	opened, err := h.app.OpenExamStarterWorkspaceFile(
		request.context,
		request.invocation(),
		workspace.OpenFileQuery{ExamID: examID, EntryID: entryID},
	)
	if err != nil {
		return protocolResult{}, err
	}
	if opened.Body == nil {
		return protocolResult{}, errors.New("Starter Workspace application returned incomplete content")
	}

	etag := `"` + opened.SHA256 + `"`
	headers := http.Header{"Content-Type": {opened.MediaType}, "Cache-Control": {"private, no-store"}, "ETag": {etag}}
	untrustedWorkspaceContentHeaders(headers)
	if request.http.Header.Get("If-None-Match") == etag {
		_ = opened.Body.Close()
		return notModifiedProtocolResult(opened.SizeBytes).withHeaders(headers), nil
	}

	return binaryDownloadProtocolResult(opened.Body, opened.SizeBytes).withHeaders(headers), nil
}

func examStarterWorkspaceExamID(request operationRequest) (model.ExamID, error) {
	examID, err := pathID[model.ExamID](request, "exam_id")
	if err != nil {
		return "", err
	}
	return examID, nil
}

func examStarterWorkspaceIDs(request operationRequest) (model.ExamID, model.StarterWorkspaceEntryID, error) {
	examID, err := examStarterWorkspaceExamID(request)
	if err != nil {
		return "", "", err
	}
	entryID, err := pathID[model.StarterWorkspaceEntryID](request, "starter_workspace_entry_id")
	if err != nil {
		return "", "", err
	}
	return examID, entryID, nil
}

func examStarterWorkspaceResultResponse(result workspace.Result) examStarterWorkspaceEntryResponse {
	item := examStarterWorkspaceListItem(store.ExamStarterWorkspaceItem{Entry: result.Entry, Object: result.Object})
	return examStarterWorkspaceEntryResponse{
		ID:             item.ID,
		Kind:           item.Kind,
		Path:           item.Path,
		ContentVersion: item.ContentVersion,
		MediaType:      item.MediaType,
		Size:           item.Size,
		SHA256:         item.SHA256,
		CreatedAt:      item.CreatedAt,
		UpdatedAt:      item.UpdatedAt,
		DraftRevision:  result.DraftRevision,
	}
}

func examStarterWorkspaceListItem(item store.ExamStarterWorkspaceItem) examStarterWorkspaceListItemResponse {
	entry := item.Entry
	response := examStarterWorkspaceListItemResponse{
		ID:        entry.ID.String(),
		Kind:      string(entry.Kind),
		Path:      entry.Path,
		CreatedAt: model.FormatRFC3339(entry.CreatedAt),
		UpdatedAt: model.FormatRFC3339(entry.UpdatedAt),
	}
	if item.Object != nil {
		size := item.Object.SizeBytes
		response.ContentVersion = item.Object.ContentVersion.String()
		response.MediaType = item.Object.MediaType
		response.Size = &size
		response.SHA256 = item.Object.SHA256
	}

	return response
}
