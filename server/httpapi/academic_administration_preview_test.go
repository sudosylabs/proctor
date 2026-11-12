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
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/sudosylabs/proctor/server/app/invocation"
	"github.com/sudosylabs/proctor/server/app/onboarding"
	"github.com/sudosylabs/proctor/server/app/paging"
	"github.com/sudosylabs/proctor/server/app/user"
	"github.com/sudosylabs/proctor/server/httpapi/route"
	"github.com/sudosylabs/proctor/server/model"
	"github.com/sudosylabs/proctor/server/store"
)

type academicAdministrationPreviewApplicationFake struct {
	view     onboarding.AcademicAdministrationPreviewView
	create   *onboarding.CreateAcademicAdministrationPreviewCommand
	commit   *onboarding.CommitAcademicAdministrationPreviewCommand
	rowQuery *onboarding.ListAcademicAdministrationPreviewRowsQuery
	rows     paging.Page[store.AcademicAdministrationPreviewRow]
}

func (f *academicAdministrationPreviewApplicationFake) CreateAcademicAdministrationPreview(_ context.Context, _ invocation.Invocation,
	command onboarding.CreateAcademicAdministrationPreviewCommand,
) (onboarding.AcademicAdministrationPreviewView, error) {
	f.create = &command
	return f.view, nil
}

func (f *academicAdministrationPreviewApplicationFake) GetAcademicAdministrationPreview(context.Context, invocation.Invocation, model.OnboardingImportID) (onboarding.AcademicAdministrationPreviewView, error) {
	return f.view, nil
}

func (f *academicAdministrationPreviewApplicationFake) ListAcademicAdministrationPreviewRows(_ context.Context, _ invocation.Invocation,
	query onboarding.ListAcademicAdministrationPreviewRowsQuery,
) (paging.Page[store.AcademicAdministrationPreviewRow], error) {
	f.rowQuery = &query
	return f.rows, nil
}

func (f *academicAdministrationPreviewApplicationFake) CommitAcademicAdministrationPreview(_ context.Context, _ invocation.Invocation,
	command onboarding.CommitAcademicAdministrationPreviewCommand,
) (onboarding.AcademicAdministrationPreviewView, error) {
	f.commit = &command
	return f.view, nil
}

func (f *academicAdministrationPreviewApplicationFake) CancelAcademicAdministrationPreview(context.Context, invocation.Invocation, model.OnboardingImportID) (onboarding.AcademicAdministrationPreviewView, error) {
	return f.view, nil
}

func academicAdministrationPreviewAPI(t *testing.T, strength model.AuthenticationStrength, fake *academicAdministrationPreviewApplicationFake) *route.API {
	t.Helper()
	now := time.Now().UTC()
	principal := model.Principal{UserID: model.NewUserID(), SessionID: model.NewSessionID(), CredentialID: model.PrincipalCredentialID(model.NewID()),
		CredentialType: model.CredentialSessionAccess, AuthenticationMethod: "password", AuthenticationStrength: strength,
		AuthenticatedAt: now, ClientType: model.SessionClientWeb}
	if strength == model.AuthenticationMultiFactor {
		principal.MFACompletedAt = model.OptionalTimeFrom(now)
	}
	return newResourceAPI(t, principal, academicAdministrationPreviewResource(fake))
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
	fake := &academicAdministrationPreviewApplicationFake{view: onboarding.AcademicAdministrationPreviewView{
		Import: onboarding.OnboardingImportView{ID: model.NewOnboardingImportID(), State: model.OnboardingImportParsing}}}
	api := academicAdministrationPreviewAPI(t, model.AuthenticationMultiFactor, fake)
	unitID, roleID, institutionID := model.NewAcademicUnitID(), model.NewRoleID(), model.NewInstitutionID().String()
	body := `{"operation":"user.disable","scope_type":"institution","scope_id":"` + institutionID + `","filter":{"q":"smith",` +
		`"affiliation":["student","staff"],"academic_unit_id":"` + unitID.String() + `","role_id":["` + roleID.String() + `"],` +
		`"state":"active","last_active_after":"2026-09-01T08:00:00Z","last_active_before":"2026-09-02T10:00:00+02:00"}}`
	response := httptest.NewRecorder()
	api.ServeHTTP(response, previewHTTPRequest(http.MethodPost, "/api/v1/academic-administration-previews", body))
	require.Equal(t, http.StatusAccepted, response.Code, response.Body.String())
	require.Equal(t, "no-store", response.Header().Get("Cache-Control"))
	require.Equal(t, onboarding.CreateAcademicAdministrationPreviewCommand{
		Operation: onboarding.AcademicAdministrationUserDisable, ScopeType: model.RoleScopeInstitution, ScopeID: institutionID,
		Filter: onboarding.AcademicAdministrationUserFilter{Query: "smith", Affiliations: []model.AffiliationKind{model.AffiliationStudent, model.AffiliationStaff},
			AcademicUnitID: unitID, RoleIDs: []model.RoleID{roleID}, State: user.AccountStateActive,
			LastActiveAfter: time.Date(2026, 9, 1, 8, 0, 0, 0, time.UTC), LastActiveBefore: time.Date(2026, 9, 2, 8, 0, 0, 0, time.UTC)},
	}, *fake.create)
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
		})
	}
}

