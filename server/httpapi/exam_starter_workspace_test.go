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
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/sudosylabs/proctor/server/app/apperror"
	"github.com/sudosylabs/proctor/server/app/exam/workspace"
	"github.com/sudosylabs/proctor/server/app/invocation"
	"github.com/sudosylabs/proctor/server/model"
	"github.com/sudosylabs/proctor/server/store"
)

func TestExamStarterWorkspaceHTTPFileUploadUsesMetadataFirstStreaming(t *testing.T) {
	t.Parallel()
	logger, _ := newTestLogger(t)
	fake := newExamStarterWorkspaceHTTPFake(testExamHTTPPrincipal())
	api := newFocusedResourceAPI(t, logger, fake, examStarterWorkspaceResource(fake))
	raw := []byte("package main\n")
	checksum := fmt.Sprintf("%x", sha256.Sum256(raw))
	body, contentType := examResourceMultipart(t, `{"expected_revision":1,"path":"cmd/main.go","media_type":"text/x-go","size":13,"sha256":"`+checksum+`"}`, raw, false)
	request := httptest.NewRequest(http.MethodPost, "/api/v1/exams/"+fake.examID.String()+"/draft/starter-workspace/files", body)
	request.Header.Set("Authorization", "Bearer credential")
	request.Header.Set("Content-Type", contentType)
	request.Header.Set("Idempotency-Key", "workspace-file")
	response := httptest.NewRecorder()
	api.ServeHTTP(response, request)
	require.Equal(t, http.StatusCreated, response.Code, "status=%d body=%s", response.Code, response.Body.String())
	require.Equal(t, string(raw), string(fake.uploaded), "command=%#v uploaded", fake.createFile)
	require.Equal(t, "cmd/main.go", fake.createFile.Path, "command=%#v uploaded", fake.createFile)
	require.Equal(t, checksum, fake.createFile.ExpectedSHA256, "command=%#v uploaded", fake.createFile)
	require.Equal(t, "workspace-file", fake.createFile.IdempotencyKey, "command=%#v uploaded", fake.createFile)
	require.False(t, bytes.Contains(response.Body.Bytes(), []byte(fake.object.ID.String())))
	require.False(t, bytes.Contains(response.Body.Bytes(), []byte("object")))
	require.False(t, bytes.Contains(response.Body.Bytes(), []byte("storage")))
}

func TestExamStarterWorkspaceHTTPFileUploadRequiresSizeButAcceptsExplicitZero(t *testing.T) {
	t.Parallel()
	logger, _ := newTestLogger(t)
	fake := newExamStarterWorkspaceHTTPFake(testExamHTTPPrincipal())
	api := newFocusedResourceAPI(t, logger, fake, examStarterWorkspaceResource(fake))
	path := "/api/v1/exams/" + fake.examID.String() + "/draft/starter-workspace/files"

	omittedBody, omittedType := examResourceMultipart(t, `{"expected_revision":1,"path":"empty.txt","media_type":"text/plain","sha256":"`+fmt.Sprintf("%x", sha256.Sum256(nil))+`"}`, nil, false)
	omitted := httptest.NewRequest(http.MethodPost, path, omittedBody)
	omitted.Header.Set("Authorization", "Bearer credential")
	omitted.Header.Set("Content-Type", omittedType)
	omitted.Header.Set("Idempotency-Key", "workspace-file-omitted-size")
	omittedResponse := httptest.NewRecorder()
	api.ServeHTTP(omittedResponse, omitted)
	require.Equal(t, http.StatusBadRequest, omittedResponse.Code, "omitted size status=%d command=%#v body=%s", omittedResponse.Code, fake.createFile, omittedResponse.Body.String())
	require.False(t, fake.createFile.ExamID.IsValid(), "omitted size status=%d command=%#v body=%s", omittedResponse.Code, fake.createFile, omittedResponse.Body.String())

	explicitBody, explicitType := examResourceMultipart(t, `{"expected_revision":1,"path":"empty.txt","media_type":"text/plain","size":0,"sha256":"`+fmt.Sprintf("%x", sha256.Sum256(nil))+`"}`, nil, false)
	explicit := httptest.NewRequest(http.MethodPost, path, explicitBody)
	explicit.Header.Set("Authorization", "Bearer credential")
	explicit.Header.Set("Content-Type", explicitType)
	explicit.Header.Set("Idempotency-Key", "workspace-file-empty")
	explicitResponse := httptest.NewRecorder()
	api.ServeHTTP(explicitResponse, explicit)
	require.Equal(t, http.StatusCreated, explicitResponse.Code, "explicit zero status=%d command=%#v body=%s", explicitResponse.Code, fake.createFile, explicitResponse.Body.String())
	require.Equal(t, int64(0), fake.createFile.Size, "explicit zero status=%d command=%#v body=%s", explicitResponse.Code, fake.createFile, explicitResponse.Body.String())
}

