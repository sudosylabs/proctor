// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package httpapi

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/sudosylabs/proctor/server/app"
	"github.com/sudosylabs/proctor/server/app/invocation"
	"github.com/sudosylabs/proctor/server/model"
)

type recoveryMFAHTTPFake struct{ MFA }

func (recoveryMFAHTTPFake) GetMFAStatus(_ context.Context, invocation invocation.Invocation, _ app.GetMFAStatusQuery) (*app.MFAStatus, error) {
	return &app.MFAStatus{MFARecoveryRequired: invocation.Principal().MFARecoveryRequired, AuthenticationMethod: "oidc", AuthenticationProviderID: "campus", AuthenticationStrength: model.AuthenticationSingleFactor, RecentlyAuthenticated: true}, nil
}
func (recoveryMFAHTTPFake) SetupMFA(context.Context, invocation.Invocation, app.SetupMFACommand) (*app.MFASetup, error) {
	return &app.MFASetup{Secret: "once-only", ProvisioningURI: "otpauth://totp/Proctor", ExpiresAt: time.Now().Add(time.Minute)}, nil
}
func TestMFARestrictedSessionOnlyReachesExplicitRecoveryRoutes(t *testing.T) {
	logger, _ := newTestLogger(t)
	principal := model.Principal{UserID: model.NewUserID(), SessionID: model.NewSessionID(), CredentialID: model.PrincipalCredentialID(model.NewID()), CredentialType: model.CredentialSessionAccess, AuthenticationMethod: "oidc", AuthenticationProviderID: "campus", ExternalIdentityID: model.NewExternalIdentityID(), AuthenticationStrength: model.AuthenticationSingleFactor, ClientType: model.SessionClientWeb, AuthenticatedAt: time.Now(), AuthenticationGeneration: 1, MFARecoveryRequired: true}
	api := newFocusedResourceAPI(t, logger, classRouteAuthenticator{principal: principal}, mfaResource(recoveryMFAHTTPFake{}))
	for _, test := range []struct {
		path, method string
		status       int
	}{
		{"/api/v1/users/me/mfa", http.MethodGet, 200}, {"/api/v1/users/me/mfa/setup", http.MethodPost, 201},
		{"/api/v1/users/me/mfa/challenge", http.MethodPost, 401}, {"/api/v1/users/me/mfa/recovery-codes/regenerate", http.MethodPost, 401}, {"/api/v1/users/me/mfa/disable", http.MethodPost, 401},
	} {
		t.Run(test.path, func(t *testing.T) {
			request := httptest.NewRequestWithContext(t.Context(), test.method, test.path, strings.NewReader(`{"code":"123456"}`))
			request.Header.Set("Authorization", "Bearer recovery")
			request.Header.Set("Content-Type", "application/json")
			response := httptest.NewRecorder()
			api.ServeHTTP(response, request)
			require.Equal(t, test.status, response.Code, "%s = %d: %s", test.path, response.Code, response.Body.String())
			if response.Code < 300 {
				require.Equal(t, "no-store", response.Header().Get("Cache-Control"), "recovery response is cacheable")
			}
		})
	}
}

type statusMFAHTTPFake struct {
	MFA
	status *app.MFAStatus
}

func (f statusMFAHTTPFake) GetMFAStatus(context.Context, invocation.Invocation, app.GetMFAStatusQuery) (*app.MFAStatus, error) {
	return f.status, nil
}

func TestMFAStatusResponseMapsPasswordRecoveryAndRecentWindow(t *testing.T) {
	logger, _ := newTestLogger(t)
	principal := model.Principal{UserID: model.NewUserID(), SessionID: model.NewSessionID(), CredentialID: model.PrincipalCredentialID(model.NewID()), CredentialType: model.CredentialSessionAccess, AuthenticationMethod: "password", AuthenticationStrength: model.AuthenticationMultiFactor, ClientType: model.SessionClientWeb, AuthenticatedAt: time.Now()}
	read := func(status *app.MFAStatus) string {
		api := newFocusedResourceAPI(t, logger, classRouteAuthenticator{principal: principal}, mfaResource(statusMFAHTTPFake{status: status}))
		request := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/api/v1/users/me/mfa", nil)
		request.Header.Set("Authorization", "Bearer status")
		response := httptest.NewRecorder()
		api.ServeHTTP(response, request)
		require.Equal(t, http.StatusOK, response.Code, response.Body.String())
		return response.Body.String()
	}
	body := read(&app.MFAStatus{AuthenticationMethod: "password", AuthenticationStrength: model.AuthenticationMultiFactor, Enabled: true, RecentlyAuthenticated: true,
		RecoveryCodesRemaining: 7, RecoveryCodesTotal: 10, RecentAuthenticationTTL: 15*time.Minute + 59*time.Second,
		PasswordChangedAt:          model.OptionalTimeFrom(time.Date(2026, 7, 1, 9, 30, 0, 0, time.UTC)),
		RecentlyAuthenticatedUntil: model.OptionalTimeFrom(time.Date(2026, 8, 18, 12, 14, 0, 0, time.UTC))})
	require.Contains(t, body, `"recovery_codes_total":10`)
	require.Contains(t, body, `"password_changed_at":"2026-07-01T09:30:00Z"`)
	require.Contains(t, body, `"recent_authentication_minutes":15`)
	require.Contains(t, body, `"recently_authenticated_until":"2026-08-18T12:14:00Z"`)

	body = read(&app.MFAStatus{AuthenticationMethod: "oidc", AuthenticationStrength: model.AuthenticationSingleFactor, RecoveryCodesTotal: 10, RecentAuthenticationTTL: 30 * time.Second})
	require.NotContains(t, body, "password_changed_at")
	require.NotContains(t, body, "recently_authenticated_until")
	require.Contains(t, body, `"recent_authentication_minutes":1`)
}
