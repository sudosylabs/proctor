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

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/sudosylabs/proctor/server/app/administration"
	"github.com/sudosylabs/proctor/server/app/apperror"
	"github.com/sudosylabs/proctor/server/app/invocation"
	"github.com/sudosylabs/proctor/server/model"
	"github.com/sudosylabs/proctor/server/store"
)

type replayingAccessPolicyHTTPApplication struct {
	command     *administration.ReplaceAccessPolicyCommand
	view        administration.AccessPolicyView
	preflight   administration.AccessPolicyPreflightResult
	preflighted *administration.PreflightAccessPolicyCommand
	discoveries int
}

func (a *replayingAccessPolicyHTTPApplication) GetAccessPolicy(context.Context, invocation.Invocation) (administration.AccessPolicyView, error) {
	return a.view, nil
}
func (a *replayingAccessPolicyHTTPApplication) PreflightAccessPolicy(_ context.Context, _ invocation.Invocation, command administration.PreflightAccessPolicyCommand) (administration.AccessPolicyPreflightResult, error) {
	a.preflighted = &command
	return a.preflight, nil
}
func (a *replayingAccessPolicyHTTPApplication) DiscoverAccess(context.Context, invocation.Invocation) (administration.PublicAccessDiscovery, error) {
	a.discoveries++
	return administration.PublicAccessDiscovery{}, nil
}
func (a *replayingAccessPolicyHTTPApplication) ReplaceAccessPolicy(_ context.Context, _ invocation.Invocation, command administration.ReplaceAccessPolicyCommand) (administration.AccessPolicyView, error) {
	if a.command == nil {
		stored := command
		a.command = &stored
		return a.view, nil
	}
	if a.command.IdempotencyKey == command.IdempotencyKey &&
		(a.command.ExpectedRevision != command.ExpectedRevision || a.command.RevokeExistingSessions != command.RevokeExistingSessions ||
			a.command.Settings.DesktopAuthorizationEnabled != command.Settings.DesktopAuthorizationEnabled) {
		return administration.AccessPolicyView{}, apperror.New("idempotency.conflict")
	}
	return a.view, nil
}

func TestPublicAccessDiscoveryDTOContainsNoPrivatePolicyOrDestinations(t *testing.T) {
	t.Parallel()
	institutionID := model.NewInstitutionID()
	response := publicAccessDiscoveryResponseFromView(administration.PublicAccessDiscovery{
		DiscoveryVersion: 1, CanonicalOrigin: "https://proctor.example.edu", InstallationID: institutionID.String(), Initialized: true,
		Institution:    &administration.PublicInstitutionPresentation{ID: institutionID, Name: "northbridge", DisplayName: "Northbridge"},
		PolicyRevision: 4, Capabilities: administration.PublicAccessCapabilities{LocalLogin: true, DesktopAuthorization: true},
		Providers:            []model.ExternalAuthenticationProvider{{ID: "campus", DisplayName: "Campus", Type: "oidc"}},
		DesktopAuthorization: administration.DesktopAuthorizationCompatibility{Protocol: "proctor-desktop-authorization", MinimumVersion: 1, MaximumVersion: 1},
		SupportAddress:       "help@example.edu", PasswordMinimumLength: 14,
	})
	encoded, err := json.Marshal(response)
	require.NoError(t, err)
	text := string(encoded)
	requireNoPrivateFieldNames(t, encoded, "provider_admissions", "invitation_local_credential", "client_secret", "redirect", "claims", "recipient")
	require.Contains(t, text, `"canonical_origin":"https://proctor.example.edu"`)
	require.Contains(t, text, `"id":"campus"`)
	require.Contains(t, text, `"support_address":"help@example.edu"`)
	require.Contains(t, text, `"password_minimum_length":14`)

	withoutSupport, err := json.Marshal(publicAccessDiscoveryResponseFromView(administration.PublicAccessDiscovery{PasswordMinimumLength: 12}))
	require.NoError(t, err)
	require.NotContains(t, string(withoutSupport), "support_address")
}

