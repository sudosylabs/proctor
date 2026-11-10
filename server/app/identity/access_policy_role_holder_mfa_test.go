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

const roleHolderMFATestPassword = "CorrectHorseBatteryStaple1!"

// roleHolderMFALoginFixture creates one local User without a second factor and
// returns the service and the User. covered decides whether the Access Policy
// requires that User to use two-step verification.
func roleHolderMFALoginFixture(t *testing.T, covered bool) (*Service, *authenticationStoreFake, *model.User) {
	t.Helper()
	persistence := newAuthenticationStoreFake()
	service := newTestAuthenticationService(t, persistence)
	user, err := service.CreateLocalUser(context.Background(), CreateLocalUserCommand{
		User: &model.User{Username: "role-holder-user", Email: "role-holder-user@example.edu"}, Password: roleHolderMFATestPassword,
	})
	require.NoError(t, err)
	policy := allowAllAuthenticationAccessPolicy().(authenticationAccessPolicyFake)
	policy.roleHolderMFA = map[model.UserID]bool{user.ID: covered}
	service.accessPolicy = policy
	return service, persistence, user
}

func TestLoginRestrictsSingleFactorWebSessionOfRoleHolderToMFAEnrollment(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name       string
		covered    bool
		restricted bool
	}{
		{name: "role holder without two-step", covered: true, restricted: true},
		{name: "not covered", covered: false, restricted: false},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			service, _, user := roleHolderMFALoginFixture(t, test.covered)
			result, err := service.Login(context.Background(), invocation.Invocation{}, LoginCommand{
				LoginID: user.Email, Password: roleHolderMFATestPassword, ClientType: model.SessionClientWeb, Source: "192.0.2.10",
			})
			require.NoError(t, err)
			require.Equal(t, test.restricted, result.Session.MFAEnrollmentRequired)
			principal, err := service.authenticateAccess(context.Background(), result.Tokens.AccessToken)
			require.NoError(t, err)
			require.Equal(t, test.restricted, principal.MFAEnrollmentRequired)
			require.Equal(t, test.restricted, principal.Validate() != nil, "a restricted principal never validates as ordinary")
		})
	}
}

func TestLoginRefusesNonWebSessionOfRoleHolderWithoutTwoStep(t *testing.T) {
	t.Parallel()
	service, persistence, user := roleHolderMFALoginFixture(t, true)
	result, err := service.Login(context.Background(), invocation.Invocation{}, LoginCommand{
		LoginID: user.Email, Password: roleHolderMFATestPassword, ClientType: model.SessionClientCLI, Source: "192.0.2.11",
	})
	require.Nil(t, result)
	require.True(t, apperror.Is(err, "authentication.mfa.enrollment_required"), "err=%v", err)
	require.Empty(t, persistence.sessions)
}

func TestRefreshEnforcesRoleHolderMFARequirementBySessionKind(t *testing.T) {
	t.Parallel()
	at := time.Date(2026, 11, 9, 9, 0, 0, 0, time.UTC)
	for _, test := range []struct {
		name         string
		clientType   model.SessionClientType
		strength     model.AuthenticationStrength
		covered      bool
		wantCode     string
		wantRestrict bool
		wantRotation bool
	}{
		{name: "web single factor of a role holder becomes restricted", clientType: model.SessionClientWeb, strength: model.AuthenticationSingleFactor, covered: true, wantRestrict: true, wantRotation: true},
		{name: "web multi factor of a role holder is unchanged", clientType: model.SessionClientWeb, strength: model.AuthenticationMultiFactor, covered: true, wantRotation: true},
		{name: "web single factor of a non role holder is unchanged", clientType: model.SessionClientWeb, strength: model.AuthenticationSingleFactor, wantRotation: true},
		{name: "cli single factor of a role holder is refused", clientType: model.SessionClientCLI, strength: model.AuthenticationSingleFactor, covered: true, wantCode: "authentication.mfa.enrollment_required"},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			user := &model.User{ID: model.NewUserID(), CreatedAt: at, UpdatedAt: at, Revision: 1}
			session := &model.Session{ID: model.NewSessionID(), UserID: user.ID, ClientType: test.clientType, AuthenticationStrength: test.strength}
			persistence := newAuthenticationStoreFake()
			persistence.users[user.ID.String()] = user
			persistence.rotation = &store.SessionRotation{Session: session}
			service := newTestAuthenticationServiceWithEffects(t, persistence, newAuthenticationCacheFake(), &authenticationEffectsRecorder{},
				&mfaApplicationStoreFake{}, discardPersonalAccessTokenStore{}, model.NewCredentialToken, func() time.Time { return at })
			service.accessPolicy = authenticationAccessPolicyFake{local: true, roleHolderMFA: map[model.UserID]bool{user.ID: test.covered}}
			_, _, err := service.RefreshSession(context.Background(), invocation.Invocation{}, RefreshSessionCommand{RefreshToken: model.NewCredentialToken()})
			if test.wantCode != "" {
				require.True(t, apperror.Is(err, test.wantCode), "err=%v", err)
			} else {
				require.NoError(t, err)
			}
			require.Equal(t, test.wantRotation, persistence.rotationInput != nil)
			if persistence.rotationInput != nil {
				require.Equal(t, test.wantRestrict, persistence.rotationInput.MFARequiredByPolicy)
			}
		})
	}
}

