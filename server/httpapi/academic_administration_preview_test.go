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

	"github.com/sudosylabs/proctor/server/app/onboarding"
	"github.com/sudosylabs/proctor/server/app/user"
	"github.com/sudosylabs/proctor/server/httpapi/route"
	"github.com/sudosylabs/proctor/server/model"
	"github.com/sudosylabs/proctor/server/store"
)

// academicAdministrationPreviewAPI serves the filter preview resource over
// the fixture's real service as the caller's web Session at strength.
func academicAdministrationPreviewAPI(t *testing.T, fixture *onboardingFixture, strength model.AuthenticationStrength) *route.API {
	t.Helper()
	return newResourceAPI(t, fixture.principal(strength), academicAdministrationPreviewResource(fixture.service))
}

func previewHTTPRequest(method, path, body string) *http.Request {
	request := authenticatedCatalogRequest(method, path)
	if body != "" {
		request = newJSONRequest(method, path, strings.NewReader(body))
		request.Header.Set("Authorization", "Bearer explicit-capability")
	}
	return request
}

func TestAcademicAdministrationPreviewForwardsTheExactFilter(t *testing.T) {
	t.Parallel()
	fixture := newOnboardingFixture(t)
	api := academicAdministrationPreviewAPI(t, fixture, model.AuthenticationMultiFactor)
	unitID, roleID, institutionID := model.NewAcademicUnitID(), fixture.addDelegableRole().ID, fixture.access.Institution.ID.String()
	body := `{"operation":"user.disable","scope_type":"institution","scope_id":"` + institutionID + `","filter":{"q":"smith",` +
		`"affiliation":["student","staff"],"academic_unit_id":"` + unitID.String() + `","role_id":["` + roleID.String() + `"],` +
		`"state":"active","last_active_after":"2026-09-01T08:00:00Z","last_active_before":"2026-09-02T10:00:00+02:00"}}`
	response := httptest.NewRecorder()
	api.ServeHTTP(response, previewHTTPRequest(http.MethodPost, "/api/v1/academic-administration-previews", body))
	require.Equal(t, http.StatusAccepted, response.Code, response.Body.String())
	require.Equal(t, "no-store", response.Header().Get("Cache-Control"))
	created := fixture.imports.lastCreation(t).Import
	require.Equal(t, model.OnboardingImportAcademicAdministrationFilter, created.Mode)
	require.Equal(t, model.RoleScopeInstitution, created.ScopeType)
	require.Equal(t, institutionID, created.ScopeID)
	require.Equal(t, store.AcademicAdministrationFilter{
		Operation: string(onboarding.AcademicAdministrationUserDisable), Query: "smith",
		Affiliations:   []model.AffiliationKind{model.AffiliationStudent, model.AffiliationStaff},
		AcademicUnitID: unitID, RoleIDs: []model.RoleID{roleID}, State: string(user.AccountStateActive),
		LastActiveAfter: time.Date(2026, 9, 1, 8, 0, 0, 0, time.UTC), LastActiveBefore: time.Date(2026, 9, 2, 8, 0, 0, 0, time.UTC),
	}, *created.Filter)
	require.NotEmpty(t, fixture.users.listOptions, "the caller's directory was asked whether it admits the filter")
	require.Equal(t, "smith", fixture.users.listOptions[0].Query)
	require.NotContains(t, response.Body.String(), `"summary"`, "a preview that is not ready has no summary")

	for name, invalid := range map[string]string{
		"unknown member": `{"operation":"user.disable","scope_type":"institution","scope_id":"` + institutionID + `","filter":{},"include_disabled":true}`,
		"unknown filter": `{"operation":"user.disable","scope_type":"institution","scope_id":"` + institutionID + `","filter":{"include_disabled":true}}`,
		"wire instant":   `{"operation":"class.enroll","scope_type":"class","scope_id":"` + model.NewClassID().String() + `","filter":{},"start_at":"1800000000000"}`,
	} {
		t.Run(name, func(t *testing.T) {
			rejected := httptest.NewRecorder()
			api.ServeHTTP(rejected, previewHTTPRequest(http.MethodPost, "/api/v1/academic-administration-previews", invalid))
			require.Equal(t, http.StatusBadRequest, rejected.Code, rejected.Body.String())
			require.Len(t, fixture.imports.creations, 1, "a refused preview reached the Store")
		})
	}
}