func TestAccessPolicyHTTPRetriesLostResponseAndRejectsConflictingKeyReuse(t *testing.T) {
	t.Parallel()
	logger, _ := newTestLogger(t)
	principal := model.Principal{UserID: model.NewUserID(), SessionID: model.NewSessionID(), CredentialID: model.PrincipalCredentialID(model.NewID()),
		CredentialType: model.CredentialSessionAccess, AuthenticationMethod: "password", AuthenticationStrength: model.AuthenticationMultiFactor,
		AuthenticatedAt: time.Now(), MFACompletedAt: model.OptionalTimeFrom(time.Now()), ClientType: model.SessionClientWeb}
	policy := model.NewInitialAccessPolicy(model.NewAccessPolicyID(), time.Now())
	policy.Revision = 2
	application := &replayingAccessPolicyHTTPApplication{view: administration.AccessPolicyView{Policy: policy, History: []*model.AccessPolicyTransition{}, AvailableProviders: []model.ExternalAuthenticationProvider{}}}
	httpAPI := newFocusedResourceAPI(t, logger, classRouteAuthenticator{principal: principal}, accessPolicyResource(application))
	body := `{"expected_revision":1,"revoke_existing_sessions":true,"local_login_enabled":true,"public_registration_enabled":false,"invitation_admission_enabled":true,"invitation_local_credential_enabled":true,"desktop_authorization_enabled":false,"provider_admissions":{},"role_holder_mfa_required":false,"session_idle_minutes":null,"session_absolute_minutes":null}`
	do := func(candidate string) *httptest.ResponseRecorder {
		request := httptest.NewRequest(http.MethodPut, "/api/v1/access-policy", strings.NewReader(candidate))
		request.Header.Set("Authorization", "Bearer session")
		request.Header.Set("Content-Type", "application/json")
		request.Header.Set("Idempotency-Key", "lost-response")
		response := httptest.NewRecorder()
		httpAPI.ServeHTTP(response, request)
		return response
	}
	first, replay := do(body), do(body)
	require.Equal(t, http.StatusOK, first.Code, "first")
	require.Equal(t, http.StatusOK, replay.Code, "first")
	require.Equal(t, replay.Body.String(), first.Body.String(), "first")
	conflicting := strings.Replace(body, `"revoke_existing_sessions":true`, `"revoke_existing_sessions":false`, 1)
	response := do(conflicting)
	require.Equal(t, http.StatusConflict, response.Code, "conflict")
	require.Contains(t, response.Body.String(), `"code":"idempotency.conflict"`, "conflict")
}

func TestAccessPolicyHTTPCarriesRoleHolderMFARequirementAndPreflightCounts(t *testing.T) {
	t.Parallel()
	logger, _ := newTestLogger(t)
	principal := model.Principal{UserID: model.NewUserID(), SessionID: model.NewSessionID(), CredentialID: model.PrincipalCredentialID(model.NewID()),
		CredentialType: model.CredentialSessionAccess, AuthenticationMethod: "password", AuthenticationStrength: model.AuthenticationMultiFactor,
		AuthenticatedAt: time.Now(), MFACompletedAt: model.OptionalTimeFrom(time.Now()), ClientType: model.SessionClientWeb}
	policy := model.NewInitialAccessPolicy(model.NewAccessPolicyID(), time.Now())
	policy.RoleHolderMFARequired = true
	application := &replayingAccessPolicyHTTPApplication{view: administration.AccessPolicyView{Policy: policy},
		preflight: administration.AccessPolicyPreflightResult{Blockers: []store.AccessPolicyBlocker{{Code: store.AccessPolicyBlockerMFAUnavailable}},
			RoleHoldersWithoutMFACount: 3, RoleHolderSingleFactorSessionCount: 5}}
	httpAPI := newFocusedResourceAPI(t, logger, classRouteAuthenticator{principal: principal}, accessPolicyResource(application))
	do := func(method, path, body string) *httptest.ResponseRecorder {
		request := httptest.NewRequestWithContext(t.Context(), method, path, strings.NewReader(body))
		request.Header.Set("Authorization", "Bearer session")
		request.Header.Set("Content-Type", "application/json")
		response := httptest.NewRecorder()
		httpAPI.ServeHTTP(response, request)
		return response
	}
	read := do(http.MethodGet, "/api/v1/access-policy", "")
	require.Equal(t, http.StatusOK, read.Code, read.Body.String())
	require.Contains(t, read.Body.String(), `"role_holder_mfa_required":true`)
	require.Contains(t, read.Body.String(), `"role_holder_mfa_enforced":false`, "a setting the deployment cannot enforce reads as paused")
	preflight := do(http.MethodPost, "/api/v1/access-policy/preflight", `{"expected_revision":1,"revoke_existing_sessions":true,"local_login_enabled":true,"public_registration_enabled":false,"invitation_admission_enabled":true,"invitation_local_credential_enabled":true,"desktop_authorization_enabled":true,"provider_admissions":{},"role_holder_mfa_required":true,"session_idle_minutes":60,"session_absolute_minutes":null}`)
	require.Equal(t, http.StatusOK, preflight.Code, preflight.Body.String())
	require.JSONEq(t, `{"blockers":[{"code":"mfa_unavailable"}],"role_holders_without_mfa_count":3,"role_holder_single_factor_session_count":5}`, preflight.Body.String())
	require.True(t, application.preflighted.Settings.RoleHolderMFARequired)
	require.Equal(t, 60, application.preflighted.Settings.SessionIdleMinutes)
	require.Zero(t, application.preflighted.Settings.SessionAbsoluteMinutes, "null keeps the deployment default")
}

