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
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/sudosylabs/proctor/server/app/invocation"
	"github.com/sudosylabs/proctor/server/app/onboarding"
	"github.com/sudosylabs/proctor/server/app/paging"
	"github.com/sudosylabs/proctor/server/model"
)

type studentProgressionApplicationFake struct {
	view    onboarding.OnboardingImportView
	rows    []onboarding.OnboardingImportRowView
	command onboarding.DryRunStudentProgressionCommand
	getID   string

	listQuery *onboarding.ListStudentProgressionsQuery
	listPage  paging.Counted[onboarding.OnboardingImportView]
}

func (f *studentProgressionApplicationFake) ListStudentProgressions(_ context.Context, _ invocation.Invocation, query onboarding.ListStudentProgressionsQuery) (paging.Counted[onboarding.OnboardingImportView], error) {
	f.listQuery = &query
	return f.listPage, nil
}

func (f *studentProgressionApplicationFake) DryRunStudentProgression(_ context.Context, _ invocation.Invocation, command onboarding.DryRunStudentProgressionCommand) (onboarding.OnboardingImportView, error) {
	f.command = command
	return f.view, nil
}
func (f *studentProgressionApplicationFake) GetStudentProgression(_ context.Context, _ invocation.Invocation, id model.OnboardingImportID) (onboarding.OnboardingImportView, []onboarding.OnboardingImportRowView, error) {
	f.getID = id.String()
	return f.view, f.rows, nil
}
func (f *studentProgressionApplicationFake) CommitStudentProgression(context.Context, invocation.Invocation, onboarding.CommitStudentProgressionCommand) (onboarding.OnboardingImportView, error) {
	return f.view, nil
}
func (f *studentProgressionApplicationFake) CancelStudentProgression(context.Context, invocation.Invocation, model.OnboardingImportID) (onboarding.OnboardingImportView, error) {
	return f.view, nil
}
func (f *studentProgressionApplicationFake) StudentProgressionReport(context.Context, invocation.Invocation, model.OnboardingImportID, io.Writer) error {
	return nil
}

func TestStudentProgressionHTTPRequiresRFC3339AndForwardsExactTargets(t *testing.T) {
	logger, _ := newTestLogger(t)
	at := time.Date(2026, 9, 1, 9, 0, 0, 0, time.UTC)
	sourcePeriodID, sourceClassID := model.NewAcademicPeriodID(), model.NewClassID()
	destinationPeriodID, destinationClassID := model.NewAcademicPeriodID(), model.NewClassID()
	fake := &studentProgressionApplicationFake{view: onboarding.OnboardingImportView{ID: model.NewOnboardingImportID(), Mode: model.OnboardingImportStudentProgression,
		State: model.OnboardingImportParsing, ScopeType: model.RoleScopeClass, ScopeID: destinationClassID.String(), SourcePeriodID: sourcePeriodID,
		SourceClassID: sourceClassID, DestinationPeriodID: destinationPeriodID, DestinationClassID: destinationClassID, EffectiveAt: at,
		ParseJobID: model.NewJobID(), CreatedAt: at, UpdatedAt: at, ExpiresAt: at.Add(7 * 24 * time.Hour), Revision: 1}}
	principal := model.Principal{UserID: model.NewUserID(), SessionID: model.NewSessionID(), CredentialID: model.PrincipalCredentialID(model.NewID()),
		CredentialType: model.CredentialSessionAccess, AuthenticationMethod: "password", AuthenticationStrength: model.AuthenticationSingleFactor,
		ClientType: model.SessionClientWeb, AuthenticatedAt: at}
	httpAPI := newFocusedResourceAPI(t, logger, classRouteAuthenticator{principal: principal}, studentProgressionResource(fake))
	body := `{"source_period_id":"` + sourcePeriodID.String() + `","source_class_id":"` + sourceClassID.String() + `","destination_period_id":"` + destinationPeriodID.String() + `","destination_class_id":"` + destinationClassID.String() + `","effective_at":"` + at.Format(time.RFC3339Nano) + `"}`
	request := newJSONRequest(http.MethodPost, "/api/v1/student-progressions", strings.NewReader(body))
	request.Header.Set("Authorization", "Bearer session")
	response := httptest.NewRecorder()
	httpAPI.ServeHTTP(response, request)
	require.Equal(t, http.StatusAccepted, response.Code, "student progression = %d %s command=%#v", response.Code, response.Body.String(), fake.command)
	require.Equal(t, "no-store", response.Header().Get("Cache-Control"), "student progression = %d %s command=%#v", response.Code, response.Body.String(), fake.command)
	require.Equal(t, sourcePeriodID, fake.command.SourcePeriodID, "student progression = %d %s command=%#v", response.Code, response.Body.String(), fake.command)
	require.Equal(t, sourceClassID, fake.command.SourceClassID, "student progression = %d %s command=%#v", response.Code, response.Body.String(), fake.command)
	require.Equal(t, destinationPeriodID, fake.command.DestinationPeriodID, "student progression = %d %s command=%#v", response.Code, response.Body.String(), fake.command)
	require.Equal(t, destinationClassID, fake.command.DestinationClassID, "student progression = %d %s command=%#v", response.Code, response.Body.String(), fake.command)
	require.Equal(t, at.UnixMilli(), fake.command.EffectiveAt, "student progression = %d %s command=%#v", response.Code, response.Body.String(), fake.command)
	invalid := newJSONRequest(http.MethodPost, "/api/v1/student-progressions", strings.NewReader(strings.Replace(body, at.Format(time.RFC3339Nano), "1800000000000", 1)))
	invalid.Header.Set("Authorization", "Bearer session")
	invalidResponse := httptest.NewRecorder()
	httpAPI.ServeHTTP(invalidResponse, invalid)
	require.Equal(t, http.StatusBadRequest, invalidResponse.Code, "non-RFC3339 effective_at = %d %s", invalidResponse.Code, invalidResponse.Body.String())
}

