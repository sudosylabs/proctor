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

	"github.com/sudosylabs/proctor/server/app/identity"
	"github.com/sudosylabs/proctor/server/app/invocation"
	"github.com/sudosylabs/proctor/server/httpapi/route"
	"github.com/sudosylabs/proctor/server/httpapi/route/routetest"
	"github.com/sudosylabs/proctor/server/model"
)

type accountSecurityHTTPApplication struct {
	Sessions
	MFA
	PersonalAccessTokens

	createToken  identity.CreatePersonalAccessTokenCommand
	sessions     []*model.Session
	revokeOthers int
}

func (as *accountSecurityHTTPApplication) ListSessions(
	context.Context,
	invocation.Invocation,
) ([]*model.Session, error) {
	return append([]*model.Session{}, as.sessions...), nil
}

func (as *accountSecurityHTTPApplication) RevokeOtherSessions(
	context.Context,
	invocation.Invocation,
) (*identity.RevokeOtherSessionsResult, error) {
	as.revokeOthers++
	return &identity.RevokeOtherSessionsResult{RevokedSessionCount: 2}, nil
}

func (as *accountSecurityHTTPApplication) GetMFAStatus(
	context.Context,
	invocation.Invocation,
) (*identity.MFAStatus, error) {
	return &identity.MFAStatus{Enabled: true, RecoveryCodesRemaining: 7}, nil
}

func (as *accountSecurityHTTPApplication) CreatePersonalAccessToken(
	_ context.Context,
	_ invocation.Invocation,
	command identity.CreatePersonalAccessTokenCommand,
) (*model.PersonalAccessTokenCreation, error) {
	as.createToken = command
	return &model.PersonalAccessTokenCreation{
		Token: &model.PersonalAccessToken{
			ID: model.NewPersonalAccessTokenID(), UserID: model.NewUserID(),
			Scopes: []string{"user.profile_picture.manage"},
		},
		Credential: "one-time-secret",
	}, nil
}

