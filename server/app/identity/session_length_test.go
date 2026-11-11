// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package identity

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/sudosylabs/proctor/server/app/invocation"
	"github.com/sudosylabs/proctor/server/app/realtime/realtimetest"
	"github.com/sudosylabs/proctor/server/model"
	"github.com/sudosylabs/proctor/server/store"
)

func TestEffectiveSessionLifetimesOnlyShortenTheDeploymentDefaults(t *testing.T) {
	t.Parallel()
	defaults := SessionPolicy{IdleTTL: 2 * time.Hour, AbsoluteTTL: 7 * 24 * time.Hour}
	for _, test := range []struct {
		name               string
		idle, absolute     int
		wantIdle, wantFull time.Duration
	}{
		{name: "unset keeps the defaults", wantIdle: 2 * time.Hour, wantFull: 7 * 24 * time.Hour},
		{name: "set lengths shorten", idle: 30, absolute: 240, wantIdle: 30 * time.Minute, wantFull: 4 * time.Hour},
		{name: "a longer length is capped by the default", idle: 600, absolute: 20000, wantIdle: 2 * time.Hour, wantFull: 7 * 24 * time.Hour},
		{name: "the idle default never outlasts a shorter absolute length", absolute: 60, wantIdle: time.Hour, wantFull: time.Hour},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			got := effectiveSessionLifetimes(model.AccessPolicySettings{SessionIdleMinutes: test.idle, SessionAbsoluteMinutes: test.absolute}, defaults)
			require.Equal(t, sessionLifetimes{Idle: test.wantIdle, Absolute: test.wantFull}, got)
		})
	}
}

func TestSessionCreationAndRefreshUseThePolicySessionLengths(t *testing.T) {
	t.Parallel()
	persistence := newAuthenticationStoreFake()
	service := newTestAuthenticationService(t, persistence)
	user, err := service.CreateLocalUser(context.Background(), CreateLocalUserCommand{
		User: &model.User{Username: "short-user", Email: "short-user@example.edu"}, Password: roleHolderMFATestPassword,
	})
	require.NoError(t, err)
	shortened := sessionLifetimes{Idle: 30 * time.Minute, Absolute: 90 * time.Minute}
	policy := allowAllAuthenticationAccessPolicy().(authenticationAccessPolicyFake)
	policy.lifetimes = &shortened
	service.accessPolicy = policy
	result, err := service.Login(context.Background(), invocation.Invocation{}, LoginCommand{
		LoginID: user.Email, Password: roleHolderMFATestPassword, ClientType: model.SessionClientWeb, Source: "192.0.2.20",
	})
	require.NoError(t, err)
	require.Equal(t, result.Session.CreatedAt.Add(90*time.Minute), result.Session.ExpiresAt)
	require.Equal(t, result.Session.CreatedAt.Add(30*time.Minute), result.Session.IdleExpiresAt)
	require.False(t, result.Tokens.RefreshExpiresAt.After(result.Session.ExpiresAt), "refresh credential is capped at the absolute expiry")

	at := time.Date(2026, 11, 9, 9, 0, 0, 0, time.UTC)
	refreshing := newAuthenticationStoreFake()
	refreshing.users[user.ID.String()] = user
	refreshing.rotation = &store.SessionRotation{Session: &model.Session{ID: model.NewSessionID(), UserID: user.ID, ClientType: model.SessionClientWeb,
		AuthenticationStrength: model.AuthenticationMultiFactor}}
	refresher := newTestAuthenticationServiceWithEffects(t, refreshing, newAuthenticationCacheFake(), &realtimetest.Publisher{},
		&mfaApplicationStoreFake{}, discardPersonalAccessTokenStore{}, model.NewCredentialToken, func() time.Time { return at })
	refresher.accessPolicy = policy
	_, _, err = refresher.RefreshSession(context.Background(), invocation.Invocation{}, RefreshSessionCommand{RefreshToken: model.NewCredentialToken()})
	require.NoError(t, err)
	require.Equal(t, at.Add(30*time.Minute), refreshing.rotationInput.IdleExpiresAt)
}

func TestDesktopExchangeUsesThePolicySessionLengths(t *testing.T) {
	t.Parallel()
	at := time.Date(2026, 11, 9, 9, 0, 0, 0, time.UTC)
	persistence := &desktopAuthorizationStoreFake{exchangeErr: store.ErrAuthenticationGenerationChanged}
	shortened := sessionLifetimes{Idle: 20 * time.Minute, Absolute: 40 * time.Minute}
	service := newTestDesktopAuthorizationService(t, persistence, func() time.Time { return at })
	service.accessPolicy = authenticationAccessPolicyFake{local: true, desktop: true, lifetimes: &shortened}
	command := testDesktopAuthorizationExchangeCommand(t, service, at, model.NewCredentialToken(), model.NewCredentialToken(), model.NewCredentialToken(), "")
	_, _ = service.ExchangeDesktopAuthorization(context.Background(), invocation.Invocation{}, command)
	require.NotNil(t, persistence.exchanged)
	require.Equal(t, 20*time.Minute, persistence.exchanged.IdleLifetime)
	require.Equal(t, 40*time.Minute, persistence.exchanged.AbsoluteLifetime)
	require.LessOrEqual(t, persistence.exchanged.AccessLifetime, persistence.exchanged.IdleLifetime)
	require.LessOrEqual(t, persistence.exchanged.RefreshLifetime, persistence.exchanged.AbsoluteLifetime)
}
