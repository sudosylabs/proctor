// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package httpapi

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
	"strconv"
	"strings"

	"github.com/sudosylabs/proctor/server/app/exam/attempt"
	"github.com/sudosylabs/proctor/server/httpapi/route"
	"github.com/sudosylabs/proctor/server/model"
)

type candidateWorkspaceCursor struct {
	ExpectedCursor int64
	ID             model.AttemptWorkspaceEntryID
}

type candidateWorkspaceCursorWire struct {
	ExpectedCursor int64  `json:"workspace_cursor"`
	ID             string `json:"after_entry_id,omitempty"`
}

func encodeCandidateWorkspaceCursor(cursor candidateWorkspaceCursor) (string, error) {
	wire := candidateWorkspaceCursorWire{
		ExpectedCursor: cursor.ExpectedCursor,
		ID:             cursor.ID.String(),
	}
	return route.EncodeCursor(wire, candidateWorkspaceCursorSpec())
}

func decodeCandidateWorkspaceCursor(raw string) (candidateWorkspaceCursor, error) {
	wire, err := route.DecodeCursor(raw, candidateWorkspaceCursorSpec())
	if err != nil {
		return candidateWorkspaceCursor{}, err
	}
	var id model.AttemptWorkspaceEntryID
	if wire.ID != "" {
		id, _ = model.ParseAttemptWorkspaceEntryID(wire.ID)
	}
	return candidateWorkspaceCursor{ExpectedCursor: wire.ExpectedCursor, ID: id}, nil
}

func candidateWorkspaceCursorSpec() route.CursorSpec[candidateWorkspaceCursorWire] {
	return route.CursorSpec[candidateWorkspaceCursorWire]{
		Label:                "candidate Workspace",
		MaximumEncodedLength: route.DefaultMaximumCursorLength,
		Members:              []string{"workspace_cursor", "after_entry_id"},
		Validate: func(cursor candidateWorkspaceCursorWire) error {
			if cursor.ExpectedCursor < 0 || cursor.ID != "" && !model.AttemptWorkspaceEntryID(cursor.ID).IsValid() {
				return errors.New("invalid candidate Workspace keyset")
			}
			return nil
		},
	}
}

type candidateExamWorkspaceItemResponse struct {
	ID             string `json:"id"`
	Kind           string `json:"kind"`
	Path           string `json:"path"`
	ContentVersion string `json:"content_version,omitempty"`
	MediaType      string `json:"media_type,omitempty"`
	Size           *int64 `json:"size,omitempty"`
	SHA256         string `json:"sha256,omitempty"`
}

type candidateExamWorkspaceListResponse struct {
	WorkspaceID     string                               `json:"workspace_id"`
	WorkspaceCursor int64                                `json:"workspace_cursor"`
	Items           []candidateExamWorkspaceItemResponse `json:"items"`
	NextCursor      string                               `json:"next_cursor,omitempty"`
	RefreshRequired bool                                 `json:"refresh_required"`
}

type candidateWorkspaceMutationAccessRequest struct {
	ParticipationID string `json:"participation_id"`
	Generation      int64  `json:"generation"`
}

type createCandidateWorkspaceDirectoryRequest struct {
	ParticipationID string `json:"participation_id"`
	Generation      int64  `json:"generation"`
	Path            string `json:"path"`
}

type candidateWorkspaceFileUploadMetadata struct {
	ParticipationID string `json:"participation_id"`
	Generation      int64  `json:"generation"`
	Path            string `json:"path"`
	MediaType       string `json:"media_type"`
	Size            *int64 `json:"size"`
	SHA256          string `json:"sha256"`
}

type replaceCandidateWorkspaceFileMetadata struct {
	ParticipationID        string `json:"participation_id"`
	Generation             int64  `json:"generation"`
	ExpectedPath           string `json:"expected_path"`
	ExpectedContentVersion string `json:"expected_content_version"`
	MediaType              string `json:"media_type"`
	Size                   *int64 `json:"size"`
	SHA256                 string `json:"sha256"`
}

