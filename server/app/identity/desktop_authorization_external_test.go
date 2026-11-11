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

func TestDesktopExternalAuthenticationStartUsesBoundTransactionOwner(t *testing.T) {
	t.Parallel()
	transactionID := model.NewBrowserAuthenticationTransactionID()
	binding, state := model.NewCredentialToken(), model.NewCredentialToken()
	transactions := &desktopAuthorizationStoreFake{contextResult: &store.DesktopAuthorizationContext{
		ID: transactionID, State: model.BrowserAuthenticationStateBound,
	}}
	service := newDesktopExternalAuthenticationFixture(t, transactions)
	// The handoff starts from the Desktop transaction the browser binding names;
	// external authentication reaches it only through Desktop authorization.
	result, err := service.BeginDesktopExternalAuthentication(t.Context(), invocation.Invocation{}, BeginDesktopExternalAuthenticationCommand{
		ProviderID: "campus", Binding: binding, State: state, Source: "192.0.2.10",
	})
	require.NoError(t, err, "begin Desktop external authentication")
	require.NotNil(t, result, "begin Desktop external authentication")
	saved := service.externalLoginStates.(*externalLoginStateStoreFake).saved
	require.NotNil(t, saved)
	require.Equal(t, model.ExternalAuthenticationPurposeDesktopAuthorization, saved.Purpose)
	require.Equal(t, transactionID, saved.BrowserAuthenticationTransactionID)
	require.Equal(t, model.SessionClientWeb, saved.ClientType)
	require.Equal(t, "/authorize/desktop?state="+state, saved.ReturnTo)
	require.Empty(t, saved.TargetUserID)
	require.Empty(t, saved.InvitationID)
	require.Equal(t, 1, transactions.contextCalls, "Desktop lookup calls")
	require.Equal(t, model.HashToken(binding), transactions.contextBinding, "Desktop lookup calls")
}

func TestDesktopExternalAuthenticationRejectsUnboundProofBeforeProvider(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name         string
		binding      string
		state        string
		transactions desktopAuthorizationStoreFake
		code         string
		lookups      int
	}{
		{name: "invalid binding", binding: "invalid", state: model.NewCredentialToken(), code: "authentication.desktop_authorization.invalid"},
		{name: "invalid state", binding: model.NewCredentialToken(), state: "invalid", code: "authentication.desktop_authorization.invalid"},
		{name: "wrong purpose or expired", binding: model.NewCredentialToken(), state: model.NewCredentialToken(),
			transactions: desktopAuthorizationStoreFake{contextErr: store.NewErrNotFound("browser_authentication_transaction", "")},
			code:         "authentication.desktop_authorization.rejected", lookups: 1},
		{name: "missing context", binding: model.NewCredentialToken(), state: model.NewCredentialToken(),
			transactions: desktopAuthorizationStoreFake{contextNil: true}, code: "authentication.desktop_authorization.unavailable", lookups: 1},
		{name: "invalid transaction", binding: model.NewCredentialToken(), state: model.NewCredentialToken(),
			transactions: desktopAuthorizationStoreFake{contextResult: &store.DesktopAuthorizationContext{State: model.BrowserAuthenticationStateBound}},
			code:         "authentication.desktop_authorization.unavailable", lookups: 1},
		{name: "already authenticated", binding: model.NewCredentialToken(), state: model.NewCredentialToken(),
			transactions: desktopAuthorizationStoreFake{contextResult: &store.DesktopAuthorizationContext{
				ID: model.NewBrowserAuthenticationTransactionID(), State: model.BrowserAuthenticationStateAuthenticated}},
			code: "authentication.desktop_authorization.unavailable", lookups: 1},
	} {
		t.Run(test.name, func(t *testing.T) {
			service := newDesktopExternalAuthenticationFixture(t, &test.transactions)
			provider := &recordingExternalProvider{}
			service.externalProviders = externalProviderSourceFake{provider: provider}
			result, err := service.BeginDesktopExternalAuthentication(t.Context(), invocation.Invocation{}, BeginDesktopExternalAuthenticationCommand{
				ProviderID: "campus", Binding: test.binding, State: test.state, Source: "192.0.2.10",
			})
			require.Nil(t, result, "begin = %#v, %v; want %s", result, err, test.code)
			require.True(t, apperror.Is(err, test.code), "begin = %#v, %v; want %s", result, err, test.code)
			require.Equal(t, test.lookups, test.transactions.contextCalls, "invalid Desktop proof reached provider or login-state persistence")
			require.Equal(t, 0, provider.beginCalls, "invalid Desktop proof reached provider or login-state persistence")
			require.Nil(t, service.externalLoginStates.(*externalLoginStateStoreFake).saved, "invalid Desktop proof reached provider or login-state persistence")
		})
	}
}