func TestAcademicAdministrationPreviewRequiresAStrongSessionToPreviewAndCommit(t *testing.T) {
	t.Parallel()
	fixture := newOnboardingFixture(t)
	api := academicAdministrationPreviewAPI(t, fixture, model.AuthenticationSingleFactor)
	id := fixture.storedFilterPreview(model.OnboardingImportPreviewReady).ID.String()
	for path, body := range map[string]string{
		"/api/v1/academic-administration-previews": `{"operation":"user.disable","scope_type":"institution","scope_id":"` +
			fixture.access.Institution.ID.String() + `","filter":{}}`,
		"/api/v1/academic-administration-previews/" + id + "/commit": `{"expected_revision":1,"preview_digest":"` + strings.Repeat("a", 64) + `"}`,
	} {
		t.Run(path, func(t *testing.T) {
			request := previewHTTPRequest(http.MethodPost, path, body)
			request.Header.Set("Idempotency-Key", "strong-session")
			response := httptest.NewRecorder()
			api.ServeHTTP(response, request)
			require.Equal(t, http.StatusForbidden, response.Code, response.Body.String())
			require.Contains(t, response.Body.String(), "authentication.strong_required")
		})
	}
	require.Empty(t, fixture.imports.creations, "a single-factor preview reached the Store")
	require.Empty(t, fixture.imports.commits, "a single-factor commit reached the Store")
}

func TestAcademicAdministrationPreviewCommitForwardsTheFenceAndKey(t *testing.T) {
	t.Parallel()
	fixture := newOnboardingFixture(t)
	api := academicAdministrationPreviewAPI(t, fixture, model.AuthenticationMultiFactor)
	preview := fixture.storedFilterPreview(model.OnboardingImportPreviewReady)
	id := preview.ID
	digest := strings.Repeat("b", 64)
	preview.PreviewDigest = digest
	request := previewHTTPRequest(http.MethodPost, "/api/v1/academic-administration-previews/"+id.String()+"/commit",
		`{"expected_revision":3,"preview_digest":"`+digest+`"}`)
	request.Header.Set("Idempotency-Key", "preview-commit")
	response := httptest.NewRecorder()
	api.ServeHTTP(response, request)
	require.Equal(t, http.StatusAccepted, response.Code, response.Body.String())
	require.Len(t, fixture.imports.commits, 1, "the commit reached the Store")
	commit := fixture.imports.commits[0]
	require.Equal(t, id, commit.ID)
	require.Equal(t, int64(3), commit.ExpectedRevision)
	require.Equal(t, digest, commit.PreviewDigest)
	require.Equal(t, sha256.Sum256([]byte("preview-commit")), commit.IdempotencyKey)
	require.Equal(t, fixture.callerID, commit.ActorUserID)
	require.NotNil(t, commit.BatchAuditEvent, "the first commit carries the batch-level audit entry")

	missingKey := httptest.NewRecorder()
	api.ServeHTTP(missingKey, previewHTTPRequest(http.MethodPost, "/api/v1/academic-administration-previews/"+id.String()+"/commit",
		`{"expected_revision":3,"preview_digest":"`+digest+`"}`))
	require.Equal(t, http.StatusBadRequest, missingKey.Code, missingKey.Body.String())
	require.Len(t, fixture.imports.commits, 1, "a commit without a key reached the Store")
}