func TestExamStarterWorkspaceHTTPFileReplacementRequiresSize(t *testing.T) {
	t.Parallel()
	logger, _ := newTestLogger(t)
	fake := newExamStarterWorkspaceHTTPFake(testExamHTTPPrincipal())
	api := newFocusedResourceAPI(t, logger, fake, examStarterWorkspaceResource(fake))
	path := "/api/v1/exams/" + fake.examID.String() + "/draft/starter-workspace/files/" + fake.entry.ID.String() + "/content"
	body, contentType := examResourceMultipart(t, `{"expected_revision":1,"expected_content_version":"`+fake.object.ContentVersion.String()+`","media_type":"text/plain","sha256":"`+fmt.Sprintf("%x", sha256.Sum256(nil))+`"}`, nil, false)
	request := httptest.NewRequest(http.MethodPut, path, body)
	request.Header.Set("Authorization", "Bearer credential")
	request.Header.Set("Content-Type", contentType)
	request.Header.Set("Idempotency-Key", "workspace-replace-omitted-size")
	response := httptest.NewRecorder()
	api.ServeHTTP(response, request)
	require.Equal(t, http.StatusBadRequest, response.Code, "status=%d command=%#v body=%s", response.Code, fake.replaceFile, response.Body.String())
	require.False(t, fake.replaceFile.EntryID.IsValid(), "status=%d command=%#v body=%s", response.Code, fake.replaceFile, response.Body.String())
}

func TestExamStarterWorkspaceHTTPFileReplacementRequiresAndCarriesExpectedContentVersion(t *testing.T) {
	t.Parallel()
	logger, _ := newTestLogger(t)
	fake := newExamStarterWorkspaceHTTPFake(testExamHTTPPrincipal())
	api := newFocusedResourceAPI(t, logger, fake, examStarterWorkspaceResource(fake))
	path := "/api/v1/exams/" + fake.examID.String() + "/draft/starter-workspace/files/" + fake.entry.ID.String() + "/content"
	checksum := fmt.Sprintf("%x", sha256.Sum256(nil))

	omittedBody, omittedType := examResourceMultipart(t, `{"expected_revision":1,"media_type":"text/plain","size":0,"sha256":"`+checksum+`"}`, nil, false)
	omitted := httptest.NewRequest(http.MethodPut, path, omittedBody)
	omitted.Header.Set("Authorization", "Bearer credential")
	omitted.Header.Set("Content-Type", omittedType)
	omitted.Header.Set("Idempotency-Key", "workspace-replace-no-version")
	omittedResponse := httptest.NewRecorder()
	api.ServeHTTP(omittedResponse, omitted)
	require.Equal(t, http.StatusBadRequest, omittedResponse.Code, "omitted status=%d command=%#v body=%s", omittedResponse.Code, fake.replaceFile, omittedResponse.Body.String())
	require.False(t, fake.replaceFile.EntryID.IsValid(), "omitted status=%d command=%#v body=%s", omittedResponse.Code, fake.replaceFile, omittedResponse.Body.String())

	explicitBody, explicitType := examResourceMultipart(t, `{"expected_revision":1,"expected_content_version":"`+fake.object.ContentVersion.String()+`","media_type":"text/plain","size":0,"sha256":"`+checksum+`"}`, nil, false)
	explicit := httptest.NewRequest(http.MethodPut, path, explicitBody)
	explicit.Header.Set("Authorization", "Bearer credential")
	explicit.Header.Set("Content-Type", explicitType)
	explicit.Header.Set("Idempotency-Key", "workspace-replace-version")
	explicitResponse := httptest.NewRecorder()
	api.ServeHTTP(explicitResponse, explicit)
	require.Equal(t, http.StatusOK, explicitResponse.Code, "explicit status=%d command=%#v body=%s", explicitResponse.Code, fake.replaceFile, explicitResponse.Body.String())
	require.Equal(t, fake.object.ContentVersion, fake.replaceFile.ExpectedContentVersion, "explicit status=%d command=%#v body=%s", explicitResponse.Code, fake.replaceFile, explicitResponse.Body.String())
}

