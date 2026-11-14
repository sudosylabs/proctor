// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"testing/fstest"

	"github.com/stretchr/testify/require"

	"github.com/sudosylabs/proctor/server/httpapi/route"
	"github.com/sudosylabs/proctor/server/internal/openapidoc"
	"github.com/sudosylabs/proctor/server/plugin"
)

// executionStatusFragment documents the one route the test plugin serves.
const executionStatusFragment = `
paths:
  /api/v1/plugins/execution/exam-attempts/{exam_attempt_id}/status:
    parameters:
      - $ref: '#/components/parameters/ExamAttemptID'
    get:
      operationId: getExecutionEnvironmentStatus
      summary: Get the execution environment status of a candidate Attempt
      description: Reports whether the candidate's execution environment is ready.
      tags:
        - Attempts and workspaces
      x-proctor-auth: session_required
      x-proctor-idempotency: none
      x-proctor-error-codes:
        - request.invalid
        - resource.not_found
        - exam.attempt.invalid
        - exam.attempt.unavailable
        - exam.attempt.registered_desktop_required
        - plugin.route.rate_limited
        - plugin.route.unavailable
        - plugin.route.conflict
        - authentication.required
        - authentication.token_invalid
        - authentication.credential_ambiguous
        - authentication.dpop.invalid
        - authentication.dpop.replayed
        - authentication.dpop.use_nonce
        - authentication.dpop.unavailable
      security:
        - bearerAuth: []
        - dpopAuth: []
        - sessionCookie: []
      parameters:
        - $ref: '#/components/parameters/CandidateAttemptCredential'
        - $ref: '#/components/parameters/CandidateAttemptConnectionID'
      responses:
        '204':
          description: The environment is ready.
        '400':
          $ref: '#/components/responses/BadRequest'
        '401':
          $ref: '#/components/responses/Unauthorized'
        '403':
          $ref: '#/components/responses/Forbidden'
        '404':
          $ref: '#/components/responses/NotFound'
        '409':
          $ref: '#/components/responses/Conflict'
        '429':
          $ref: '#/components/responses/TooManyRequests'
        '503':
          $ref: '#/components/responses/ServiceUnavailable'
`

func openAPISourceDirectory(t *testing.T) string {
	t.Helper()
	_, currentFile, _, ok := runtime.Caller(0)
	require.True(t, ok)
	return filepath.Join(filepath.Dir(currentFile), "..", "openapi")
}

// TestPluginOpenAPIAgreesWithMountedRoutes compiles the contract with a
// plugin's fragment, as the build does for a compiled plugin, and holds the
// plugin's mounted routes to it like any catalog route.
func TestPluginOpenAPIAgreesWithMountedRoutes(t *testing.T) {
	t.Parallel()

	compiled, err := openapidoc.Compile(os.DirFS(openAPISourceDirectory(t)), openapidoc.PluginSource{
		ID:    "execution",
		Files: fstest.MapFS{"status.yaml": {Data: []byte(executionStatusFragment)}},
	})
	require.NoError(t, err)
	var document openAPIDocument
	require.NoError(t, json.Unmarshal(compiled, &document))
	var operation openAPIOperation
	require.NoError(t, json.Unmarshal(document.Paths["/api/v1/plugins/execution/exam-attempts/{exam_attempt_id}/status"]["get"], &operation))
	require.Equal(t, "execution", operation.Plugin, "the build marks a plugin's operations as optional")

	source := &fakePluginRoutes{routes: []PluginRoute{executionRoute(plugin.Route{
		Method: http.MethodGet, Path: "status", Guard: plugin.GuardCandidateAttemptAccess,
		Handler: func(context.Context, plugin.Access, plugin.Request) (plugin.Response, error) {
			return plugin.Response{Status: http.StatusNoContent}, nil
		},
	})}}
	settings := validHTTPSettings(t)
	settings.PluginRoutes = source
	settings.PluginSockets = hubPluginSockets{}
	api, err := New(completeHTTPApplication(), settings)
	require.NoError(t, err)
	require.Len(t, api.LiveRoutes(), 1)

	requireOpenAPIAgreement(t, document, api.Routes(), map[string][]route.Entry{"execution": api.LiveRoutes()})
}
