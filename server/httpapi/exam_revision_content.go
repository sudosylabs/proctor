// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package httpapi

import (
	"errors"
	"fmt"
	"net/http"

	"github.com/sudosylabs/proctor/server/httpapi/route"
	"github.com/sudosylabs/proctor/server/model"
)

// examRevisionContentResponse is what a Revision froze besides its files,
// shaped like the Draft so a client can compare the two.
type examRevisionContentResponse struct {
	RevisionID           string                `json:"revision_id"`
	ExamID               string                `json:"exam_id"`
	Number               int64                 `json:"number"`
	Title                string                `json:"title"`
	InstructionsMarkdown string                `json:"instructions_markdown"`
	Policy               examPolicyResponse    `json:"policy"`
	BrowserPolicy        browserPolicyDocument `json:"browser_policy"`
}

type examRevisionResourceResponse struct {
	ID                  string `json:"id"`
	DisplayName         string `json:"display_name"`
	DescriptionMarkdown string `json:"description_markdown"`
	Position            int    `json:"position"`
	ContentRevisionID   string `json:"content_revision_id"`
	MediaType           string `json:"media_type"`
	Size                int64  `json:"size"`
	SHA256              string `json:"sha256"`
}

type examRevisionStarterEntryResponse struct {
	ID             string `json:"id"`
	Kind           string `json:"kind"`
	Path           string `json:"path"`
	ContentVersion string `json:"content_version,omitempty"`
	MediaType      string `json:"media_type,omitempty"`
	Size           *int64 `json:"size,omitempty"`
	SHA256         string `json:"sha256,omitempty"`
}

// examRevisionContentEndpoints are the read-only views of a Revision's
// content. They mirror the Draft reads and need the same permission.
func (h examRevisionHandlers) contentEndpoints() []route.Endpoint {
	member := "/exams/{exam_id}/revisions/{exam_revision_id}"
	errorsFor := func(extra ...string) []string {
		return append([]string{
			"authorization.request.invalid",
			"authorization.unavailable",
			"request.invalid",
			"resource.not_found",
			"exam.invalid",
			"exam.unavailable",
		}, extra...)
	}
	return []route.Endpoint{
		{
			Method: http.MethodGet,
			Path:   member + "/content",
			Auth:   route.AuthPrincipalRequired,
			Errors: errorsFor(),
			Handle: h.getContent,
		},
		{
			Method: http.MethodGet,
			Path:   member + "/resources",
			Auth:   route.AuthPrincipalRequired,
			Errors: errorsFor(),
			Handle: h.listResources,
		},
		{
			Method:       http.MethodGet,
			Path:         member + "/resources/{exam_resource_id}/content",
			Auth:         route.AuthPrincipalRequired,
			ProtocolName: "exam-revision-resource-protected-content",
			ProtocolKind: route.ProtocolBinaryDownload,
			Errors:       errorsFor(),
			Protocol:     h.openResource,
		},
		{
			Method: http.MethodGet,
			Path:   member + "/starter-workspace",
			Auth:   route.AuthPrincipalRequired,
			Errors: errorsFor(),
			Handle: h.listStarterWorkspace,
		},
		{
			Method:       http.MethodGet,
			Path:         member + "/starter-workspace/files/{starter_workspace_entry_id}/content",
			Auth:         route.AuthPrincipalRequired,
			ProtocolName: "exam-revision-starter-workspace-protected-content",
			ProtocolKind: route.ProtocolBinaryDownload,
			Errors:       errorsFor(),
			Protocol:     h.openStarterFile,
		},
	}
}

func (h examRevisionHandlers) getContent(request route.Request) (route.Result, error) {
	examID, revisionID, err := examRevisionIDs(request)
	if err != nil {
		return route.Result{}, err
	}

	text, err := h.app.GetExamRevisionText(request.Context, request.Invocation(), examID, revisionID)
	if err != nil {
		return route.Result{}, err
	}

	return route.JSON(http.StatusOK, examRevisionContentResponse{
		RevisionID:           text.RevisionID.String(),
		ExamID:               text.ExamID.String(),
		Number:               text.Number,
		Title:                text.Title,
		InstructionsMarkdown: text.InstructionsMarkdown,
		Policy:               examPolicyResponseFromModel(text.Policy),
		BrowserPolicy:        browserPolicyDocumentFromModel(text.BrowserPolicy),
	}), nil
}

