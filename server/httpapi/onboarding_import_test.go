// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package httpapi

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/sudosylabs/proctor/server/app"
	"github.com/sudosylabs/proctor/server/model"
)

func TestOnboardingImportResponseExcludesPrivateRowAndCredentialMaterial(t *testing.T) {
	t.Parallel()
	at := time.Date(2026, 8, 20, 9, 0, 0, 0, time.UTC)
	view := app.OnboardingImportView{ID: model.NewOnboardingImportID(), Mode: model.OnboardingImportInstitution,
		State: model.OnboardingImportPreviewReady, ScopeType: model.RoleScopeInstitution, ScopeID: model.NewInstitutionID().String(),
		ParseJobID: model.NewJobID(), CreatedAt: at, UpdatedAt: at, ExpiresAt: at.Add(7 * 24 * time.Hour), Revision: 2, TotalRows: 1, ValidRows: 1}
	rows := []app.OnboardingImportRowView{{RowNumber: 1, Reference: "safe-row", Operation: "institution_role.create", Status: model.OnboardingImportRowValid}}
	encoded, err := json.Marshal(onboardingImportResponseFromView(view, rows))
	require.NoError(t, err)
	body := string(encoded)
	requireNoPrivateFieldNames(t, encoded, "target_email", "email", "claim", "hash", "recipient", "secret")
	require.Contains(t, body, `"reference":"safe-row"`)
	require.Contains(t, body, `"status":"valid"`)
}

func TestOnboardingImportListParsesFiltersAndPagesTheEnvelope(t *testing.T) {
	t.Parallel()
	at := time.Date(2026, 8, 20, 9, 0, 0, 0, time.UTC)
	newest := app.OnboardingImportView{ID: model.NewOnboardingImportID(), Mode: model.OnboardingImportInstitution,
		State: model.OnboardingImportCompleted, ScopeType: model.RoleScopeInstitution, ScopeID: model.NewInstitutionID().String(),
		ParseJobID: model.NewJobID(), CreatedAt: at, UpdatedAt: at, ExpiresAt: at.Add(7 * 24 * time.Hour), Revision: 3}
	older := newest
	older.ID, older.CreatedAt = model.NewOnboardingImportID(), at.Add(-time.Minute)
	fake := &recordingOnboardingImportApplication{listPage: app.Page[app.OnboardingImportView]{Items: []app.OnboardingImportView{newest, older}, HasMore: true}}
	httpAPI := onboardingImportListAPI(t, fake)

	response := httptest.NewRecorder()
	httpAPI.ServeHTTP(response, authenticatedCatalogRequest(http.MethodGet, "/api/v1/onboarding-imports?state=completed&state=failed&limit=2"))
	require.Equal(t, http.StatusOK, response.Code, response.Body.String())
	require.Equal(t, app.ListOnboardingImportsQuery{States: []model.OnboardingImportState{model.OnboardingImportCompleted, model.OnboardingImportFailed}, Limit: 2}, *fake.listQuery)
	var page struct {
		Items      []map[string]any `json:"items"`
		NextCursor string           `json:"next_cursor"`
	}
	require.NoError(t, json.Unmarshal(response.Body.Bytes(), &page))
	require.Len(t, page.Items, 2)
	require.Equal(t, newest.ID.String(), page.Items[0]["id"])
	require.NotContains(t, page.Items[0], "rows")
	require.NotEmpty(t, page.NextCursor)

	fake.listPage = app.Page[app.OnboardingImportView]{Items: []app.OnboardingImportView{}}
	response = httptest.NewRecorder()
	httpAPI.ServeHTTP(response, authenticatedCatalogRequest(http.MethodGet, "/api/v1/onboarding-imports?cursor="+page.NextCursor))
	require.Equal(t, http.StatusOK, response.Code, response.Body.String())
	require.Equal(t, app.ListOnboardingImportsQuery{BeforeCreatedAt: older.CreatedAt, BeforeID: older.ID, Limit: 50}, *fake.listQuery)
	require.JSONEq(t, `{"items":[]}`, response.Body.String())
}

func TestOnboardingImportListRejectsMalformedParameters(t *testing.T) {
	t.Parallel()
	for name, query := range map[string]string{
		"zero limit":    "limit=0",
		"large limit":   "limit=201",
		"empty state":   "state=",
		"unknown param": "mode=institution",
		"bad cursor":    "cursor=not-a-cursor",
		"repeat limit":  "limit=1&limit=2",
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			fake := &recordingOnboardingImportApplication{}
			response := httptest.NewRecorder()
			onboardingImportListAPI(t, fake).ServeHTTP(response, authenticatedCatalogRequest(http.MethodGet, "/api/v1/onboarding-imports?"+query))
			require.Equal(t, http.StatusBadRequest, response.Code, response.Body.String())
			require.Nil(t, fake.listQuery)
		})
	}
}

func onboardingImportListAPI(t *testing.T, fake *recordingOnboardingImportApplication) *API {
	t.Helper()
	principal := model.Principal{
		UserID: model.NewUserID(), SessionID: model.NewSessionID(), CredentialID: model.PrincipalCredentialID(model.NewID()),
		CredentialType: model.CredentialSessionAccess, AuthenticationMethod: "password", AuthenticationStrength: model.AuthenticationMultiFactor,
		AuthenticatedAt: time.Now().UTC(), MFACompletedAt: model.OptionalTimeFrom(time.Now().UTC()), ClientType: model.SessionClientWeb,
	}
	options := validHTTPOptions(t)
	options.OnboardingImports = fake
	options.Application.(*testHTTPApplication).principal = &principal
	httpAPI, err := New(options)
	require.NoError(t, err)
	return httpAPI
}
