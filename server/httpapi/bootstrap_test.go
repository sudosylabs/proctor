// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/sudosylabs/proctor/server/app/identity"
	"github.com/sudosylabs/proctor/server/httpapi/route"
	"github.com/sudosylabs/proctor/server/httpapi/route/routetest"
	"github.com/sudosylabs/proctor/server/model"
	"github.com/sudosylabs/proctor/server/store"
)

type systemHTTPHealth struct {
	live  bool
	ready bool
}

func (h systemHTTPHealth) Live() bool  { return h.live }
func (h systemHTTPHealth) Ready() bool { return h.ready }

func TestSystemResourceReturnsTypedUnhealthyProblemThroughKernel(t *testing.T) {
	t.Parallel()
	logger, _ := routetest.NewLogger(t)
	httpAPI := newFocusedResourceAPI(t, logger, routetest.Authenticator{}, systemResource(systemHTTPHealth{}, BuildInfo{Version: "test"}))
	response := httptest.NewRecorder()
	httpAPI.ServeHTTP(response, httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/health/live", nil))
	require.Equal(t, http.StatusServiceUnavailable, response.Code, "liveness status = %d: %s", response.Code, response.Body.String())
	var problem route.Problem
	require.NoError(t, json.Unmarshal(response.Body.Bytes(), &problem))
	require.Equal(t, "health.not_live", problem.Code)
	require.Equal(t, "The process is not healthy.", problem.Detail)
}

func TestSystemResourceReturnsHealthyStatusAndVersionThroughKernel(t *testing.T) {
	t.Parallel()
	logger, _ := routetest.NewLogger(t)
	httpAPI := newFocusedResourceAPI(
		t,
		logger,
		routetest.Authenticator{},
		systemResource(systemHTTPHealth{live: true, ready: true}, BuildInfo{Version: "test-version"}),
	)
	for path, body := range map[string]string{
		"/health/live":           `"status":"ok"`,
		"/health/ready":          `"status":"ok"`,
		"/api/v1/system/version": `"version":"test-version"`,
	} {
		response := httptest.NewRecorder()
		httpAPI.ServeHTTP(response, httptest.NewRequestWithContext(t.Context(), http.MethodGet, path, nil))
		require.Equal(t, http.StatusOK, response.Code, "%s response = %d %s", path, response.Code, response.Body.String())
		require.True(t, bytes.Contains(response.Body.Bytes(), []byte(body)), "%s response = %d %s", path, response.Code, response.Body.String())
	}
}

// desktopBootstrapSecret is the deployment bootstrap secret the fixture's
// service is configured with.
const desktopBootstrapSecret = "deployment-bootstrap-secret"

// desktopInstallations is the installation aggregate. Bootstrap records each
// input and either fails with err or commits the input as the installation.
type desktopInstallations struct {
	store.InstallationStore
	state  *model.InstallationState
	err    error
	inputs []*store.InstallationBootstrap
}

func (s *desktopInstallations) Get(context.Context) (*model.InstallationState, error) {
	if s.state == nil {
		return nil, store.NewErrNotFound("installation", "")
	}
	copied := *s.state
	return &copied, nil
}

func (s *desktopInstallations) Bootstrap(_ context.Context, input *store.InstallationBootstrap) (*model.InstallationBootstrapResult, error) {
	s.inputs = append(s.inputs, input)
	if s.err != nil {
		return nil, s.err
	}
	institution := *input.Institution
	institution.ID = model.NewInstitutionID()
	s.state = &model.InstallationState{
		InitializedAt: input.Administrator.CreatedAt, InstitutionID: institution.ID, AdministratorUserID: input.Administrator.ID,
	}
	administrator := *input.Administrator
	return &model.InstallationBootstrapResult{State: s.state, Institution: &institution, Administrator: &administrator}, nil
}

// bootstrapAPI serves the bootstrap resource over the real service and the
// installation aggregate.
func bootstrapAPI(t *testing.T, installations *desktopInstallations) (http.Handler, *identityFixture) {
	t.Helper()
	logger, _ := routetest.NewLogger(t)
	fixture := newIdentityFixture(t, identityOptions{
		Stores:          identityStores{Installations: installations},
		BootstrapSecret: desktopBootstrapSecret,
	})
	return newFocusedResourceAPI(t, logger, routetest.Authenticator{}, bootstrapResource(fixture.service)), fixture
}

func bootstrapBody(t *testing.T, password, secret string) *bytes.Reader {
	t.Helper()
	body, err := json.Marshal(map[string]any{
		"institution":      map[string]any{"name": "northbridge", "display_name": "Northbridge"},
		"administrator":    map[string]any{"username": "admin", "email": "admin@example.edu"},
		"password":         password,
		"bootstrap_secret": secret,
	})
	require.NoError(t, err)
	return bytes.NewReader(body)
}

func TestBootstrapResourceStrictDecodeAndDeclaredFailureThroughKernel(t *testing.T) {
	t.Parallel()
	installations := &desktopInstallations{err: errors.New("database unavailable")}
	httpAPI, _ := bootstrapAPI(t, installations)

	invalid := httptest.NewRecorder()
	httpAPI.ServeHTTP(invalid, newJSONRequest(http.MethodPost, "/api/v1/bootstrap", bytes.NewBufferString(`{"unknown":true}`)))
	require.Equal(t, http.StatusBadRequest, invalid.Code, "strict bootstrap decode = %d %s", invalid.Code, invalid.Body.String())
	require.True(t, bytes.Contains(invalid.Body.Bytes(), []byte(`"code":"request.invalid"`)), "strict bootstrap decode = %d %s", invalid.Code, invalid.Body.String())
	require.Empty(t, installations.inputs, "a body the decoder refused reached the installation")

	failure := httptest.NewRecorder()
	httpAPI.ServeHTTP(failure, newJSONRequest(http.MethodPost, "/api/v1/bootstrap", bootstrapBody(t, "correct-horse-battery", desktopBootstrapSecret)))
	require.Equal(t, http.StatusServiceUnavailable, failure.Code, "bootstrap failure = %d %s", failure.Code, failure.Body.String())
	require.True(t, bytes.Contains(failure.Body.Bytes(), []byte(`"code":"installation.unavailable"`)), "bootstrap failure = %d %s", failure.Code, failure.Body.String())
	// An ambiguous commit is retried once before it is reported unavailable.
	require.Len(t, installations.inputs, 2)
}

func TestBootstrapStatusExposesOnlyInitializedFlag(t *testing.T) {
	t.Parallel()
	httpAPI, _ := bootstrapAPI(t, &desktopInstallations{state: &model.InstallationState{
		InitializedAt: time.UnixMilli(1_000).UTC(), InstitutionID: model.NewInstitutionID(), AdministratorUserID: model.NewUserID(),
	}})
	response := httptest.NewRecorder()
	httpAPI.ServeHTTP(response, httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/api/v1/bootstrap", nil))
	require.Equal(t, http.StatusOK, response.Code, "status = %d: %s", response.Code, response.Body.String())
	var body map[string]any
	require.NoError(t, json.Unmarshal(response.Body.Bytes(), &body))
	require.Equal(t, true, body["initialized"])
	for _, key := range []string{"institution_id", "administrator_user_id", "state", "role", "password"} {
		_, exists := body[key]
		require.False(t, exists, "status leaked %q: %#v", key, body)
	}
}

func TestBootstrapDeniedDoesNotEchoSecretOrAccountDetail(t *testing.T) {
	t.Parallel()
	installations := &desktopInstallations{}
	httpAPI, _ := bootstrapAPI(t, installations)
	const secret = "wrong-secret-material-that-must-not-be-echoed"
	response := httptest.NewRecorder()
	httpAPI.ServeHTTP(response, newJSONRequest(http.MethodPost, "/api/v1/bootstrap", bootstrapBody(t, "password", secret)))
	require.Equal(t, http.StatusForbidden, response.Code, "response = %d %s", response.Code, response.Body.String())
	require.True(t, bytes.Contains(response.Body.Bytes(), []byte(`"code":"installation.bootstrap_denied"`)), "response = %d %s", response.Code, response.Body.String())
	require.False(t, bytes.Contains(response.Body.Bytes(), []byte(secret)), "response = %d %s", response.Code, response.Body.String())
	require.False(t, bytes.Contains(response.Body.Bytes(), []byte("admin@example.edu")), "response = %d %s", response.Code, response.Body.String())
	require.Empty(t, installations.inputs, "a denied bootstrap reached the installation")
}

func TestBootstrapResponseDTOPreservesHistoricalEnvelope(t *testing.T) {
	t.Parallel()
	institutionID := model.NewID()
	adminID := model.NewID()
	roleID := model.NewID()
	bindingID := model.NewID()
	result := &model.InstallationBootstrapResult{
		State: &model.InstallationState{
			InitializedAt: model.TimeFromMillis(100), InstitutionID: model.InstitutionID(institutionID), AdministratorUserID: model.UserID(adminID),
		},
		Institution: &model.Institution{
			ID: model.InstitutionID(institutionID), CreatedAt: model.TimeFromMillis(100), UpdatedAt: model.TimeFromMillis(100),
			Name: "northbridge", DisplayName: "Northbridge",
		},
		Administrator: &model.User{
			ID: model.UserID(adminID), CreatedAt: model.TimeFromMillis(100), UpdatedAt: model.TimeFromMillis(100),
			Username: "admin", Email: "admin@example.com",
			Locale: "en", Timezone: "UTC",
		},
		Role: &model.Role{
			ID: model.RoleID(roleID), CreatedAt: model.TimeFromMillis(100), UpdatedAt: model.TimeFromMillis(100), Name: model.SystemAdministratorRoleName,
			DisplayName: "System Administrator", BuiltIn: true,
			Permissions: []string{string(model.ActionUserView)},
		},
		RoleBinding: &model.RoleBinding{
			ID: model.RoleBindingID(bindingID), CreatedAt: model.TimeFromMillis(100), UpdatedAt: model.TimeFromMillis(100), UserID: model.UserID(adminID), RoleID: model.RoleID(roleID),
			ScopeType: model.RoleScopeInstitution, ScopeID: institutionID, StartsAt: model.TimeFromMillis(100),
		},
		AccessPolicy: model.NewInitialAccessPolicy(model.NewAccessPolicyID(), model.TimeFromMillis(100)),
	}
	encoded, err := json.Marshal(installationBootstrapResponseFromModel(nil, result))
	require.NoError(t, err)
	var decoded installationBootstrapResponse
	require.NoError(t, json.Unmarshal(encoded, &decoded))
	require.NotNil(t, decoded.State, "decoded historical envelope = %#v from %s", decoded, encoded)
	require.Equal(t, institutionID, decoded.State.InstitutionID, "decoded historical envelope = %#v from %s", decoded, encoded)
	require.NotNil(t, decoded.Administrator, "decoded historical envelope = %#v from %s", decoded, encoded)
	require.Equal(t, adminID, decoded.Administrator.ID, "decoded historical envelope = %#v from %s", decoded, encoded)
	require.NotNil(t, decoded.Role, "decoded historical envelope = %#v from %s", decoded, encoded)
	require.Equal(t, model.SystemAdministratorRoleName, decoded.Role.Name, "decoded historical envelope = %#v from %s", decoded, encoded)
	require.True(t, decoded.Role.BuiltIn, "decoded historical envelope = %#v from %s", decoded, encoded)
	require.NotNil(t, decoded.RoleBinding, "decoded historical envelope = %#v from %s", decoded, encoded)
	require.Equal(t, model.RoleScopeInstitution, decoded.RoleBinding.ScopeType, "decoded historical envelope = %#v from %s", decoded, encoded)
}

func TestBootstrapUsesApplicationCommand(t *testing.T) {
	t.Parallel()
	installations := &desktopInstallations{}
	httpAPI, fixture := bootstrapAPI(t, installations)
	request := newJSONRequest(http.MethodPost, "/api/v1/bootstrap", bootstrapBody(t, "correct-horse-battery", desktopBootstrapSecret))
	request.RemoteAddr = "127.0.0.1:1234"
	response := httptest.NewRecorder()
	httpAPI.ServeHTTP(response, request)
	require.Equal(t, http.StatusCreated, response.Code, "status = %d: %s", response.Code, response.Body.String())

	require.Len(t, installations.inputs, 1)
	input := installations.inputs[0]
	require.Equal(t, "northbridge", input.Institution.Name)
	require.Equal(t, "Northbridge", input.Institution.DisplayName)
	require.Equal(t, "admin", input.Administrator.Username)
	require.Equal(t, "admin@example.edu", input.Administrator.Email)
	hasher, err := identity.NewPasswordHasher(identity.PasswordHasherDependencies{Settings: identityPasswordPolicy()})
	require.NoError(t, err)
	require.NoError(t, hasher.Verify(t.Context(), input.PasswordHash, "correct-horse-battery"), "the stored hash does not prove the requested password")
	require.NotEqual(t, [32]byte{}, input.BootstrapSecretDigest)
	require.NotEqual(t, [32]byte{}, input.CommandFingerprint)
	// The request's source is the bootstrap audit's network identity and
	// the key of its rate limit.
	require.Equal(t, "127.0.0.1", input.AuditEvent.IPAddress)
	require.Equal(t, 1, desktopAttemptCount(fixture.cache, "authentication/attempts/installation-bootstrap/source/"))

	var body installationBootstrapResponse
	require.NoError(t, json.Unmarshal(response.Body.Bytes(), &body))
	require.NotNil(t, body.State)
	require.Equal(t, input.Administrator.ID.String(), body.State.AdministratorUserID)
	require.Equal(t, installations.state.InstitutionID.String(), body.State.InstitutionID)
}

// desktopAttemptCount is the number of attempt counters under prefix.
func desktopAttemptCount(cache *identityCache, prefix string) int {
	cache.mu.Lock()
	defer cache.mu.Unlock()
	count := 0
	for key := range cache.counters {
		if strings.HasPrefix(key, prefix) {
			count++
		}
	}
	return count
}