func TestAccountSecurityResourcesRunThroughFocusedKernel(t *testing.T) {
	t.Parallel()

	logger, _ := routetest.NewLogger(t)
	now := time.Now()
	principal := model.Principal{
		UserID: model.NewUserID(), SessionID: model.NewSessionID(),
		CredentialID:         model.PrincipalCredentialID(model.NewID()),
		CredentialType:       model.CredentialSessionAccess,
		AuthenticationMethod: "password", AuthenticationStrength: model.AuthenticationMultiFactor,
		AuthenticatedAt: now, MFACompletedAt: model.OptionalTimeFrom(now),
		ClientType: model.SessionClientWeb,
	}
	otherSessionID := model.NewSessionID()
	applicationStub := &accountSecurityHTTPApplication{sessions: []*model.Session{
		{ID: principal.SessionID, UserID: principal.UserID, CreatedAt: now},
		{ID: otherSessionID, UserID: principal.UserID, CreatedAt: now.Add(-time.Minute)},
	}}
	cookies, err := route.NewCookies("http://localhost:8065")
	require.NoError(t, err)
	httpAPI := newFocusedResourceAPI(
		t,
		logger,
		routetest.Authenticator{Principal: principal},
		sessionResource(applicationStub, cookies),
		mfaResource(applicationStub),
		personalAccessTokenResource(applicationStub),
	)

	statusRequest := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/api/v1/users/me/mfa", nil)
	statusRequest.Header.Set("Authorization", "Bearer credential")
	statusResponse := httptest.NewRecorder()
	httpAPI.ServeHTTP(statusResponse, statusRequest)
	require.Equal(t, http.StatusOK, statusResponse.Code, "MFA status = %d, cache = %q: %s", statusResponse.Code, statusResponse.Header().Get("Cache-Control"), statusResponse.Body.String())
	require.Equal(t, "no-store", statusResponse.Header().Get("Cache-Control"), "MFA status = %d, cache = %q: %s", statusResponse.Code, statusResponse.Header().Get("Cache-Control"), statusResponse.Body.String())

	listRequest := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/api/v1/users/me/sessions", nil)
	listRequest.Header.Set("Authorization", "Bearer credential")
	listResponse := httptest.NewRecorder()
	httpAPI.ServeHTTP(listResponse, listRequest)
	require.Equal(t, http.StatusOK, listResponse.Code, "session list: %s", listResponse.Body.String())
	var listed struct {
		Items []struct {
			ID      string `json:"id"`
			Current *bool  `json:"current"`
		} `json:"items"`
	}
	require.NoError(t, json.Unmarshal(listResponse.Body.Bytes(), &listed))
	require.Len(t, listed.Items, 2)
	currentByID := map[string]bool{}
	for _, item := range listed.Items {
		require.NotNil(t, item.Current, "session %s omits current", item.ID)
		currentByID[item.ID] = *item.Current
	}
	require.Equal(t, map[string]bool{principal.SessionID.String(): true, otherSessionID.String(): false}, currentByID)

	revokeOthersRequest := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/api/v1/users/me/sessions/revoke-others", nil)
	revokeOthersRequest.Header.Set("Authorization", "Bearer credential")
	revokeOthersResponse := httptest.NewRecorder()
	httpAPI.ServeHTTP(revokeOthersResponse, revokeOthersRequest)
	require.Equal(t, http.StatusOK, revokeOthersResponse.Code, "revoke others: %s", revokeOthersResponse.Body.String())
	require.JSONEq(t, `{"revoked_session_count":2}`, revokeOthersResponse.Body.String())
	require.Empty(t, revokeOthersResponse.Header().Values("Set-Cookie"), "revoke others must leave cookies in place")
	require.Equal(t, 1, applicationStub.revokeOthers)

	createRequest := httptest.NewRequestWithContext(t.Context(),
		http.MethodPost,
		"/api/v1/users/me/tokens",
		strings.NewReader(`{"description":"editor","scopes":["user.profile_picture.manage"],"expires_at":"1970-01-01T00:00:00.123Z"}`),
	)
	createRequest.Header.Set("Authorization", "Bearer credential")
	createRequest.Header.Set("Content-Type", "application/json")
	createResponse := httptest.NewRecorder()
	httpAPI.ServeHTTP(createResponse, createRequest)
	require.Equal(t, http.StatusCreated, createResponse.Code, "token create = %d, cache = %q: %s", createResponse.Code, createResponse.Header().Get("Cache-Control"), createResponse.Body.String())
	require.Equal(t, "no-store", createResponse.Header().Get("Cache-Control"), "token create = %d, cache = %q: %s", createResponse.Code, createResponse.Header().Get("Cache-Control"), createResponse.Body.String())
	require.Equal(t, "editor", applicationStub.createToken.Description)
	require.Equal(t, int64(123), applicationStub.createToken.ExpiresAt)
	var created map[string]any
	require.NoError(t, json.Unmarshal(createResponse.Body.Bytes(), &created))
	require.Equal(t, "one-time-secret", created["credential"])

	invalidRequest := httptest.NewRequestWithContext(t.Context(),
		http.MethodPost,
		"/api/v1/users/me/tokens",
		strings.NewReader(`{"description":"editor","scopes":[],"expires_at":"1970-01-01T00:00:00.123Z","unknown":true}`),
	)
	invalidRequest.Header.Set("Authorization", "Bearer credential")
	invalidRequest.Header.Set("Content-Type", "application/json")
	invalidResponse := httptest.NewRecorder()
	httpAPI.ServeHTTP(invalidResponse, invalidRequest)
	require.Equal(t, http.StatusBadRequest, invalidResponse.Code, "strict decode status = %d: %s", invalidResponse.Code, invalidResponse.Body.String())

	missingCredential := httptest.NewRecorder()
	httpAPI.ServeHTTP(missingCredential, httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/api/v1/users/me/sessions", nil))
	require.Equal(t, http.StatusUnauthorized, missingCredential.Code, "missing credential status = %d: %s", missingCredential.Code, missingCredential.Body.String())

	weakPrincipal := principal
	weakPrincipal.AuthenticationStrength = model.AuthenticationSingleFactor
	weakPrincipal.MFACompletedAt = model.OptionalTime{}
	weakAPI := newFocusedResourceAPI(
		t,
		logger,
		routetest.Authenticator{Principal: weakPrincipal},
		mfaResource(applicationStub),
	)
	weakRequest := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/api/v1/users/me/mfa/disable", nil)
	weakRequest.Header.Set("Authorization", "Bearer credential")
	weakResponse := httptest.NewRecorder()
	weakAPI.ServeHTTP(weakResponse, weakRequest)
	require.Equal(t, http.StatusForbidden, weakResponse.Code, "weak assurance response")
	require.Contains(t, weakResponse.Body.String(), `"code":"authentication.strong_required"`, "weak assurance response")

	stalePrincipal := principal
	stalePrincipal.AuthenticatedAt = now.Add(-time.Hour)
	stalePrincipal.MFACompletedAt = model.OptionalTimeFrom(now.Add(-time.Hour))
	staleAPI := newFocusedResourceAPI(
		t,
		logger,
		routetest.Authenticator{Principal: stalePrincipal},
		personalAccessTokenResource(applicationStub),
	)
	staleRequest := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/api/v1/users/me/tokens", strings.NewReader(`{"description":"editor","scopes":[],"expires_at":"1970-01-01T00:00:00.123Z"}`))
	staleRequest.Header.Set("Authorization", "Bearer credential")
	staleResponse := httptest.NewRecorder()
	staleAPI.ServeHTTP(staleResponse, staleRequest)
	require.Equal(t, http.StatusForbidden, staleResponse.Code, "stale assurance response")
	require.Contains(t, staleResponse.Body.String(), `"code":"authentication.reauthentication_required"`, "stale assurance response")
}
