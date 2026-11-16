// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package httpapi

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/sudosylabs/proctor/server/httpapi/route/routetest"
)

// newStarterWorkspaceAPI serves the Starter Workspace routes over a fresh
// fixture, as its caller.
func newStarterWorkspaceAPI(t *testing.T) (*examContentFixture, http.Handler) {
	t.Helper()
	logger, _ := routetest.NewLogger(t)
	principal := testExamHTTPPrincipal()
	f := newExamContentFixture(t, principal)
	return f, newFocusedResourceAPI(t, logger, routetest.Authenticator{Principal: principal}, examStarterWorkspaceResource(f.workspaceService))
}

func TestExamStarterWorkspaceHTTPFileUploadUsesMetadataFirstStreaming(t *testing.T) {
	t.Parallel()
	f, api := newStarterWorkspaceAPI(t)
	raw := []byte("package main\n")
	checksum := fmt.Sprintf("%x", sha256.Sum256(raw))
	body, contentType := examResourceMultipart(t, `{"expected_revision":1,"path":"cmd/main.go","media_type":"text/x-go","size":13,"sha256":"`+checksum+`"}`, raw, false)
	request := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/api/v1/exams/"+f.examID.String()+"/draft/starter-workspace/files", body)
	request.Header.Set("Authorization", "Bearer credential")
	request.Header.Set("Content-Type", contentType)
	request.Header.Set("Idempotency-Key", "workspace-file")
	response := httptest.NewRecorder()
	api.ServeHTTP(response, request)
	require.Equal(t, http.StatusCreated, response.Code, "status=%d body=%s", response.Code, response.Body.String())
	require.Equal(t, [][]byte{raw}, f.workspaceObjects.staged, "the uploaded bytes reached the file store unchanged")
	require.Len(t, f.workspaceStore.files, 1, "the file never reached the Starter Workspace Store")
	created := f.workspaceStore.files[0]
	require.Equal(t, "cmd/main.go", created.Path)
	require.Equal(t, checksum, created.SHA256)
	require.Equal(t, sha256.Sum256([]byte("workspace-file")), f.workspaceStore.lastCommand(t).KeyDigest)
	require.False(t, bytes.Contains(response.Body.Bytes(), []byte(created.ObjectID.String())))
	require.False(t, bytes.Contains(response.Body.Bytes(), []byte("object")))
	require.False(t, bytes.Contains(response.Body.Bytes(), []byte("storage")))
}

func TestExamStarterWorkspaceHTTPFileUploadRequiresSizeButAcceptsExplicitZero(t *testing.T) {
	t.Parallel()
	f, api := newStarterWorkspaceAPI(t)
	path := "/api/v1/exams/" + f.examID.String() + "/draft/starter-workspace/files"

	omittedBody, omittedType := examResourceMultipart(t, `{"expected_revision":1,"path":"empty.txt","media_type":"text/plain","sha256":"`+fmt.Sprintf("%x", sha256.Sum256(nil))+`"}`, nil, false)
	omitted := httptest.NewRequestWithContext(t.Context(), http.MethodPost, path, omittedBody)
	omitted.Header.Set("Authorization", "Bearer credential")
	omitted.Header.Set("Content-Type", omittedType)
	omitted.Header.Set("Idempotency-Key", "workspace-file-omitted-size")
	omittedResponse := httptest.NewRecorder()
	api.ServeHTTP(omittedResponse, omitted)
	require.Equal(t, http.StatusBadRequest, omittedResponse.Code, "omitted size status=%d body=%s", omittedResponse.Code, omittedResponse.Body.String())
	require.Empty(t, f.workspaceStore.reservations, "an upload without a size reached the Starter Workspace Store")

	explicitBody, explicitType := examResourceMultipart(t, `{"expected_revision":1,"path":"empty.txt","media_type":"text/plain","size":0,"sha256":"`+fmt.Sprintf("%x", sha256.Sum256(nil))+`"}`, nil, false)
	explicit := httptest.NewRequestWithContext(t.Context(), http.MethodPost, path, explicitBody)
	explicit.Header.Set("Authorization", "Bearer credential")
	explicit.Header.Set("Content-Type", explicitType)
	explicit.Header.Set("Idempotency-Key", "workspace-file-empty")
	explicitResponse := httptest.NewRecorder()
	api.ServeHTTP(explicitResponse, explicit)
	require.Equal(t, http.StatusCreated, explicitResponse.Code, "explicit zero status=%d body=%s", explicitResponse.Code, explicitResponse.Body.String())
	require.Len(t, f.workspaceStore.files, 1, "the empty file never reached the Starter Workspace Store")
	require.Equal(t, int64(0), f.workspaceStore.files[0].SizeBytes)
}