func TestDesktopAuthorizationRefusesSingleFactorProofOfRoleHolder(t *testing.T) {
	t.Parallel()
	at := time.Date(2026, 11, 9, 9, 0, 0, 0, time.UTC)
	userID := model.NewUserID()
	persistence := &desktopAuthorizationStoreFake{contextUserID: userID}
	service := newTestDesktopAuthorizationService(t, persistence, func() time.Time { return at })
	service.accessPolicy = authenticationAccessPolicyFake{local: true, desktop: true, roleHolderMFA: map[model.UserID]bool{userID: true}}
	principal := model.Principal{UserID: userID, SessionID: model.NewSessionID(), CredentialID: model.PrincipalCredentialID(model.NewID()),
		CredentialType: model.CredentialSessionAccess, AuthenticationMethod: "password", AuthenticationStrength: model.AuthenticationSingleFactor,
		ClientType: model.SessionClientWeb, AuthenticatedAt: at}
	for _, test := range []struct {
		name      string
		principal model.Principal
	}{
		{name: "ordinary single-factor web session", principal: principal},
		{name: "enrollment-restricted web session", principal: func() model.Principal {
			restricted := principal
			restricted.MFAEnrollmentRequired = true
			return restricted
		}()},
	} {
		t.Run(test.name, func(t *testing.T) {
			_, err := service.AuthenticateDesktopAuthorizationSession(context.Background(), invocation.New(test.principal, model.RequestMetadata{}),
				AuthenticateDesktopAuthorizationCommand{Binding: model.NewCredentialToken()})
			require.True(t, apperror.Is(err, "authentication.mfa.enrollment_required"), "err=%v", err)
		})
	}
	require.Nil(t, persistence.authenticated, "a refused proof never reaches the browser transaction")

}

func TestDesktopExchangeCarriesRoleHolderMFARequirementToTheStore(t *testing.T) {
	t.Parallel()
	at := time.Date(2026, 11, 9, 9, 0, 0, 0, time.UTC)
	userID := model.NewUserID()
	persistence := &desktopAuthorizationStoreFake{exchangeUserID: userID, exchangeErr: store.ErrMFAEnrollmentRequired}
	service := newTestDesktopAuthorizationService(t, persistence, func() time.Time { return at })
	service.accessPolicy = authenticationAccessPolicyFake{local: true, desktop: true, roleHolderMFA: map[model.UserID]bool{userID: true}}
	command := testDesktopAuthorizationExchangeCommand(t, service, at, model.NewCredentialToken(), model.NewCredentialToken(), model.NewCredentialToken(), "")
	_, err := service.ExchangeDesktopAuthorization(context.Background(), invocation.Invocation{}, command)
	require.True(t, apperror.Is(err, "authentication.mfa.enrollment_required"), "err=%v", err)
	require.NotNil(t, persistence.exchanged)
	require.True(t, persistence.exchanged.MFARequiredByPolicy)
}

func TestMFAChallengeAdmitsEnrollmentRestrictionButNotRecovery(t *testing.T) {
	t.Parallel()
	at := time.Date(2026, 11, 9, 9, 0, 0, 0, time.UTC)
	service := newTestMFAApplicationService(t, &mfaApplicationStoreFake{}, &securityAuditorFake{}, &mfaApplicationEffectsFake{}, at)
	base := model.Principal{UserID: model.NewUserID(), SessionID: model.NewSessionID(), CredentialID: model.PrincipalCredentialID(model.NewID()),
		CredentialType: model.CredentialSessionAccess, AuthenticationMethod: "password", AuthenticationStrength: model.AuthenticationSingleFactor,
		ClientType: model.SessionClientWeb, AuthenticatedAt: at}
	recovery := base
	recovery.MFARecoveryRequired, recovery.AuthenticationGeneration = true, 1
	_, err := service.ChallengeMFA(context.Background(), invocation.New(recovery, model.RequestMetadata{}), ChallengeMFACommand{Code: "123456"})
	require.True(t, apperror.Is(err, "authentication.session_required"), "err=%v", err)

	enrollment := base
	enrollment.MFAEnrollmentRequired = true
	_, err = service.ChallengeMFA(context.Background(), invocation.New(enrollment, model.RequestMetadata{}), ChallengeMFACommand{Code: "123456"})
	require.False(t, apperror.Is(err, "authentication.session_required"), "an enrollment-restricted Session reaches factor verification: %v", err)
}

func TestMFADisableIsRefusedWhileThePolicyRequiresIt(t *testing.T) {
	t.Parallel()
	at := time.Date(2026, 11, 9, 9, 0, 0, 0, time.UTC)
	events := []string{}
	audit := &securityAuditorFake{trace: &events}
	persistence := &mfaApplicationStoreFake{}
	service := newTestMFAApplicationService(t, persistence, audit, &mfaApplicationEffectsFake{}, at)
	principal := model.Principal{UserID: model.NewUserID(), SessionID: model.NewSessionID(), CredentialID: model.PrincipalCredentialID(model.NewID()),
		CredentialType: model.CredentialSessionAccess, AuthenticationMethod: "password", AuthenticationStrength: model.AuthenticationMultiFactor,
		ClientType: model.SessionClientWeb, AuthenticatedAt: at, MFACompletedAt: model.OptionalTimeFrom(at)}
	service.accessPolicy = authenticationAccessPolicyFake{roleHolderMFA: map[model.UserID]bool{principal.UserID: true}}
	err := service.DisableMFA(context.Background(), invocation.New(principal, model.RequestMetadata{}), DisableMFACommand{})
	require.True(t, apperror.Is(err, "mfa.required_by_policy"), "err=%v", err)
	// The refusal is an audited failure of the attempted disablement.
	require.Equal(t, []string{"audit_begin", "audit_fail"}, events)
	require.Nil(t, persistence.disablement, "the second factor was not disabled")
}
