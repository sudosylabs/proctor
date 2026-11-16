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
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/sudosylabs/proctor/server/httpapi/route/routetest"
	"github.com/sudosylabs/proctor/server/model"
)

// newExamResourceAPI serves the Exam Resource routes over a fresh fixture, as
// its caller.
func newExamResourceAPI(t *testing.T) (*examContentFixture, http.Handler) {
	t.Helper()
	logger, _ := routetest.NewLogger(t)
	principal := testExamHTTPPrincipal()
	f := newExamContentFixture(t, principal)
	return f, newFocusedResourceAPI(t, logger, routetest.Authenticator{Principal: principal}, examResourceResource(f.resourceService))
}

func TestExamResourceHTTPUploadUsesStrictMetadataFirstStreamingContract(t *testing.T) {
	t.Parallel()
	f, api := newExamResourceAPI(t)
	raw := []byte("# Notes")
	checksum := fmt.Sprintf("%x", sha256.Sum256(raw))
	body, contentType := examResourceMultipart(t, `{"expected_revision":1,"display_name":"Reference","description_markdown":"Read **this**.","media_type":"text/markdown","size":7,"sha256":"`+checksum+`"}`, raw, false)
	request := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/api/v1/exams/"+f.examID.String()+"/draft/resources", body)
	request.Header.Set("Authorization", "Bearer credential")
	request.Header.Set("Content-Type", contentType)
	request.Header.Set("Idempotency-Key", "resource-once")
	response := httptest.NewRecorder()
	api.ServeHTTP(response, request)
	require.Equal(t, http.StatusCreated, response.Code, "status=%d body=%s", response.Code, response.Body.String())
	require.Equal(t, [][]byte{raw}, f.resourceFiles.stored, "the uploaded bytes reached the file store unchanged")
	require.Len(t, f.resourceStore.finalizations, 1, "the upload never reached the Exam Resource Store")
	finalization := f.resourceStore.finalizations[0]
	require.Equal(t, checksum, finalization.Rendition.SHA256)
	require.Equal(t, string(model.ExamResourceMediaMarkdown), finalization.Rendition.MediaType)
	require.Equal(t, "Reference", finalization.Resource.DisplayName)
	require.Equal(t, "Read **this**.", finalization.Resource.DescriptionMarkdown)
	require.Equal(t, sha256.Sum256([]byte("resource-once")), f.resourceStore.lastCommand(t).KeyDigest)
	require.False(t, bytes.Contains(response.Body.Bytes(), []byte(`"file_entry"`)))
	require.False(t, bytes.Contains(response.Body.Bytes(), []byte(`"path"`)))
	require.False(t, bytes.Contains(response.Body.Bytes(), []byte(`"key"`)))
	require.False(t, bytes.Contains(response.Body.Bytes(), []byte(`"url"`)))
}

func TestExamResourceHTTPRejectsTrailingMultipartPartBeforeApplicationCommit(t *testing.T) {
	t.Parallel()
	f, api := newExamResourceAPI(t)
	raw := []byte("notes")
	checksum := fmt.Sprintf("%x", sha256.Sum256(raw))
	body, contentType := examResourceMultipart(t, `{"expected_revision":1,"display_name":"Reference","description_markdown":"","media_type":"text/plain","size":5,"sha256":"`+checksum+`"}`, raw, true)
	request := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/api/v1/exams/"+f.examID.String()+"/draft/resources", body)
	request.Header.Set("Authorization", "Bearer credential")
	request.Header.Set("Content-Type", contentType)
	request.Header.Set("Idempotency-Key", "resource-once")
	response := httptest.NewRecorder()
	api.ServeHTTP(response, request)
	require.Equal(t, http.StatusBadRequest, response.Code, "status=%d body=%s", response.Code, response.Body.String())
	require.Empty(t, f.resourceFiles.stored, "content followed by another part was stored")
	require.Empty(t, f.resourceStore.finalizations, "content followed by another part was committed")
}

func TestExamResourceHTTPRejectsDuplicateOrMissingMultipartMetadata(t *testing.T) {
	t.Parallel()
	checksum := fmt.Sprintf("%x", sha256.Sum256([]byte("notes")))
	for _, test := range []struct {
		name     string
		metadata string
	}{
		{name: "duplicate expected revision", metadata: `{"expected_revision":1,"expected_revision":2,"display_name":"Reference","media_type":"text/plain","size":5,"sha256":"` + checksum + `"}`},
		{name: "duplicate checksum", metadata: `{"expected_revision":1,"display_name":"Reference","media_type":"text/plain","size":5,"sha256":"` + checksum + `","sha256":"` + checksum + `"}`},
		{name: "missing size", metadata: `{"expected_revision":1,"display_name":"Reference","media_type":"text/plain","sha256":"` + checksum + `"}`},
	} {
		t.Run(test.name, func(t *testing.T) {
			f, api := newExamResourceAPI(t)
			body, contentType := examResourceMultipart(t, test.metadata, []byte("notes"), false)
			request := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/api/v1/exams/"+f.examID.String()+"/draft/resources", body)
			request.Header.Set("Authorization", "Bearer credential")
			request.Header.Set("Content-Type", contentType)
			request.Header.Set("Idempotency-Key", "resource-once")
			response := httptest.NewRecorder()
			api.ServeHTTP(response, request)
			require.Equal(t, http.StatusBadRequest, response.Code, "status=%d body=%s", response.Code, response.Body.String())
			require.Empty(t, f.resourceStore.reservations, "a rejected upload reached the Exam Resource Store")
			require.Empty(t, f.resourceFiles.stored, "a rejected upload reached the file store")
		})
	}
}