func TestExamStarterWorkspaceHTTPFileReplacementRequiresSize(t *testing.T) {
	t.Parallel()
	f, api := newStarterWorkspaceAPI(t)
	path := "/api/v1/exams/" + f.examID.String() + "/draft/starter-workspace/files/" + f.file.Entry.ID.String() + "/content"
	body, contentType := examResourceMultipart(t, `{"expected_revision":1,"expected_content_version":"`+f.file.Object.ContentVersion.String()+`","media_type":"text/plain","sha256":"`+fmt.Sprintf("%x", sha256.Sum256(nil))+`"}`, nil, false)
	request := httptest.NewRequestWithContext(t.Context(), http.MethodPut, path, body)
	request.Header.Set("Authorization", "Bearer credential")
	request.Header.Set("Content-Type", contentType)
	request.Header.Set("Idempotency-Key", "workspace-replace-omitted-size")
	response := httptest.NewRecorder()
	api.ServeHTTP(response, request)
	require.Equal(t, http.StatusBadRequest, response.Code, "status=%d body=%s", response.Code, response.Body.String())
	require.Empty(t, f.workspaceStore.reservations, "a replacement without a size reached the Starter Workspace Store")
	require.Empty(t, f.workspaceStore.replacements, "a replacement without a size reached the Starter Workspace Store")
}

func TestExamStarterWorkspaceHTTPFileReplacementRequiresAndCarriesExpectedContentVersion(t *testing.T) {
	t.Parallel()
	f, api := newStarterWorkspaceAPI(t)
	path := "/api/v1/exams/" + f.examID.String() + "/draft/starter-workspace/files/" + f.file.Entry.ID.String() + "/content"
	checksum := fmt.Sprintf("%x", sha256.Sum256(nil))

	omittedBody, omittedType := examResourceMultipart(t, `{"expected_revision":1,"media_type":"text/plain","size":0,"sha256":"`+checksum+`"}`, nil, false)
	omitted := httptest.NewRequestWithContext(t.Context(), http.MethodPut, path, omittedBody)
	omitted.Header.Set("Authorization", "Bearer credential")
	omitted.Header.Set("Content-Type", omittedType)
	omitted.Header.Set("Idempotency-Key", "workspace-replace-no-version")
	omittedResponse := httptest.NewRecorder()
	api.ServeHTTP(omittedResponse, omitted)
	require.Equal(t, http.StatusBadRequest, omittedResponse.Code, "omitted status=%d body=%s", omittedResponse.Code, omittedResponse.Body.String())
	require.Empty(t, f.workspaceStore.replacements, "a replacement without a content version reached the Starter Workspace Store")

	explicitBody, explicitType := examResourceMultipart(t, `{"expected_revision":1,"expected_content_version":"`+f.file.Object.ContentVersion.String()+`","media_type":"text/plain","size":0,"sha256":"`+checksum+`"}`, nil, false)
	explicit := httptest.NewRequestWithContext(t.Context(), http.MethodPut, path, explicitBody)
	explicit.Header.Set("Authorization", "Bearer credential")
	explicit.Header.Set("Content-Type", explicitType)
	explicit.Header.Set("Idempotency-Key", "workspace-replace-version")
	explicitResponse := httptest.NewRecorder()
	api.ServeHTTP(explicitResponse, explicit)
	require.Equal(t, http.StatusOK, explicitResponse.Code, "explicit status=%d body=%s", explicitResponse.Code, explicitResponse.Body.String())
	require.Len(t, f.workspaceStore.replacements, 1, "the replacement never reached the Starter Workspace Store")
	require.Equal(t, f.file.Object.ContentVersion, f.workspaceStore.replacements[0].ExpectedContentVersion)
}

func TestExamStarterWorkspaceHTTPDirectoryAndMoveUseStrictJSON(t *testing.T) {
	t.Parallel()
	f, api := newStarterWorkspaceAPI(t)
	base := "/api/v1/exams/" + f.examID.String() + "/draft/starter-workspace"
	create := httptest.NewRequestWithContext(t.Context(), http.MethodPost, base+"/directories", strings.NewReader(`{"expected_revision":1,"path":"cmd"}`))
	create.Header.Set("Authorization", "Bearer credential")
	create.Header.Set("Content-Type", "application/json")
	create.Header.Set("Idempotency-Key", "workspace-directory")
	created := httptest.NewRecorder()
	api.ServeHTTP(created, create)
	require.Equal(t, http.StatusCreated, created.Code, "create=%d body=%s", created.Code, created.Body.String())
	require.Len(t, f.workspaceStore.directories, 1, "the directory never reached the Starter Workspace Store")
	require.Equal(t, "cmd", f.workspaceStore.directories[0].Path)
	move := httptest.NewRequestWithContext(t.Context(), http.MethodPatch, base+"/entries/"+f.file.Entry.ID.String(), strings.NewReader(`{"expected_revision":2,"path":"src/main.go","unexpected":true}`))
	move.Header.Set("Authorization", "Bearer credential")
	move.Header.Set("Content-Type", "application/json")
	move.Header.Set("Idempotency-Key", "workspace-move")
	moved := httptest.NewRecorder()
	api.ServeHTTP(moved, move)
	require.Equal(t, http.StatusBadRequest, moved.Code, "strict move=%d body=%s", moved.Code, moved.Body.String())
	require.Empty(t, f.workspaceStore.moves, "a move with an unknown field reached the Starter Workspace Store")
}