func TestExamStarterWorkspaceHTTPDirectoryAndMoveUseStrictJSON(t *testing.T) {
	t.Parallel()
	logger, _ := newTestLogger(t)
	fake := newExamStarterWorkspaceHTTPFake(testExamHTTPPrincipal())
	api := newFocusedResourceAPI(t, logger, fake, examStarterWorkspaceResource(fake))
	base := "/api/v1/exams/" + fake.examID.String() + "/draft/starter-workspace"
	create := httptest.NewRequestWithContext(t.Context(), http.MethodPost, base+"/directories", strings.NewReader(`{"expected_revision":1,"path":"cmd"}`))
	create.Header.Set("Authorization", "Bearer credential")
	create.Header.Set("Content-Type", "application/json")
	create.Header.Set("Idempotency-Key", "workspace-directory")
	created := httptest.NewRecorder()
	api.ServeHTTP(created, create)
	require.Equal(t, http.StatusCreated, created.Code, "create=%d command=%#v body=%s", created.Code, fake.createDirectory, created.Body.String())
	require.Equal(t, "cmd", fake.createDirectory.Path, "create=%d command=%#v body=%s", created.Code, fake.createDirectory, created.Body.String())
	move := httptest.NewRequestWithContext(t.Context(), http.MethodPatch, base+"/entries/"+fake.entry.ID.String(), strings.NewReader(`{"expected_revision":2,"path":"src/main.go","unexpected":true}`))
	move.Header.Set("Authorization", "Bearer credential")
	move.Header.Set("Content-Type", "application/json")
	move.Header.Set("Idempotency-Key", "workspace-move")
	moved := httptest.NewRecorder()
	api.ServeHTTP(moved, move)
	require.Equal(t, http.StatusBadRequest, moved.Code, "strict move=%d command=%#v body=%s", moved.Code, fake.move, moved.Body.String())
	require.False(t, fake.move.EntryID.IsValid(), "strict move=%d command=%#v body=%s", moved.Code, fake.move, moved.Body.String())
}

