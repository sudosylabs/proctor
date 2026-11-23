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
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/sudosylabs/proctor/server/app/onboarding"
	"github.com/sudosylabs/proctor/server/httpapi/route"
	"github.com/sudosylabs/proctor/server/httpapi/route/routetest"
	"github.com/sudosylabs/proctor/server/model"
	"github.com/sudosylabs/proctor/server/store"
)

// progressionClasses stores a source Class in one Academic Period and a
// destination Class in the next, at Programme Levels of one Programme, so a
// progression between them is coherent.
func progressionClasses(f *onboardingFixture, effectiveAt time.Time) (*model.Class, *model.Class) {
	sourcePeriod := f.addPeriod(effectiveAt.AddDate(-1, 0, 0), effectiveAt.AddDate(0, 0, -14))
	destinationPeriod := f.addPeriod(effectiveAt.AddDate(0, 0, -14), effectiveAt.AddDate(1, 0, 0))
	programmeID := model.NewProgrammeID()
	source := &model.Class{ID: model.NewClassID(), AcademicPeriodID: sourcePeriod.ID, ProgrammeLevelID: model.NewProgrammeLevelID(),
		Name: "year-1-blue", DisplayName: "Year 1 Blue", Revision: 3}
	destination := &model.Class{ID: model.NewClassID(), AcademicPeriodID: destinationPeriod.ID, ProgrammeLevelID: model.NewProgrammeLevelID(),
		Name: "year-2-blue", DisplayName: "Year 2 Blue", Revision: 4}
	for _, class := range []*model.Class{source, destination} {
		f.classes.add(class)
		f.levels.byID[class.ProgrammeLevelID] = &model.ProgrammeLevel{ID: class.ProgrammeLevelID, ProgrammeID: programmeID, Name: "level"}
	}
	return source, destination
}

func TestStudentProgressionHTTPRequiresRFC3339AndForwardsExactTargets(t *testing.T) {
	logger, _ := routetest.NewLogger(t)
	fixture := newOnboardingFixture(t)
	at := time.Date(2026, 9, 1, 9, 0, 0, 0, time.UTC)
	source, destination := progressionClasses(fixture, at)
	httpAPI := newFocusedResourceAPI(t, logger, routetest.Authenticator{Principal: fixture.principal(model.AuthenticationSingleFactor)},
		studentProgressionResource(fixture.service, fixture.people))
	body := `{"source_period_id":"` + source.AcademicPeriodID.String() + `","source_class_id":"` + source.ID.String() + `","destination_period_id":"` +
		destination.AcademicPeriodID.String() + `","destination_class_id":"` + destination.ID.String() + `","effective_at":"` + at.Format(time.RFC3339Nano) + `"}`
	request := newJSONRequest(http.MethodPost, "/api/v1/student-progressions", strings.NewReader(body))
	request.Header.Set("Authorization", "Bearer session")
	response := httptest.NewRecorder()
	httpAPI.ServeHTTP(response, request)
	require.Equal(t, http.StatusAccepted, response.Code, "student progression = %d %s", response.Code, response.Body.String())
	require.Equal(t, "no-store", response.Header().Get("Cache-Control"))
	created := fixture.imports.lastCreation(t).Import
	require.Equal(t, model.OnboardingImportStudentProgression, created.Mode)
	require.Equal(t, source.AcademicPeriodID, created.SourcePeriodID)
	require.Equal(t, source.ID, created.SourceClassID)
	require.Equal(t, destination.AcademicPeriodID, created.DestinationPeriodID)
	require.Equal(t, destination.ID, created.DestinationClassID)
	require.Equal(t, at.UnixMilli(), created.EffectiveAt.UnixMilli())
	require.Equal(t, source.Revision, created.SourceClassRevision, "the progression is fenced on the Classes it read")
	require.Equal(t, destination.Revision, created.DestinationClassRevision, "the progression is fenced on the Classes it read")
	invalid := newJSONRequest(http.MethodPost, "/api/v1/student-progressions", strings.NewReader(strings.Replace(body, at.Format(time.RFC3339Nano), "1800000000000", 1)))
	invalid.Header.Set("Authorization", "Bearer session")
	invalidResponse := httptest.NewRecorder()
	httpAPI.ServeHTTP(invalidResponse, invalid)
	require.Equal(t, http.StatusBadRequest, invalidResponse.Code, "non-RFC3339 effective_at = %d %s", invalidResponse.Code, invalidResponse.Body.String())
	require.Len(t, fixture.imports.creations, 1, "a refused progression reached the Store")
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
	encoded, err := json.Marshal(onboardingImportResponseFromView(view, rows, nil))
	require.NoError(t, err)
	body := string(encoded)
	for _, required := range []string{view.SourcePeriodID.String(), view.SourceClassID.String(), view.DestinationPeriodID.String(), view.DestinationClassID.String(), `"effective_at":"`} {
		require.Contains(t, body, required, "response omits")
	}
	requireNoPrivateFieldNames(t, encoded, "email", "username", "recipient", "claim", "secret", "principal")
}