func TestAcademicAdministrationPreviewRowsPageOneGroupWithABoundCursor(t *testing.T) {
	t.Parallel()
	fixture := newOnboardingFixture(t)
	userID := model.NewUserID()
	// The Store reads one past the page, which tells the page another follows.
	fixture.imports.previewRows = []store.AcademicAdministrationPreviewRow{
		{RowNumber: 4, UserID: userID, Username: "ada", DisplayName: "Ada",
			Class: store.AcademicAdministrationPreviewSkipped, Reason: "academic_administration_preview.self", Status: model.OnboardingImportRowSkipped},
		{RowNumber: 5, UserID: model.NewUserID(), Username: "bea", DisplayName: "Bea",
			Class: store.AcademicAdministrationPreviewSkipped, Reason: "academic_administration_preview.self", Status: model.OnboardingImportRowSkipped},
	}
	api := academicAdministrationPreviewAPI(t, fixture, model.AuthenticationMultiFactor)
	preview := fixture.storedFilterPreview(model.OnboardingImportPreviewReady)
	path := "/api/v1/academic-administration-previews/" + preview.ID.String() + "/rows"

	response := httptest.NewRecorder()
	api.ServeHTTP(response, authenticatedCatalogRequest(http.MethodGet, path+"?group=skipped&reason=academic_administration_preview.self&limit=1"))
	require.Equal(t, http.StatusOK, response.Code, response.Body.String())
	require.Len(t, fixture.imports.previewRowOptions, 1, "the rows read reached the Store")
	options := fixture.imports.previewRowOptions[0]
	require.Equal(t, preview.ID, options.ImportID)
	require.Equal(t, store.AcademicAdministrationPreviewSkipped, options.Class)
	require.Equal(t, "academic_administration_preview.self", options.Reason)
	require.Equal(t, 2, options.Limit, "a page of one reads one more to learn whether another follows")
	require.Equal(t, 0, options.AfterRowNumber)
	require.True(t, options.Visibility.InstitutionWide, "the caller's directory visibility reached the Store")
	var page struct {
		Items      []academicAdministrationPreviewRowResponse `json:"items"`
		NextCursor string                                     `json:"next_cursor"`
	}
	require.NoError(t, json.Unmarshal(response.Body.Bytes(), &page))
	require.Equal(t, []academicAdministrationPreviewRowResponse{{RowNumber: 4, UserID: userID.String(), Username: "ada", DisplayName: "Ada",
		Group: "skipped", Reason: "academic_administration_preview.self", Status: "skipped"}}, page.Items)
	require.NotEmpty(t, page.NextCursor)
	requireNoPrivateFieldNames(t, response.Body.Bytes(), "email", "principal")

	continued := httptest.NewRecorder()
	api.ServeHTTP(continued, authenticatedCatalogRequest(http.MethodGet,
		path+"?group=skipped&reason=academic_administration_preview.self&cursor="+page.NextCursor))
	require.Equal(t, http.StatusOK, continued.Code, continued.Body.String())
	require.Len(t, fixture.imports.previewRowOptions, 2, "the continued read reached the Store")
	require.Equal(t, 4, fixture.imports.previewRowOptions[1].AfterRowNumber)

	for name, query := range map[string]string{
		"no group":            "",
		"repeated group":      "?group=skipped&group=no_op",
		"unknown parameter":   "?group=skipped&user_id=" + userID.String(),
		"cursor of a group":   "?group=no_op&cursor=" + page.NextCursor,
		"cursor of a reason":  "?group=skipped&cursor=" + page.NextCursor,
		"cursor of no reason": "?group=skipped&reason=academic_administration_preview.system_administrator&cursor=" + page.NextCursor,
	} {
		t.Run(name, func(t *testing.T) {
			rejected := httptest.NewRecorder()
			api.ServeHTTP(rejected, authenticatedCatalogRequest(http.MethodGet, path+query))
			require.Equal(t, http.StatusBadRequest, rejected.Code, rejected.Body.String())
			require.Len(t, fixture.imports.previewRowOptions, 2, "a refused rows read reached the Store")
		})
	}
}

func TestAcademicAdministrationPreviewResponseSummarizesGroups(t *testing.T) {
	t.Parallel()
	at := time.Date(2026, 9, 1, 9, 0, 0, 0, time.UTC)
	response := academicAdministrationPreviewResponseFromView(onboarding.AcademicAdministrationPreviewView{
		Import: onboarding.OnboardingImportView{ID: model.NewOnboardingImportID(), State: model.OnboardingImportPreviewReady, CreatedAt: at,
			UpdatedAt: at, ExpiresAt: at.Add(7 * 24 * time.Hour), TotalRows: 6, Revision: 2},
		Filter:           store.AcademicAdministrationFilter{Operation: "user.disable", Affiliations: []model.AffiliationKind{model.AffiliationStudent}},
		PreviewExpiresAt: model.OptionalTimeFrom(at.Add(15 * time.Minute)),
		Groups: []store.AcademicAdministrationPreviewGroup{
			{Class: store.AcademicAdministrationPreviewNoOp, Rows: 1},
			{Class: store.AcademicAdministrationPreviewSkipped, Reason: "academic_administration_preview.self", Rows: 1},
			{Class: store.AcademicAdministrationPreviewSkipped, Reason: "academic_administration_preview.system_administrator", Rows: 2},
			{Class: store.AcademicAdministrationPreviewWillChange, Rows: 2},
		},
	})
	require.Equal(t, &academicAdministrationPreviewSummaryResponse{WillChange: 2, NoOp: 1, Skipped: 3, SkipReasons: []academicAdministrationPreviewSkipResponse{
		{Reason: "academic_administration_preview.self", Rows: 1}, {Reason: "academic_administration_preview.system_administrator", Rows: 2},
	}}, response.Summary)
	require.Equal(t, "2026-09-01T09:15:00Z", response.PreviewExpiresAt)
	require.Equal(t, []string{"student"}, response.Filter.Affiliation)
}