func (h examRevisionHandlers) listResources(request route.Request) (route.Result, error) {
	examID, revisionID, err := examRevisionIDs(request)
	if err != nil {
		return route.Result{}, err
	}

	items, err := h.app.ListExamRevisionResources(request.Context, request.Invocation(), examID, revisionID)
	if err != nil {
		return route.Result{}, err
	}

	// The capacity policy the Revision froze bounds its Resources, so the
	// whole ordered set is paged by position, like the Draft's.
	response, err := route.BoundedList(
		request,
		"Exam Revision Resource",
		items,
		func(item model.ExamRevisionResource) route.BoundedKey {
			return route.BoundedKey{Name: fmt.Sprintf("%08d", item.Position), ID: item.ResourceID.String()}
		},
		func(item model.ExamRevisionResource) examRevisionResourceResponse {
			return examRevisionResourceResponse{
				ID:                  item.ResourceID.String(),
				DisplayName:         item.DisplayName,
				DescriptionMarkdown: item.DescriptionMarkdown,
				Position:            item.Position,
				ContentRevisionID:   item.FileRevisionID.String(),
				MediaType:           string(item.MediaType),
				Size:                item.SizeBytes,
				SHA256:              item.SHA256,
			}
		},
	)
	if err != nil {
		return route.Result{}, err
	}

	return route.JSON(http.StatusOK, response), nil
}

func (h examRevisionHandlers) openResource(request route.Request) (route.ProtocolResult, error) {
	examID, revisionID, err := examRevisionIDs(request)
	if err != nil {
		return route.ProtocolResult{}, err
	}
	resourceID, err := route.PathID[model.ExamResourceID](request, "exam_resource_id")
	if err != nil {
		return route.ProtocolResult{}, err
	}

	opened, err := h.app.OpenExamRevisionResource(request.Context, request.Invocation(), examID, revisionID, resourceID)
	if err != nil {
		return route.ProtocolResult{}, err
	}
	if opened.Body == nil {
		return route.ProtocolResult{}, errors.New("Exam Revision application returned incomplete Resource content")
	}

	// Frozen bytes never change, so the digest is a stable validator.
	etag := `"` + opened.Resource.SHA256 + `"`
	headers := http.Header{"Content-Type": {string(opened.Resource.MediaType)}, "Cache-Control": {"private, max-age=300"}, "ETag": {etag}}
	if request.HTTP.Header.Get("If-None-Match") == etag {
		_ = opened.Body.Close()
		return route.NotModified(opened.Resource.SizeBytes).WithHeaders(headers), nil
	}

	return route.BinaryDownload(opened.Body, opened.Resource.SizeBytes).WithHeaders(headers), nil
}

func (h examRevisionHandlers) listStarterWorkspace(request route.Request) (route.Result, error) {
	examID, revisionID, err := examRevisionIDs(request)
	if err != nil {
		return route.Result{}, err
	}

	items, err := h.app.ListExamRevisionStarterWorkspace(request.Context, request.Invocation(), examID, revisionID)
	if err != nil {
		return route.Result{}, err
	}

	response, err := route.BoundedList(
		request,
		"Exam Revision Starter Workspace",
		items,
		func(item model.ExamRevisionStarterWorkspaceEntry) route.BoundedKey {
			return route.BoundedKey{Name: item.Path, ID: item.EntryID.String()}
		},
		func(item model.ExamRevisionStarterWorkspaceEntry) examRevisionStarterEntryResponse {
			entry := examRevisionStarterEntryResponse{ID: item.EntryID.String(), Kind: string(item.Kind), Path: item.Path}
			if item.Kind == model.StarterWorkspaceEntryFile {
				size := item.SizeBytes
				entry.ContentVersion, entry.MediaType, entry.Size, entry.SHA256 = string(item.ContentVersion), item.MediaType, &size, item.SHA256
			}
			return entry
		},
	)
	if err != nil {
		return route.Result{}, err
	}

	return route.JSON(http.StatusOK, response), nil
}

func (h examRevisionHandlers) openStarterFile(request route.Request) (route.ProtocolResult, error) {
	examID, revisionID, err := examRevisionIDs(request)
	if err != nil {
		return route.ProtocolResult{}, err
	}
	entryID, err := route.PathID[model.StarterWorkspaceEntryID](request, "starter_workspace_entry_id")
	if err != nil {
		return route.ProtocolResult{}, err
	}

	opened, err := h.app.OpenExamRevisionStarterFile(request.Context, request.Invocation(), examID, revisionID, entryID)
	if err != nil {
		return route.ProtocolResult{}, err
	}
	if opened.Body == nil {
		return route.ProtocolResult{}, errors.New("Exam Revision application returned incomplete Starter Workspace content")
	}

	etag := `"` + opened.Entry.SHA256 + `"`
	headers := http.Header{"Content-Type": {opened.Entry.MediaType}, "Cache-Control": {"private, no-store"}, "ETag": {etag}}
	untrustedWorkspaceContentHeaders(headers)
	if request.HTTP.Header.Get("If-None-Match") == etag {
		_ = opened.Body.Close()
		return route.NotModified(opened.Entry.SizeBytes).WithHeaders(headers), nil
	}

	return route.BinaryDownload(opened.Body, opened.Entry.SizeBytes).WithHeaders(headers), nil
}