func TestAccessPolicyHTTPCarriesSessionLengthsAndTheirBounds(t *testing.T) {
	t.Parallel()
	logger, _ := newTestLogger(t)
	principal := model.Principal{UserID: model.NewUserID(), SessionID: model.NewSessionID(), CredentialID: model.PrincipalCredentialID(model.NewID()),
		CredentialType: model.CredentialSessionAccess, AuthenticationMethod: "password", AuthenticationStrength: model.AuthenticationMultiFactor,
		AuthenticatedAt: time.Now(), MFACompletedAt: model.OptionalTimeFrom(time.Now()), ClientType: model.SessionClientWeb}
	policy := model.NewInitialAccessPolicy(model.NewAccessPolicyID(), time.Now())
	policy.SessionIdleMinutes = 480
	application := &replayingAccessPolicyHTTPApplication{view: administration.AccessPolicyView{Policy: policy, SessionLimits: model.SessionLengthLimits{
		IdleMinimumMinutes: 16, IdleDefaultMinutes: 10080, IdleMaximumMinutes: 10080,
		AbsoluteMinimumMinutes: 16, AbsoluteDefaultMinutes: 43200, AbsoluteMaximumMinutes: 43200}}}
	httpAPI := newFocusedResourceAPI(t, logger, classRouteAuthenticator{principal: principal}, accessPolicyResource(application))
	do := func(method, body string) *httptest.ResponseRecorder {
		path := "/api/v1/access-policy"
		if method == http.MethodPost {
			path += "/preflight"
		}
		request := httptest.NewRequestWithContext(t.Context(), method, path, strings.NewReader(body))
		request.Header.Set("Authorization", "Bearer session")
		request.Header.Set("Content-Type", "application/json")
		response := httptest.NewRecorder()
		httpAPI.ServeHTTP(response, request)
		return response
	}
	read := do(http.MethodGet, "")
	require.Equal(t, http.StatusOK, read.Code, read.Body.String())
	var lengths struct {
		Idle            *int `json:"session_idle_minutes"`
		Absolute        *int `json:"session_absolute_minutes"`
		IdleDefault     int  `json:"session_idle_minutes_default"`
		IdleMaximum     int  `json:"session_idle_minutes_maximum"`
		AbsoluteDefault int  `json:"session_absolute_minutes_default"`
		AbsoluteMaximum int  `json:"session_absolute_minutes_maximum"`
	}
	require.NoError(t, json.Unmarshal(read.Body.Bytes(), &lengths))
	require.Equal(t, 480, *lengths.Idle)
	require.Nil(t, lengths.Absolute, "an unset length is null")
	require.Equal(t, 10080, lengths.IdleDefault)
	require.Equal(t, 10080, lengths.IdleMaximum)
	require.Equal(t, 43200, lengths.AbsoluteDefault)
	require.Equal(t, 43200, lengths.AbsoluteMaximum)
	require.Contains(t, read.Body.String(), `"session_absolute_minutes":null`)

	base := `{"expected_revision":1,"revoke_existing_sessions":false,"local_login_enabled":true,"public_registration_enabled":false,"invitation_admission_enabled":true,"invitation_local_credential_enabled":true,"desktop_authorization_enabled":true,"provider_admissions":{},"role_holder_mfa_required":false`
	for _, test := range []struct {
		name, lengths string
	}{
		{"omitted idle length", `,"session_absolute_minutes":null}`},
		{"omitted absolute length", `,"session_idle_minutes":null}`},
		{"zero idle length", `,"session_idle_minutes":0,"session_absolute_minutes":null}`},
		{"negative absolute length", `,"session_idle_minutes":null,"session_absolute_minutes":-5}`},
	} {
		t.Run(test.name, func(t *testing.T) {
			response := do(http.MethodPost, base+test.lengths)
			require.Equal(t, http.StatusBadRequest, response.Code, response.Body.String())
			require.Contains(t, response.Body.String(), `"code":"request.invalid"`)
		})
	}
}

