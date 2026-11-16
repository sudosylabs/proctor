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
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/sudosylabs/proctor/server/app/access/accesstest"
	"github.com/sudosylabs/proctor/server/app/identity"
	"github.com/sudosylabs/proctor/server/httpapi/route"
	"github.com/sudosylabs/proctor/server/httpapi/route/routetest"
	"github.com/sudosylabs/proctor/server/localization"
	"github.com/sudosylabs/proctor/server/model"
	"github.com/sudosylabs/proctor/server/store"
)

func repositoryLocalizer(t *testing.T) *localization.Localizer {
	t.Helper()
	_, currentFile, _, ok := runtime.Caller(0)
	require.True(t, ok)
	localizer, err := localization.New(os.DirFS(filepath.Join(filepath.Dir(currentFile), "..", "i18n")), "en")
	require.NoError(t, err)
	return localizer
}

// TestEveryPersonalAccessTokenActionHasLocalizedCopy keeps the grantable-scope
// catalogue from ever serving an Action name in place of its title.
func TestEveryPersonalAccessTokenActionHasLocalizedCopy(t *testing.T) {
	t.Parallel()

	localizer := repositoryLocalizer(t)
	for _, action := range model.PersonalAccessTokenActions() {
		for _, id := range []string{
			accessActionTitleLocalizationID(model.Action(action)),
			accessActionDescriptionLocalizationID(model.Action(action)),
		} {
			text, err := localizer.Translate("en", id, nil)
			require.NoError(t, err, id)
			require.NotEmpty(t, text, id)
		}
	}
}

// TestPersonalAccessTokenScopeEnumMatchesEligibleActions keeps the creation
// contract's scope enum equal to model.PersonalAccessTokenActions.
func TestPersonalAccessTokenScopeEnumMatchesEligibleActions(t *testing.T) {
	t.Parallel()

	encoded, err := readOpenAPIDocumentFile()
	require.NoError(t, err)
	var document struct {
		Components struct {
			Schemas map[string]struct {
				Enum       []string `json:"enum"`
				Properties map[string]struct {
					Items struct {
						Ref string `json:"$ref"`
					} `json:"items"`
				} `json:"properties"`
			} `json:"schemas"`
		} `json:"components"`
	}
	require.NoError(t, json.Unmarshal(encoded, &document))
	require.Equal(t, model.PersonalAccessTokenActions(), document.Components.Schemas["PersonalAccessTokenScope"].Enum)
	require.Equal(t, "#/components/schemas/PersonalAccessTokenScope",
		document.Components.Schemas["CreatePersonalAccessTokenRequest"].Properties["scopes"].Items.Ref)
}

// The catalogue lists at most 50 Academic Unit roots, so a caller who may
// view exams in 51 units sees the first 50 by display name and is told there
// are more.
func TestGrantablePersonalAccessTokenScopesRouteLocalizesCopy(t *testing.T) {
	t.Parallel()

	logger, _ := routetest.NewLogger(t)
	now := time.Now()
	principal := model.Principal{
		UserID: model.NewUserID(), SessionID: model.NewSessionID(),
		CredentialID:         model.PrincipalCredentialID(model.NewID()),
		CredentialType:       model.CredentialSessionAccess,
		AuthenticationMethod: "password", AuthenticationStrength: model.AuthenticationSingleFactor,
		AuthenticatedAt: now, ClientType: model.SessionClientWeb,
	}
	units := &accountAcademicUnits{byID: map[model.AcademicUnitID]*model.AcademicUnit{}}
	grants := []accesstest.Grant{}
	wantUnits := []personalAccessTokenAcademicUnitResponse{}
	wantUnitIDs := []string{}
	for index := range 51 {
		unit := &model.AcademicUnit{ID: model.NewAcademicUnitID(), DisplayName: fmt.Sprintf("School of Computing %02d", index)}
		units.byID[unit.ID] = unit
		grants = append(grants, inAcademicUnit(principal.UserID, unit.ID, model.ActionExamView))
		if index < 50 {
			wantUnits = append(wantUnits, personalAccessTokenAcademicUnitResponse{ID: unit.ID.String(), DisplayName: unit.DisplayName})
			wantUnitIDs = append(wantUnitIDs, unit.ID.String())
		}
	}
	fixture := newIdentityFixture(t, identityOptions{Stores: identityStores{AcademicUnits: units}, Grants: grants})
	httpAPI := newFocusedResourceAPI(t, logger, routetest.Authenticator{Principal: principal}, personalAccessTokenResource(fixture.service))

	request := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/api/v1/users/me/tokens/grantable-scopes", nil)
	request.Header.Set("Authorization", "Bearer credential")
	request = request.WithContext(route.WithLocalization(request.Context(), repositoryLocalizer(t), "en"))
	response := httptest.NewRecorder()
	httpAPI.ServeHTTP(response, request)
	require.Equal(t, http.StatusOK, response.Code, response.Body.String())
	require.Equal(t, "no-store", response.Header().Get("Cache-Control"))
	require.Equal(t, 51, units.lookupCount(), "every delegable root was read from the Academic Unit Store")

	var body personalAccessTokenGrantableScopesResponse
	require.NoError(t, json.Unmarshal(response.Body.Bytes(), &body))
	require.True(t, body.AcademicUnitsHasMore)
	require.Equal(t, wantUnits, body.AcademicUnits)
	require.Len(t, body.Scopes, 1)
	scope := body.Scopes[0]
	require.Equal(t, "exam.view", scope.Name)
	require.Equal(t, "View exams", scope.Title)
	require.NotEmpty(t, scope.Description)
	require.Equal(t, []string{"exam"}, scope.ResourceTypes)
	require.False(t, scope.InstitutionWide)
	slices.Sort(wantUnitIDs)
	require.Equal(t, wantUnitIDs, scope.AcademicUnitIDs, "a scope lists the returned roots in ID order")
}

// accountAcademicUnits holds the Academic Units and counts the reads.
type accountAcademicUnits struct {
	store.AcademicUnitStore
	mu      sync.Mutex
	byID    map[model.AcademicUnitID]*model.AcademicUnit
	lookups int
}

func (s *accountAcademicUnits) lookupCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.lookups
}

func (s *accountAcademicUnits) Get(_ context.Context, id model.AcademicUnitID) (*model.AcademicUnit, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.lookups++
	unit, ok := s.byID[id]
	if !ok {
		return nil, store.NewErrNotFound("academic_unit", id.String())
	}
	copied := *unit
	return &copied, nil
}

func TestGrantablePersonalAccessTokenScopesResponseUsesEmptyCollections(t *testing.T) {
	t.Parallel()

	encoded, err := json.Marshal(personalAccessTokenGrantableScopesResponseFromApp(
		httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/", nil),
		&identity.PersonalAccessTokenGrantableScopes{Scopes: []identity.PersonalAccessTokenGrantableScope{{Action: model.ActionAuditView, InstitutionWide: true}}},
	))
	require.NoError(t, err)
	require.JSONEq(t, `{"scopes":[{"name":"audit.view","title":"audit.view","description":"","resource_types":[],"institution_wide":true,"academic_unit_ids":[]}],"academic_units":[],"academic_units_has_more":false}`, string(encoded))
}
