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

// candidateWorkspaceBatchManifest is the first multipart part of a batch. Its
// file mutations' bodies follow it as "content" parts, in the same order.
type candidateWorkspaceBatchManifest struct {
	ParticipationID         string                            `json:"participation_id"`
	Generation              int64                             `json:"generation"`
	ExpectedWorkspaceCursor route.Optional[int64]             `json:"expected_workspace_cursor,omitempty"`
	Mutations               []candidateWorkspaceBatchMutation `json:"mutations"`
}

type candidateWorkspaceBatchMutation struct {
	Operation              string `json:"operation"`
	EntryID                string `json:"entry_id,omitempty"`
	Path                   string `json:"path,omitempty"`
	ExpectedPath           string `json:"expected_path,omitempty"`
	DestinationPath        string `json:"destination_path,omitempty"`
	ExpectedContentVersion string `json:"expected_content_version,omitempty"`
	Recursive              bool   `json:"recursive,omitempty"`
	MediaType              string `json:"media_type,omitempty"`
	Size                   *int64 `json:"size,omitempty"`
	SHA256                 string `json:"sha256,omitempty"`
}

type candidateWorkspaceBatchResponse struct {
	WorkspaceID          string                                   `json:"workspace_id"`
	FirstWorkspaceCursor int64                                    `json:"first_workspace_cursor"`
	WorkspaceCursor      int64                                    `json:"workspace_cursor"`
	Changes              []candidateWorkspaceJournalEntryResponse `json:"changes"`
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
	Origin         string `json:"origin"`
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
		response.Items = append(response.Items, candidateWorkspaceJournalEntry(entry))
	}

	return route.JSON(http.StatusOK, response).WithHeaders(noStoreHeaders()), nil
}

func candidateWorkspaceJournalEntry(entry model.AttemptWorkspaceJournalEntry) candidateWorkspaceJournalEntryResponse {
	return candidateWorkspaceJournalEntryResponse{
		Cursor:         entry.Cursor,
		EntryID:        entry.EntryID.String(),
		Kind:           string(entry.EntryKind),
		Operation:      string(entry.Operation),
		OldPath:        entry.OldPath,
		NewPath:        entry.NewPath,
		ContentVersion: entry.ContentVersion.String(),
		ChangedAt:      model.FormatRFC3339(entry.ChangedAt),
		Recursive:      entry.Recursive,
		Origin:         string(entry.Origin),
	}
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