func TestExamStarterWorkspaceHTTPContentIsInlinePrivateAndConditional(t *testing.T) {
	t.Parallel()
	logger, _ := newTestLogger(t)
	fake := newExamStarterWorkspaceHTTPFake(testExamHTTPPrincipal())
	api := newFocusedResourceAPI(t, logger, fake, examStarterWorkspaceResource(fake))
	path := "/api/v1/exams/" + fake.examID.String() + "/draft/starter-workspace/files/" + fake.entry.ID.String() + "/content"
	request := httptest.NewRequest(http.MethodGet, path, nil)
	request.Header.Set("Authorization", "Bearer credential")
	response := httptest.NewRecorder()
	api.ServeHTTP(response, request)
	etag := `"` + fake.object.SHA256 + `"`
	require.Equal(t, http.StatusOK, response.Code, "response=%d headers=%v body", response.Code, response.Header())
	require.Equal(t, "protected code", response.Body.String(), "response=%d headers=%v body", response.Code, response.Header())
	require.Empty(t, response.Header().Get("Content-Disposition"), "response=%d headers=%v body", response.Code, response.Header())
	require.Contains(t, response.Header().Values("Content-Security-Policy"), "sandbox", "authored content renders with an opaque origin")
	require.Equal(t, "private, no-store", response.Header().Get("Cache-Control"), "response=%d headers=%v body", response.Code, response.Header())
	require.Equal(t, etag, response.Header().Get("ETag"), "response=%d headers=%v body", response.Code, response.Header())
	require.Len(t, response.Header().Values("X-Content-Type-Options"), 1, "response=%d headers=%v body", response.Code, response.Header())
	require.Equal(t, "nosniff", response.Header().Get("X-Content-Type-Options"), "response=%d headers=%v body", response.Code, response.Header())
	conditional := httptest.NewRequest(http.MethodGet, path, nil)
	conditional.Header.Set("Authorization", "Bearer credential")
	conditional.Header.Set("If-None-Match", etag)
	notModified := httptest.NewRecorder()
	api.ServeHTTP(notModified, conditional)
	require.Equal(t, http.StatusNotModified, notModified.Code, "conditional=%d body=%q", notModified.Code, notModified.Body.String())
	require.Equal(t, 0, notModified.Body.Len(), "conditional=%d body=%q", notModified.Code, notModified.Body.String())
}

func TestExamStarterWorkspaceHTTPRemoveReturnsNoContent(t *testing.T) {
	t.Parallel()
	logger, _ := newTestLogger(t)
	fake := newExamStarterWorkspaceHTTPFake(testExamHTTPPrincipal())
	api := newFocusedResourceAPI(t, logger, fake, examStarterWorkspaceResource(fake))
	path := "/api/v1/exams/" + fake.examID.String() + "/draft/starter-workspace/entries/" + fake.entry.ID.String()
	request := httptest.NewRequestWithContext(t.Context(), http.MethodDelete, path, strings.NewReader(`{"expected_revision":1}`))
	request.Header.Set("Authorization", "Bearer credential")
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Idempotency-Key", "workspace-remove")
	response := httptest.NewRecorder()
	api.ServeHTTP(response, request)
	require.Equal(t, http.StatusNoContent, response.Code, "status=%d command=%#v body=%q", response.Code, fake.removeEntry, response.Body.String())
	require.Equal(t, 0, response.Body.Len(), "status=%d command=%#v body=%q", response.Code, fake.removeEntry, response.Body.String())
	require.Equal(t, fake.entry.ID, fake.removeEntry.EntryID, "status=%d command=%#v body=%q", response.Code, fake.removeEntry, response.Body.String())
	request = httptest.NewRequestWithContext(t.Context(), http.MethodDelete, path, strings.NewReader(`{"expected_revision":1,"recursive":true}`))
	request.Header.Set("Authorization", "Bearer credential")
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Idempotency-Key", "workspace-recursive-remove")
	response = httptest.NewRecorder()
	api.ServeHTTP(response, request)
	require.Equal(t, http.StatusNoContent, response.Code, "recursive status=%d command=%#v body=%q", response.Code, fake.removeEntry, response.Body.String())
	require.True(t, fake.removeEntry.Recursive, "recursive status=%d command=%#v body=%q", response.Code, fake.removeEntry, response.Body.String())
	require.Equal(t, int64(1), fake.removeEntry.ExpectedDraftRevision, "recursive status=%d command=%#v body=%q", response.Code, fake.removeEntry, response.Body.String())
}

type examStarterWorkspaceHTTPFake struct {
	principal       model.Principal
	examID          model.ExamID
	entry           model.StarterWorkspaceEntry
	object          model.StarterWorkspaceObject
	createDirectory workspace.CreateDirectoryCommand
	createFile      workspace.CreateFileCommand
	move            workspace.MoveEntryCommand
	replaceFile     workspace.ReplaceFileCommand
	removeEntry     workspace.RemoveEntryCommand
	uploaded        []byte
}