type moveCandidateWorkspaceEntryRequest struct {
	ParticipationID string `json:"participation_id"`
	Generation      int64  `json:"generation"`
	ExpectedPath    string `json:"expected_path"`
	DestinationPath string `json:"destination_path"`
}

type deleteCandidateWorkspaceEntryRequest struct {
	ParticipationID         string                `json:"participation_id"`
	Generation              int64                 `json:"generation"`
	ExpectedPath            string                `json:"expected_path"`
	ExpectedContentVersion  string                `json:"expected_content_version,omitempty"`
	Recursive               bool                  `json:"recursive,omitempty"`
	ExpectedWorkspaceCursor route.Optional[int64] `json:"expected_workspace_cursor,omitempty"`
}

type candidateWorkspaceMutationResponse struct {
	WorkspaceID     string                              `json:"workspace_id"`
	WorkspaceCursor int64                               `json:"workspace_cursor"`
	Operation       string                              `json:"operation"`
	Entry           *candidateExamWorkspaceItemResponse `json:"entry,omitempty"`
}

type candidateWorkspaceJournalEntryResponse struct {
	Cursor         int64  `json:"cursor"`
	EntryID        string `json:"entry_id"`
	Kind           string `json:"kind"`
	Operation      string `json:"operation"`
	OldPath        string `json:"old_path,omitempty"`
	NewPath        string `json:"new_path,omitempty"`
	ContentVersion string `json:"content_version,omitempty"`
	ChangedAt      string `json:"changed_at"`
	Recursive      bool   `json:"recursive,omitempty"`
}

type candidateWorkspaceJournalResponse struct {
	WorkspaceID     string                                   `json:"workspace_id"`
	CurrentCursor   int64                                    `json:"current_cursor"`
	Items           []candidateWorkspaceJournalEntryResponse `json:"items"`
	NextCursor      string                                   `json:"next_cursor,omitempty"`
	RefreshRequired bool                                     `json:"refresh_required"`
}

// candidateWorkspaceJournalCursor continues the change journal after the
// last Workspace Cursor of the previous page.
type candidateWorkspaceJournalCursor struct {
	After int64 `json:"after_workspace_cursor"`
}

func candidateWorkspaceJournalCursorSpec() route.CursorSpec[candidateWorkspaceJournalCursor] {
	return route.CursorSpec[candidateWorkspaceJournalCursor]{
		Label:                "Workspace journal",
		MaximumEncodedLength: route.DefaultMaximumCursorLength,
		Members:              []string{"after_workspace_cursor"},
		Validate: func(cursor candidateWorkspaceJournalCursor) error {
			if cursor.After < 1 {
				return errors.New("invalid Workspace journal keyset")
			}
			return nil
		},
	}
}

func (h examAttemptHandlers) workspace(request route.Request) (route.Result, error) {
	access, err := candidateAccess(request)
	if err != nil {
		return route.Result{}, err
	}

	query := attempt.WorkspaceQuery{Access: access, ExpectedCursor: -1, Limit: 50}
	values := request.HTTP.URL.Query()
	if raw := values.Get("limit"); raw != "" {
		query.Limit, err = strconv.Atoi(raw)
		if err != nil || query.Limit < 1 || query.Limit > 200 {
			return route.Result{}, route.InvalidRequest(errors.New("must be between 1 and 200"))
		}
	}

	if raw := values.Get("cursor"); raw != "" {
		cursor, decodeErr := decodeCandidateWorkspaceCursor(raw)
		if decodeErr != nil {
			return route.Result{}, route.InvalidRequest(decodeErr)
		}

		query.ExpectedCursor, query.AfterEntryID = cursor.ExpectedCursor, cursor.ID
	}

	page, err := h.app.ListCandidateExamWorkspace(request.Context, request.Invocation(), query)
	if err != nil {
		return route.Result{}, err
	}

	response := candidateExamWorkspaceListResponse{
		WorkspaceID:     page.WorkspaceID.String(),
		WorkspaceCursor: page.Cursor,
		Items:           make([]candidateExamWorkspaceItemResponse, 0, len(page.Items)),
		RefreshRequired: page.RefreshRequired,
	}
	for _, item := range page.Items {
		mapped := candidateExamWorkspaceItemResponse{
			ID:             item.EntryID.String(),
			Kind:           string(item.Kind),
			Path:           item.Path,
			ContentVersion: item.ContentVersion.String(),
			MediaType:      item.MediaType,
			SHA256:         item.SHA256,
		}
		if item.Kind == model.StarterWorkspaceEntryFile {
			size := item.SizeBytes
			mapped.Size = &size
		}

		response.Items = append(response.Items, mapped)
	}

	if page.HasMore {
		if len(page.Items) == 0 {
			return route.Result{}, errors.New("Exam Attempt application returned an invalid Workspace page")
		}

		last := page.Items[len(page.Items)-1]
		response.NextCursor, err = encodeCandidateWorkspaceCursor(candidateWorkspaceCursor{ExpectedCursor: page.Cursor, ID: last.EntryID})
		if err != nil {
			return route.Result{}, route.Unpresentable(err)
		}
	}

	return route.JSON(http.StatusOK, response).WithHeaders(noStoreHeaders()), nil
}

