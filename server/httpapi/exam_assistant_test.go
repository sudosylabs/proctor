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
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/sudosylabs/proctor/server/httpapi/route/routetest"
	"github.com/sudosylabs/proctor/server/model"
)

func TestExamAssistantHTTPStrictBoundary(t *testing.T) {
	principal := testExamHTTPPrincipal()
	policy := model.DefaultAssistantPolicy()
	policy.Enabled = true
	policy.ProfileID = model.NewAssistantProfileID()
	policy.Level = model.AssistantLevelTutor
	policy.Budgets = model.AssistantBudgets{Turns: 10, InputTokens: 10000, OutputTokens: 1000, CommandTimeoutSeconds: 60}
	valid, err := json.Marshal(configureExamDraftAssistantRequest{ExpectedDraftRevision: 1, Assistant: policy})
	require.NoError(t, err)
	for name, body := range map[string]string{
		"valid":      string(valid),
		"case alias": strings.Replace(string(valid), `"enabled":`, `"ENABLED":`, 1),
		"duplicate":  strings.Replace(string(valid), `"enabled":true,`, `"enabled":true,"enabled":true,`, 1), "missing assistant": `{"expected_revision":1}`,
		"null":            `{"expected_revision":1,"assistant":null}`,
		"missing budgets": strings.Replace(string(valid), `"turns":10,`, "", 1),
		"unknown field":   strings.Replace(string(valid), `"enabled":true,`, `"enabled":true,"extra":true,`, 1),
	} {
		t.Run(name, func(t *testing.T) {
			logger, _ := routetest.NewLogger(t)
			f := newExamFixture(t, principal)
			api := newFocusedResourceAPI(t, logger, routetest.Authenticator{Principal: principal}, examResource(f.authoring, f.people))
			req := httptest.NewRequestWithContext(t.Context(), http.MethodPut, "/api/v1/exams/"+f.examID.String()+"/draft/assistant", bytes.NewBufferString(body))
			req.Header.Set("Authorization", "Bearer credential")
			req.Header.Set("Content-Type", "application/json")
			req.Header.Set("Idempotency-Key", "assistant-policy-key")
			response := httptest.NewRecorder()
			api.ServeHTTP(response, req)
			want := http.StatusBadRequest
			if name == "valid" {
				want = http.StatusOK
			}
			require.Equal(t, want, response.Code, response.Body.String())
			require.Equal(t, name == "valid", len(f.exams.assistantUpdates) == 1)
			if name == "valid" {
				require.Equal(t, policy, f.exams.assistantUpdates[0].Assistant)
			}
		})
	}
}
