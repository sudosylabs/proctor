// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package httpapi

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/sudosylabs/proctor/server/httpapi/route/routetest"
	"github.com/sudosylabs/proctor/server/model"
)

func TestExamRevisionContentHTTPReadsTheFrozenRevision(t *testing.T) {
	t.Parallel()
	logger, _ := routetest.NewLogger(t)
	principal := testExamHTTPPrincipal()
	f := newExamRevisionFixture(t, principal)
	policy, err := model.NewExamRevisionPolicy(model.DefaultExamPolicySet())
	require.NoError(t, err)
	digest := func(value string) string { sum := sha256.Sum256([]byte(value)); return hex.EncodeToString(sum[:]) }
	resource := model.ExamRevisionResource{
		ResourceID: model.NewExamResourceID(), FileEntryID: model.NewFileEntryID(), FileRevisionID: model.NewFileRevisionID(),
		RenditionID: model.NewFileRenditionID(), DisplayName: "Reference", MediaType: model.ExamResourceMediaText,
		SizeBytes: 9, SHA256: digest("reference"),
	}
	file := model.ExamRevisionStarterWorkspaceEntry{
		EntryID: model.NewStarterWorkspaceEntryID(), Kind: model.StarterWorkspaceEntryFile, Path: "main.go",
		ObjectID: model.NewStarterWorkspaceObjectID(), ContentVersion: model.NewWorkspaceContentVersion(),
		MediaType: "text/x-go", SizeBytes: 12, SHA256: digest("package main"),
	}
	f.revisions.snapshot, err = model.NewExamRevision(model.ExamRevisionSpecification{
		ID: f.revisions.summary.ID, ExamID: f.examID, Number: 3, SourceDraftRevision: 4, Title: "Distributed Systems",
		InstructionsMarkdown: "Begin.", Policy: policy, BrowserPolicy: model.DisabledBrowserPolicy(),
		Capacity: model.DefaultExamCapacityPolicy(), Resources: []model.ExamRevisionResource{resource},
		StarterWorkspace:  []model.ExamRevisionStarterWorkspaceEntry{file},
		PublishedByUserID: principal.UserID, PublishedAt: f.revisions.summary.PublishedAt, Kind: model.ExamRevisionPublicationStandard,
	})
	require.NoError(t, err)
	f.revisionFiles.bytes[resource.RenditionID.String()] = []byte("reference")
	f.revisionFiles.bytes[file.ObjectID.String()] = []byte("package main")
	httpAPI := newFocusedResourceAPI(t, logger, routetest.Authenticator{Principal: principal}, examRevisionResource(f.publication, f.people))
	base := "/api/v1/exams/" + f.examID.String() + "/revisions/" + f.revisions.summary.ID.String()
	get := func(path string) *httptest.ResponseRecorder {
		request := httptest.NewRequestWithContext(t.Context(), http.MethodGet, base+path, nil)
		request.Header.Set("Authorization", "Bearer credential")
		response := httptest.NewRecorder()
		httpAPI.ServeHTTP(response, request)
		return response
	}

	content := get("/content")
	require.Equal(t, http.StatusOK, content.Code, "body = %s", content.Body.String())
	var text examRevisionContentResponse
	require.NoError(t, json.Unmarshal(content.Body.Bytes(), &text))
	require.Equal(t, "Begin.", text.InstructionsMarkdown)
	require.Equal(t, int64(3), text.Number)

	resources := get("/resources")
	require.Equal(t, http.StatusOK, resources.Code, "body = %s", resources.Body.String())
	require.Contains(t, resources.Body.String(), resource.ResourceID.String())

	bytes := get("/resources/" + resource.ResourceID.String() + "/content")
	require.Equal(t, http.StatusOK, bytes.Code, "body = %s", bytes.Body.String())
	require.Equal(t, "reference", bytes.Body.String())

	workspace := get("/starter-workspace")
	require.Equal(t, http.StatusOK, workspace.Code, "body = %s", workspace.Body.String())
	require.Contains(t, workspace.Body.String(), `"path":"main.go"`)

	starter := get("/starter-workspace/files/" + file.EntryID.String() + "/content")
	require.Equal(t, http.StatusOK, starter.Code, "body = %s", starter.Body.String())
	require.Equal(t, "package main", starter.Body.String())
}