func (h examAttemptHandlers) workspaceChanges(request route.Request) (route.Result, error) {
	access, err := candidateAccess(request)
	if err != nil {
		return route.Result{}, err
	}

	paging, err := request.Page()
	if err != nil {
		return route.Result{}, err
	}
	if paging.Limit > model.AttemptWorkspaceJournalReadMaximum {
		return route.Result{}, route.InvalidRequest(errors.New("must be between 1 and 200"))
	}

	query := attempt.WorkspaceJournalQuery{Access: access, Limit: paging.Limit}
	values := request.HTTP.URL.Query()
	cursor, continued, err := route.DecodePageCursor(paging, candidateWorkspaceJournalCursorSpec())
	if err != nil {
		return route.Result{}, err
	}

	switch raw := values.Get("workspace_cursor"); {
	case continued && values.Has("workspace_cursor"):
		return route.Result{}, route.InvalidRequest(errors.New("workspace_cursor starts a listing that cursor continues"))
	case continued:
		query.AfterCursor = cursor.After
	case raw != "":
		query.AfterCursor, err = strconv.ParseInt(raw, 10, 64)
		if err != nil || query.AfterCursor < 0 {
			return route.Result{}, route.InvalidRequest(errors.New("must be nonnegative"))
		}
	}

	page, err := h.app.ListCandidateExamWorkspaceJournal(request.Context, request.Invocation(), query)
	if err != nil {
		return route.Result{}, err
	}

	response := candidateWorkspaceJournalResponse{
		WorkspaceID:     page.WorkspaceID.String(),
		CurrentCursor:   page.CurrentCursor,
		Items:           make([]candidateWorkspaceJournalEntryResponse, 0, len(page.Entries)),
		RefreshRequired: page.RefreshRequired,
	}
	if page.HasMore {
		if len(page.Entries) == 0 {
			return route.Result{}, route.Unpresentable(errors.New("Workspace journal page made no progress"))
		}

		next := candidateWorkspaceJournalCursor{After: page.Entries[len(page.Entries)-1].Cursor}
		if response.NextCursor, err = route.EncodeCursor(next, candidateWorkspaceJournalCursorSpec()); err != nil {
			return route.Result{}, route.Unpresentable(err)
		}
	}

	for _, entry := range page.Entries {
		response.Items = append(response.Items, candidateWorkspaceJournalEntryResponse{
			Cursor:         entry.Cursor,
			EntryID:        entry.EntryID.String(),
			Kind:           string(entry.EntryKind),
			Operation:      string(entry.Operation),
			OldPath:        entry.OldPath,
			NewPath:        entry.NewPath,
			ContentVersion: entry.ContentVersion.String(),
			ChangedAt:      model.FormatRFC3339(entry.ChangedAt),
			Recursive:      entry.Recursive,
		})
	}

	return route.JSON(http.StatusOK, response).WithHeaders(noStoreHeaders()), nil
}

