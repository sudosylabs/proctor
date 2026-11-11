// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package identity

import (
	"context"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/sudosylabs/proctor/server/app/apperror"
	"github.com/sudosylabs/proctor/server/app/invocation"
	"github.com/sudosylabs/proctor/server/model"
	"github.com/sudosylabs/proctor/server/store"
)

const desktopReauthenticationTestPassword = "CorrectHorseBatteryStaple1!"

type desktopReauthenticationStoreFake struct {
	store.BrowserAuthenticationStore
	context        *store.DesktopReauthenticationContext
	created        *store.DesktopReauthenticationCreation
	authentication *store.DesktopReauthenticationAuthentication
	authResult     *store.DesktopReauthenticationAuthenticationResult
	completion     *store.DesktopReauthenticationCompletion
	completed      *store.SessionReauthenticationResult
	completeErr    error
}

func (s *desktopReauthenticationStoreFake) CreateDesktopReauthentication(_ context.Context, input *store.DesktopReauthenticationCreation) (*store.DesktopAuthorizationCreated, error) {
	s.created = input
	return &store.DesktopAuthorizationCreated{ID: input.ID, ExpiresAt: time.Now().Add(input.Lifetime)}, nil
}

func (s *desktopReauthenticationStoreFake) BindDesktopReauthentication(context.Context, *store.DesktopAuthorizationBinding) (*store.DesktopAuthorizationBound, error) {
	return &store.DesktopAuthorizationBound{ExpiresAt: time.Now().Add(time.Minute)}, nil
}

func (s *desktopReauthenticationStoreFake) GetDesktopReauthenticationContext(context.Context, string) (*store.DesktopReauthenticationContext, error) {
	if s.context == nil {
		return nil, store.NewErrNotFound("browser_authentication_transaction", "binding")
	}
	return s.context, nil
}

func (s *desktopReauthenticationStoreFake) AuthenticateDesktopReauthentication(_ context.Context, input *store.DesktopReauthenticationAuthentication) (*store.DesktopReauthenticationAuthenticationResult, error) {
	s.authentication = input
	if s.authResult != nil {
		return s.authResult, nil
	}
	return &store.DesktopReauthenticationAuthenticationResult{}, nil
}

func (s *desktopReauthenticationStoreFake) IssueDesktopReauthenticationCode(context.Context, *store.DesktopReauthenticationCodeIssue) (*store.DesktopAuthorizationCodeIssued, error) {
	return &store.DesktopAuthorizationCodeIssued{CallbackURL: "http://127.0.0.1:49152/" + model.NewCredentialToken(), CodeExpiresAt: time.Now().Add(time.Minute)}, nil
}

func (s *desktopReauthenticationStoreFake) CancelDesktopReauthentication(context.Context, *store.DesktopAuthorizationCancellation) error {
	return nil
}

func (s *desktopReauthenticationStoreFake) CompleteDesktopReauthentication(_ context.Context, input *store.DesktopReauthenticationCompletion) (*store.SessionReauthenticationResult, error) {
	s.completion = input
	return s.completed, s.completeErr
}

type desktopReauthenticationTestHarness struct {
	service       *Service
	transactions  *desktopReauthenticationStoreFake
	audit         *securityAuditorFake
	mutations     *mutationAttemptAuditorFake
	user          *model.User
	institutionID model.InstitutionID
}

func newDesktopReauthenticationTestHarness(t *testing.T) desktopReauthenticationTestHarness {
	t.Helper()
	persistence := newAuthenticationStoreFake()
	service := newTestAuthenticationService(t, persistence)
	user, err := service.CreateLocalUser(context.Background(), CreateLocalUserCommand{
		User: &model.User{Username: "desktop-reauthentication", Email: "desktop-reauthentication@example.edu", DisplayName: "Ada Candidate"}, Password: desktopReauthenticationTestPassword,
	})
	require.NoError(t, err)
	events := []string{}
	mutations := &mutationAttemptAuditorFake{events: &events, beginID: model.NewID()}
	service.auditor = mutations
	transactions := &desktopReauthenticationStoreFake{}
	audit := &securityAuditorFake{}
	institutionID := model.NewInstitutionID()
	service.browserAuthentications, service.securityAudit = transactions, audit
	service.institutions = desktopAuthorizationInstitutionStoreFake{institution: &model.Institution{ID: institutionID}}
	service.capabilities = &accessPolicyCapabilitiesFake{snapshot: AccessPolicyCapabilitySnapshot{Providers: []AccessPolicyProviderCapability{
		{Descriptor: model.ExternalAuthenticationProvider{ID: "campus", DisplayName: "Campus sign-in", Type: "oidc"}},
	}}}
	service.desktopPolicy = DesktopAuthorizationPolicy{Issuer: "https://proctor.example.edu"}
	service.recentAuthenticationTTL = 15 * time.Minute
	require.NoError(t, service.requireDesktopReauthentication())
	return desktopReauthenticationTestHarness{service: service, transactions: transactions, audit: audit, mutations: mutations, user: user,
		institutionID: institutionID}
}

