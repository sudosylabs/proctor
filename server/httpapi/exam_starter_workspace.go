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
	"github.com/sudosylabs/proctor/server/httpapi/route"
	"github.com/sudosylabs/proctor/server/model"
	"github.com/sudosylabs/proctor/server/store"
)

const examStarterWorkspaceUploadBodyLimit = model.StarterWorkspaceMaximumFileBytes + 64*1024

type ExamStarterWorkspaceApplication interface {
	ListExamStarterWorkspace(context.Context, invocation.Invocation, model.ExamID) ([]store.ExamStarterWorkspaceItem, error)
	OpenExamStarterWorkspaceFile(
		context.Context,
		invocation.Invocation,
		model.ExamID,
		model.StarterWorkspaceEntryID,
	) (workspace.OpenedFile, error)
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

func examStarterWorkspaceResource(app ExamStarterWorkspaceApplication) route.Resource {
	h := examStarterWorkspaceHandlers{app: app}
	collection := "/exams/{exam_id}/draft/starter-workspace"
	directories := "/exams/{exam_id}/draft/starter-workspace/directories"
	files := "/exams/{exam_id}/draft/starter-workspace/files"
	entry := "/exams/{exam_id}/draft/starter-workspace/entries/{starter_workspace_entry_id}"
	fileContent := "/exams/{exam_id}/draft/starter-workspace/files/{starter_workspace_entry_id}/content"
	return route.NewResource("exam-starter-workspace",
		route.Endpoint{
			Method: http.MethodGet,
			Path:   collection,
			Auth:   route.AuthPrincipalRequired,
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
		route.Endpoint{
			Method:      http.MethodPost,
			Path:        directories,
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
		route.Endpoint{
			Method:       http.MethodPost,
			Path:         files,
			Auth:         route.AuthPrincipalRequired,
			Idempotency:  route.IdempotencyRequired,
			MaxBodyBytes: examStarterWorkspaceUploadBodyLimit,
			ProtocolName: "exam-starter-workspace-file-upload",
			ProtocolKind: route.ProtocolStreamingUpload,
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
		route.Endpoint{
			Method:      http.MethodPatch,
			Path:        entry,
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
		route.Endpoint{
			Method:       http.MethodPut,
			Path:         fileContent,
			Auth:         route.AuthPrincipalRequired,
			Idempotency:  route.IdempotencyRequired,
			MaxBodyBytes: examStarterWorkspaceUploadBodyLimit,
			ProtocolName: "exam-starter-workspace-file-replacement",
			ProtocolKind: route.ProtocolStreamingUpload,
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
		route.Endpoint{
			Method:      http.MethodDelete,
			Path:        entry,
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
		route.Endpoint{
			Method:       http.MethodGet,
			Path:         fileContent,
			Auth:         route.AuthPrincipalRequired,
			ProtocolName: "exam-starter-workspace-protected-content",
			ProtocolKind: route.ProtocolBinaryDownload,
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

func (h examStarterWorkspaceHandlers) list(request route.Request) (route.Result, error) {
	examID, err := examStarterWorkspaceExamID(request)
	if err != nil {
		return route.Result{}, err
	}

	items, err := h.app.ListExamStarterWorkspace(request.Context, request.Invocation(), examID)
	if err != nil {
		return route.Result{}, err
	}

	// The Exam Capacity Policy bounds the Starter Workspace, so the complete
	// set is paged in canonical path order.
	response, err := route.BoundedList(
		request,
		"Starter Workspace",
		items,
		func(item store.ExamStarterWorkspaceItem) route.BoundedKey {
			return route.BoundedKey{Name: item.Entry.Path, ID: item.Entry.ID.String()}
		},
		examStarterWorkspaceListItem,
	)
	if err != nil {
		return route.Result{}, err
	}

	return route.JSON(http.StatusOK, response), nil
}

func (h examStarterWorkspaceHandlers) createDirectory(request route.Request) (route.Result, error) {
	examID, err := examStarterWorkspaceExamID(request)
	if err != nil {
		return route.Result{}, err
	}

	var body createExamStarterWorkspaceDirectoryRequest
	if err = request.DecodeJSON(&body); err != nil {
		return route.Result{}, err
	}

	result, err := h.app.CreateExamStarterWorkspaceDirectory(request.Context, request.Invocation(), workspace.CreateDirectoryCommand{
		ExamID:                examID,
		ExpectedDraftRevision: body.ExpectedDraftRevision,
		Path:                  body.Path,
		IdempotencyKey:        request.IdempotencyKey,
	})
	if err != nil {
		return route.Result{}, err
	}

	return route.JSON(http.StatusCreated, examStarterWorkspaceResultResponse(result)), nil
}

func (h examStarterWorkspaceHandlers) createFile(request route.Request) (route.ProtocolResult, error) {
	examID, err := examStarterWorkspaceExamID(request)
	if err != nil {
		return route.ProtocolResult{}, err
	}

	var metadata examStarterWorkspaceFileUploadMetadata
	content, err := decodeExamResourceMultipart(request.HTTP, &metadata)
	if err != nil {
		return route.ProtocolResult{}, route.InvalidRequest(err)
	}
	if metadata.Size == nil {
		return route.ProtocolResult{}, route.InvalidRequest(errors.New("metadata size is required"))
	}

	result, err := h.app.CreateExamStarterWorkspaceFile(request.Context, request.Invocation(), workspace.CreateFileCommand{
		ExamID:                examID,
		ExpectedDraftRevision: metadata.ExpectedDraftRevision,
		Path:                  metadata.Path,
		MediaType:             metadata.MediaType,
		ExpectedSHA256:        metadata.SHA256,
		Body:                  content,
		Size:                  *metadata.Size,
		IdempotencyKey:        request.IdempotencyKey,
	})
	if err != nil {
		return route.ProtocolResult{}, err
	}

	return route.StreamingUpload(http.StatusCreated, examStarterWorkspaceResultResponse(result)), nil
}

func (h examStarterWorkspaceHandlers) moveEntry(request route.Request) (route.Result, error) {
	examID, entryID, err := examStarterWorkspaceIDs(request)
	if err != nil {
		return route.Result{}, err
	}

	var body moveExamStarterWorkspaceEntryRequest
	if err = request.DecodeJSON(&body); err != nil {
		return route.Result{}, err
	}

	result, err := h.app.MoveExamStarterWorkspaceEntry(request.Context, request.Invocation(), workspace.MoveEntryCommand{
		ExamID:                examID,
		EntryID:               entryID,
		ExpectedDraftRevision: body.ExpectedDraftRevision,
		Path:                  body.Path,
		IdempotencyKey:        request.IdempotencyKey,
	})
	if err != nil {
		return route.Result{}, err
	}

	return route.JSON(http.StatusOK, examStarterWorkspaceResultResponse(result)), nil
}

func (h examStarterWorkspaceHandlers) replaceFile(request route.Request) (route.ProtocolResult, error) {
	examID, entryID, err := examStarterWorkspaceIDs(request)
	if err != nil {
		return route.ProtocolResult{}, err
	}

	var metadata examStarterWorkspaceFileReplacementMetadata
	content, err := decodeExamResourceMultipart(request.HTTP, &metadata)
	if err != nil {
		return route.ProtocolResult{}, route.InvalidRequest(err)
	}
	if metadata.Size == nil {
		return route.ProtocolResult{}, route.InvalidRequest(errors.New("metadata size is required"))
	}

	expectedContentVersion, err := model.ParseWorkspaceContentVersion(metadata.ExpectedContentVersion)
	if err != nil {
		return route.ProtocolResult{}, route.InvalidRequest(err)
	}

	result, err := h.app.ReplaceExamStarterWorkspaceFile(request.Context, request.Invocation(), workspace.ReplaceFileCommand{
		ExamID:                 examID,
		EntryID:                entryID,
		ExpectedDraftRevision:  metadata.ExpectedDraftRevision,
		ExpectedContentVersion: expectedContentVersion,
		MediaType:              metadata.MediaType,
		ExpectedSHA256:         metadata.SHA256,
		Body:                   content,
		Size:                   *metadata.Size,
		IdempotencyKey:         request.IdempotencyKey,
	})
	if err != nil {
		return route.ProtocolResult{}, err
	}

	return route.StreamingUpload(http.StatusOK, examStarterWorkspaceResultResponse(result)), nil
}

func (h examStarterWorkspaceHandlers) removeEntry(request route.Request) (route.Result, error) {
	examID, entryID, err := examStarterWorkspaceIDs(request)
	if err != nil {
		return route.Result{}, err
	}

	var body removeExamStarterWorkspaceEntryRequest
	if err = request.DecodeJSON(&body); err != nil {
		return route.Result{}, err
	}

	_, err = h.app.RemoveExamStarterWorkspaceEntry(request.Context, request.Invocation(), workspace.RemoveEntryCommand{
		ExamID:                examID,
		EntryID:               entryID,
		ExpectedDraftRevision: body.ExpectedDraftRevision,
		Recursive:             body.Recursive,
		IdempotencyKey:        request.IdempotencyKey,
	})
	if err != nil {
		return route.Result{}, err
	}

	return route.NoContent(), nil
}

func (h examStarterWorkspaceHandlers) openFile(request route.Request) (route.ProtocolResult, error) {
	examID, entryID, err := examStarterWorkspaceIDs(request)
	if err != nil {
		return route.ProtocolResult{}, err
	}

	opened, err := h.app.OpenExamStarterWorkspaceFile(request.Context, request.Invocation(), examID, entryID)
	if err != nil {
		return route.ProtocolResult{}, err
	}
	if opened.Body == nil {
		return route.ProtocolResult{}, errors.New("Starter Workspace application returned incomplete content")
	}

	etag := `"` + opened.SHA256 + `"`
	headers := http.Header{"Content-Type": {opened.MediaType}, "Cache-Control": {"private, no-store"}, "ETag": {etag}}
	untrustedWorkspaceContentHeaders(headers)
	if request.HTTP.Header.Get("If-None-Match") == etag {
		_ = opened.Body.Close()
		return route.NotModified(opened.SizeBytes).WithHeaders(headers), nil
	}

	return route.BinaryDownload(opened.Body, opened.SizeBytes).WithHeaders(headers), nil
}

func examStarterWorkspaceExamID(request route.Request) (model.ExamID, error) {
	examID, err := route.PathID[model.ExamID](request, "exam_id")
	if err != nil {
		return "", err
	}
	return examID, nil
}

func examStarterWorkspaceIDs(request route.Request) (model.ExamID, model.StarterWorkspaceEntryID, error) {
	examID, err := examStarterWorkspaceExamID(request)
	if err != nil {
		return "", "", err
	}
	entryID, err := route.PathID[model.StarterWorkspaceEntryID](request, "starter_workspace_entry_id")
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