func (h examAttemptHandlers) createWorkspaceDirectory(request route.Request) (route.Result, error) {
	access, err := candidateAccess(request)
	if err != nil {
		return route.Result{}, err
	}

	var body createCandidateWorkspaceDirectoryRequest
	if err = decodeCandidateWorkspaceJSON(request, &body); err != nil {
		return route.Result{}, err
	}

	mutationAccess, err := candidateWorkspaceMutationAccess(
		access,
		candidateWorkspaceMutationAccessRequest{ParticipationID: body.ParticipationID, Generation: body.Generation},
	)
	if err != nil {
		return route.Result{}, err
	}

	result, err := h.app.CreateCandidateExamWorkspaceDirectory(
		request.Context,
		request.Invocation(),
		attempt.CreateWorkspaceDirectoryCommand{
			Access:         mutationAccess,
			Path:           body.Path,
			IdempotencyKey: request.IdempotencyKey,
		},
	)
	if err != nil {
		return route.Result{}, err
	}

	return route.JSON(http.StatusCreated, candidateWorkspaceMutationResult(result)).WithHeaders(noStoreHeaders()), nil
}

func (h examAttemptHandlers) createWorkspaceFile(request route.Request) (route.ProtocolResult, error) {
	access, err := candidateAccess(request)
	if err != nil {
		return route.ProtocolResult{}, err
	}

	var metadata candidateWorkspaceFileUploadMetadata
	content, err := decodeExamResourceMultipart(request.HTTP, &metadata)
	if err != nil || metadata.Size == nil {
		return route.ProtocolResult{}, route.InvalidRequest(errors.New("valid metadata and size are required"))
	}

	mutationAccess, err := candidateWorkspaceMutationAccess(
		access,
		candidateWorkspaceMutationAccessRequest{ParticipationID: metadata.ParticipationID, Generation: metadata.Generation},
	)
	if err != nil {
		return route.ProtocolResult{}, err
	}

	result, err := h.app.CreateCandidateExamWorkspaceFile(
		request.Context,
		request.Invocation(),
		attempt.CreateWorkspaceFileCommand{
			Access:         mutationAccess,
			Path:           metadata.Path,
			MediaType:      metadata.MediaType,
			ExpectedSHA256: metadata.SHA256,
			Body:           content,
			Size:           *metadata.Size,
			IdempotencyKey: request.IdempotencyKey,
		},
	)
	if err != nil {
		return route.ProtocolResult{}, err
	}

	return route.StreamingUpload(http.StatusCreated, candidateWorkspaceMutationResult(result)).WithHeaders(noStoreHeaders()), nil
}

func (h examAttemptHandlers) moveWorkspaceEntry(request route.Request) (route.Result, error) {
	access, entryID, err := candidateWorkspaceEntryAccess(request)
	if err != nil {
		return route.Result{}, err
	}

	var body moveCandidateWorkspaceEntryRequest
	if err = decodeCandidateWorkspaceJSON(request, &body); err != nil {
		return route.Result{}, err
	}

	mutationAccess, err := candidateWorkspaceMutationAccess(
		access,
		candidateWorkspaceMutationAccessRequest{ParticipationID: body.ParticipationID, Generation: body.Generation},
	)
	if err != nil {
		return route.Result{}, err
	}

	result, err := h.app.MoveCandidateExamWorkspaceEntry(
		request.Context,
		request.Invocation(),
		attempt.MoveWorkspaceEntryCommand{
			Access:          mutationAccess,
			EntryID:         entryID,
			ExpectedPath:    body.ExpectedPath,
			DestinationPath: body.DestinationPath,
			IdempotencyKey:  request.IdempotencyKey,
		},
	)
	if err != nil {
		return route.Result{}, err
	}

	return route.JSON(http.StatusOK, candidateWorkspaceMutationResult(result)).WithHeaders(noStoreHeaders()), nil
}