func newExamStarterWorkspaceHTTPFake(principal model.Principal) *examStarterWorkspaceHTTPFake {
	at := time.Date(2026, 8, 14, 10, 0, 0, 0, time.UTC)
	examID := model.NewExamID()
	object := model.StarterWorkspaceObject{ID: model.NewStarterWorkspaceObjectID(), ExamID: examID, CreatedByUserID: principal.UserID,
		CreatedAt: at, UpdatedAt: at, ExpiresAt: at.Add(time.Hour), State: model.StarterWorkspaceObjectCurrent,
		ContentVersion: model.NewWorkspaceContentVersion(), MediaType: "text/x-go", SizeBytes: 14, SHA256: strings.Repeat("a", 64)}
	entry, _ := model.NewStarterWorkspaceFile(model.NewStarterWorkspaceEntryID(), examID, "cmd/main.go", object.ID, at)
	return &examStarterWorkspaceHTTPFake{principal: principal, examID: examID, entry: *entry, object: object}
}
func (f *examStarterWorkspaceHTTPFake) AuthenticateAccess(context.Context, string) (*model.Principal, error) {
	principal := f.principal
	return &principal, nil
}
func (f *examStarterWorkspaceHTTPFake) AuthenticateBearer(context.Context, string) (*model.Principal, error) {
	principal := f.principal
	return &principal, nil
}
func (f *examStarterWorkspaceHTTPFake) result() workspace.Result {
	return workspace.Result{Entry: f.entry, Object: &f.object, DraftRevision: 2}
}
func (f *examStarterWorkspaceHTTPFake) ListExamStarterWorkspace(context.Context, invocation.Invocation, model.ExamID) ([]store.ExamStarterWorkspaceItem, error) {
	return []store.ExamStarterWorkspaceItem{{Entry: f.entry, Object: &f.object}}, nil
}
func (f *examStarterWorkspaceHTTPFake) OpenExamStarterWorkspaceFile(
	context.Context,
	invocation.Invocation,
	model.ExamID,
	model.StarterWorkspaceEntryID,
) (workspace.OpenedFile, error) {
	return workspace.OpenedFile{Body: io.NopCloser(strings.NewReader("protected code")), MediaType: f.object.MediaType,
		SizeBytes: 14, SHA256: f.object.SHA256, ContentVersion: f.object.ContentVersion}, nil
}
func (f *examStarterWorkspaceHTTPFake) CreateExamStarterWorkspaceDirectory(_ context.Context, _ invocation.Invocation, command workspace.CreateDirectoryCommand) (workspace.Result, error) {
	f.createDirectory = command
	directory, _ := model.NewStarterWorkspaceDirectory(f.entry.ID, f.examID, command.Path, f.entry.CreatedAt)
	return workspace.Result{Entry: *directory, DraftRevision: 2}, nil
}
func (f *examStarterWorkspaceHTTPFake) CreateExamStarterWorkspaceFile(_ context.Context, _ invocation.Invocation, command workspace.CreateFileCommand) (workspace.Result, error) {
	raw, err := io.ReadAll(command.Body)
	if err != nil {
		return workspace.Result{}, apperror.New("exam.starter_workspace.invalid")
	}
	f.createFile, f.uploaded = command, raw
	return f.result(), nil
}
func (f *examStarterWorkspaceHTTPFake) MoveExamStarterWorkspaceEntry(_ context.Context, _ invocation.Invocation, command workspace.MoveEntryCommand) (workspace.Result, error) {
	f.move = command
	return f.result(), nil
}
func (f *examStarterWorkspaceHTTPFake) ReplaceExamStarterWorkspaceFile(_ context.Context, _ invocation.Invocation, command workspace.ReplaceFileCommand) (workspace.Result, error) {
	f.replaceFile = command
	return f.result(), nil
}
func (f *examStarterWorkspaceHTTPFake) RemoveExamStarterWorkspaceEntry(_ context.Context, _ invocation.Invocation, command workspace.RemoveEntryCommand) (workspace.Result, error) {
	f.removeEntry = command
	return f.result(), nil
}

