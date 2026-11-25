//go:build integration

// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package integration_test

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/sudosylabs/proctor/server/app/identity"
	"github.com/sudosylabs/proctor/server/app/invocation"
	"github.com/sudosylabs/proctor/server/config"
	"github.com/sudosylabs/proctor/server/httpapi/route"
	"github.com/sudosylabs/proctor/server/model"
	"github.com/sudosylabs/proctor/server/store"
	"github.com/sudosylabs/proctor/server/testlib"
)

func TestExternalSecondFactorAllowsDesktopAccessPolicyReplacement(t *testing.T) {
	const (
		origin     = "https://proctor.example.test"
		providerID = "campus-cas"
		subject    = "cas-administrator"
		username   = "campus-administrator"
		factorPath = "/api/v1/auth/external/second-factor"
		policyPath = "/api/v1/access-policy"
	)
	ctx := t.Context()
	cas := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/cas/p3/serviceValidate" || request.URL.Query().Get("ticket") == "" {
			writer.WriteHeader(http.StatusBadRequest)
			return
		}
		writer.Header().Set("Content-Type", "application/xml")
		_, _ = io.WriteString(writer, `<cas:serviceResponse xmlns:cas="urn:cas">
			<cas:authenticationSuccess><cas:user>`+subject+`</cas:user></cas:authenticationSuccess>
			</cas:serviceResponse>`)
	}))
	t.Cleanup(cas.Close)

	persistence := openAuthenticationStore(t, requireAuthenticationDatabase(t))
	build := model.DesktopBuildTuple{
		DesktopRelease: "0.1.0", DesktopBuildID: "integration-build",
		Platform: model.DesktopPlatformDarwin, Architecture: model.DesktopArchitectureARM64, RealtimeProtocol: 1,
		AttemptConfigurationManifestFingerprint: model.CurrentAttemptConfigurationManifestFingerprint(),
		DesktopSettingsRegistryFingerprint:      "fnv1a64:" + strings.Repeat("b", 16),
		DesktopTarget:                           "darwin-arm64", ConfigurationManifest: model.EmptyAttemptConfigurationManifest(),
		CapabilityMatrixIdentity: "integration-matrix",
	}
	node := testlib.Setup(t, testlib.WithStore(persistence), testlib.WithDesktopBuildCatalog(build), testlib.WithConfig(func(cfg *config.Config) {
		cfg.Server.PublicURL = origin
		cfg.Authentication.MFA.Enabled = true
		cfg.Authentication.MFA.EncryptionKey = base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{31}, 32))
		cfg.Authentication.LoginRateLimit.MaximumSourceAttempts = 100
		cfg.Authentication.External.Providers = []config.ExternalAuthenticationProvider{
			{ID: providerID, Type: "cas", DisplayName: "Campus CAS", Enabled: true,
				Settings: json.RawMessage(`{"BaseURL":"` + cas.URL + `/cas","ValidationPath":"/p3/serviceValidate","Timeout":"5s","MaxResponseBytes":65536,"Claims":{"Subject":"user"}}`)},
		}
	}))
	handler := node.Handler()
	bootstrap := performJSONRequest(t, handler, http.MethodPost, "/api/v1/bootstrap", map[string]any{
		"bootstrap_secret": testlib.BootstrapSecret,
		"institution":      map[string]any{"name": "campus-mfa", "display_name": "Campus MFA"},
		"administrator":    map[string]any{"username": username, "email": "campus-admin@example.edu"},
		"password":         "amber walrus ledger tidal frost",
	}, "")
	require.Equal(t, http.StatusCreated, bootstrap.Code, bootstrap.Body.String())

	admin, err := persistence.User().GetByUsername(ctx, username)
	require.NoError(t, err)
	_, err = persistence.SeedExternalIdentity(ctx, &model.ExternalIdentity{UserID: admin.ID, Provider: providerID, Subject: subject})
	require.NoError(t, err)
	_, err = persistence.GetMaster().Exec(ctx, `UPDATE access_policies SET local_login_enabled = FALSE,
        invitation_admission_enabled = FALSE, invitation_local_credential_enabled = FALSE,
		provider_admissions = '{"campus-cas":"linked_only"}', role_holder_mfa_required = TRUE WHERE singleton = 1`)
	require.NoError(t, err)

	webLogin := func() *httptest.ResponseRecorder {
		t.Helper()
		begin := performJSONRequest(t, handler, http.MethodGet, "/api/v1/auth/providers/"+providerID+"/login?return_to=%2Fexams", nil, "")
		require.Equal(t, http.StatusSeeOther, begin.Code, "%s; logs=%s", begin.Body.String(), node.Logs.String())
		return completeSecondFactorCASCallback(t, handler, begin.Header().Get("Location"),
			secondFactorResponseCookie(t, begin, route.BrowserExternalLoginCookieName).Value)
	}
	web := webLogin()
	require.Equal(t, http.StatusSeeOther, web.Code, web.Body.String())
	require.Equal(t, "/exams", web.Header().Get("Location"))
	access := secondFactorResponseCookie(t, web, route.BrowserAccessCookieName)
	initial, err := node.App.Identity.AuthenticateAccess(ctx, access.Value)
	require.NoError(t, err)
	require.Equal(t, model.AuthenticationSingleFactor, initial.AuthenticationStrength)
	require.True(t, initial.MFAEnrollmentRequired)
	webInvocation := invocation.New(*initial, model.RequestMetadata{})
	setup, err := node.App.Identity.SetupMFA(ctx, webInvocation, identity.SetupMFACommand{})
	require.NoError(t, err)
	activated, err := node.App.Identity.ActivateMFA(ctx, webInvocation, identity.ActivateMFACommand{
		Code: integrationTOTP(t, setup.Secret, time.Now().Unix()/30),
	})
	require.NoError(t, err)
	require.GreaterOrEqual(t, len(activated.RecoveryCodes), 2)

	// A completed assisted reset leaves the generation fence in place.
	_, err = persistence.GetMaster().Exec(ctx, `INSERT INTO user_mfa_recovery
        (user_id, generation, reset_at, reenrollment_required, updated_at) VALUES (?, 1, now(), false, now())`, admin.ID.String())
	require.NoError(t, err)

	webPause := webLogin()
	require.Equal(t, http.StatusSeeOther, webPause.Code, webPause.Body.String())
	require.Equal(t, "/login/verify", webPause.Header().Get("Location"))
	for _, cookie := range webPause.Result().Cookies() {
		require.NotEqual(t, route.BrowserAccessCookieName, cookie.Name)
	}
	pauseCookie := secondFactorResponseCookie(t, webPause, route.BrowserExternalSecondFactorCookieName)
	require.True(t, pauseCookie.HttpOnly)
	require.True(t, pauseCookie.Secure)
	require.Equal(t, http.SameSiteLaxMode, pauseCookie.SameSite)
	require.Equal(t, factorPath, pauseCookie.Path)
	contextResponse := callExternalSecondFactor(t, handler, http.MethodGet, factorPath, pauseCookie, "")
	require.Equal(t, http.StatusOK, contextResponse.Code, contextResponse.Body.String())
	require.Contains(t, contextResponse.Body.String(), `"provider_display_name":"Campus CAS"`)
	require.Equal(t, "no-store", contextResponse.Header().Get("Cache-Control"))
	wrongCookie := &http.Cookie{Name: pauseCookie.Name, Value: model.NewCredentialToken()}
	invalid := callExternalSecondFactor(t, handler, http.MethodPost, factorPath, wrongCookie, "invalid")
	require.Equal(t, http.StatusUnauthorized, invalid.Code, invalid.Body.String())
	wrongCode := callExternalSecondFactor(t, handler, http.MethodPost, factorPath, pauseCookie, "invalid")
	require.Contains(t, wrongCode.Body.String(), `"code":"authentication.mfa.code_invalid"`)
	success := callExternalSecondFactor(t, handler, http.MethodPost, factorPath, pauseCookie, activated.RecoveryCodes[0])
	require.Equal(t, http.StatusOK, success.Code, success.Body.String())
	require.JSONEq(t, `{"return_to":"/exams"}`, success.Body.String())
	require.Negative(t, secondFactorResponseCookie(t, success, route.BrowserExternalSecondFactorCookieName).MaxAge)
	webPrincipal, err := node.App.Identity.AuthenticateAccess(ctx, secondFactorResponseCookie(t, success, route.BrowserAccessCookieName).Value)
	require.NoError(t, err)
	require.Equal(t, model.AuthenticationMultiFactor, webPrincipal.AuthenticationStrength)
	require.EqualValues(t, 1, webPrincipal.AuthenticationGeneration)
	audits, err := persistence.Audit().List(ctx, store.AuditListOptions{
		Actions: []string{string(model.ActionMFAChallenge)}, ActorID: admin.ID, Limit: 10,
		Visibility: store.AuditVisibilityScope{InstitutionWide: true},
	})
	require.NoError(t, err)
	require.Len(t, audits, 2)
	require.Equal(t, model.AuditStatusSuccess, audits[0].Status)
	require.Equal(t, "authentication.mfa.code_invalid", audits[1].ErrorCode)

	expiryPause := webLogin()
	require.Equal(t, http.StatusSeeOther, expiryPause.Code, expiryPause.Body.String())
	expiring := secondFactorResponseCookie(t, expiryPause, route.BrowserExternalSecondFactorCookieName)
	_, err = persistence.GetMaster().Exec(ctx, `UPDATE external_second_factor_states
		SET created_at = clock_timestamp() - interval '2 minutes', expires_at = clock_timestamp() - interval '1 second'
		WHERE binding_hash = ?`, model.HashToken(expiring.Value))
	require.NoError(t, err)
	expired := callExternalSecondFactor(t, handler, http.MethodPost, factorPath, expiring, "invalid")
	require.Equal(t, http.StatusGone, expired.Code, expired.Body.String())
	require.Contains(t, expired.Body.String(), `"code":"authentication.external.second_factor_expired"`)

	state, verifier := model.NewCredentialToken(), model.NewCredentialToken()
	privateKey, publicJWK := integrationDesktopDPoPKey(t)
	started, err := node.App.Identity.StartDesktopAuthorization(ctx, invocation.Invocation{}, identity.StartDesktopAuthorizationCommand{
		CallbackURL: "http://127.0.0.1:49152/" + model.NewCredentialToken(), State: state, CodeChallenge: model.PKCES256Challenge(verifier),
		DeviceName: "Exam laptop", PublicJWK: publicJWK, DesktopRelease: build.DesktopRelease, DesktopBuildID: build.DesktopBuildID,
		Platform: build.Platform, Architecture: build.Architecture, RealtimeProtocol: build.RealtimeProtocol, Source: "127.0.0.1",
	})
	require.NoError(t, err)
	authorizationURL, err := url.Parse(started.AuthorizationURL)
	require.NoError(t, err)
	bound, err := node.App.Identity.BindDesktopAuthorization(ctx, invocation.Invocation{}, identity.BindDesktopAuthorizationCommand{
		Handle: authorizationURL.Query().Get("request"), BrowserProof: strings.TrimPrefix(authorizationURL.Fragment, "proof="), State: state,
	})
	require.NoError(t, err)
	beginDesktop, err := node.App.Identity.BeginDesktopExternalAuthentication(ctx, invocation.Invocation{}, identity.BeginDesktopExternalAuthenticationCommand{
		ProviderID: providerID, Binding: bound.Binding, State: state, Source: "127.0.0.1",
	})
	require.NoError(t, err)
	desktopPause := completeSecondFactorCASCallback(t, handler, beginDesktop.RedirectURL, beginDesktop.Binding)
	require.Equal(t, http.StatusSeeOther, desktopPause.Code, desktopPause.Body.String())
	require.Equal(t, "/authorize/desktop/verify?state="+state, desktopPause.Header().Get("Location"))
	desktopContext, err := node.App.Identity.DesktopAuthorizationContext(ctx, invocation.Invocation{}, bound.Binding)
	require.NoError(t, err)
	require.Equal(t, model.BrowserAuthenticationStateSecondFactorRequired, desktopContext.State)
	completed := callExternalSecondFactor(t, handler, http.MethodPost, factorPath,
		secondFactorResponseCookie(t, desktopPause, route.BrowserExternalSecondFactorCookieName), activated.RecoveryCodes[1])
	require.Equal(t, http.StatusOK, completed.Code, completed.Body.String())
	require.Contains(t, completed.Body.String(), `"state":"authenticated"`)
	approved, err := node.App.Identity.ApproveDesktopAuthorization(ctx, invocation.Invocation{}, identity.ApproveDesktopAuthorizationCommand{Binding: bound.Binding, State: state})
	require.NoError(t, err)
	approvedURL, err := url.Parse(approved.RedirectURL)
	require.NoError(t, err)
	exchanged, err := node.App.Identity.ExchangeDesktopAuthorization(ctx, invocation.Invocation{}, identity.ExchangeDesktopAuthorizationCommand{
		Code: approvedURL.Query().Get("code"), State: state, CodeVerifier: verifier,
		DPoPProof: integrationDesktopDPoPProof(t, privateKey, publicJWK, map[string]any{
			"jti": model.NewCredentialToken(), "htm": "POST", "htu": origin + "/api/v1/auth/desktop/token",
			"iat": time.Now().Unix(), "nonce": started.DPoPNonce,
		}),
		PublicJWK: publicJWK, DesktopRelease: build.DesktopRelease, DesktopBuildID: build.DesktopBuildID,
		Platform: build.Platform, Architecture: build.Architecture, RealtimeProtocol: build.RealtimeProtocol, Source: "127.0.0.1",
	})
	require.NoError(t, err)
	require.Equal(t, model.AuthenticationMultiFactor, exchanged.Session.AuthenticationStrength)

	policy, err := node.App.Administration.GetAccessPolicy(ctx, invocation.New(*webPrincipal, model.RequestMetadata{}))
	require.NoError(t, err)
	payload, err := json.Marshal(map[string]any{
		"expected_revision": policy.Policy.Revision, "local_login_enabled": false,
		"invitation_admission_enabled": false, "invitation_local_credential_enabled": false,
		"public_registration_enabled": false, "desktop_authorization_enabled": true,
		"provider_admissions": map[string]string{providerID: "linked_only"}, "role_holder_mfa_required": true,
		"revoke_existing_sessions": false, "session_idle_minutes": 30, "session_absolute_minutes": nil,
	})
	require.NoError(t, err)
	accessHash := sha256.Sum256([]byte(exchanged.Tokens.AccessToken))
	nonce := exchanged.DPoPNonce
	for range 2 {
		request := httptest.NewRequestWithContext(ctx, http.MethodPut, policyPath, bytes.NewReader(payload))
		request.Header.Set("Content-Type", "application/json")
		request.Header.Set("Idempotency-Key", "access-policy-from-cas-desktop")
		request.Header.Set("Authorization", "DPoP "+exchanged.Tokens.AccessToken)
		request.Header.Set("DPoP", integrationDesktopDPoPProof(t, privateKey, publicJWK, map[string]any{
			"jti": model.NewCredentialToken(), "htm": "PUT", "htu": origin + policyPath, "iat": time.Now().Unix(),
			"nonce": nonce, "ath": base64.RawURLEncoding.EncodeToString(accessHash[:]),
		}))
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		if response.Code == http.StatusUnauthorized && response.Header().Get("DPoP-Nonce") != "" {
			nonce = response.Header().Get("DPoP-Nonce")
			continue
		}
		require.Equal(t, http.StatusOK, response.Code, response.Body.String())
		require.Contains(t, response.Body.String(), `"session_idle_minutes":30`)
		return
	}
	t.Fatal("policy replacement kept challenging the Desktop nonce")
}