func TestExamStarterWorkspaceHTTPContentIsInlinePrivateAndConditional(t *testing.T) {
	t.Parallel()
	f, api := newStarterWorkspaceAPI(t)
	path := "/api/v1/exams/" + f.examID.String() + "/draft/starter-workspace/files/" + f.file.Entry.ID.String() + "/content"
	request := httptest.NewRequestWithContext(t.Context(), http.MethodGet, path, nil)
	request.Header.Set("Authorization", "Bearer credential")
	response := httptest.NewRecorder()
	api.ServeHTTP(response, request)
	etag := `"` + f.file.Object.SHA256 + `"`
	require.Equal(t, http.StatusOK, response.Code, "response=%d headers=%v body", response.Code, response.Header())
	require.Equal(t, "protected code", response.Body.String(), "response=%d headers=%v body", response.Code, response.Header())
	require.Empty(t, response.Header().Get("Content-Disposition"), "response=%d headers=%v body", response.Code, response.Header())
	require.Contains(t, response.Header().Values("Content-Security-Policy"), "sandbox", "authored content renders with an opaque origin")
	require.Equal(t, "private, no-store", response.Header().Get("Cache-Control"), "response=%d headers=%v body", response.Code, response.Header())
	require.Equal(t, etag, response.Header().Get("ETag"), "response=%d headers=%v body", response.Code, response.Header())
	require.Len(t, response.Header().Values("X-Content-Type-Options"), 1, "response=%d headers=%v body", response.Code, response.Header())
	require.Equal(t, "nosniff", response.Header().Get("X-Content-Type-Options"), "response=%d headers=%v body", response.Code, response.Header())
	conditional := httptest.NewRequestWithContext(t.Context(), http.MethodGet, path, nil)
	conditional.Header.Set("Authorization", "Bearer credential")
	conditional.Header.Set("If-None-Match", etag)
	notModified := httptest.NewRecorder()
	api.ServeHTTP(notModified, conditional)
	require.Equal(t, http.StatusNotModified, notModified.Code, "conditional=%d body=%q", notModified.Code, notModified.Body.String())
	require.Equal(t, 0, notModified.Body.Len(), "conditional=%d body=%q", notModified.Code, notModified.Body.String())
}

func TestExamStarterWorkspaceHTTPRemoveReturnsNoContent(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name, body, key string
		recursive       bool
	}{
		{name: "entry", body: `{"expected_revision":1}`, key: "workspace-remove"},
		{name: "recursive", body: `{"expected_revision":1,"recursive":true}`, key: "workspace-recursive-remove", recursive: true},
	} {
		// Each removal starts from a Draft that still holds the file.
		f, api := newStarterWorkspaceAPI(t)
		path := "/api/v1/exams/" + f.examID.String() + "/draft/starter-workspace/entries/" + f.file.Entry.ID.String()
		request := httptest.NewRequestWithContext(t.Context(), http.MethodDelete, path, strings.NewReader(test.body))
		request.Header.Set("Authorization", "Bearer credential")
		request.Header.Set("Content-Type", "application/json")
		request.Header.Set("Idempotency-Key", test.key)
		response := httptest.NewRecorder()
		api.ServeHTTP(response, request)
		require.Equal(t, http.StatusNoContent, response.Code, "%s status=%d body=%q", test.name, response.Code, response.Body.String())
		require.Equal(t, 0, response.Body.Len(), "%s status=%d body=%q", test.name, response.Code, response.Body.String())
		require.Len(t, f.workspaceStore.removals, 1, "%s removal never reached the Starter Workspace Store", test.name)
		removal := f.workspaceStore.removals[0]
		require.Equal(t, f.file.Entry.ID, removal.EntryID, test.name)
		require.Equal(t, test.recursive, removal.Recursive, test.name)
		require.Equal(t, int64(1), removal.ExpectedDraftRevision, test.name)
		require.Equal(t, sha256.Sum256([]byte(test.key)), f.workspaceStore.lastCommand(t).KeyDigest, test.name)
	}
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
