// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package identity

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/sudosylabs/proctor/server/app/apperror"
	"github.com/sudosylabs/proctor/server/app/invocation"
	"github.com/sudosylabs/proctor/server/app/realtime/realtimetest"
	"github.com/sudosylabs/proctor/server/model"
	"github.com/sudosylabs/proctor/server/store"
)

func TestAuthenticationValidatesLongLivedSessionPrincipal(t *testing.T) {
	t.Parallel()

	at := time.Date(2026, time.August, 12, 12, 0, 0, 0, time.UTC)
	user := &model.User{ID: model.NewUserID()}
	session := &model.Session{
		ID: model.NewSessionID(), UserID: user.ID,
		ExpiresAt: at.Add(time.Hour), IdleExpiresAt: at.Add(30 * time.Minute),
	}
	persistence := newAuthenticationStoreFake()
	persistence.users[user.ID.String()] = user
	persistence.sessions[session.ID.String()] = session
	service := newTestAuthenticationServiceWithPorts(
		t, persistence, newAuthenticationCacheFake(),
		&mfaApplicationStoreFake{}, discardPersonalAccessTokenStore{},
		model.NewCredentialToken, func() time.Time { return at },
	)
	principal := model.Principal{
		UserID: user.ID, SessionID: session.ID,
		CredentialID:         model.PrincipalCredentialID(model.NewID()),
		CredentialType:       model.CredentialSessionAccess,
		AuthenticationMethod: "password", AuthenticationStrength: model.AuthenticationSingleFactor,
		AuthenticatedAt: at.Add(-time.Minute), ClientType: model.SessionClientWeb,
	}
	require.NoError(t, service.validatePrincipal(context.Background(), principal))
	persistence.userGetErr = errors.New("database unavailable")
	err := service.validatePrincipal(context.Background(), principal)
	require.True(t, apperror.Is(err, "authentication.internal"), "dependency error = %v", err)
	persistence.userGetErr = nil
	session.UserID = model.NewUserID()
	err = service.validatePrincipal(context.Background(), principal)
	require.True(t, apperror.Is(err, "authentication.token_invalid"), "mismatched session error = %v", err)
}

func TestAuthenticationExpiryEnforcementClosesEstablishedWebSocketSession(t *testing.T) {
	t.Parallel()

	at := time.Date(2026, time.August, 12, 12, 0, 0, 0, time.UTC)
	user := &model.User{ID: model.NewUserID()}
	session := &model.Session{
		ID: model.NewSessionID(), UserID: user.ID,
		ExpiresAt: at.Add(time.Hour), IdleExpiresAt: at,
	}
	persistence := newAuthenticationStoreFake()
	persistence.users[user.ID.String()] = user
	persistence.sessions[session.ID.String()] = session
	effects := &realtimetest.Publisher{}
	service := newTestAuthenticationServiceWithEffects(
		t, persistence, newAuthenticationCacheFake(), effects,
		&mfaApplicationStoreFake{}, discardPersonalAccessTokenStore{},
		model.NewCredentialToken, func() time.Time { return at },
	)
	principal := model.Principal{
		UserID: user.ID, SessionID: session.ID,
		CredentialID:         model.PrincipalCredentialID(model.NewSessionCredentialID()),
		CredentialType:       model.CredentialSessionAccess,
		AuthenticationMethod: "password", AuthenticationStrength: model.AuthenticationSingleFactor,
		AuthenticatedAt: at.Add(-time.Minute), ClientType: model.SessionClientWeb,
	}
	err := service.validatePrincipal(context.Background(), principal)
	require.True(t, apperror.Is(err, "authentication.token_invalid"), "ValidatePrincipal(expired) error = %v", err)
	// Contract: the expiry publishes one Session revocation and nothing else.
	require.Equal(t, []realtimetest.Method{realtimetest.SessionsRevoked}, publishedMethods(effects), "expiry session=%#v", session)
	require.True(t, session.RevokedAt.Valid, "expiry effects=%q session=%#v", publishedMethods(effects), session)
	require.Equal(t, model.SessionRevocationExpired, session.RevocationReason, "expiry effects=%q session=%#v", publishedMethods(effects), session)
}

