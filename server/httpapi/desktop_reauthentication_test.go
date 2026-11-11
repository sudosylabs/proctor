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

	"github.com/sudosylabs/proctor/server/app/identity"
	"github.com/sudosylabs/proctor/server/app/invocation"
	"github.com/sudosylabs/proctor/server/httpapi/route"
	"github.com/sudosylabs/proctor/server/httpapi/route/routetest"
	"github.com/sudosylabs/proctor/server/model"
)

type desktopReauthenticationHTTPApplication struct {
	DesktopReauthentication
	passwordBinding string
	complete        identity.CompleteDesktopReauthenticationCommand
}

func (a *desktopReauthenticationHTTPApplication) BindDesktopReauthentication(context.Context, invocation.Invocation, identity.BindDesktopAuthorizationCommand) (*identity.DesktopAuthorizationBinding, error) {
	return &identity.DesktopAuthorizationBinding{Binding: "reauthentication-binding", ExpiresAt: time.Now().Add(time.Minute).UnixMilli()}, nil
}

func (a *desktopReauthenticationHTTPApplication) AuthenticateDesktopReauthenticationPassword(_ context.Context, _ invocation.Invocation, command identity.AuthenticateDesktopReauthenticationPasswordCommand) (*identity.DesktopReauthenticationBrowserContext, error) {
	a.passwordBinding = command.Binding
	return &identity.DesktopReauthenticationBrowserContext{State: model.BrowserAuthenticationStateAuthenticated, DisplayName: "Ada Candidate",
		AuthenticationMethod: "password", ExpiresAt: time.Now().Add(time.Minute)}, nil
}

func (a *desktopReauthenticationHTTPApplication) CompleteDesktopReauthentication(_ context.Context, _ invocation.Invocation, command identity.CompleteDesktopReauthenticationCommand) (*identity.DesktopReauthenticationResult, error) {
	a.complete = command
	return &identity.DesktopReauthenticationResult{RecentlyAuthenticatedUntil: time.Date(2026, 8, 1, 9, 17, 0, 0, time.UTC)}, nil
}

func TestDesktopReauthenticationHTTPScopesItsBindingAndReturnsRecency(t *testing.T) {
	t.Parallel()
	logger, _ := routetest.NewLogger(t)
	applicationFake := &desktopReauthenticationHTTPApplication{}
	principal := model.Principal{UserID: model.NewUserID(), SessionID: model.NewSessionID(),
		CredentialID: model.PrincipalCredentialID(model.NewID()), CredentialType: model.CredentialSessionAccess,
		AuthenticationMethod: "password", AuthenticationStrength: model.AuthenticationSingleFactor,
		ClientType: model.SessionClientWeb, AuthenticatedAt: time.Now()}
	httpAPI := newFocusedResourceAPI(t, logger, routetest.Authenticator{Principal: principal},
		desktopReauthenticationResource(applicationFake, testDesktopAuthorizationCookies(t)))

	bind := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/api/v1/auth/desktop/reauthentications/bind",
		strings.NewReader(`{"handle":"handle","browser_proof":"proof","state":"state"}`))
	bind.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	httpAPI.ServeHTTP(response, bind)
	require.Equal(t, http.StatusNoContent, response.Code, response.Body.String())
	cookies := response.Result().Cookies()
	require.Len(t, cookies, 1)
	require.Equal(t, route.BrowserDesktopReauthenticationCookieName, cookies[0].Name)
	require.Equal(t, "/api/v1/auth/desktop/reauthentications", cookies[0].Path, "binding escaped its route family")
	require.True(t, cookies[0].HttpOnly, "binding is readable by page script")
	require.Equal(t, "no-store", response.Header().Get("Cache-Control"))

	password := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/api/v1/auth/desktop/reauthentications/authenticate/password",
		strings.NewReader(`{"password":"synthetic-local-password"}`))
	password.Header.Set("Content-Type", "application/json")
	response = httptest.NewRecorder()
	httpAPI.ServeHTTP(response, password)
	require.Equal(t, http.StatusBadRequest, response.Code, "password proof without the binding cookie: %s", response.Body.String())
	require.Empty(t, applicationFake.passwordBinding, "a request without the binding reached the application")

	password = httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/api/v1/auth/desktop/reauthentications/authenticate/password",
		strings.NewReader(`{"password":"synthetic-local-password"}`))
	password.Header.Set("Content-Type", "application/json")
	password.AddCookie(&http.Cookie{Name: route.BrowserDesktopReauthenticationCookieName, Value: "reauthentication-binding"})
	response = httptest.NewRecorder()
	httpAPI.ServeHTTP(response, password)
	require.Equal(t, http.StatusOK, response.Code, response.Body.String())
	require.Equal(t, "reauthentication-binding", applicationFake.passwordBinding)
	require.Contains(t, response.Body.String(), `"authentication_method":"password"`)
	require.NotContains(t, response.Body.String(), `"provider"`, "a password reauthentication offered a provider")

	complete := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/api/v1/auth/desktop/reauthentications/complete",
		strings.NewReader(`{"code":"code","code_verifier":"verifier","state":"state"}`))
	complete.Header.Set("Content-Type", "application/json")
	complete.Header.Set("Authorization", "Bearer access")
	response = httptest.NewRecorder()
	httpAPI.ServeHTTP(response, complete)
	require.Equal(t, http.StatusOK, response.Code, response.Body.String())
	require.JSONEq(t, `{"recently_authenticated_until":"2026-08-01T09:17:00Z"}`, response.Body.String())
	require.Equal(t, identity.CompleteDesktopReauthenticationCommand{Code: "code", CodeVerifier: "verifier", State: "state", Source: applicationFake.complete.Source}, applicationFake.complete)
}