func desktopReauthenticationPrincipal(userID model.UserID, client model.SessionClientType) model.Principal {
	principal := model.Principal{
		UserID: userID, SessionID: model.NewSessionID(), CredentialID: model.PrincipalCredentialID(model.NewSessionCredentialID()),
		CredentialType: model.CredentialSessionAccess, ClientType: client, AuthenticationMethod: "password",
		AuthenticationStrength: model.AuthenticationSingleFactor, AuthenticatedAt: time.Now().UTC().Add(-time.Hour),
	}
	if client == model.SessionClientDesktop {
		principal.DesktopRegistrationID = model.NewDesktopRegistrationID()
		principal.DPoPKeyThumbprint = strings.Repeat("A", 43)
		principal.RegisteredDesktopKey = true
		principal.DesktopRelease, principal.DesktopBuildID = "0.1.0", "test-build"
		principal.DesktopPlatform, principal.DesktopArchitecture = model.DesktopPlatformDarwin, model.DesktopArchitectureARM64
		principal.DesktopRealtimeProtocol = 1
	}
	return principal
}

func TestDesktopReauthenticationStartAcceptsOnlyARegisteredDesktopKey(t *testing.T) {
	harness := newDesktopReauthenticationTestHarness(t)
	command := StartDesktopReauthenticationCommand{
		CallbackURL: "http://127.0.0.1:49152/" + model.NewCredentialToken(), State: model.NewCredentialToken(),
		CodeChallenge: model.PKCES256Challenge(model.NewCredentialToken()), Source: "192.0.2.10",
	}
	web := desktopReauthenticationPrincipal(harness.user.ID, model.SessionClientWeb)
	// A Desktop Session whose current request carried no DPoP proof.
	withoutProof := desktopReauthenticationPrincipal(harness.user.ID, model.SessionClientWeb)
	withoutProof.ClientType = model.SessionClientDesktop
	token := model.Principal{UserID: harness.user.ID, CredentialID: model.PrincipalCredentialID(model.NewID()),
		CredentialType: model.CredentialPersonalAccessToken, AuthenticatedAt: time.Now().UTC()}
	for name, principal := range map[string]model.Principal{"Web Session": web, "Desktop Session without DPoP": withoutProof, "Personal Access Token": token} {
		t.Run(name, func(t *testing.T) {
			_, err := harness.service.StartDesktopReauthentication(context.Background(), invocation.New(principal, model.RequestMetadata{}), command)
			require.True(t, apperror.Is(err, "authentication.desktop_reauthentication.invalid"), "start error = %v", err)
		})
	}
	require.Nil(t, harness.transactions.created, "a refused caller created a reauthentication")

	desktop := desktopReauthenticationPrincipal(harness.user.ID, model.SessionClientDesktop)
	started, err := harness.service.StartDesktopReauthentication(context.Background(), invocation.New(desktop, model.RequestMetadata{}), command)
	require.NoError(t, err)
	created := harness.transactions.created
	require.Equal(t, desktop.SessionID, created.SessionID, "reauthentication was not bound to the calling Session")
	require.Equal(t, desktop.CredentialID.String(), created.CredentialID.String(), "reauthentication was not bound to the calling Session")
	require.Equal(t, desktop.DesktopRegistrationID, created.DesktopRegistrationID, "reauthentication was not bound to the registered key")
	require.Equal(t, desktop.DPoPKeyThumbprint, created.KeyThumbprint, "reauthentication was not bound to the registered key")
	require.Equal(t, harness.user.ID, created.UserID)
	page, err := url.Parse(started.AuthorizationURL)
	require.NoError(t, err)
	require.Equal(t, "/reauthenticate/desktop", page.Path)
	require.Equal(t, command.State, page.Query().Get("state"))
	require.True(t, strings.HasPrefix(page.Fragment, "proof="), "browser proof must travel only in the fragment")
}