func TestExamStarterWorkspaceOpenAPIDocument(t *testing.T) {
	t.Parallel()
	base := "/api/v1/exams/{exam_id}/draft/starter-workspace"
	content := base + "/files/{starter_workspace_entry_id}/content"

	document := readOpenAPIDocument(t)
	assertExamMultipartOperation(t, document, base+"/files", "post", "#/components/schemas/ExamStarterWorkspaceFileUploadMultipart")
	assertExamMultipartOperation(t, document, content, "put", "#/components/schemas/ExamStarterWorkspaceFileReplacementMultipart")
	assertProtectedExamContentResponse(t, document, content, "ExamStarterWorkspaceProtectedContent")
	assertProtectedExamCacheControl(t, "ExamStarterWorkspaceProtectedContent", "private, no-store")
	_, ok := document.Components.Responses["ExamStarterWorkspaceProtectedContent"].Headers["Content-Disposition"]
	assert.False(t, ok, "GET %s protected response documents forbidden Content-Disposition", content)
	operation := decodeExamContentOpenAPIOperation(t, document, content, "get")
	response304 := operation.Responses["304"]
	assert.Equal(t, "#/components/responses/ExamStarterWorkspaceContentNotModified", response304.Ref, "GET %s 304 response = %#v", content, response304)
	assertWorkspaceContentVersionOpenAPI(t, document)
	assertWorkspaceEntryVariantsOpenAPI(t)
}

func assertWorkspaceContentVersionOpenAPI(t *testing.T, document openAPIDocument) {
	t.Helper()
	const wantRef = "#/components/schemas/WorkspaceContentVersion"
	for _, target := range []struct {
		schema   string
		property string
	}{
		{schema: "ExamStarterWorkspaceFileReplacementMetadata", property: "expected_content_version"},
		{schema: "ExamStarterWorkspaceEntryResponse", property: "content_version"},
		{schema: "ExamStarterWorkspaceListItemResponse", property: "content_version"},
	} {
		t.Run(target.schema, func(t *testing.T) {
			var shape openAPISchemaShape
			require.NoError(t, json.Unmarshal(document.Components.Schemas[target.schema].Properties[target.property], &shape), "decode %s.%s", target.schema, target.property)
			assert.Equal(t, wantRef, shape.Ref, "%s.%s reference", target.schema, target.property)
		})
	}

	var raw struct {
		Components struct {
			Schemas map[string]struct {
				Type    any    `json:"type"`
				Pattern string `json:"pattern"`
			} `json:"schemas"`
		} `json:"components"`
	}
	require.NoError(t, json.Unmarshal(readOpenAPIDocumentBytes(t), &raw))
	token := raw.Components.Schemas["WorkspaceContentVersion"]
	assert.Equal(t, "string", token.Type)
	assert.Equal(t, `^[A-Za-z0-9_-]{26}$`, token.Pattern)
}

func assertWorkspaceEntryVariantsOpenAPI(t *testing.T) {
	t.Helper()
	var document struct {
		Components struct {
			Schemas map[string]struct {
				Required []string          `json:"required"`
				OneOf    []json.RawMessage `json:"oneOf"`
			} `json:"schemas"`
		} `json:"components"`
	}
	require.NoError(t, json.Unmarshal(readOpenAPIDocumentBytes(t), &document))
	for _, name := range []string{"ExamStarterWorkspaceEntryResponse", "ExamStarterWorkspaceListItemResponse"} {
		schema := document.Components.Schemas[name]
		assert.Len(t, schema.OneOf, 2, "%s oneOf variants", name)
		encoded, err := json.Marshal(schema.OneOf)
		require.NoError(t, err)
		text := string(encoded)
		for _, invariant := range []string{`"const":"file"`, `"const":"directory"`, `"content_version"`, `"media_type"`, `"size"`, `"sha256"`, `"not"`} {
			assert.Contains(t, text, invariant, "%s variants omit", name)
		}
	}
	mutation := document.Components.Schemas["ExamStarterWorkspaceEntryResponse"]
	list := document.Components.Schemas["ExamStarterWorkspaceListItemResponse"]
	assert.Contains(t, mutation.Required, "draft_revision", "draft_revision requirement mutation")
	assert.NotContains(t, list.Required, "draft_revision", "draft_revision requirement mutation")
}
