// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package app

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/sudosylabs/proctor/server/app/apperror"
	"github.com/sudosylabs/proctor/server/app/invocation"
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

func TestSessionLengthLimitsFollowTheDeploymentLifetimes(t *testing.T) {
	t.Parallel()
	limits := sessionLengthLimits(SessionPolicy{AccessTTL: 15 * time.Minute, ActivityUpdateInterval: 5 * time.Minute,
		IdleTTL: 7 * 24 * time.Hour, AbsoluteTTL: 30 * 24 * time.Hour}, 30*time.Minute)
	require.Equal(t, model.SessionLengthLimits{
		IdleMinimumMinutes: 15, IdleDefaultMinutes: 10080, IdleMaximumMinutes: 10080,
		AbsoluteMinimumMinutes: 30, AbsoluteDefaultMinutes: 43200, AbsoluteMaximumMinutes: 43200,
	}, limits)
	slowActivity := sessionLengthLimits(SessionPolicy{AccessTTL: 5 * time.Minute, ActivityUpdateInterval: 20 * time.Minute,
		IdleTTL: time.Hour, AbsoluteTTL: 2 * time.Hour}, 15*time.Minute)
	require.Equal(t, 21, slowActivity.IdleMinimumMinutes, "the idle length outlasts the activity debounce")
	require.Equal(t, 21, slowActivity.AbsoluteMinimumMinutes, "the absolute length outlasts the idle minimum")
}

func TestAccessPolicyPreflightNamesTheRefusedSessionLength(t *testing.T) {
	t.Parallel()
	service, persistence, _, _, _, invocation := accessPolicyFixture(t)
	service.sessionLimits = model.SessionLengthLimits{IdleMinimumMinutes: 16, IdleDefaultMinutes: 120, IdleMaximumMinutes: 120,
		AbsoluteMinimumMinutes: 16, AbsoluteDefaultMinutes: 1440, AbsoluteMaximumMinutes: 1440}
	for _, test := range []struct {
		name           string
		idle, absolute int
		field          string
	}{
		{name: "idle below the minimum", idle: 15, field: "session_idle_minutes"},
		{name: "idle above the deployment ceiling", idle: 121, field: "session_idle_minutes"},
		{name: "idle longer than the absolute length", idle: 100, absolute: 60, field: "session_idle_minutes"},
		{name: "absolute above the deployment ceiling", absolute: 1441, field: "session_absolute_minutes"},
	} {
		t.Run(test.name, func(t *testing.T) {
			settings := persistence.snapshot.Policy.Settings()
			settings.SessionIdleMinutes, settings.SessionAbsoluteMinutes = test.idle, test.absolute
			_, err := service.PreflightAccessPolicy(context.Background(), invocation, PreflightAccessPolicyCommand{
				ExpectedRevision: persistence.snapshot.Policy.Revision, Settings: settings})
			failure, ok := apperror.As(err)
			require.True(t, ok, "err=%v", err)
			require.Equal(t, "access_policy.invalid", failure.Code())
			require.Equal(t, test.field, failure.Fields()["field"])
		})
	}
	settings := persistence.snapshot.Policy.Settings()
	settings.SessionIdleMinutes, settings.SessionAbsoluteMinutes = 60, 600
	_, err := service.PreflightAccessPolicy(context.Background(), invocation, PreflightAccessPolicyCommand{
		ExpectedRevision: persistence.snapshot.Policy.Revision, Settings: settings})
	require.NoError(t, err)
	require.Equal(t, service.sessionLimits, persistence.preflightInput.SessionLimits, "the store rechecks the same limits")
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
	service.deps.AccessPolicy = policy
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
	refresher := newTestAuthenticationServiceWithEffects(t, refreshing, newAuthenticationCacheFake(), &authenticationEffectsRecorder{},
		discardAuthenticationMFAVerifier{}, discardAuthenticationPATResolver{}, model.NewCredentialToken, func() time.Time { return at })
	refresher.deps.AccessPolicy = policy
	_, _, err = refresher.RefreshSession(context.Background(), invocation.Invocation{}, RefreshSessionCommand{RefreshToken: model.NewCredentialToken()})
	require.NoError(t, err)
	require.Equal(t, at.Add(30*time.Minute), refreshing.rotationInput.IdleExpiresAt)
}

func TestDesktopExchangeUsesThePolicySessionLengths(t *testing.T) {
	t.Parallel()
	at := time.Date(2026, 11, 9, 9, 0, 0, 0, time.UTC)
	persistence := &desktopAuthorizationStoreFake{exchangeErr: store.ErrAuthenticationGenerationChanged}
	shortened := sessionLifetimes{Idle: 20 * time.Minute, Absolute: 40 * time.Minute}
	service, err := newDesktopAuthorizationService(desktopAuthorizationServiceDependencies{Transactions: persistence, Institutions: desktopAuthorizationInstitutionStoreFake{institution: &model.Institution{ID: model.NewInstitutionID()}}, AccessPolicy: desktopAuthorizationAccessPolicyFake{enabled: true, lifetimes: &shortened}, Capabilities: &accessPolicyCapabilitiesFake{}, Audit: desktopAuthorizationAuditorFake{}, Attempts: &desktopAuthorizationAttemptLimiterFake{}, SessionPolicy: testDesktopSessionPolicy(), Policy: DesktopAuthorizationPolicy{Issuer: "https://proctor.example.edu"}, NewCredential: model.NewCredentialToken, Now: func() time.Time { return at }, Identity: testDesktopAuthorizationIdentity(t, "https://proctor.example.edu", func() time.Time { return at })})
	require.NoError(t, err)
	command := testDesktopAuthorizationExchangeCommand(t, service, at, model.NewCredentialToken(), model.NewCredentialToken(), model.NewCredentialToken(), "")
	_, _ = service.ExchangeDesktopAuthorization(context.Background(), invocation.Invocation{}, command)
	require.NotNil(t, persistence.exchanged)
	require.Equal(t, 20*time.Minute, persistence.exchanged.IdleLifetime)
	require.Equal(t, 40*time.Minute, persistence.exchanged.AbsoluteLifetime)
	require.LessOrEqual(t, persistence.exchanged.AccessLifetime, persistence.exchanged.IdleLifetime)
	require.LessOrEqual(t, persistence.exchanged.RefreshLifetime, persistence.exchanged.AbsoluteLifetime)
}
