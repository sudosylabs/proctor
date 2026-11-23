//go:build integration

// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package integration_test

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/sudosylabs/proctor/server/app/identity"
	"github.com/sudosylabs/proctor/server/app/invocation"
	"github.com/sudosylabs/proctor/server/config"
	"github.com/sudosylabs/proctor/server/model"
	"github.com/sudosylabs/proctor/server/store"
	"github.com/sudosylabs/proctor/server/testlib"
)

func TestAuditExportHTTPBuildAndDownload(t *testing.T) {
	dataSource := os.Getenv("PROCTOR_TEST_DATABASE_URL")
	require.NotEmpty(t, dataSource)
	persistence := openAuthenticationStore(t, dataSource)
	helper := testlib.Setup(t, testlib.WithStore(persistence), testlib.WithConfig(func(cfg *config.Config) {
		cfg.Authentication.MFA.Enabled = true
		cfg.Authentication.MFA.EncryptionKey = base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{19}, 32))
	}))
	handler := helper.Handler()
	const user, password = "audit-export-admin", "correct horse battery staple"
	bootstrap := performJSONRequest(t, handler, http.MethodPost, "/api/v1/bootstrap", map[string]any{
		"bootstrap_secret": testlib.BootstrapSecret,
		"institution":      map[string]any{"name": "audit-export-test", "display_name": "Audit Export Test"},
		"administrator":    map[string]any{"username": user, "email": user + "@example.edu"},
		"password":         password,
	}, "")
	require.Equal(t, http.StatusCreated, bootstrap.Code, bootstrap.Body.String())
	login, err := helper.App.Identity.Login(t.Context(), invocation.Invocation{}, identity.LoginCommand{
		LoginID: user, Password: password, ClientType: model.SessionClientCLI, DeviceID: "audit-export-cli", Source: "127.0.0.1:1",
	})
	require.NoError(t, err)
	token := login.Tokens.AccessToken
	strengthenRoleAdministratorSession(t, handler, token)

	request := func(method, path, key, body string) *httptest.ResponseRecorder {
		req := httptest.NewRequestWithContext(t.Context(), method, path, bytes.NewBufferString(body))
		req.Header.Set("Authorization", "Bearer "+token)
		if body != "" {
			req.Header.Set("Content-Type", "application/json")
			req.Header.Set("Idempotency-Key", key)
		}
		out := httptest.NewRecorder()
		handler.ServeHTTP(out, req)
		return out
	}
	policy := request(http.MethodPut, "/api/v1/retention-policy", "audit-export-policy",
		`{"expected_revision":1,"submission_retention_days":730,"integrity_retention_days":365,"browser_activity_retention_days":0,"security_operational_retention_days":0,"audit_retention_days":0,"export_retention_days":3,"deletion_grace_days":30}`)
	require.Equal(t, http.StatusOK, policy.Code, policy.Body.String())

	const path = "/api/v1/audits/exports"
	creation := request(http.MethodPost, path, "audit-export", `{"format":"jsonl","action":["retention_policy.manage"],"kind":"change","status":"success"}`)
	require.Equal(t, http.StatusAccepted, creation.Code, creation.Body.String())
	var export struct {
		ID    model.AuditExportID    `json:"id"`
		JobID model.JobID            `json:"job_id"`
		State model.AuditExportState `json:"state"`
	}
	require.NoError(t, json.Unmarshal(creation.Body.Bytes(), &export))
	require.True(t, export.ID.IsValid())
	require.True(t, export.JobID.IsValid())
	require.Equal(t, model.ExamExportQueued, export.State)

	require.NoError(t, helper.App.JobEngine().Start(t.Context()))
	require.EventuallyWithT(t, func(c *assert.CollectT) {
		status := request(http.MethodGet, path+"/"+export.ID.String(), "", "")
		require.Equal(c, http.StatusOK, status.Code, status.Body.String())
		require.NoError(c, json.Unmarshal(status.Body.Bytes(), &export))
		require.Equal(c, model.ExamExportReady, export.State)
	}, 15*time.Second, 100*time.Millisecond)

	download := request(http.MethodGet, path+"/"+export.ID.String()+"/download", "", "")
	require.Equal(t, http.StatusOK, download.Code, download.Body.String())
	require.Equal(t, "application/jsonl", download.Header().Get("Content-Type"))
	require.Equal(t, "private, no-store", download.Header().Get("Cache-Control"))
	records := json.NewDecoder(download.Body)
	require.True(t, records.More(), "the successful policy change must appear in the file")
	for records.More() {
		var record struct {
			Action string `json:"action"`
			Status string `json:"status"`
		}
		require.NoError(t, records.Decode(&record))
		require.Equal(t, string(model.ActionRetentionPolicyManage), record.Action)
		require.Equal(t, string(model.AuditStatusSuccess), record.Status)
	}
	audits, err := persistence.Audit().List(t.Context(), store.AuditListOptions{
		Actions: []string{string(model.ActionAuditView)}, Limit: 30,
		Status:     model.AuditStatusSuccess,
		Visibility: store.AuditVisibilityScope{InstitutionWide: true},
	})
	require.NoError(t, err)
	require.Condition(t, func() bool {
		for _, event := range audits {
			if bytes.Contains(event.Parameters, []byte("create_audit_export")) &&
				bytes.Contains(event.Parameters, []byte(export.ID.String())) {
				return true
			}
		}
		return false
	}, "creating the export must commit its audit record")

}