func TestDesktopReauthenticationAcceptsOnlyTheSessionMethod(t *testing.T) {
	harness := newDesktopReauthenticationTestHarness(t)
	binding := model.NewCredentialToken()
	harness.transactions.context = &store.DesktopReauthenticationContext{ID: model.NewBrowserAuthenticationTransactionID(),
		State: model.BrowserAuthenticationStateBound, UserID: harness.user.ID, ExpectedAuthenticationMethod: "password",
		CreatedAt: time.Now(), ExpiresAt: time.Now().Add(time.Minute)}

	_, err := harness.service.resolveExternalReauthentication(context.Background(), binding, model.NewCredentialToken(), "campus")
	require.True(t, apperror.Is(err, "authentication.reauthentication_method_required"), "password Session took the provider path: %v", err)

	harness.transactions.context.ExpectedAuthenticationMethod, harness.transactions.context.ExpectedProviderID = "oidc", "campus"
	_, err = harness.service.AuthenticateDesktopReauthenticationPassword(context.Background(), invocation.Invocation{},
		AuthenticateDesktopReauthenticationPasswordCommand{Binding: binding, Password: desktopReauthenticationTestPassword, Source: "192.0.2.10"})
	require.True(t, apperror.Is(err, "authentication.reauthentication_method_required"), "provider Session took the password path: %v", err)
	_, err = harness.service.resolveExternalReauthentication(context.Background(), binding, model.NewCredentialToken(), "another")
	require.True(t, apperror.Is(err, "authentication.reauthentication_method_required"), "another provider was offered: %v", err)
	target, err := harness.service.resolveExternalReauthentication(context.Background(), binding, model.NewCredentialToken(), "campus")
	require.NoError(t, err)
	require.True(t, strings.HasPrefix(target.returnTo, "/reauthenticate/desktop?state="), "provider return = %q", target.returnTo)
	require.Nil(t, harness.transactions.authentication, "a refused method recorded proof")
}

func TestDesktopReauthenticationNeverAcceptsABrowserWebSessionAsProof(t *testing.T) {
	harness := newDesktopReauthenticationTestHarness(t)
	binding := model.NewCredentialToken()
	harness.transactions.context = &store.DesktopReauthenticationContext{ID: model.NewBrowserAuthenticationTransactionID(),
		State: model.BrowserAuthenticationStateBound, UserID: harness.user.ID, ExpectedAuthenticationMethod: "password",
		CreatedAt: time.Now(), ExpiresAt: time.Now().Add(time.Minute)}
	// A fresh Web Session of the same User arrives with the browser request,
	// yet only the password proves anything.
	web := desktopReauthenticationPrincipal(harness.user.ID, model.SessionClientWeb)
	web.AuthenticatedAt = time.Now().UTC()
	_, err := harness.service.AuthenticateDesktopReauthenticationPassword(context.Background(), invocation.New(web, model.RequestMetadata{}),
		AuthenticateDesktopReauthenticationPasswordCommand{Binding: binding, Password: "IncorrectHorseBatteryStaple1!", Source: "192.0.2.10"})
	require.True(t, apperror.Is(err, "authentication.credentials_invalid"), "Web Session substituted for the password: %v", err)
	require.Nil(t, harness.transactions.authentication, "a wrong password recorded proof")

	result, err := harness.service.AuthenticateDesktopReauthenticationPassword(context.Background(), invocation.New(web, model.RequestMetadata{}),
		AuthenticateDesktopReauthenticationPasswordCommand{Binding: binding, Password: desktopReauthenticationTestPassword, Source: "192.0.2.10"})
	require.NoError(t, err)
	require.Equal(t, "Ada Candidate", result.DisplayName)
	proof := harness.transactions.authentication
	require.Equal(t, harness.user.ID, proof.UserID, "proof did not target the Desktop Session's User")
	require.True(t, proof.PasswordProof.IsValid(), "proof did not pin the password revision")
	require.Empty(t, proof.ProviderID)
}

