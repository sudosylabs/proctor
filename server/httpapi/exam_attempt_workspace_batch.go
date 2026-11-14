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
	"io"
	"mime/multipart"
	"net/http"

	"github.com/sudosylabs/proctor/server/app/exam/attempt"
	"github.com/sudosylabs/proctor/server/httpapi/route"
	"github.com/sudosylabs/proctor/server/model"
)

// applyWorkspaceBatch is the one Workspace write route. Its multipart body is
// a strict JSON "manifest" part, then one "content" part for each create_file
// and replace_file mutation, in manifest order, and nothing else.
func (h examAttemptHandlers) applyWorkspaceBatch(request route.Request) (route.ProtocolResult, error) {
	access, err := candidateAccess(request)
	if err != nil {
		return route.ProtocolResult{}, err
	}

	reader, manifest, err := decodeWorkspaceBatchManifest(request.HTTP)
	if err != nil {
		return route.ProtocolResult{}, route.InvalidRequest(err)
	}

	mutationAccess, err := candidateWorkspaceMutationAccess(
		access,
		candidateWorkspaceMutationAccessRequest{ParticipationID: manifest.ParticipationID, Generation: manifest.Generation},
	)
	if err != nil {
		return route.ProtocolResult{}, err
	}

	mutations, files, err := workspaceBatchMutations(manifest.Mutations)
	if err != nil {
		return route.ProtocolResult{}, route.InvalidRequest(err)
	}
	if manifest.ExpectedWorkspaceCursor.IsNull() {
		return route.ProtocolResult{}, route.InvalidRequest(errors.New("expected_workspace_cursor must be a number when present"))
	}

	content := &workspaceBatchParts{reader: reader, remaining: files}
	if files == 0 {
		if err = content.requireEnd(); err != nil {
			return route.ProtocolResult{}, route.InvalidRequest(err)
		}
	}

	result, err := h.app.ApplyCandidateExamWorkspaceBatch(
		request.Context,
		request.Invocation(),
		attempt.ApplyWorkspaceBatchCommand{
			Access:                  mutationAccess,
			Mutations:               mutations,
			ExpectedWorkspaceCursor: manifest.ExpectedWorkspaceCursor.ValuePointer(),
			Content:                 content,
			IdempotencyKey:          request.IdempotencyKey,
		},
	)
	if err != nil {
		return route.ProtocolResult{}, err
	}
	if len(result.Changes) != len(mutations) {
		return route.ProtocolResult{}, route.Unpresentable(errors.New("Workspace batch result has one change per mutation"))
	}

	response := candidateWorkspaceBatchResponse{
		WorkspaceID:          result.WorkspaceID.String(),
		FirstWorkspaceCursor: result.Changes[0].Cursor,
		WorkspaceCursor:      result.Changes[len(result.Changes)-1].Cursor,
		Changes:              make([]candidateWorkspaceJournalEntryResponse, len(result.Changes)),
	}
	for index, change := range result.Changes {
		response.Changes[index] = candidateWorkspaceJournalEntry(change)
	}

	return route.StreamingUpload(http.StatusOK, response).WithHeaders(noStoreHeaders()), nil
}

func decodeWorkspaceBatchManifest(request *http.Request) (*multipart.Reader, candidateWorkspaceBatchManifest, error) {
	var manifest candidateWorkspaceBatchManifest
	if request == nil {
		return nil, manifest, errors.New("request is required")
	}

	reader, err := request.MultipartReader()
	if err != nil {
		return nil, manifest, errors.New("multipart/form-data is required")
	}

	part, err := reader.NextPart()
	if err != nil {
		return nil, manifest, errors.New("manifest part is required")
	}
	defer part.Close()
	if part.FormName() != "manifest" || part.FileName() != "" {
		return nil, manifest, errors.New("manifest must be the first non-file part")
	}

	raw, err := io.ReadAll(io.LimitReader(part, model.AttemptWorkspaceBatchManifestMaximumBytes+1))
	if err != nil || len(raw) > model.AttemptWorkspaceBatchManifestMaximumBytes || rejectDuplicateTopLevelJSONMembers(raw) != nil {
		return nil, manifest, errors.New("manifest is invalid")
	}

	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err = decoder.Decode(&manifest); err != nil {
		return nil, manifest, errors.New("manifest is invalid")
	}
	var trailing any
	if err = decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return nil, manifest, errors.New("manifest must contain one JSON value")
	}

	if len(manifest.Mutations) == 0 || len(manifest.Mutations) > model.AttemptWorkspaceBatchMaximumMutations {
		return nil, manifest, errors.New("a batch has 1 to 128 mutations")
	}

	return reader, manifest, nil
}