func TestAccessPolicyHTTPRequiresExplicitSessionRevocationChoice(t *testing.T) {
	t.Parallel()
	logger, _ := newTestLogger(t)
	principal := model.Principal{UserID: model.NewUserID(), SessionID: model.NewSessionID(), CredentialID: model.PrincipalCredentialID(model.NewID()),
		CredentialType: model.CredentialSessionAccess, AuthenticationMethod: "password", AuthenticationStrength: model.AuthenticationMultiFactor,
		AuthenticatedAt: time.Now(), MFACompletedAt: model.OptionalTimeFrom(time.Now()), ClientType: model.SessionClientWeb}
	application := &replayingAccessPolicyHTTPApplication{}
	httpAPI := newFocusedResourceAPI(t, logger, classRouteAuthenticator{principal: principal}, accessPolicyResource(application))
	for _, candidate := range []string{
		`{"expected_revision":1,"local_login_enabled":true,"public_registration_enabled":false,"invitation_admission_enabled":true,"invitation_local_credential_enabled":true,"desktop_authorization_enabled":false,"provider_admissions":{},"role_holder_mfa_required":false,"session_idle_minutes":null,"session_absolute_minutes":null}`,
		`{"expected_revision":1,"revoke_existing_sessions":null,"local_login_enabled":true,"public_registration_enabled":false,"invitation_admission_enabled":true,"invitation_local_credential_enabled":true,"desktop_authorization_enabled":false,"provider_admissions":{},"role_holder_mfa_required":false,"session_idle_minutes":null,"session_absolute_minutes":null}`,
	} {
		request := httptest.NewRequest(http.MethodPut, "/api/v1/access-policy", strings.NewReader(candidate))
		request.Header.Set("Authorization", "Bearer session")
		request.Header.Set("Content-Type", "application/json")
		request.Header.Set("Idempotency-Key", "explicit-choice")
		response := httptest.NewRecorder()
		httpAPI.ServeHTTP(response, request)
		require.Equal(t, http.StatusBadRequest, response.Code, "response")
		require.Contains(t, response.Body.String(), `"code":"request.invalid"`, "response")
	}
}