func TestDesktopReauthenticationDeniesAndAuditsAnotherAccount(t *testing.T) {
	harness := newDesktopReauthenticationTestHarness(t)
	owner := model.NewUserID()
	harness.transactions.authResult = &store.DesktopReauthenticationAuthenticationResult{Denied: true, UserID: owner}
	identity := &model.ExternalIdentity{ID: model.NewExternalIdentityID(), UserID: harness.user.ID, Provider: "campus"}
	err := harness.service.authenticateExternalReauthentication(context.Background(), invocation.Invocation{},
		model.NewBrowserAuthenticationTransactionID(), identity, "oidc",
		&model.ExternalAuthenticationAssertion{ProviderID: "campus", AuthenticatedAt: time.Now().UnixMilli()})
	require.True(t, apperror.Is(err, "authentication.desktop_reauthentication.denied"), "mismatch error = %v", err)
	require.Len(t, harness.audit.terminal, 1)
	rejection := harness.audit.terminal[0]
	require.Equal(t, harness.user.ID, rejection.actor)
	require.Equal(t, owner, rejection.target)
	require.Equal(t, model.ActionAuthenticationReauthenticate, rejection.action)
	require.Equal(t, model.SessionClientWeb, rejection.clientType)
	require.Equal(t, model.RoleScopeInstitution, rejection.scopeType)
	require.Equal(t, harness.institutionID.String(), rejection.scopeID)
	require.Equal(t, "desktop_external", rejection.operation())
	require.Equal(t, model.AuditStatusFail, rejection.status)
	require.Equal(t, "authentication.desktop_reauthentication.denied", rejection.errorCode)
}

func TestDesktopReauthenticationCompletesFromTheSameDesktopSession(t *testing.T) {
	harness := newDesktopReauthenticationTestHarness(t)
	desktop := desktopReauthenticationPrincipal(harness.user.ID, model.SessionClientDesktop)
	// Stored instants carry microseconds; Linux clocks also report nanoseconds.
	reauthenticatedAt := model.TimeUTC(time.Now())
	harness.transactions.completed = &store.SessionReauthenticationResult{Session: &model.Session{ID: desktop.SessionID,
		AuthenticatedAt: desktop.AuthenticatedAt, ReauthenticatedAt: model.OptionalTimeFrom(reauthenticatedAt)}}
	command := CompleteDesktopReauthenticationCommand{Code: model.NewCredentialToken(), CodeVerifier: model.NewCredentialToken(),
		State: model.NewCredentialToken(), Source: "192.0.2.10"}

	_, err := harness.service.CompleteDesktopReauthentication(context.Background(),
		invocation.New(desktopReauthenticationPrincipal(harness.user.ID, model.SessionClientWeb), model.RequestMetadata{}), command)
	require.True(t, apperror.Is(err, "authentication.desktop_reauthentication.invalid"), "Web Session redeemed a Desktop code: %v", err)
	require.Nil(t, harness.transactions.completion, "a Web Session reached the Store")

	result, err := harness.service.CompleteDesktopReauthentication(context.Background(), invocation.New(desktop, model.RequestMetadata{}), command)
	require.NoError(t, err)
	require.True(t, result.RecentlyAuthenticatedUntil.Equal(reauthenticatedAt.Add(15*time.Minute)))
	completion := harness.transactions.completion
	require.Equal(t, desktop.SessionID, completion.SessionID, "completion was not bound to the calling Session and key")
	require.Equal(t, desktop.DesktopRegistrationID, completion.DesktopRegistrationID, "completion was not bound to the calling Session and key")
	require.Equal(t, desktop.DPoPKeyThumbprint, completion.KeyThumbprint, "completion was not bound to the calling Session and key")
	require.Equal(t, model.PKCES256Challenge(command.CodeVerifier), completion.CodeChallenge)
	require.Equal(t, "desktop_password", harness.mutations.attempt.Operation)
	require.Equal(t, model.ActionAuthenticationReauthenticate, harness.mutations.attempt.Action)

	harness.transactions.completeErr = store.NewErrNotFound("browser_authentication_transaction", "desktop_reauthentication_code")
	_, err = harness.service.CompleteDesktopReauthentication(context.Background(), invocation.New(desktop, model.RequestMetadata{}), command)
	require.True(t, apperror.Is(err, "authentication.desktop_reauthentication.invalid"), "another Session or key redeemed the code: %v", err)
}