func TestExamResourceHTTPAcceptsExplicitZeroSizeText(t *testing.T) {
	t.Parallel()
	f, api := newExamResourceAPI(t)
	checksum := fmt.Sprintf("%x", sha256.Sum256(nil))
	body, contentType := examResourceMultipart(t, `{"expected_revision":1,"display_name":"Empty notes","media_type":"text/plain","size":0,"sha256":"`+checksum+`"}`, nil, false)
	request := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/api/v1/exams/"+f.examID.String()+"/draft/resources", body)
	request.Header.Set("Authorization", "Bearer credential")
	request.Header.Set("Content-Type", contentType)
	request.Header.Set("Idempotency-Key", "resource-empty")
	response := httptest.NewRecorder()
	api.ServeHTTP(response, request)
	require.Equal(t, http.StatusCreated, response.Code, "status=%d body=%s", response.Code, response.Body.String())
	require.Len(t, f.resourceStore.finalizations, 1, "the empty upload never reached the Exam Resource Store")
	require.Equal(t, int64(0), f.resourceStore.finalizations[0].Rendition.Size)
	require.Equal(t, [][]byte{{}}, f.resourceFiles.stored, "the file store read an empty body")
}

func TestExamResourceHTTPMetadataPatchIsPresenceAware(t *testing.T) {
	t.Parallel()
	f, api := newExamResourceAPI(t)
	path := "/api/v1/exams/" + f.examID.String() + "/draft/resources/" + f.resource.Resource.ID.String()
	request := httptest.NewRequestWithContext(t.Context(), http.MethodPatch, path, strings.NewReader(`{"expected_revision":1,"description_markdown":""}`))
	request.Header.Set("Authorization", "Bearer credential")
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Idempotency-Key", "resource-metadata")
	response := httptest.NewRecorder()
	api.ServeHTTP(response, request)
	require.Equal(t, http.StatusOK, response.Code, "status=%d body=%s", response.Code, response.Body.String())
	require.Len(t, f.resourceStore.metadataUpdates, 1, "the edit never reached the Exam Resource Store")
	// The explicit empty description replaces the seeded "Old notes"; the
	// omitted display name keeps the value the Resource already had.
	update := f.resourceStore.metadataUpdates[0]
	require.Equal(t, f.resource.Resource.DisplayName, update.DisplayName)
	require.Empty(t, update.DescriptionMarkdown)

	omitted := httptest.NewRequestWithContext(t.Context(), http.MethodPatch, path, strings.NewReader(`{"expected_revision":1}`))
	omitted.Header = request.Header.Clone()
	omittedResponse := httptest.NewRecorder()
	api.ServeHTTP(omittedResponse, omitted)
	require.Equal(t, http.StatusBadRequest, omittedResponse.Code, "omitted status=%d body=%s", omittedResponse.Code, omittedResponse.Body.String())
	require.Len(t, f.resourceStore.metadataUpdates, 1, "an edit naming nothing reached the Exam Resource Store")
}

func TestExamResourceHTTPRemoveReturnsNoContent(t *testing.T) {
	t.Parallel()
	f, api := newExamResourceAPI(t)
	path := "/api/v1/exams/" + f.examID.String() + "/draft/resources/" + f.resource.Resource.ID.String()
	request := httptest.NewRequestWithContext(t.Context(), http.MethodDelete, path, strings.NewReader(`{"expected_revision":1}`))
	request.Header.Set("Authorization", "Bearer credential")
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Idempotency-Key", "resource-remove")
	response := httptest.NewRecorder()
	api.ServeHTTP(response, request)
	require.Equal(t, http.StatusNoContent, response.Code, "status=%d body=%q", response.Code, response.Body.String())
	require.Equal(t, 0, response.Body.Len(), "status=%d body=%q", response.Code, response.Body.String())
	require.Len(t, f.resourceStore.removals, 1, "the removal never reached the Exam Resource Store")
	require.Equal(t, f.resource.Resource.ID, f.resourceStore.removals[0].ResourceID)
	require.Empty(t, f.resourceStore.records, "the Resource is gone from the Draft")
}

