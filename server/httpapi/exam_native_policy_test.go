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

func TestExamNativePolicyHTTPStrictBoundary(t *testing.T) {
	principal := testExamHTTPPrincipal()
	valid, err := json.Marshal(configureExamDraftNativePolicyRequest{ExpectedDraftRevision: 1, Native: model.DefaultNativeSecurityPolicy()})
	require.NoError(t, err)
	for name, body := range map[string]string{"valid": string(valid), "missing native": `{"expected_revision":1}`, "null native": `{"expected_revision":1,"native":null}`, "case alias": strings.Replace(string(valid), `"native":`, `"NATIVE":`, 1), "extra": strings.Replace(string(valid), `"native":`, `"extra":1,"native":`, 1), "missing boolean": strings.Replace(string(valid), `"observe_os_clipboard_changes":false,`, "", 1), "external capture enabled": strings.Replace(string(valid), `"id":"external_capture","mode":"disabled"`, `"id":"external_capture","mode":"enforce"`, 1)} {
		t.Run(name, func(t *testing.T) {
			logger, _ := routetest.NewLogger(t)
			f := newExamFixture(t, principal)
			// The Draft observes clipboard changes, so the default policy the
			// valid request sends is a change the use case accepts.
			f.seeded().draft.Policy.Native = clipboardObservingNativePolicy(t)
			require.NoError(t, f.seeded().draft.Validate())
			api := newFocusedResourceAPI(t, logger, routetest.Authenticator{Principal: principal}, examResource(f.authoring))
			request := httptest.NewRequestWithContext(t.Context(), http.MethodPut, "/api/v1/exams/"+f.examID.String()+"/draft/policies/native", bytes.NewBufferString(body))
			request.Header.Set("Authorization", "Bearer credential")
			request.Header.Set("Content-Type", "application/json")
			request.Header.Set("Idempotency-Key", "native-policy-key")
			response := httptest.NewRecorder()
			api.ServeHTTP(response, request)
			want := http.StatusBadRequest
			if name == "valid" {
				want = http.StatusOK
			}
			require.Equal(t, want, response.Code, "status = %d: %s", response.Code, response.Body.String())
			require.Equal(t, name == "valid", len(f.exams.nativePolicyUpdates) == 1, "invalid command crossed boundary")
		})
	}
}

// clipboardObservingNativePolicy is the default Native policy with the
// clipboard family observing OS clipboard changes.
func clipboardObservingNativePolicy(t *testing.T) model.NativeSecurityPolicy {
	t.Helper()
	encoded, err := json.Marshal(model.DefaultNativeSecurityPolicy())
	require.NoError(t, err)
	disabled := `"id":"clipboard","mode":"disabled","observe_os_clipboard_changes":false`
	require.Contains(t, string(encoded), disabled)
	observing := strings.Replace(string(encoded), disabled, `"id":"clipboard","mode":"observe","observe_os_clipboard_changes":true`, 1)
	var policy model.NativeSecurityPolicy
	require.NoError(t, json.Unmarshal([]byte(observing), &policy))
	return policy
}