func TestExternalAuthenticationCompletionIsolatesDesktopPurpose(t *testing.T) {
	t.Parallel()
	for _, purpose := range []model.ExternalAuthenticationPurpose{
		model.ExternalAuthenticationPurposeLogin, model.ExternalAuthenticationPurposeDesktopAuthorization,
		model.ExternalAuthenticationPurposeConnect, model.ExternalAuthenticationPurposeInvitationAdmission,
	} {
		t.Run(string(purpose), func(t *testing.T) {
			at := time.Date(2026, 8, 12, 12, 0, 0, 0, time.UTC)
			transactions := &desktopAuthorizationStoreFake{}
			service := newDesktopExternalAuthenticationFixture(t, transactions)
			user := &model.User{ID: model.NewUserID(), Username: "student", Email: "student@example.edu"}
			identity := &model.ExternalIdentity{ID: model.NewExternalIdentityID(), UserID: user.ID, Provider: "campus", Subject: "opaque-subject"}
			identities := &externalHandoffIdentityStore{user: user, identity: identity}
			sessions, recovery := &externalSessionStoreFake{}, &externalRecoveryStateStoreFake{}
			acceptor := &externalInvitationAcceptorFake{result: &store.ExternalIdentityInvitationAcceptanceResult{User: user, Identity: identity}}
			stateToken, binding := model.NewCredentialToken(), model.NewCredentialToken()
			state := &model.ExternalLoginState{Provider: "campus", Purpose: purpose,
				StateHash: model.HashToken(stateToken), BindingHash: model.HashToken(binding), ClientType: model.SessionClientWeb,
				ReturnTo: "/authorize/desktop?state=" + model.NewCredentialToken(), ExpiresAt: at.Add(time.Minute)}
			switch purpose {
			case model.ExternalAuthenticationPurposeDesktopAuthorization:
				state.BrowserAuthenticationTransactionID = model.NewBrowserAuthenticationTransactionID()
			case model.ExternalAuthenticationPurposeConnect:
				state.TargetUserID, state.AuditEventID = user.ID, model.NewAuditEventID().String()
			case model.ExternalAuthenticationPurposeInvitationAdmission:
				state.InvitationID = model.NewInvitationID()
			}
			assertion := &model.ExternalAuthenticationAssertion{ProviderID: "campus", Subject: identity.Subject, Email: user.Email,
				EmailVerified: true, AuthenticationStrength: model.AuthenticationMultiFactor, AuthenticatedAt: at.UnixMilli()}
			service.externalProviders = externalProviderSourceFake{provider: providerConnectionProvider{state: stateToken, assertion: assertion}}
			service.externalLoginStates = &externalLoginStateStoreFake{get: state, consumeResult: state, storeNow: at}
			service.externalIdentities, service.invitations = identities, acceptor
			service.sessions, service.mfaCredentials = sessions, recovery
			service.accessPolicy = authenticationAccessPolicyFake{providers: map[string]bool{"campus": true}, desktop: true,
				providerModes: map[string]model.ProviderAdmissionMode{"campus": model.ProviderAdmissionInvitationRequired}}
			audit := &securityAuditorFake{}
			service.securityAudit, service.now = audit, func() time.Time { return at }
			result, err := service.CompleteExternalAuthentication(t.Context(), invocation.Invocation{}, CompleteExternalAuthenticationCommand{
				ProviderID: "campus", Binding: binding,
			})
			require.NoError(t, err, "complete %s", purpose)
			require.NotNil(t, result, "complete %s", purpose)
			require.Equal(t, state.ReturnTo, result.ReturnTo, "complete %s", purpose)
			if purpose == model.ExternalAuthenticationPurposeDesktopAuthorization {
				proof := transactions.authenticated
				require.NotNil(t, proof)
				require.Equal(t, state.BrowserAuthenticationTransactionID, proof.TransactionID)
				require.Equal(t, user.ID, proof.UserID)
				require.Equal(t, identity.ID, proof.ExternalIdentityID)
				require.Equal(t, "campus", proof.AuthenticationProviderID)
				require.Equal(t, "oidc", proof.AuthenticationMethod)
				require.True(t, model.TimeUTC(at).Equal(proof.AuthenticatedAt))
				require.Equal(t, model.OptionalTimeFrom(model.TimeUTC(at)), proof.MFACompletedAt)
			} else {
				require.Nil(t, transactions.authenticated, "%s authenticated a Desktop transaction", purpose)
			}
			if purpose == model.ExternalAuthenticationPurposeLogin {
				require.Len(t, sessions.saves, 1)
				require.Same(t, sessions.saved, result.Session)
				require.NotNil(t, result.Tokens)
				saved := sessions.saves[0]
				require.Equal(t, state.ID, saved.ExternalLoginStateID, "the Session consumes the login state")
				require.Equal(t, identity.ID, saved.Session.ExternalIdentityID)
				require.Equal(t, model.HashToken(result.Tokens.AccessToken), saved.Credentials[0].TokenHash, "the returned token is the saved access credential")
				begun := audit.events[len(audit.events)-1]
				require.Equal(t, model.ActionAuthenticationExternalLogin, begun.action, "the Session completes the external sign-in attempt")
				require.Equal(t, begun.id, saved.AuditEventID, "the Session completes the external sign-in attempt")
			} else {
				require.Empty(t, sessions.saves, "%s created an ordinary Session", purpose)
				require.Nil(t, result.Session, "%s created an ordinary Session", purpose)
				require.Nil(t, result.Tokens, "%s created an ordinary Session", purpose)
			}
			require.Equal(t, purpose == model.ExternalAuthenticationPurposeConnect, identities.linkCalls == 1, "callback crossed a different authentication purpose")
			require.Equal(t, purpose == model.ExternalAuthenticationPurposeInvitationAdmission, acceptor.state != nil, "callback crossed a different authentication purpose")
		})
	}
}