func TestStudentProgressionResponseExposesOnlySafeExactTargetsAndRows(t *testing.T) {
	t.Parallel()
	at := time.Date(2026, 8, 21, 9, 0, 0, 0, time.UTC)
	view := onboarding.OnboardingImportView{ID: model.NewOnboardingImportID(), Mode: model.OnboardingImportStudentProgression,
		State: model.OnboardingImportPreviewReady, ScopeType: model.RoleScopeClass, ScopeID: model.NewClassID().String(),
		SourcePeriodID: model.NewAcademicPeriodID(), SourceClassID: model.NewClassID(), DestinationPeriodID: model.NewAcademicPeriodID(),
		DestinationClassID: model.NewClassID(), EffectiveAt: at.Add(24 * time.Hour), ParseJobID: model.NewJobID(),
		CreatedAt: at, UpdatedAt: at, ExpiresAt: at.Add(7 * 24 * time.Hour), Revision: 2, TotalRows: 1, ValidRows: 1}
	rows := []onboarding.OnboardingImportRowView{{RowNumber: 1, Reference: "safe-membership-reference", Operation: "class.enroll", Status: model.OnboardingImportRowValid}}
	encoded, err := json.Marshal(onboardingImportResponseFromView(view, rows))
	require.NoError(t, err)
	body := string(encoded)
	for _, required := range []string{view.SourcePeriodID.String(), view.SourceClassID.String(), view.DestinationPeriodID.String(), view.DestinationClassID.String(), `"effective_at":"`} {
		require.Contains(t, body, required, "response omits")
	}
	requireNoPrivateFieldNames(t, encoded, "email", "username", "recipient", "claim", "secret", "principal")
}

func TestStudentProgressionListParsesFiltersAndPagesTheCountedEnvelope(t *testing.T) {
	t.Parallel()
	at := time.Date(2026, 8, 20, 9, 0, 0, 0, time.UTC)
	sourcePeriodID, sourceClassID := model.NewAcademicPeriodID(), model.NewClassID()
	destinationPeriodID, destinationClassID := model.NewAcademicPeriodID(), model.NewClassID()
	newest := onboarding.OnboardingImportView{ID: model.NewOnboardingImportID(), Mode: model.OnboardingImportStudentProgression,
		State: model.OnboardingImportPreviewReady, ScopeType: model.RoleScopeClass, ScopeID: destinationClassID.String(),
		SourcePeriodID: sourcePeriodID, SourceClassID: sourceClassID, DestinationPeriodID: destinationPeriodID, DestinationClassID: destinationClassID,
		SourceClassName: "Year 1 Blue", DestinationClassName: "Year 2 Blue", EffectiveAt: at.Add(24 * time.Hour),
		ParseJobID: model.NewJobID(), CreatedAt: at, UpdatedAt: at, ExpiresAt: at.Add(7 * 24 * time.Hour), Revision: 2}
	older := newest
	older.ID, older.CreatedAt = model.NewOnboardingImportID(), at.Add(-time.Minute)
	fake := &studentProgressionApplicationFake{listPage: paging.Counted[onboarding.OnboardingImportView]{
		Page:  paging.Page[onboarding.OnboardingImportView]{Items: []onboarding.OnboardingImportView{newest, older}, HasMore: true},
		Total: paging.Total{Counted: true, Total: 3}}}
	httpAPI := studentProgressionListAPI(t, fake)

	response := httptest.NewRecorder()
	httpAPI.ServeHTTP(response, authenticatedCatalogRequest(http.MethodGet, "/api/v1/student-progressions?state=preview_ready&state=failed&limit=2"+
		"&source_period_id="+sourcePeriodID.String()+"&source_class_id="+sourceClassID.String()+
		"&destination_period_id="+destinationPeriodID.String()+"&destination_class_id="+destinationClassID.String()))
	require.Equal(t, http.StatusOK, response.Code, response.Body.String())
	require.Equal(t, "no-store", response.Header().Get("Cache-Control"))
	require.Equal(t, onboarding.ListStudentProgressionsQuery{States: []model.OnboardingImportState{model.OnboardingImportPreviewReady, model.OnboardingImportFailed},
		SourcePeriodID: sourcePeriodID, SourceClassID: sourceClassID, DestinationPeriodID: destinationPeriodID, DestinationClassID: destinationClassID,
		Limit: 2}, *fake.listQuery)
	var page struct {
		Items             []map[string]any `json:"items"`
		NextCursor        string           `json:"next_cursor"`
		Total             *int             `json:"total"`
		TotalIsLowerBound *bool            `json:"total_is_lower_bound"`
	}
	require.NoError(t, json.Unmarshal(response.Body.Bytes(), &page))
	require.Len(t, page.Items, 2)
	require.Equal(t, newest.ID.String(), page.Items[0]["id"])
	require.Equal(t, "Year 1 Blue", page.Items[0]["source_class_name"])
	require.Equal(t, "Year 2 Blue", page.Items[0]["destination_class_name"])
	require.NotContains(t, page.Items[0], "rows")
	require.NotEmpty(t, page.NextCursor)
	require.Equal(t, 3, *page.Total)
	require.False(t, *page.TotalIsLowerBound)

	fake.listPage = paging.Counted[onboarding.OnboardingImportView]{Page: paging.Page[onboarding.OnboardingImportView]{Items: []onboarding.OnboardingImportView{}}}
	response = httptest.NewRecorder()
	httpAPI.ServeHTTP(response, authenticatedCatalogRequest(http.MethodGet, "/api/v1/student-progressions?cursor="+page.NextCursor))
	require.Equal(t, http.StatusOK, response.Code, response.Body.String())
	require.Equal(t, onboarding.ListStudentProgressionsQuery{BeforeCreatedAt: older.CreatedAt, BeforeID: older.ID, Limit: 50}, *fake.listQuery)
	require.JSONEq(t, `{"items":[]}`, response.Body.String(), "a later page carries no total")
}