func (h examAttemptHandlers) replaceWorkspaceFile(request route.Request) (route.ProtocolResult, error) {
	access, entryID, err := candidateWorkspaceEntryAccess(request)
	if err != nil {
		return route.ProtocolResult{}, err
	}

	var metadata replaceCandidateWorkspaceFileMetadata
	content, err := decodeExamResourceMultipart(request.HTTP, &metadata)
	if err != nil || metadata.Size == nil {
		return route.ProtocolResult{}, route.InvalidRequest(errors.New("valid metadata and size are required"))
	}

	mutationAccess, err := candidateWorkspaceMutationAccess(
		access,
		candidateWorkspaceMutationAccessRequest{ParticipationID: metadata.ParticipationID, Generation: metadata.Generation},
	)
	if err != nil {
		return route.ProtocolResult{}, err
	}

	version, err := model.ParseWorkspaceContentVersion(metadata.ExpectedContentVersion)
	if err != nil {
		return route.ProtocolResult{}, route.InvalidRequest(err)
	}

	result, err := h.app.ReplaceCandidateExamWorkspaceFile(
		request.Context,
		request.Invocation(),
		attempt.ReplaceWorkspaceFileCommand{
			Access:                 mutationAccess,
			EntryID:                entryID,
			ExpectedPath:           metadata.ExpectedPath,
			ExpectedContentVersion: version,
			MediaType:              metadata.MediaType,
			ExpectedSHA256:         metadata.SHA256,
			Body:                   content,
			Size:                   *metadata.Size,
			IdempotencyKey:         request.IdempotencyKey,
		},
	)
	if err != nil {
		return route.ProtocolResult{}, err
	}

	return route.StreamingUpload(http.StatusOK, candidateWorkspaceMutationResult(result)).WithHeaders(noStoreHeaders()), nil
}

func (h examAttemptHandlers) deleteWorkspaceEntry(request route.Request) (route.Result, error) {
	access, entryID, err := candidateWorkspaceEntryAccess(request)
	if err != nil {
		return route.Result{}, err
	}

	var body deleteCandidateWorkspaceEntryRequest
	if err = decodeCandidateWorkspaceJSON(request, &body); err != nil {
		return route.Result{}, err
	}

	cursor := body.ExpectedWorkspaceCursor.ValuePointer()
	if body.ExpectedWorkspaceCursor.IsNull() ||
		(body.Recursive && (cursor == nil || *cursor < 0 || body.ExpectedContentVersion != "")) ||
		(!body.Recursive && cursor != nil) {
		return route.Result{}, route.InvalidRequest(
			errors.New("recursive directory deletion requires expected_workspace_cursor and no content version"),
		)
	}

	mutationAccess, err := candidateWorkspaceMutationAccess(
		access,
		candidateWorkspaceMutationAccessRequest{ParticipationID: body.ParticipationID, Generation: body.Generation},
	)
	if err != nil {
		return route.Result{}, err
	}

	var version model.WorkspaceContentVersion
	if body.ExpectedContentVersion != "" {
		version, err = model.ParseWorkspaceContentVersion(body.ExpectedContentVersion)
		if err != nil {
			return route.Result{}, route.InvalidRequest(err)
		}
	}

	result, err := h.app.DeleteCandidateExamWorkspaceEntry(
		request.Context,
		request.Invocation(),
		attempt.DeleteWorkspaceEntryCommand{
			Access:                  mutationAccess,
			EntryID:                 entryID,
			ExpectedPath:            body.ExpectedPath,
			ExpectedContentVersion:  version,
			Recursive:               body.Recursive,
			ExpectedWorkspaceCursor: cursor,
			IdempotencyKey:          request.IdempotencyKey,
		},
	)
	if err != nil {
		return route.Result{}, err
	}

	return route.JSON(http.StatusOK, candidateWorkspaceMutationResult(result)).WithHeaders(noStoreHeaders()), nil
}

func candidateWorkspaceMutationAccess(access attempt.CandidateAccess,
	wire candidateWorkspaceMutationAccessRequest,
) (attempt.WorkspaceMutationAccess, error) {
	participationID, err := model.ParseAttemptParticipationID(wire.ParticipationID)
	if err != nil || wire.Generation < 1 {
		return attempt.WorkspaceMutationAccess{}, route.InvalidRequest(errors.New("valid participation_id and generation are required"))
	}

	return attempt.WorkspaceMutationAccess{
		CandidateAccess: access,
		ParticipationID: participationID,
		Generation:      wire.Generation,
	}, nil
}

