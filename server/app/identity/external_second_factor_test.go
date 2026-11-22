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
	"github.com/sudosylabs/proctor/server/app/apperror"
	"github.com/sudosylabs/proctor/server/app/invocation"
	"github.com/sudosylabs/proctor/server/model"
	"github.com/sudosylabs/proctor/server/store"
)

type secondFactorStatesFake struct {
	*externalLoginStateStoreFake
	created *model.ExternalSecondFactorState
}

func (f *secondFactorStatesFake) CreateExternalSecondFactorState(_ context.Context, input *store.ExternalSecondFactorStateCreation) (*model.ExternalSecondFactorState, error) {
	state := *input.State
	state.ExpiresAt = state.PrimaryAuthenticatedAt.Add(input.Lifetime)
	f.created = &state
	return &state, nil
}

func (f *secondFactorStatesFake) GetExternalSecondFactorStateByBinding(_ context.Context, binding string, _ time.Time) (*model.ExternalSecondFactorState, error) {
	if f.created == nil || f.created.BindingHash != binding {
		return nil, store.NewErrNotFound("external_second_factor_state", "")
	}
	return f.created, nil
}

type secondFactorTestProvider struct{ providerConnectionProvider }

func (secondFactorTestProvider) Begin(context.Context, ExternalProviderBeginRequest) (*ExternalProviderBeginResponse, error) {
	return &ExternalProviderBeginResponse{RedirectURL: "https://campus.example.test/login"}, nil
}

func TestExternalCallbackMakesProctorFactorMandatoryOnlyWhenNeeded(t *testing.T) {
	for _, test := range []struct {
		name         string
		strength     model.AuthenticationStrength
		enrolled     bool
		enabled      bool
		reenrollment bool
		wantPause    bool
		wantError    string
	}{
		{name: "enrolled", strength: model.AuthenticationSingleFactor, enrolled: true, enabled: true, wantPause: true},
		{name: "provider MFA", strength: model.AuthenticationMultiFactor, enrolled: true, enabled: true},
		{name: "no enrollment", strength: model.AuthenticationSingleFactor, enabled: true},
		{name: "disabled", strength: model.AuthenticationSingleFactor, enrolled: true, wantError: "authentication.mfa.unavailable"},
		{name: "re-enrollment takes precedence", strength: model.AuthenticationSingleFactor, enrolled: true, enabled: true, reenrollment: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			at := time.Now().UTC()
			binding, providerState := model.NewCredentialToken(), model.NewCredentialToken()
			user := &model.User{ID: model.NewUserID(), Username: "sasha", Email: "sasha@example.edu"}
			identity := &model.ExternalIdentity{ID: model.NewExternalIdentityID(), UserID: user.ID, Provider: "campus", Subject: "opaque"}
			callback := &model.ExternalLoginState{ID: model.NewExternalLoginStateID(),
				Provider: "campus", Purpose: model.ExternalAuthenticationPurposeLogin,
				ReturnTo: "/exams", ClientType: model.SessionClientWeb,
				StateHash: model.HashToken(providerState), BindingHash: model.HashToken(binding), ExpiresAt: at.Add(time.Minute),
			}
			states := &secondFactorStatesFake{externalLoginStateStoreFake: &externalLoginStateStoreFake{
				get: callback, consumeResult: callback, storeNow: at,
			}}
			service := newDesktopExternalAuthenticationFixture(t, &desktopAuthorizationStoreFake{})
			service.externalProviders = externalProviderSourceFake{provider: secondFactorTestProvider{providerConnectionProvider{
				state: providerState, assertion: &model.ExternalAuthenticationAssertion{
					ProviderID: identity.Provider, Subject: identity.Subject, AuthenticationStrength: test.strength,
					AuthenticatedAt: at.UnixMilli(),
				},
			}}}
			service.externalLoginStates = states
			service.externalIdentities = &externalHandoffIdentityStore{user: user, identity: identity}
			recovery := &externalRecoveryStateStoreFake{state: &model.UserMFARecovery{
				UserID: user.ID, ReenrollmentRequired: test.reenrollment,
			}}
			if test.enrolled {
				recovery.credential = &model.MFACredential{ID: model.NewMFACredentialID(), State: model.MFAStateActive}
			}
			service.mfaCredentials = recovery
			service.mfa = &mfaMechanics{deps: mfaMechanicsDependencies{Settings: MFAPolicy{Enabled: test.enabled}}}
			sessions := &externalSessionStoreFake{}
			service.sessions = sessions
			result, err := service.CompleteExternalAuthentication(t.Context(), invocation.Invocation{}, CompleteExternalAuthenticationCommand{
				ProviderID: identity.Provider, Binding: binding,
			})
			if test.wantError != "" {
				require.True(t, apperror.Is(err, test.wantError), "%v", err)
				require.Nil(t, states.created)
				require.Empty(t, sessions.saves)
				return
			}
			require.NoError(t, err)
			if test.wantPause {
				require.NotNil(t, result.SecondFactor)
				require.Equal(t, "/login/verify", result.ReturnTo)
				require.NotEqual(t, binding, result.SecondFactor.Binding)
				require.Equal(t, callback.ID, states.created.ExternalLoginStateID)
				require.Empty(t, sessions.saves)

				service.loginRateLimit.MaximumAttempts = 1
				command := CompleteExternalSecondFactorCommand{Binding: result.SecondFactor.Binding, Code: "invalid", Source: "192.0.2.10"}
				_, err = service.CompleteExternalSecondFactor(t.Context(), invocation.Invocation{}, command)
				require.True(t, apperror.Is(err, "authentication.mfa.code_invalid"), "%v", err)
				_, err = service.CompleteExternalSecondFactor(t.Context(), invocation.Invocation{}, command)
				require.True(t, apperror.Is(err, "authentication.rate_limited"), "%v", err)
				return
			}
			require.Nil(t, result.SecondFactor)
			if test.reenrollment {
				require.NotNil(t, result.Restart)
				require.Empty(t, sessions.saves)
				return
			}
			require.Equal(t, test.strength, result.Session.AuthenticationStrength)
		})
	}
}