func TestAuthenticationExpiryEnforcementRejectsHTTPAccessAndRefresh(t *testing.T) {
	t.Parallel()

	at := time.Date(2026, time.August, 12, 12, 0, 0, 0, time.UTC)
	user := &model.User{ID: model.NewUserID()}
	accessRaw := model.NewCredentialToken()
	accessHash := model.HashToken(accessRaw)
	accessSession := &model.Session{
		ID: model.NewSessionID(), UserID: user.ID, ClientType: model.SessionClientWeb,
		ExpiresAt: at.Add(time.Hour), IdleExpiresAt: at,
	}
	persistence := newAuthenticationStoreFake()
	persistence.users[user.ID.String()] = user
	persistence.sessions[accessSession.ID.String()] = accessSession
	persistence.accessByHash[accessHash] = &model.SessionCredential{
		ID: model.NewSessionCredentialID(), SessionID: accessSession.ID,
		Kind: model.SessionCredentialAccess, TokenHash: accessHash, ExpiresAt: at.Add(time.Hour),
	}
	persistence.sessionByCredential[accessHash] = accessSession
	effects := &realtimetest.Publisher{}
	service := newTestAuthenticationServiceWithEffects(
		t, persistence, newAuthenticationCacheFake(), effects,
		&mfaApplicationStoreFake{}, discardPersonalAccessTokenStore{},
		model.NewCredentialToken, func() time.Time { return at },
	)
	principal, err := service.authenticateAccess(context.Background(), accessRaw)
	require.Nil(t, principal, "authenticateAccess(expired) = %#v, %v", principal, err)
	require.True(t, apperror.Is(err, "authentication.token_invalid"), "authenticateAccess(expired) = %#v, %v", principal, err)
	// Contract: the access expiry publishes one Session revocation and nothing else.
	require.Equal(t, []realtimetest.Method{realtimetest.SessionsRevoked}, publishedMethods(effects), "access expiry session=%#v", accessSession)
	require.Equal(t, model.SessionRevocationExpired, accessSession.RevocationReason, "access expiry effects=%q session=%#v", publishedMethods(effects), accessSession)

	refreshSession := &model.Session{
		ID: model.NewSessionID(), UserID: user.ID, ClientType: model.SessionClientWeb,
		ExpiresAt: at.Add(time.Hour), IdleExpiresAt: at,
	}
	persistence.rotation = &store.SessionRotation{
		Session: refreshSession, Expired: true,
		RevokedAccessHashes: []string{model.HashToken(model.NewCredentialToken())},
	}
	session, tokens, err := service.RefreshSession(context.Background(), invocation.Invocation{}, RefreshSessionCommand{
		RefreshToken: model.NewCredentialToken(),
	})
	require.Nil(t, session, "refresh(expired) = %#v, %#v, %v", session, tokens, err)
	require.Nil(t, tokens, "refresh(expired) = %#v, %#v, %v", session, tokens, err)
	require.True(t, apperror.Is(err, "authentication.token_invalid"), "refresh(expired) = %#v, %#v, %v", session, tokens, err)
	// Contract: the expired refresh publishes a second Session revocation after
	// the access expiry's, and nothing else.
	require.Equal(t, []realtimetest.Method{realtimetest.SessionsRevoked, realtimetest.SessionsRevoked}, publishedMethods(effects))
}

func TestAuthenticationAuthoritativeIdleExtensionOverridesStaleExpiredCache(t *testing.T) {
	t.Parallel()

	at := time.Date(2026, time.August, 12, 12, 0, 0, 0, time.UTC)
	user := &model.User{ID: model.NewUserID()}
	accessRaw := model.NewCredentialToken()
	accessHash := model.HashToken(accessRaw)
	authoritative := &model.Session{
		ID: model.NewSessionID(), UserID: user.ID, ClientType: model.SessionClientWeb,
		AuthenticationMethod: "password", AuthenticationStrength: model.AuthenticationSingleFactor,
		AuthenticatedAt: at.Add(-time.Hour), LastActivityAt: at.Add(-30 * time.Second),
		IdleExpiresAt: at.Add(time.Hour), ExpiresAt: at.Add(24 * time.Hour),
	}
	credential := &model.SessionCredential{
		ID: model.NewSessionCredentialID(), SessionID: authoritative.ID,
		Kind: model.SessionCredentialAccess, TokenHash: accessHash, ExpiresAt: at.Add(time.Hour),
	}
	persistence := newAuthenticationStoreFake()
	persistence.users[user.ID.String()] = user
	persistence.sessions[authoritative.ID.String()] = authoritative
	persistence.accessByHash[accessHash] = credential
	persistence.sessionByCredential[accessHash] = authoritative
	cache := newAuthenticationCacheFake()
	service := newTestAuthenticationServiceWithPorts(
		t, persistence, cache, &mfaApplicationStoreFake{}, discardPersonalAccessTokenStore{},
		model.NewCredentialToken, func() time.Time { return at },
	)
	stale := *authoritative
	stale.IdleExpiresAt = at
	cacheLegacyAuthentication(t, cache, credential, &stale, user)

	principal, err := service.authenticateAccess(context.Background(), accessRaw)
	require.NoError(t, err, "authenticateAccess(stale cached expiry)")
	require.NotNil(t, principal, "authenticateAccess(stale cached expiry)")
	require.Equal(t, authoritative.ID, principal.SessionID, "authenticateAccess(stale cached expiry)")
	require.False(t, authoritative.RevokedAt.Valid)
}