func decodeCandidateWorkspaceJSON(request route.Request, target any) error {
	if request.HTTP == nil || request.HTTP.Body == nil {
		return route.InvalidRequest(errors.New("request body is required"))
	}
	if err := route.RequireJSONMediaType(request.HTTP); err != nil {
		return route.InvalidRequest(err)
	}
	raw, err := io.ReadAll(io.LimitReader(request.HTTP.Body, (64<<10)+1))
	if err != nil || len(raw) > 64<<10 || rejectDuplicateTopLevelJSONMembers(raw) != nil {
		return route.InvalidRequest(errors.New("invalid strict JSON body"))
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err = decoder.Decode(target); err != nil {
		return route.InvalidRequest(err)
	}
	var trailing any
	if err = decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return route.InvalidRequest(errors.New("request body must contain one JSON value"))
	}
	return nil
}

func candidateWorkspaceEntryAccess(request route.Request) (attempt.CandidateAccess, model.AttemptWorkspaceEntryID, error) {
	access, err := candidateAccess(request)
	if err != nil {
		return attempt.CandidateAccess{}, "", err
	}
	entryID, err := route.PathID[model.AttemptWorkspaceEntryID](request, "attempt_workspace_entry_id")
	if err != nil {
		return attempt.CandidateAccess{}, "", err
	}
	return access, entryID, nil
}

func candidateWorkspaceMutationResult(result attempt.WorkspaceMutationResult) candidateWorkspaceMutationResponse {
	response := candidateWorkspaceMutationResponse{
		WorkspaceID:     result.WorkspaceID.String(),
		WorkspaceCursor: result.Change.Cursor,
		Operation:       string(result.Change.Operation),
	}
	if result.Entry != nil {
		entry := candidateExamWorkspaceItemResponse{
			ID:             result.Entry.EntryID.String(),
			Kind:           string(result.Entry.Kind),
			Path:           result.Entry.Path,
			ContentVersion: result.Entry.ContentVersion.String(),
			MediaType:      result.Entry.MediaType,
			SHA256:         result.Entry.SHA256,
		}
		if result.Entry.Kind == model.StarterWorkspaceEntryFile {
			size := result.Entry.SizeBytes
			entry.Size = &size
		}

		response.Entry = &entry
	}

	return response
}

func (h examAttemptHandlers) openWorkspaceFile(request route.Request) (route.ProtocolResult, error) {
	access, err := candidateAccess(request)
	if err != nil {
		return route.ProtocolResult{}, err
	}

	entryID, err := route.PathID[model.AttemptWorkspaceEntryID](request, "attempt_workspace_entry_id")
	if err != nil {
		return route.ProtocolResult{}, err
	}

	opened, err := h.app.OpenCandidateExamWorkspaceFile(
		request.Context,
		request.Invocation(),
		attempt.OpenWorkspaceFileQuery{Access: access, EntryID: entryID},
	)
	if err != nil {
		return route.ProtocolResult{}, err
	}

	return candidateAttemptContentResult(request, opened, `"`+opened.ContentVersion.String()+`"`, untrustedWorkspaceContentHeaders)
}

// untrustedWorkspaceContentHeaders keeps Workspace bytes, whose media type an
// author chose, from rendering as an application page. A media type that a
// browser renders as an active document (HTML, XML, or SVG) is served as plain
// text, and the sandbox policy gives any rendering an opaque origin with no
// scripts or form submission. The content stays inline: the protected clients
// read it with fetch and take its authored media type from the metadata.
func untrustedWorkspaceContentHeaders(headers http.Header) {
	headers.Set("Content-Type", passiveWorkspaceMediaType(headers.Get("Content-Type")))
	headers.Set("Content-Security-Policy", "sandbox")
}

// passiveWorkspaceMediaType returns mediaType unless a browser would render it
// as an active document, in which case it returns plain text.
func passiveWorkspaceMediaType(mediaType string) string {
	parsed, _, err := mime.ParseMediaType(mediaType)
	if err != nil {
		return "application/octet-stream"
	}

	switch {
	case parsed == "text/html",
		parsed == "text/xml",
		parsed == "text/xsl",
		parsed == "application/xml",
		strings.HasSuffix(parsed, "+xml"):
		return "text/plain; charset=utf-8"
	default:
		return mediaType
	}
}