func TestAccessPolicyHTTPRequiresEveryCompleteReplacementSetting(t *testing.T) {
	t.Parallel()
	logger, _ := newTestLogger(t)
	principal := model.Principal{UserID: model.NewUserID(), SessionID: model.NewSessionID(), CredentialID: model.PrincipalCredentialID(model.NewID()),
		CredentialType: model.CredentialSessionAccess, AuthenticationMethod: "password", AuthenticationStrength: model.AuthenticationMultiFactor,
		AuthenticatedAt: time.Now(), MFACompletedAt: model.OptionalTimeFrom(time.Now()), ClientType: model.SessionClientWeb}
	application := &replayingAccessPolicyHTTPApplication{}
	httpAPI := newFocusedResourceAPI(t, logger, classRouteAuthenticator{principal: principal}, accessPolicyResource(application))
	valid := map[string]any{
		"expected_revision": 1, "revoke_existing_sessions": false,
		"local_login_enabled": true, "public_registration_enabled": false,
		"invitation_admission_enabled": true, "invitation_local_credential_enabled": true,
		"desktop_authorization_enabled": true,
		"provider_admissions":           map[string]any{},
		"role_holder_mfa_required":      false,
		"session_idle_minutes":          nil,
		"session_absolute_minutes":      nil,
	}
	for _, field := range []string{
		"local_login_enabled", "public_registration_enabled", "invitation_admission_enabled",
		"invitation_local_credential_enabled", "desktop_authorization_enabled", "provider_admissions", "role_holder_mfa_required",
	} {
		field := field
		for _, mutation := range []struct {
			name  string
			apply func(map[string]any)
		}{
			{name: "omitted", apply: func(body map[string]any) { delete(body, field) }},
			{name: "null", apply: func(body map[string]any) { body[field] = nil }},
		} {
			mutation := mutation
			t.Run(field+"/"+mutation.name, func(t *testing.T) {
				body := make(map[string]any, len(valid))
				for key, value := range valid {
					body[key] = value
				}
				mutation.apply(body)
				encoded, err := json.Marshal(body)
				require.NoError(t, err)
				request := httptest.NewRequest(http.MethodPut, "/api/v1/access-policy", strings.NewReader(string(encoded)))
				request.Header.Set("Authorization", "Bearer session")
				request.Header.Set("Content-Type", "application/json")
				request.Header.Set("Idempotency-Key", "complete-replacement-"+field+"-"+mutation.name)
				response := httptest.NewRecorder()
				httpAPI.ServeHTTP(response, request)
				require.Equal(t, http.StatusBadRequest, response.Code, "response")
				require.Contains(t, response.Body.String(), `"code":"request.invalid"`, "response")
				require.Contains(t, response.Body.String(), `"field":"`+field+`"`, "response")
			})
		}
	}
}

func TestAccessPolicyOpenAPIDocument(t *testing.T) {
	t.Parallel()

	document := readOpenAPIDocument(t)
	providerAdmissions := document.Components.Schemas["AccessPolicySettingsRequest"].Properties["provider_admissions"]
	var providerGrammar struct {
		MaxProperties int `json:"maxProperties"`
		PropertyNames struct {
			Pattern string `json:"pattern"`
		} `json:"propertyNames"`
	}
	require.NoError(t, json.Unmarshal(providerAdmissions, &providerGrammar))
	require.Equal(t, model.AccessPolicyProviderMaxCount, providerGrammar.MaxProperties)
	require.Equal(t, `^[a-z0-9][a-z0-9._-]{0,63}$`, providerGrammar.PropertyNames.Pattern)
	providerAdmissionsResponse := document.Components.Schemas["AccessPolicyResponse"].Properties["provider_admissions"]
	providerGrammar = struct {
		MaxProperties int `json:"maxProperties"`
		PropertyNames struct {
			Pattern string `json:"pattern"`
		} `json:"propertyNames"`
	}{}
	require.NoError(t, json.Unmarshal(providerAdmissionsResponse, &providerGrammar))
	require.Equal(t, model.AccessPolicyProviderMaxCount, providerGrammar.MaxProperties)
	var providerProjection struct {
		MaxItems int `json:"maxItems"`
	}
	availableProviders := document.Components.Schemas["AccessPolicyResponse"].Properties["available_providers"]
	require.NoError(t, json.Unmarshal(availableProviders, &providerProjection))
	require.Equal(t, model.AccessPolicyProviderMaxCount, providerProjection.MaxItems)
	for _, responseName := range []string{"AccessPolicyOK", "AccessPolicyPreflightOK", "PublicAccessDiscoveryOK"} {
		assert.Equal(t, "#/components/headers/NoStore", document.Components.Responses[responseName].Headers["Cache-Control"].Ref, "%s does not require no-store", responseName)
	}
	for _, forbidden := range []string{"provider_admissions", "durable_mail", "invitation_local_credential_enabled", "client_secret", "redirect_uri", "claims", "recipient"} {
		_, exists := document.Components.Schemas["PublicAccessDiscoveryResponse"].Properties[forbidden]
		assert.False(t, exists, "PublicAccessDiscoveryResponse exposes forbidden field %q", forbidden)
	}
}
