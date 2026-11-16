// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package httpapi

import (
	"crypto/sha256"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/sudosylabs/proctor/server/httpapi/route/routetest"
	"github.com/sudosylabs/proctor/server/model"
)

func TestRetentionPolicyHTTPRequiresCompleteSettingsAndNeverEnablesDeletion(t *testing.T) {
	t.Parallel()
	logger, _ := routetest.NewLogger(t)
	principal := administrationStrongPrincipal()
	fixture := newAdministrationFixture(t, institutionWide(principal.UserID, model.ActionRetentionPolicyView, model.ActionRetentionPolicyManage))
	policies := fixture.retentionPolicies
	policies.policy = model.NewInitialRetentionPolicy(fixture.institution().ID, time.Now())
	api := newFocusedResourceAPI(t, logger, routetest.Authenticator{Principal: principal}, retentionPolicyResource(fixture.service))
	serve := func(method, body string) *httptest.ResponseRecorder {
		request := httptest.NewRequestWithContext(t.Context(), method, "/api/v1/retention-policy", strings.NewReader(body))
		request.Header.Set("Authorization", "Bearer session")
		request.Header.Set("Content-Type", "application/json")
		if method == http.MethodPut {
			request.Header.Set("Idempotency-Key", "retention-policy-1")
		}
		response := httptest.NewRecorder()
		api.ServeHTTP(response, request)
		return response
	}
	const valid = `{"expected_revision":1,"submission_retention_days":730,"integrity_retention_days":365,"browser_activity_retention_days":0,"security_operational_retention_days":0,"audit_retention_days":0,"export_retention_days":7,"deletion_grace_days":30}`
	response := serve(http.MethodPut, valid)
	require.Equal(t, http.StatusOK, response.Code, "replace: %d %s", response.Code, response.Body.String())
	require.Equal(t, "private, no-store", response.Header().Get("Cache-Control"), "replace: %d %s", response.Code, response.Body.String())
	require.NotNil(t, policies.replacement, "the replacement never reached the Store")
	require.Equal(t, int64(1), policies.replacement.ExpectedRevision)
	require.Equal(t, sha256.Sum256([]byte("retention-policy-1")), policies.command.KeyDigest, "the Idempotency-Key did not key the replacement")
	require.Equal(t, model.RetentionPolicySettings{SubmissionRetentionDays: 730, IntegrityRetentionDays: 365,
		ExportRetentionDays: 7, DeletionGraceDays: 30}, policies.replacement.Settings)
	require.Equal(t, principal.UserID, policies.replacement.Principal.UserID)
	response = serve(http.MethodGet, "")
	var output map[string]any
	require.NoError(t, json.Unmarshal(response.Body.Bytes(), &output))
	require.Equal(t, http.StatusOK, response.Code, "get: %d %s", response.Code, response.Body.String())
	require.Equal(t, "private, no-store", response.Header().Get("Cache-Control"), "get: %d %s", response.Code, response.Body.String())
	require.Equal(t, false, output["automatic_deletion_enabled"], "get: %d %s", response.Code, response.Body.String())
	require.Equal(t, float64(1), output["revision"], "get: %d %s", response.Code, response.Body.String())
	for _, field := range []string{"created_at", "updated_at"} {
		value, ok := output[field].(string)
		require.True(t, ok, "missing time %s", field)
		_, err := time.Parse(time.RFC3339Nano, value)
		require.NoError(t, err)
	}
	for _, field := range []string{"submission_retention_days", "integrity_retention_days", "browser_activity_retention_days", "security_operational_retention_days", "audit_retention_days",
		"export_retention_days", "deletion_grace_days"} {
		for _, invalid := range []string{"missing", "null", "fraction", "string"} {
			t.Run(field+"/"+invalid, func(t *testing.T) {
				var body map[string]any
				require.NoError(t, json.Unmarshal([]byte(valid), &body))
				switch invalid {
				case "missing":
					delete(body, field)
				case "null":
					body[field] = nil
				case "fraction":
					body[field] = 1.5
				case "string":
					body[field] = "30"
				}
				encoded, err := json.Marshal(body)
				require.NoError(t, err)
				before := policies.callCount()
				invalidResponse := serve(http.MethodPut, string(encoded))
				require.Equal(t, http.StatusBadRequest, invalidResponse.Code, "invalid settings reached the Store: %d %s", invalidResponse.Code, invalidResponse.Body.String())
				require.Equal(t, before, policies.callCount(), "invalid settings reached the Store: %d %s", invalidResponse.Code, invalidResponse.Body.String())
			})
		}
	}
	before := policies.callCount()
	response = serve(http.MethodPut, strings.TrimSuffix(valid, "}")+`,"automatic_deletion_enabled":true}`)
	require.Equal(t, http.StatusBadRequest, response.Code, "deletion enable attempt reached the Store: %d %s", response.Code, response.Body.String())
	require.Equal(t, before, policies.callCount(), "deletion enable attempt reached the Store: %d %s", response.Code, response.Body.String())
}

func TestRetentionPolicyOpenAPIDocument(t *testing.T) {
	t.Parallel()

	document := readOpenAPIDocument(t)
	require.Equal(t, "#/components/headers/PrivateNoStore", document.Components.Responses["RetentionPolicyOK"].Headers["Cache-Control"].Ref, "RetentionPolicyOK must require private no-store")
	for _, name := range []string{"RetentionPolicyRequest", "RetentionPolicyResponse"} {
		for _, field := range []string{"submission_retention_days", "integrity_retention_days", "browser_activity_retention_days", "security_operational_retention_days", "audit_retention_days", "export_retention_days", "deletion_grace_days"} {
			var bounds struct {
				Minimum *int `json:"minimum"`
				Maximum *int `json:"maximum"`
			}
			require.NoError(t, json.Unmarshal(document.Components.Schemas[name].Properties[field], &bounds))
			maximum := 36500
			if field == "export_retention_days" {
				maximum = 7
			}
			require.NotNil(t, bounds.Minimum, "%s.%s must accept 0 through", name, field)
			require.Equal(t, 0, *bounds.Minimum, "%s.%s must accept 0 through", name, field)
			require.NotNil(t, bounds.Maximum, "%s.%s must accept 0 through", name, field)
			require.Equal(t, maximum, *bounds.Maximum, "%s.%s must accept 0 through", name, field)
		}
	}
}