// newDesktopExternalAuthenticationFixture builds a Service with Desktop
// authorization over transactions and the external authentication
// collaborators, checked as New checks them.
func newDesktopExternalAuthenticationFixture(t *testing.T, transactions *desktopAuthorizationStoreFake) *Service {
	t.Helper()
	service := newTestDesktopAuthorizationService(t, transactions, time.Now)
	service.desktopPolicy.Issuer = "https://proctor.example.test"
	service.accessPolicy = authenticationAccessPolicyFake{local: true, desktop: true, providers: map[string]bool{"campus": true}}
	events := []string{}
	service.externalProviders = externalProviderSourceFake{provider: externalProviderFake{}}
	service.externalLoginStates, service.externalIdentities = &externalLoginStateStoreFake{}, externalIdentityStoreFake{}
	service.invalidator, service.invitations = externalInvalidatorFake{}, &externalInvitationAcceptorFake{}
	service.auditor = &mutationAttemptAuditorFake{events: &events, beginID: model.NewAuditEventID().String()}
	service.diagnostics = newDiscardLogger(t)
	service.externalAuthenticationPolicy = ExternalAuthenticationPolicy{PublicURL: "https://proctor.example.test", LoginStateTTL: 5 * time.Minute,
		LoginRateLimit: LoginRateLimitPolicy{Window: time.Minute, MaximumSourceAttempts: 10}}
	require.NoError(t, service.requireExternalAuthentication())
	return service
}

type externalHandoffIdentityStore struct {
	store.ExternalIdentityStore
	user      *model.User
	identity  *model.ExternalIdentity
	linkCalls int
}

func (s *externalHandoffIdentityStore) ResolveOrProvision(context.Context, *store.ExternalIdentityResolutionRequest) (*store.ExternalIdentityResolution, error) {
	return &store.ExternalIdentityResolution{User: s.user, Identity: s.identity}, nil
}

func (s *externalHandoffIdentityStore) Link(context.Context, *store.ExternalIdentityLink) (*store.AuthenticationMethodMutationResult, error) {
	s.linkCalls++
	return &store.AuthenticationMethodMutationResult{Identity: s.identity}, nil
}