func TestAuthenticationConsumesMFAAndPATThroughBehavioralPorts(t *testing.T) {
	t.Parallel()

	at := time.Date(2026, 8, 12, 11, 0, 0, 0, time.UTC)
	persistence := newAuthenticationStoreFake()
	mfa := &mfaApplicationStoreFake{}
	patPrincipal := &model.Principal{
		UserID:           model.NewUserID(),
		CredentialID:     model.PrincipalCredentialID(model.NewPersonalAccessTokenID()),
		CredentialScopes: []string{string(model.ActionUserView)},
	}
	patToken := personalAccessTokenForTest(patPrincipal.UserID, at.Add(-time.Minute))
	patToken.ID = model.PersonalAccessTokenID(patPrincipal.CredentialID.String())
	patToken.Scopes = patPrincipal.CredentialScopes
	pat := &personalAccessTokenResolutionStoreFake{
		resolution: &store.PersonalAccessTokenResolution{Token: patToken, User: &model.User{ID: patPrincipal.UserID}},
	}
	service := newTestAuthenticationServiceWithPorts(
		t,
		persistence,
		newAuthenticationCacheFake(),
		mfa,
		pat,
		model.NewCredentialToken,
		func() time.Time { return at },
	)
	user, err := service.CreateLocalUser(context.Background(), CreateLocalUserCommand{
		User:     &model.User{Username: "behavior-port-user", Email: "behavior-port@example.edu"},
		Password: "CorrectHorseBatteryStaple1!",
	})
	require.NoError(t, err)
	// The User has an active authenticator; sign-in verifies and consumes
	// the current TOTP step through the MFA Store.
	const secret = "JBSWY3DPEHPK3PXP" // #nosec G101 -- Public synthetic TOTP seed.
	sealed, err := service.mfa.sealTOTPSecret(user.ID.String(), secret)
	require.NoError(t, err)
	mfa.credential = &model.MFACredential{ID: model.NewMFACredentialID(), UserID: user.ID, State: model.MFAStateActive,
		EncryptedSecret: sealed.encoded, EncryptionKeyID: sealed.keyID}
	code, err := computeTOTP(secret, at.Unix()/30)
	require.NoError(t, err)
	login, err := service.Login(context.Background(), invocation.Invocation{}, LoginCommand{
		LoginID:    user.Username,
		Password:   "CorrectHorseBatteryStaple1!",
		ClientType: model.SessionClientCLI,
		MFACode:    code,
		Source:     "127.0.0.1:1",
	})
	require.NoError(t, err)
	require.Len(t, mfa.consumptions, 1, "MFA consumptions=%d session=%#v", len(mfa.consumptions), login.Session)
	require.Equal(t, user.ID, mfa.consumptions[0].UserID, "MFA consumed another User's factor")
	require.Equal(t, at.Unix()/30, mfa.consumptions[0].TimeStep, "MFA consumed another TOTP step")
	require.Equal(t, model.AuthenticationMultiFactor, login.Session.AuthenticationStrength, "MFA consumptions=%d session=%#v", len(mfa.consumptions), login.Session)

	rawToken := model.NewCredentialToken()
	resolved, err := service.AuthenticateBearer(context.Background(), rawToken)
	require.NoError(t, err)
	require.Equal(t, model.HashToken(rawToken), pat.hash, "PAT resolution received the raw token")
	require.True(t, pat.at.Equal(at), "PAT resolution time = %s", pat.at)
	require.Equal(t, 1, pat.calls, "PAT calls=%d principal=%#v", pat.calls, resolved)
	require.Equal(t, patPrincipal.UserID, resolved.UserID, "PAT calls=%d principal=%#v", pat.calls, resolved)
}

func TestAuthenticateAccessRejectsUnknownToken(t *testing.T) {
	t.Parallel()

	service := newTestAuthenticationService(t, newAuthenticationStoreFake())
	raw := model.NewCredentialToken()
	_, err := service.authenticateAccess(context.Background(), raw)
	failure, ok := apperror.As(err)
	require.True(t, ok)
	require.Equal(t, "authentication.token_invalid", failure.Code())
}