func TestAcademicAdministrationPreviewRequiresAStrongSessionToPreviewAndCommit(t *testing.T) {
	t.Parallel()
	fake := &academicAdministrationPreviewApplicationFake{}
	api := academicAdministrationPreviewAPI(t, model.AuthenticationSingleFactor, fake)
	id := model.NewOnboardingImportID().String()
	for path, body := range map[string]string{
		"/api/v1/academic-administration-previews": `{"operation":"user.disable","scope_type":"institution","scope_id":"` +
			model.NewInstitutionID().String() + `","filter":{}}`,
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
	require.Nil(t, fake.create)
	require.Nil(t, fake.commit)
}

func TestAcademicAdministrationPreviewCommitForwardsTheFenceAndKey(t *testing.T) {
	t.Parallel()
	fake := &academicAdministrationPreviewApplicationFake{}
	api := academicAdministrationPreviewAPI(t, model.AuthenticationMultiFactor, fake)
	id := model.NewOnboardingImportID()
	digest := strings.Repeat("b", 64)
	request := previewHTTPRequest(http.MethodPost, "/api/v1/academic-administration-previews/"+id.String()+"/commit",
		`{"expected_revision":3,"preview_digest":"`+digest+`"}`)
	request.Header.Set("Idempotency-Key", "preview-commit")
	response := httptest.NewRecorder()
	api.ServeHTTP(response, request)
	require.Equal(t, http.StatusAccepted, response.Code, response.Body.String())
	require.Equal(t, onboarding.CommitAcademicAdministrationPreviewCommand{ID: id, ExpectedRevision: 3, PreviewDigest: digest, IdempotencyKey: "preview-commit"}, *fake.commit)

	missingKey := httptest.NewRecorder()
	api.ServeHTTP(missingKey, previewHTTPRequest(http.MethodPost, "/api/v1/academic-administration-previews/"+id.String()+"/commit",
		`{"expected_revision":3,"preview_digest":"`+digest+`"}`))
	require.Equal(t, http.StatusBadRequest, missingKey.Code, missingKey.Body.String())
}

func TestAcademicAdministrationPreviewRowsPageOneGroupWithABoundCursor(t *testing.T) {
	t.Parallel()
	userID := model.NewUserID()
	fake := &academicAdministrationPreviewApplicationFake{rows: paging.Page[store.AcademicAdministrationPreviewRow]{HasMore: true,
		Items: []store.AcademicAdministrationPreviewRow{{RowNumber: 4, UserID: userID, Username: "ada", DisplayName: "Ada",
			Class: store.AcademicAdministrationPreviewSkipped, Reason: "academic_administration_preview.self", Status: model.OnboardingImportRowSkipped}}}}
	api := academicAdministrationPreviewAPI(t, model.AuthenticationMultiFactor, fake)
	path := "/api/v1/academic-administration-previews/" + model.NewOnboardingImportID().String() + "/rows"

	response := httptest.NewRecorder()
	api.ServeHTTP(response, authenticatedCatalogRequest(http.MethodGet, path+"?group=skipped&reason=academic_administration_preview.self&limit=1"))
	require.Equal(t, http.StatusOK, response.Code, response.Body.String())
	require.Equal(t, store.AcademicAdministrationPreviewSkipped, fake.rowQuery.Class)
	require.Equal(t, "academic_administration_preview.self", fake.rowQuery.Reason)
	require.Equal(t, 1, fake.rowQuery.Limit)
	require.Equal(t, 0, fake.rowQuery.AfterRowNumber)
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
	require.Equal(t, 4, fake.rowQuery.AfterRowNumber)

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