func TestExamResourceHTTPProtectedContentIsInlinePrivateAndConditional(t *testing.T) {
	t.Parallel()
	f, api := newExamResourceAPI(t)
	path := "/api/v1/exams/" + f.examID.String() + "/draft/resources/" + f.resource.Resource.ID.String() + "/content"
	request := httptest.NewRequestWithContext(t.Context(), http.MethodGet, path, nil)
	request.Header.Set("Authorization", "Bearer credential")
	response := httptest.NewRecorder()
	api.ServeHTTP(response, request)
	etag := `"` + f.resource.Rendition.SHA256 + `"`
	require.Equal(t, http.StatusOK, response.Code, "response=%d headers=%v body", response.Code, response.Header())
	require.Equal(t, "protected", response.Body.String(), "response=%d headers=%v body", response.Code, response.Header())
	require.Empty(t, response.Header().Get("Content-Disposition"), "response=%d headers=%v body", response.Code, response.Header())
	require.Equal(t, "private, max-age=300", response.Header().Get("Cache-Control"), "response=%d headers=%v body", response.Code, response.Header())
	require.Equal(t, etag, response.Header().Get("ETag"), "response=%d headers=%v body", response.Code, response.Header())
	require.NotEmpty(t, response.Header().Get("Content-Security-Policy"), "response=%d headers=%v body", response.Code, response.Header())
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

func examResourceMultipart(t *testing.T, metadata string, content []byte, trailing bool) (*bytes.Buffer, string) {
	t.Helper()
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	part, err := writer.CreateFormField("metadata")
	require.NoError(t, err)
	_, _ = part.Write([]byte(metadata))
	contentPart, err := writer.CreateFormFile("content", "ignored.txt")
	require.NoError(t, err)
	_, _ = contentPart.Write(content)
	if trailing {
		extra, _ := writer.CreateFormField("unexpected")
		_, _ = extra.Write([]byte("value"))
	}
	err = writer.Close()
	require.NoError(t, err)
	return &body, writer.FormDataContentType()
}

func TestExamResourceOpenAPIDocument(t *testing.T) {
	t.Parallel()
	base := "/api/v1/exams/{exam_id}/draft/resources"
	member := base + "/{exam_resource_id}"
	content := member + "/content"

	document := readOpenAPIDocument(t)
	assertExamMultipartOperation(t, document, base, "post", "#/components/schemas/ExamResourceUploadMultipart")
	assertExamMultipartOperation(t, document, content, "put", "#/components/schemas/ExamResourceContentReplacementMultipart")
	assertProtectedExamContentResponse(t, document, content, "ExamResourceProtectedContent")
	assertProtectedExamCacheControl(t, "ExamResourceProtectedContent", "private, max-age=300")
	_, ok := document.Components.Responses["ExamResourceProtectedContent"].Headers["Content-Disposition"]
	assert.False(t, ok, "GET %s protected response documents forbidden Content-Disposition", content)
	operation := decodeExamContentOpenAPIOperation(t, document, content, "get")
	response304 := operation.Responses["304"]
	assert.Equal(t, "#/components/responses/ExamProtectedContentNotModified", response304.Ref, "GET %s 304 response = %#v", content, response304)
}

func assertExamMultipartOperation(t *testing.T, document openAPIDocument, path, method, schemaRef string) {
	t.Helper()
	operation := decodeExamContentOpenAPIOperation(t, document, path, method)
	shape, ok := operation.RequestBody.Content["multipart/form-data"]
	assert.True(t, ok, "%s %s has no multipart body", method, path)
	assert.Equal(t, schemaRef, shape.Schema.Ref, "%s %s multipart body", method, path)
	assert.Len(t, operation.RequestBody.Content, 1, "%s %s accepts more than a multipart body", method, path)
}

func assertProtectedExamContentResponse(t *testing.T, document openAPIDocument, path, responseName string) {
	t.Helper()
	operation := decodeExamContentOpenAPIOperation(t, document, path, "get")
	assert.True(t, hasOpenAPIHeaderParameter(operation, "If-None-Match"), "GET %s does not document If-None-Match", path)
	response := document.Components.Responses[responseName]
	shape, ok := response.Content["*/*"]
	assert.True(t, ok, "GET %s has no protected binary response", path)
	assert.Equal(t, "string", shape.Schema.Type, "GET %s protected binary response", path)
	assert.Equal(t, "binary", shape.Schema.Format, "GET %s protected binary response", path)
	for _, header := range []string{"ETag", "Cache-Control", "X-Content-Type-Options"} {
		_, ok := response.Headers[header]
		assert.True(t, ok, "GET %s protected response does not document %s", path, header)
	}
}

func decodeExamContentOpenAPIOperation(t *testing.T, document openAPIDocument, path, method string) openAPIOperation {
	t.Helper()
	var operation openAPIOperation
	require.NoError(t, json.Unmarshal(document.Paths[path][method], &operation), "decode %s %s", method, path)
	return operation
}

func assertProtectedExamCacheControl(t *testing.T, responseName, want string) {
	t.Helper()
	var document struct {
		Components struct {
			Responses map[string]struct {
				Headers map[string]struct {
					Schema struct {
						Const string `json:"const"`
					} `json:"schema"`
				} `json:"headers"`
			} `json:"responses"`
		} `json:"components"`
	}
	require.NoError(t, json.Unmarshal(readOpenAPIDocumentBytes(t), &document))
	assert.Equal(t, want, document.Components.Responses[responseName].Headers["Cache-Control"].Schema.Const, "%s Cache-Control", responseName)
}