func TestStudentProgressionListParsesFiltersAndPagesTheCountedEnvelope(t *testing.T) {
	t.Parallel()
	fixture := newOnboardingFixture(t)
	at := time.Date(2026, 8, 20, 9, 0, 0, 0, time.UTC)
	source, destination := progressionClasses(fixture, at)
	newest := fixture.storedProgression(source, destination)
	older, oldest := *newest, *newest
	older.ID, older.CreatedAt = model.NewOnboardingImportID(), newest.CreatedAt.Add(-time.Minute)
	oldest.ID, oldest.CreatedAt = model.NewOnboardingImportID(), newest.CreatedAt.Add(-2*time.Minute)
	// The Store reads one past the page, which tells the page another follows.
	for _, value := range []*store.OnboardingImport{newest, &older, &oldest} {
		fixture.imports.progressions = append(fixture.imports.progressions,
			&store.StudentProgressionSummary{Progression: value, SourceClassName: "Year 1 Blue", DestinationClassName: "Year 2 Blue"})
	}
	fixture.imports.progressionTotal = 3
	httpAPI := studentProgressionAPI(t, fixture)

	response := httptest.NewRecorder()
	httpAPI.ServeHTTP(response, authenticatedCatalogRequest(http.MethodGet, "/api/v1/student-progressions?state=preview_ready&state=failed&limit=2"+
		"&source_period_id="+source.AcademicPeriodID.String()+"&source_class_id="+source.ID.String()+
		"&destination_period_id="+destination.AcademicPeriodID.String()+"&destination_class_id="+destination.ID.String()))
	require.Equal(t, http.StatusOK, response.Code, response.Body.String())
	require.Equal(t, "no-store", response.Header().Get("Cache-Control"))
	require.Len(t, fixture.imports.progressionOptions, 1, "the list reached the Store")
	options := fixture.imports.progressionOptions[0]
	require.Equal(t, []model.OnboardingImportState{model.OnboardingImportPreviewReady, model.OnboardingImportFailed}, options.States)
	require.Equal(t, source.AcademicPeriodID, options.SourcePeriodID)
	require.Equal(t, source.ID, options.SourceClassID)
	require.Equal(t, destination.AcademicPeriodID, options.DestinationPeriodID)
	require.Equal(t, destination.ID, options.DestinationClassID)
	require.Equal(t, 3, options.Limit, "a page of two reads one more to learn whether another follows")
	require.True(t, options.BeforeID.IsZero())
	require.True(t, options.Visibility.InstitutionWide, "the caller's Institution-wide progression authority reached the Store")
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

	fixture.imports.progressions = nil
	response = httptest.NewRecorder()
	httpAPI.ServeHTTP(response, authenticatedCatalogRequest(http.MethodGet, "/api/v1/student-progressions?cursor="+page.NextCursor))
	require.Equal(t, http.StatusOK, response.Code, response.Body.String())
	require.Len(t, fixture.imports.progressionOptions, 2, "the second page reached the Store")
	options = fixture.imports.progressionOptions[1]
	require.Equal(t, older.CreatedAt, options.BeforeCreatedAt)
	require.Equal(t, older.ID, options.BeforeID)
	require.Equal(t, 51, options.Limit, "the default page of fifty reads one more")
	require.Empty(t, options.States)
	require.True(t, options.SourceClassID.IsZero(), "a later page keeps only the cursor")
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
			fixture := newOnboardingFixture(t)
			response := httptest.NewRecorder()
			studentProgressionAPI(t, fixture).ServeHTTP(response, authenticatedCatalogRequest(http.MethodGet, "/api/v1/student-progressions?"+query))
			require.Equal(t, http.StatusBadRequest, response.Code, response.Body.String())
			require.Empty(t, fixture.imports.progressionOptions, "a malformed list reached the Store")
		})
	}
}

func TestStudentProgressionReadNamesBothClasses(t *testing.T) {
	t.Parallel()
	fixture := newOnboardingFixture(t)
	value := fixture.storedProgression(fixture.addClass("Year 1 Blue"), fixture.addClass("Year 2 Blue"))
	response := httptest.NewRecorder()
	studentProgressionAPI(t, fixture).ServeHTTP(response, authenticatedCatalogRequest(http.MethodGet, "/api/v1/student-progressions/"+value.ID.String()))
	require.Equal(t, http.StatusOK, response.Code, response.Body.String())
	require.Equal(t, []model.OnboardingImportID{value.ID}, fixture.imports.reads, "the read reached the Store")
	require.Contains(t, response.Body.String(), `"source_class_name":"Year 1 Blue"`)
	require.Contains(t, response.Body.String(), `"destination_class_name":"Year 2 Blue"`)
}

// studentProgressionAPI serves the Student Progression resource over the
// fixture's real service as the caller's multi-factor web Session.
func studentProgressionAPI(t *testing.T, fixture *onboardingFixture) *route.API {
	t.Helper()
	return newResourceAPI(t, fixture.principal(model.AuthenticationMultiFactor), studentProgressionResource(fixture.service, fixture.people))
}
