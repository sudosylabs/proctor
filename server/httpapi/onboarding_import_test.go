// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package httpapi

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/sudosylabs/proctor/server/app/onboarding"
	"github.com/sudosylabs/proctor/server/model"
	"github.com/sudosylabs/proctor/server/store"
)

func TestOnboardingImportResponseExcludesPrivateRowAndCredentialMaterial(t *testing.T) {
	t.Parallel()
	at := time.Date(2026, 8, 20, 9, 0, 0, 0, time.UTC)
	view := onboarding.OnboardingImportView{ID: model.NewOnboardingImportID(), Mode: model.OnboardingImportInstitution,
		State: model.OnboardingImportPreviewReady, ScopeType: model.RoleScopeInstitution, ScopeID: model.NewInstitutionID().String(),
		ParseJobID: model.NewJobID(), CreatedAt: at, UpdatedAt: at, ExpiresAt: at.Add(7 * 24 * time.Hour), Revision: 2, TotalRows: 1, ValidRows: 1}
	rows := []onboarding.OnboardingImportRowView{{RowNumber: 1, Reference: "safe-row", Operation: "institution_role.create", Status: model.OnboardingImportRowValid}}
	encoded, err := json.Marshal(onboardingImportResponseFromView(view, rows))
	require.NoError(t, err)
	body := string(encoded)
	requireNoPrivateFieldNames(t, encoded, "target_email", "email", "claim", "hash", "recipient", "secret")
	require.Contains(t, body, `"reference":"safe-row"`)
	require.Contains(t, body, `"status":"valid"`)
}

func TestOnboardingImportListParsesFiltersAndPagesTheEnvelope(t *testing.T) {
	t.Parallel()
	fixture := newOnboardingFixture(t)
	at := time.Date(2026, 8, 20, 9, 0, 0, 0, time.UTC)
	newest := &store.OnboardingImport{ID: model.NewOnboardingImportID(), Mode: model.OnboardingImportInstitution,
		State: model.OnboardingImportCompleted, ScopeType: model.RoleScopeInstitution, ScopeID: fixture.access.Institution.ID.String(),
		ActorUserID: fixture.callerID, ParseJobID: model.NewJobID(), CreatedAt: at, UpdatedAt: at, ExpiresAt: at.Add(7 * 24 * time.Hour), Revision: 3}
	older, oldest := *newest, *newest
	older.ID, older.CreatedAt = model.NewOnboardingImportID(), at.Add(-time.Minute)
	oldest.ID, oldest.CreatedAt = model.NewOnboardingImportID(), at.Add(-2*time.Minute)
	// The Store reads one past the page, which tells the page another follows.
	fixture.imports.listed = []*store.OnboardingImport{newest, &older, &oldest}
	httpAPI := newResourceAPI(t, fixture.principal(model.AuthenticationMultiFactor), onboardingImportResource(fixture.service))

	response := httptest.NewRecorder()
	httpAPI.ServeHTTP(response, authenticatedCatalogRequest(http.MethodGet, "/api/v1/onboarding-imports?state=completed&state=failed&limit=2"))
	require.Equal(t, http.StatusOK, response.Code, response.Body.String())
	require.Len(t, fixture.imports.listOptions, 1, "the list reached the Store")
	options := fixture.imports.listOptions[0]
	require.Equal(t, []model.OnboardingImportState{model.OnboardingImportCompleted, model.OnboardingImportFailed}, options.States)
	require.Equal(t, 3, options.Limit, "a page of two reads one more to learn whether another follows")
	require.True(t, options.BeforeCreatedAt.IsZero())
	require.True(t, options.BeforeID.IsZero())
	require.True(t, options.Visibility.InstitutionWide, "the caller's Institution-wide onboarding_batch.view reached the Store")
	var page struct {
		Items      []map[string]any `json:"items"`
		NextCursor string           `json:"next_cursor"`
	}
	require.NoError(t, json.Unmarshal(response.Body.Bytes(), &page))
	require.Len(t, page.Items, 2)
	require.Equal(t, newest.ID.String(), page.Items[0]["id"])
	require.NotContains(t, page.Items[0], "rows")
	require.NotEmpty(t, page.NextCursor)

	fixture.imports.listed = nil
	response = httptest.NewRecorder()
	httpAPI.ServeHTTP(response, authenticatedCatalogRequest(http.MethodGet, "/api/v1/onboarding-imports?cursor="+page.NextCursor))
	require.Equal(t, http.StatusOK, response.Code, response.Body.String())
	require.Len(t, fixture.imports.listOptions, 2, "the second page reached the Store")
	options = fixture.imports.listOptions[1]
	require.Equal(t, older.CreatedAt, options.BeforeCreatedAt)
	require.Equal(t, older.ID, options.BeforeID)
	require.Equal(t, 51, options.Limit, "the default page of fifty reads one more")
	require.Empty(t, options.States)
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
			fixture := newOnboardingFixture(t)
			httpAPI := newResourceAPI(t, fixture.principal(model.AuthenticationMultiFactor), onboardingImportResource(fixture.service))
			response := httptest.NewRecorder()
			httpAPI.ServeHTTP(response, authenticatedCatalogRequest(http.MethodGet, "/api/v1/onboarding-imports?"+query))
			require.Equal(t, http.StatusBadRequest, response.Code, response.Body.String())
			require.Empty(t, fixture.imports.listOptions, "a malformed list reached the Store")
		})
	}
}

func TestOnboardingImportUploadForwardsTheFileNameAndReturnsIt(t *testing.T) {
	t.Parallel()
	fixture := newOnboardingFixture(t)
	classID := fixture.addClass("Year 1").ID.String()
	httpAPI := newResourceAPI(t, fixture.principal(model.AuthenticationMultiFactor), onboardingImportResource(fixture.service))
	upload := func(query string) *httptest.ResponseRecorder {
		request := authenticatedCatalogRequest(http.MethodPost, "/api/v1/onboarding-imports?mode=student_class&scope_type=class&scope_id="+classID+query)
		request.Body = io.NopCloser(strings.NewReader("email\nstudent@example.edu\n"))
		request.Header.Set("Content-Type", "text/csv")
		response := httptest.NewRecorder()
		httpAPI.ServeHTTP(response, request)
		return response
	}

	response := upload("&file_name=" + url.QueryEscape(" Year 1 roster.csv "))
	require.Equal(t, http.StatusAccepted, response.Code, response.Body.String())
	created := fixture.imports.lastCreation(t).Import
	require.Equal(t, "Year 1 roster.csv", created.FileName, "the application trims and validates the name")
	require.Equal(t, model.OnboardingImportStudentClass, created.Mode)
	require.Equal(t, model.RoleScopeClass, created.ScopeType)
	require.Equal(t, classID, created.ScopeID)
	require.Equal(t, []byte("email\nstudent@example.edu\n"), fixture.files.staged[created.ID], "the uploaded file was staged")
	require.Contains(t, response.Body.String(), `"file_name":"Year 1 roster.csv"`)

	response = upload("")
	require.Equal(t, http.StatusAccepted, response.Code, response.Body.String())
	require.Len(t, fixture.imports.creations, 2)
	require.Empty(t, fixture.imports.lastCreation(t).Import.FileName, "an omitted name stays absent")
	require.NotContains(t, response.Body.String(), "file_name")

	response = upload("&file_name=a.csv&file_name=b.csv")
	require.Equal(t, http.StatusBadRequest, response.Code, response.Body.String())
	require.Len(t, fixture.imports.creations, 2, "a repeated name reached the Store")
}
