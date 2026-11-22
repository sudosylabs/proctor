// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package identity

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/sudosylabs/proctor/server/app/apperror"
	"github.com/sudosylabs/proctor/server/app/invocation"
	"github.com/sudosylabs/proctor/server/model"
	"github.com/sudosylabs/proctor/server/store"
)

func TestDesktopReauthenticationSecondFactorDecision(t *testing.T) {
	for _, tc := range []struct {
		name     string
		strength model.AuthenticationStrength
		external bool
		paused   bool
	}{
		{name: "password needs a second factor", strength: model.AuthenticationSingleFactor, paused: true},
		{name: "provider needs a second factor", strength: model.AuthenticationSingleFactor, external: true, paused: true},
		{name: "already multi-factor", strength: model.AuthenticationMultiFactor},
	} {
		t.Run(tc.name, func(t *testing.T) {
			harness := newDesktopReauthenticationTestHarness(t)
			service := harness.service
			binding := model.NewCredentialToken()
			current := &store.DesktopReauthenticationContext{
				ID: model.NewBrowserAuthenticationTransactionID(), UserID: harness.user.ID,
				State: model.BrowserAuthenticationStateBound, ExpectedAuthenticationMethod: "password",
				BindingHash: model.HashToken(binding), AuthenticationStrength: tc.strength,
				ExpiresAt: time.Now().Add(time.Minute),
			}
			harness.transactions.context = current
			pauses := &secondFactorStatesFake{externalLoginStateStoreFake: &externalLoginStateStoreFake{}}
			service.externalLoginStates = pauses
			service.externalAuthenticationPolicy.LoginStateTTL = 10 * time.Minute
			service.mfaCredentials = &externalRecoveryStateStoreFake{credential: &model.MFACredential{
				ID: model.NewMFACredentialID(), State: model.MFAStateActive,
			}}
			service.mfa = &mfaMechanics{deps: mfaMechanicsDependencies{Settings: MFAPolicy{Enabled: true}}}
			if tc.external {
				current.ExpectedAuthenticationMethod, current.ExpectedProviderID = "oidc", "campus"
				current.ExternalIdentityID = model.NewExternalIdentityID()
				err := service.authenticateExternalReauthentication(t.Context(), invocation.Invocation{}, current.ID,
					&model.ExternalIdentity{ID: current.ExternalIdentityID, UserID: current.UserID, Provider: "campus"},
					"oidc", &model.ExternalAuthenticationAssertion{ProviderID: "campus", AuthenticatedAt: time.Now().UnixMilli()}, "/reauthenticate/desktop")
				require.NoError(t, err)
			} else {
				_, err := service.AuthenticateDesktopReauthenticationPassword(t.Context(), invocation.Invocation{},
					AuthenticateDesktopReauthenticationPasswordCommand{Binding: binding, Password: desktopReauthenticationTestPassword, Source: "192.0.2.10"})
				require.NoError(t, err)
			}
			if !tc.paused {
				require.Nil(t, pauses.created)
				require.NotNil(t, harness.transactions.authentication)
				return
			}
			require.Nil(t, harness.transactions.authentication)
			require.NotNil(t, pauses.created)
			require.Equal(t, model.ExternalAuthenticationPurposeDesktopReauthentication, pauses.created.Purpose)
			require.Equal(t, current.BindingHash, pauses.created.BindingHash)
			if !tc.external {
				require.True(t, pauses.created.PasswordCredentialID.IsValid())
				require.Positive(t, pauses.created.PasswordCredentialRevision)
			}
			current.State = model.BrowserAuthenticationStateSecondFactorRequired
			service.loginRateLimit.MaximumAttempts = 1
			command := CompleteExternalSecondFactorCommand{Binding: binding, Code: "invalid", Source: "192.0.2.10"}
			_, err := service.AuthenticateDesktopReauthenticationSecondFactor(t.Context(), invocation.Invocation{}, command)
			require.True(t, apperror.Is(err, "authentication.mfa.code_invalid"), "%v", err)
			_, err = service.AuthenticateDesktopReauthenticationSecondFactor(t.Context(), invocation.Invocation{}, command)
			require.True(t, apperror.Is(err, "authentication.rate_limited"), "%v", err)
		})
	}
}
