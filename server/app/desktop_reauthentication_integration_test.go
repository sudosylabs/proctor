//go:build integration

// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package app_test

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/sudosylabs/proctor/server/app/apperror"
	"github.com/sudosylabs/proctor/server/app/identity"
	"github.com/sudosylabs/proctor/server/app/invocation"
	"github.com/sudosylabs/proctor/server/config"
	"github.com/sudosylabs/proctor/server/model"
	"github.com/sudosylabs/proctor/server/store"
	"github.com/sudosylabs/proctor/server/testlib"
)

// TestDesktopReauthenticationRestoresRecencyWithoutMFA signs a password-only
// account in to Desktop, lets the Session go stale, and proves that the
// browser reauthentication journey refreshes recency on that same Session.
func TestDesktopReauthenticationRestoresRecencyWithoutMFA(t *testing.T) {
	dataSource := os.Getenv("PROCTOR_TEST_DATABASE_URL")
	require.NotEmpty(t, dataSource, "PROCTOR_TEST_DATABASE_URL is not set")
	persistence := openAuthenticationStore(t, dataSource)
	seedInitialAuthenticationAccessPolicy(t, persistence)
	origin := "https://proctor.example.edu"
	build := model.DesktopBuildTuple{
		DesktopRelease: "0.1.0", DesktopBuildID: "integration-build",
		Platform: model.DesktopPlatformDarwin, Architecture: model.DesktopArchitectureARM64,
		RealtimeProtocol:                        1,
		AttemptConfigurationManifestFingerprint: model.CurrentAttemptConfigurationManifestFingerprint(),
		DesktopSettingsRegistryFingerprint:      "fnv1a64:" + strings.Repeat("b", 16),
		DesktopTarget:                           "darwin-arm64", ConfigurationManifest: model.EmptyAttemptConfigurationManifest(),
		CapabilityMatrixIdentity: "integration-matrix",
	}
	node := testlib.Setup(t, testlib.WithConfig(func(cfg *config.Config) { cfg.Server.PublicURL = origin }),
		testlib.WithStore(persistence), testlib.WithDesktopBuildCatalog(build))
	ctx := context.Background()
	_, err := persistence.Institution().Save(ctx, &model.Institution{Name: "desktop-reauth", DisplayName: "Desktop Reauthentication"})
	require.NoError(t, err)
	const password = "correct horse battery staple"
	user, err := node.App.Identity.CreateLocalUser(ctx, identity.CreateLocalUserCommand{User: &model.User{
		Username: "desktop-reauth", Email: "desktop-reauth@example.edu", DisplayName: "Ada Candidate",
	}, Password: password})
	require.NoError(t, err)

	// Desktop sign-in through a Web Session, without any MFA enrollment.
	web, err := node.App.Identity.Login(ctx, invocation.Invocation{}, identity.LoginCommand{
		LoginID: user.Username, Password: password, ClientType: model.SessionClientWeb, DeviceID: "browser", Source: "127.0.0.1:1",
	})
	require.NoError(t, err)
	webPrincipal, err := node.App.Identity.AuthenticateAccess(ctx, web.Tokens.AccessToken)
	require.NoError(t, err)
	state, verifier := model.NewCredentialToken(), model.NewCredentialToken()
	privateKey, publicJWK := integrationDesktopDPoPKey(t)
	started, err := node.App.Identity.StartDesktopAuthorization(ctx, invocation.Invocation{}, identity.StartDesktopAuthorizationCommand{
		CallbackURL: "http://127.0.0.1:49152/" + model.NewCredentialToken(), State: state, CodeChallenge: model.PKCES256Challenge(verifier),
		DeviceName: "Exam laptop", PublicJWK: publicJWK, DesktopRelease: build.DesktopRelease, DesktopBuildID: build.DesktopBuildID,
		Platform: build.Platform, Architecture: build.Architecture, RealtimeProtocol: build.RealtimeProtocol, Source: "127.0.0.1:2",
	})
	require.NoError(t, err)
	authorizationURL, err := url.Parse(started.AuthorizationURL)
	require.NoError(t, err)
	binding, err := node.App.Identity.BindDesktopAuthorization(ctx, invocation.Invocation{}, identity.BindDesktopAuthorizationCommand{
		Handle: authorizationURL.Query().Get("request"), BrowserProof: strings.TrimPrefix(authorizationURL.Fragment, "proof="), State: state,
	})
	require.NoError(t, err)
	_, err = node.App.Identity.AuthenticateDesktopAuthorizationSession(ctx, invocation.New(*webPrincipal, model.RequestMetadata{}),
		identity.AuthenticateDesktopAuthorizationCommand{Binding: binding.Binding})
	require.NoError(t, err)
	approved, err := node.App.Identity.ApproveDesktopAuthorization(ctx, invocation.Invocation{}, identity.ApproveDesktopAuthorizationCommand{Binding: binding.Binding, State: state})
	require.NoError(t, err)
	callback, err := url.Parse(approved.RedirectURL)
	require.NoError(t, err)
	exchanged, err := node.App.Identity.ExchangeDesktopAuthorization(ctx, invocation.Invocation{}, identity.ExchangeDesktopAuthorizationCommand{
		Code: callback.Query().Get("code"), State: state, CodeVerifier: verifier,
		DPoPProof: integrationDesktopDPoPProof(t, privateKey, publicJWK, map[string]any{
			"jti": model.NewCredentialToken(), "htm": "POST", "htu": origin + "/api/v1/auth/desktop/token",
			"iat": time.Now().Unix(), "nonce": started.DPoPNonce,
		}),
		PublicJWK: publicJWK, DesktopRelease: build.DesktopRelease, DesktopBuildID: build.DesktopBuildID,
		Platform: build.Platform, Architecture: build.Architecture, RealtimeProtocol: build.RealtimeProtocol, Source: "127.0.0.1:2",
	})
	require.NoError(t, err)
	nonce := exchanged.DPoPNonce
	desktopPrincipal := func(path string) model.Principal {
		t.Helper()
		accessHash := sha256.Sum256([]byte(exchanged.Tokens.AccessToken))
		for attempt := 0; attempt < 2; attempt++ {
			proof := integrationDesktopDPoPProof(t, privateKey, publicJWK, map[string]any{
				"jti": model.NewCredentialToken(), "htm": "POST", "htu": origin + path, "iat": time.Now().Unix(),
				"nonce": nonce, "ath": base64.RawURLEncoding.EncodeToString(accessHash[:]),
			})
			principal, authErr := node.App.Identity.AuthenticateDPoP(ctx, exchanged.Tokens.AccessToken, proof, "POST", path)
			if replacement, challenged := identity.DPoPChallengeNonce(authErr); challenged {
				nonce = replacement
				continue
			}
			require.NoError(t, authErr, "Desktop DPoP authentication")
			return *principal
		}
		require.Fail(t, "Desktop DPoP authentication kept challenging the nonce")
		return model.Principal{}
	}

	// The Desktop Session signed in long ago: token creation needs fresh proof.
	stale := desktopPrincipal("/api/v1/users/me/tokens")
	stale.AuthenticatedAt = stale.AuthenticatedAt.Add(-time.Hour)
	stale.MFACompletedAt = model.OptionalTime{}
	createToken := func(principal model.Principal) error {
		_, createErr := node.App.Identity.CreatePersonalAccessToken(ctx, invocation.New(principal, model.RequestMetadata{}), identity.CreatePersonalAccessTokenCommand{
			Description: "Desktop automation", Scopes: []string{string(model.ActionExamCreate)}, ExpiresAt: time.Now().Add(24 * time.Hour).UnixMilli(),
		})
		return createErr
	}
	require.True(t, apperror.Is(createToken(stale), "authentication.reauthentication_required"), "a stale Desktop Session created a token")

	// A browser Web Session can never start Desktop reauthentication.
	reauthState, reauthVerifier := model.NewCredentialToken(), model.NewCredentialToken()
	startCommand := identity.StartDesktopReauthenticationCommand{CallbackURL: "http://127.0.0.1:49153/" + model.NewCredentialToken(),
		State: reauthState, CodeChallenge: model.PKCES256Challenge(reauthVerifier), Source: "127.0.0.1:3"}
	_, err = node.App.Identity.StartDesktopReauthentication(ctx, invocation.New(*webPrincipal, model.RequestMetadata{}), startCommand)
	require.True(t, apperror.Is(err, "authentication.desktop_reauthentication.invalid"), "Web Session started Desktop reauthentication: %v", err)

	reauthentication, err := node.App.Identity.StartDesktopReauthentication(ctx,
		invocation.New(desktopPrincipal("/api/v1/auth/desktop/reauthentications"), model.RequestMetadata{}), startCommand)
	require.NoError(t, err)
	page, err := url.Parse(reauthentication.AuthorizationURL)
	require.NoError(t, err)
	require.Equal(t, "/reauthenticate/desktop", page.Path)
	reauthBinding, err := node.App.Identity.BindDesktopReauthentication(ctx, invocation.Invocation{}, identity.BindDesktopAuthorizationCommand{
		Handle: page.Query().Get("request"), BrowserProof: strings.TrimPrefix(page.Fragment, "proof="), State: reauthState,
	})
	require.NoError(t, err)
	hosted, err := node.App.Identity.DesktopReauthenticationContext(ctx, invocation.Invocation{}, reauthBinding.Binding)
	require.NoError(t, err)
	require.Equal(t, "Ada Candidate", hosted.DisplayName)
	require.Equal(t, "password", hosted.AuthenticationMethod)
	require.Nil(t, hosted.Provider)
	_, err = node.App.Identity.AuthenticateDesktopReauthenticationPassword(ctx, invocation.Invocation{}, identity.AuthenticateDesktopReauthenticationPasswordCommand{
		Binding: reauthBinding.Binding, Password: password, Source: "127.0.0.1:4",
	})
	require.NoError(t, err, "password proof without MFA")
	reauthApproved, err := node.App.Identity.ApproveDesktopReauthentication(ctx, invocation.Invocation{},
		identity.ApproveDesktopAuthorizationCommand{Binding: reauthBinding.Binding, State: reauthState})
	require.NoError(t, err)
	loopback, err := url.Parse(reauthApproved.RedirectURL)
	require.NoError(t, err)
	require.Len(t, loopback.Query(), 2, "the loopback redirect carries only code and state")
	completed, err := node.App.Identity.CompleteDesktopReauthentication(ctx,
		invocation.New(desktopPrincipal("/api/v1/auth/desktop/reauthentications/complete"), model.RequestMetadata{}),
		identity.CompleteDesktopReauthenticationCommand{Code: loopback.Query().Get("code"), CodeVerifier: reauthVerifier, State: reauthState, Source: "127.0.0.1:3"})
	require.NoError(t, err)
	require.True(t, completed.RecentlyAuthenticatedUntil.After(time.Now()), "completion did not restore recency")

	// The same Session, still signed in long ago, now carries fresh primary proof.
	fresh := desktopPrincipal("/api/v1/users/me/tokens")
	require.Equal(t, exchanged.Session.ID, fresh.SessionID, "reauthentication replaced the Desktop Session")
	require.True(t, fresh.ReauthenticatedAt.Valid, "the Desktop Session carries no fresh primary proof")
	fresh.AuthenticatedAt = fresh.AuthenticatedAt.Add(-time.Hour)
	fresh.MFACompletedAt = model.OptionalTime{}
	// The account holds no delegable role, so creation now stops only at scope
	// delegation, after the recency requirement has been met.
	require.True(t, apperror.Is(createToken(fresh), "personal_access_token.invalid"), "recency gate after reauthentication: %v", createToken(fresh))
	events, err := persistence.Audit().List(ctx, store.AuditListOptions{Action: string(model.ActionAuthenticationReauthenticate),
		ActorID: user.ID, Limit: 10, Visibility: store.AuditVisibilityScope{InstitutionWide: true}})
	require.NoError(t, err)
	require.Len(t, events, 1, "Desktop reauthentication audits")
	require.Equal(t, model.AuditStatusSuccess, events[0].Status, "Desktop reauthentication audits")
}