func TestStudentProgressionListRejectsMalformedParameters(t *testing.T) {
	t.Parallel()
	classID := model.NewClassID().String()
	for name, query := range map[string]string{
		"zero limit":      "limit=0",
		"large limit":     "limit=201",
		"empty state":     "state=",
		"unknown param":   "mode=student_progression",
		"bad cursor":      "cursor=not-a-cursor",
		"repeat limit":    "limit=1&limit=2",
		"empty class":     "source_class_id=",
		"repeated class":  "destination_class_id=" + classID + "&destination_class_id=" + classID,
		"repeated period": "source_period_id=a&source_period_id=b",
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			fake := &studentProgressionApplicationFake{}
			response := httptest.NewRecorder()
			studentProgressionListAPI(t, fake).ServeHTTP(response, authenticatedCatalogRequest(http.MethodGet, "/api/v1/student-progressions?"+query))
			require.Equal(t, http.StatusBadRequest, response.Code, response.Body.String())
			require.Nil(t, fake.listQuery)
		})
	}
}

func TestStudentProgressionReadNamesBothClasses(t *testing.T) {
	t.Parallel()
	at := time.Date(2026, 8, 20, 9, 0, 0, 0, time.UTC)
	fake := &studentProgressionApplicationFake{view: onboarding.OnboardingImportView{ID: model.NewOnboardingImportID(), Mode: model.OnboardingImportStudentProgression,
		State: model.OnboardingImportPreviewReady, ScopeType: model.RoleScopeClass, ScopeID: model.NewClassID().String(),
		SourceClassName: "Year 1 Blue", DestinationClassName: "Year 2 Blue", ParseJobID: model.NewJobID(),
		CreatedAt: at, UpdatedAt: at, ExpiresAt: at.Add(7 * 24 * time.Hour), Revision: 2}}
	response := httptest.NewRecorder()
	studentProgressionListAPI(t, fake).ServeHTTP(response, authenticatedCatalogRequest(http.MethodGet, "/api/v1/student-progressions/"+fake.view.ID.String()))
	require.Equal(t, http.StatusOK, response.Code, response.Body.String())
	require.Contains(t, response.Body.String(), `"source_class_name":"Year 1 Blue"`)
	require.Contains(t, response.Body.String(), `"destination_class_name":"Year 2 Blue"`)
}

func studentProgressionListAPI(t *testing.T, fake *studentProgressionApplicationFake) *API {
	t.Helper()
	principal := model.Principal{
		UserID: model.NewUserID(), SessionID: model.NewSessionID(), CredentialID: model.PrincipalCredentialID(model.NewID()),
		CredentialType: model.CredentialSessionAccess, AuthenticationMethod: "password", AuthenticationStrength: model.AuthenticationMultiFactor,
		AuthenticatedAt: time.Now().UTC(), MFACompletedAt: model.OptionalTimeFrom(time.Now().UTC()), ClientType: model.SessionClientWeb,
	}
	options := validHTTPOptions(t)
	options.StudentProgressions = fake
	options.Identity.(*testHTTPApplication).principal = &principal
	httpAPI, err := New(options)
	require.NoError(t, err)
	return httpAPI
}