// workspaceBatchMutations turns the wire mutations into commands and counts
// the file bodies that must follow.
func workspaceBatchMutations(wire []candidateWorkspaceBatchMutation) ([]attempt.WorkspaceBatchMutation, int, error) {
	mutations := make([]attempt.WorkspaceBatchMutation, len(wire))
	files := 0
	for index, item := range wire {
		operation := model.AttemptWorkspaceMutationKind(item.Operation)
		if !operation.IsValid() {
			return nil, 0, errors.New("unknown mutation operation")
		}

		mutation := attempt.WorkspaceBatchMutation{
			Operation:       operation,
			ExpectedPath:    item.ExpectedPath,
			DestinationPath: item.DestinationPath,
			Recursive:       item.Recursive,
			MediaType:       item.MediaType,
			SHA256:          item.SHA256,
		}

		creates := operation == model.AttemptWorkspaceMutationCreateFile || operation == model.AttemptWorkspaceMutationCreateDirectory
		switch {
		case creates && (item.EntryID != "" || item.DestinationPath != ""):
			return nil, 0, errors.New("a create names its entry by path only")
		case creates:
			mutation.DestinationPath = item.Path
		case item.Path != "":
			return nil, 0, errors.New("only a create takes path")
		default:
			entryID, err := model.ParseAttemptWorkspaceEntryID(item.EntryID)
			if err != nil {
				return nil, 0, errors.New("entry_id is invalid")
			}
			mutation.EntryID = entryID
		}

		if item.ExpectedContentVersion != "" {
			version, err := model.ParseWorkspaceContentVersion(item.ExpectedContentVersion)
			if err != nil {
				return nil, 0, errors.New("expected_content_version is invalid")
			}
			mutation.ExpectedContentVersion = version
		}

		file := operation == model.AttemptWorkspaceMutationCreateFile || operation == model.AttemptWorkspaceMutationReplaceFile
		switch {
		case file && item.Size == nil:
			return nil, 0, errors.New("a file mutation needs size")
		case file:
			mutation.Size = *item.Size
			files++
		case item.Size != nil:
			return nil, 0, errors.New("only a file mutation takes size")
		}

		mutations[index] = mutation
	}

	return mutations, files, nil
}

// workspaceBatchParts hands the application one "content" part per file
// mutation. Each part is read to its end before the next is opened; the last
// one also proves that no further part follows.
type workspaceBatchParts struct {
	reader    *multipart.Reader
	remaining int
	current   *multipart.Part
}

func (p *workspaceBatchParts) Next(context.Context) (io.Reader, error) {
	if p.current != nil {
		_ = p.current.Close()
		p.current = nil
	}
	if p.remaining < 1 {
		return nil, errors.New("more content parts were requested than the manifest declares")
	}

	part, err := p.reader.NextPart()
	if err != nil {
		return nil, errors.New("a content part is missing")
	}
	if part.FormName() != "content" {
		_ = part.Close()
		return nil, errors.New("file bodies must be content parts")
	}

	p.remaining--
	if p.remaining == 0 {
		return &exactMultipartContent{part: part, reader: p.reader}, nil
	}

	p.current = part
	return part, nil
}

func (p *workspaceBatchParts) requireEnd() error {
	part, err := p.reader.NextPart()
	if err == nil {
		_ = part.Close()
		return errors.New("a batch without files carries no content parts")
	}
	if !errors.Is(err, io.EOF) {
		return err
	}
	return nil
}