func completeSecondFactorCASCallback(t *testing.T, handler http.Handler, redirect, binding string) *httptest.ResponseRecorder {
	t.Helper()
	providerURL, err := url.Parse(redirect)
	require.NoError(t, err)
	callback, err := url.Parse(providerURL.Query().Get("service"))
	require.NoError(t, err)
	query := callback.Query()
	query.Set("ticket", "ST-"+model.NewCredentialToken())
	callback.RawQuery = query.Encode()
	request := httptest.NewRequestWithContext(t.Context(), http.MethodGet, callback.RequestURI(), nil)
	request.AddCookie(&http.Cookie{Name: route.BrowserExternalLoginCookieName, Value: binding})
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	return response
}

func callExternalSecondFactor(t *testing.T, handler http.Handler, method, path string, binding *http.Cookie, code string) *httptest.ResponseRecorder {
	t.Helper()
	var body io.Reader
	if method == http.MethodPost {
		encoded, err := json.Marshal(map[string]string{"code": code})
		require.NoError(t, err)
		body = bytes.NewReader(encoded)
	}
	request := httptest.NewRequestWithContext(t.Context(), method, path, body)
	request.AddCookie(binding)
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	return response
}

func secondFactorResponseCookie(t *testing.T, response *httptest.ResponseRecorder, name string) *http.Cookie {
	t.Helper()
	for _, cookie := range response.Result().Cookies() {
		if cookie.Name == name {
			return cookie
		}
	}
	t.Fatalf("response %d did not set %s", response.Code, name)
	return nil
}
