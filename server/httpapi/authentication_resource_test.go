// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/sudosylabs/proctor/server/app/identity"
	"github.com/sudosylabs/proctor/server/app/realtime/realtimetest"
	"github.com/sudosylabs/proctor/server/httpapi/route"
	"github.com/sudosylabs/proctor/server/httpapi/route/routetest"
	"github.com/sudosylabs/proctor/server/model"
	"github.com/sudosylabs/proctor/server/store"
)

// signinStudent is an active local User and their stored password.
func signinStudent(t *testing.T, password string) (*model.User, *identityPasswords) {
	t.Helper()
	user := &model.User{
		ID: model.NewUserID(), Username: "student", Email: "student@example.edu",
		FirstName: "Sam", LastName: "Student", DisplayName: "Sam Student",
	}
	passwords := &identityPasswords{byUser: map[model.UserID]*model.PasswordCredential{
		user.ID: {ID: model.NewPasswordCredentialID(), UserID: user.ID, Revision: 3, PasswordHash: hashPassword(t, password)},
	}}
	return user, passwords
}

// signinSessionStore records the Sessions issued and revoked.
type signinSessionStore struct {
	store.SessionStore
	mu          sync.Mutex
	saves       []*store.SessionCreation
	revocations []*store.SessionRevocation
	saveErr     error
}

func (s *signinSessionStore) Save(_ context.Context, input *store.SessionCreation) (*model.Session, []*model.SessionCredential, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.saves = append(s.saves, input)
	if s.saveErr != nil {
		return nil, nil, s.saveErr
	}
	saved := *input.Session
	saved.ID = model.NewSessionID()
	saved.CreatedAt, saved.UpdatedAt = input.AuditAt, input.AuditAt
	return &saved, input.Credentials, nil
}

func (s *signinSessionStore) Revoke(_ context.Context, input *store.SessionRevocation) (*store.SessionRevocationResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.revocations = append(s.revocations, input)
	return &store.SessionRevocationResult{
		Session:     &model.Session{ID: input.SessionID, UserID: input.UserID, RevocationReason: input.Reason},
		TokenHashes: []string{model.HashToken("access")},
	}, nil
}

// signinMFAStore is the two-step verification state of the test's Users: no
// pending recovery, and the active credential when one is set.
type signinMFAStore struct {
	identityMFAKeys
	credential   *model.MFACredential
	consumeErr   error
	consumptions []*store.MFASecondFactorConsumption
}

func (s *signinMFAStore) GetRecoveryState(_ context.Context, userID model.UserID) (*model.UserMFARecovery, error) {
	return &model.UserMFARecovery{UserID: userID}, nil
}

func (s *signinMFAStore) GetByUser(_ context.Context, userID model.UserID) (*model.MFACredential, error) {
	if s.credential == nil || s.credential.UserID != userID {
		return nil, store.NewErrNotFound("mfa_credential", userID.String())
	}
	copied := *s.credential
	return &copied, nil
}

func (s *signinMFAStore) ConsumeSecondFactor(_ context.Context, input *store.MFASecondFactorConsumption) error {
	s.consumptions = append(s.consumptions, input)
	return s.consumeErr
}

// signinAccessPolicy is the current Access Policy, which becomes unreadable
// once a test marks it unavailable.
type signinAccessPolicy struct {
	*identityAccessPolicy
	mu          sync.Mutex
	unavailable bool
}

func (s *signinAccessPolicy) Get(ctx context.Context, version int) (*store.AccessPolicySnapshot, error) {
	s.mu.Lock()
	unavailable := s.unavailable
	s.mu.Unlock()
	if unavailable {
		return nil, errors.New("access policy store is unavailable")
	}
	return s.identityAccessPolicy.Get(ctx, version)
}

// signinProviderPolicy admits each provider as linked-only, or through an
// Invitation when invitations is set.
func signinProviderPolicy(t *testing.T, invitations bool, providers ...string) *signinAccessPolicy {
	t.Helper()
	return &signinAccessPolicy{identityAccessPolicy: newIdentityAccessPolicy(t, func(policy *model.AccessPolicy) {
		for _, provider := range providers {
			policy.ProviderAdmissions[provider] = model.ProviderAdmissionLinkedOnly
			if invitations {
				policy.ProviderAdmissions[provider] = model.ProviderAdmissionInvitationRequired
			}
		}
		if invitations {
			policy.InvitationAdmissionEnabled = true
		}
	})}
}

// signinProvider is a configured external identity provider. It reads the
// state back from the callback's "state" value and asserts subject.
type signinProvider struct {
	descriptor model.ExternalAuthenticationProvider
	subject    string
	mu         sync.Mutex
	begins     []identity.ExternalProviderBeginRequest
	completes  []identity.ExternalProviderCompleteRequest
}

func newSigninProvider(id string) *signinProvider {
	return &signinProvider{
		descriptor: model.ExternalAuthenticationProvider{ID: id, DisplayName: "Institution Login", Type: "oidc"},
		subject:    "subject-" + id,
	}
}

func (p *signinProvider) Descriptor() model.ExternalAuthenticationProvider { return p.descriptor }

func (*signinProvider) AutoProvision() bool { return false }

func (p *signinProvider) redirectURL() string {
	return "https://identity.example.edu/authorize/" + p.descriptor.ID
}

func (p *signinProvider) Begin(_ context.Context, request identity.ExternalProviderBeginRequest) (*identity.ExternalProviderBeginResponse, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.begins = append(p.begins, request)
	return &identity.ExternalProviderBeginResponse{RedirectURL: p.redirectURL()}, nil
}

func (*signinProvider) State(callback model.ExternalAuthenticationCallback) (string, error) {
	values := callback.Values["state"]
	if len(values) != 1 {
		return "", identity.ErrExternalAuthenticationInvalid
	}
	return values[0], nil
}

func (p *signinProvider) Complete(_ context.Context, request identity.ExternalProviderCompleteRequest) (*model.ExternalAuthenticationAssertion, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.completes = append(p.completes, request)
	return &model.ExternalAuthenticationAssertion{
		ProviderID: p.descriptor.ID, Subject: p.subject,
		AuthenticationStrength: model.AuthenticationSingleFactor, AuthenticatedAt: time.Now().UnixMilli(),
	}, nil
}

func (p *signinProvider) begun() []identity.ExternalProviderBeginRequest {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]identity.ExternalProviderBeginRequest(nil), p.begins...)
}

// signinLoginStates keeps the external login states sign-in begins and
// consumes, keyed by state hash.
type signinLoginStates struct {
	store.ExternalLoginStateStore
	mu           sync.Mutex
	byStateHash  map[string]*model.ExternalLoginState
	saves        []*store.ExternalLoginStateCreation
	admissions   []*store.ExternalLoginStateInvitationAdmission
	consumptions []*store.ExternalLoginStateConsumption
}

func newSigninLoginStates() *signinLoginStates {
	return &signinLoginStates{byStateHash: map[string]*model.ExternalLoginState{}}
}

func (s *signinLoginStates) Save(_ context.Context, input *store.ExternalLoginStateCreation) (*model.ExternalLoginState, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.saves = append(s.saves, input)
	return s.keep(input.State, input.Lifetime), nil
}

func (s *signinLoginStates) SaveInvitationAdmission(
	_ context.Context, input *store.ExternalLoginStateInvitationAdmission,
) (*model.ExternalLoginState, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.admissions = append(s.admissions, input)
	state := *input.State
	state.InvitationID = model.NewInvitationID()
	return s.keep(&state, input.Lifetime), nil
}

func (s *signinLoginStates) keep(state *model.ExternalLoginState, lifetime time.Duration) *model.ExternalLoginState {
	saved := *state
	now := model.NowUTC()
	saved.PrepareCreate(model.NewExternalLoginStateID(), now)
	saved.ExpiresAt = now.Add(lifetime)
	s.byStateHash[saved.StateHash] = &saved
	copied := saved
	return &copied
}

func (s *signinLoginStates) GetByStateHash(_ context.Context, hash string) (*model.ExternalLoginState, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	state, ok := s.byStateHash[hash]
	if !ok {
		return nil, store.NewErrNotFound("external_login_state", "")
	}
	copied := *state
	return &copied, nil
}

func (s *signinLoginStates) Consume(_ context.Context, input *store.ExternalLoginStateConsumption) (*model.ExternalLoginState, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.consumptions = append(s.consumptions, input)
	state, ok := s.byStateHash[input.StateHash]
	if !ok || state.Provider != input.Provider || state.BindingHash != input.BindingHash || state.ConsumedAt.Valid {
		return nil, store.NewErrNotFound("external_login_state", "")
	}
	state.ConsumedAt = model.OptionalTimeFrom(model.NowUTC())
	copied := *state
	return &copied, nil
}

func (s *signinLoginStates) saved() []*store.ExternalLoginStateCreation {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]*store.ExternalLoginStateCreation(nil), s.saves...)
}

// signinExternalIdentities links every provider subject to one User.
type signinExternalIdentities struct {
	store.ExternalIdentityStore
	user        *model.User
	resolutions []*store.ExternalIdentityResolutionRequest
}

func (s *signinExternalIdentities) ResolveOrProvision(
	_ context.Context, input *store.ExternalIdentityResolutionRequest,
) (*store.ExternalIdentityResolution, error) {
	s.resolutions = append(s.resolutions, input)
	linked := *input.Identity
	linked.ID, linked.UserID = model.NewExternalIdentityID(), s.user.ID
	user := *s.user
	return &store.ExternalIdentityResolution{Identity: &linked, User: &user}, nil
}

// signinExternalFixture is a deployment offering providers for sign-in.
func signinExternalFixture(
	t *testing.T, policy *signinAccessPolicy, states *signinLoginStates, stores identityStores, providers ...*signinProvider,
) *identityFixture {
	t.Helper()
	stores.AccessPolicy, stores.ExternalLoginStates = policy, states
	options := identityOptions{Stores: stores}
	for _, provider := range providers {
		options.Providers = append(options.Providers, provider)
	}
	return newIdentityFixture(t, options)
}

func TestExternalAuthenticationCallbackReturnsValidatedRedirectAndCookies(t *testing.T) {
	t.Parallel()

	logger, _ := routetest.NewLogger(t)
	cookies, err := route.NewCookies("http://localhost:8065")
	require.NoError(t, err)
	user := &model.User{ID: model.NewUserID(), Username: "student", Email: "student@example.edu"}
	provider := newSigninProvider("oidc")
	states := newSigninLoginStates()
	sessions := &signinSessionStore{}
	identities := &signinExternalIdentities{user: user}
	fixture := signinExternalFixture(t, signinProviderPolicy(t, false, "oidc"), states, identityStores{
		Users: newIdentityUsers(user), Sessions: sessions, MFA: &signinMFAStore{}, ExternalIdentities: identities,
	}, provider)
	httpAPI := newFocusedResourceAPI(t, logger, routetest.Authenticator{}, externalAuthenticationResource(fixture.service, cookies))

	begin := httptest.NewRecorder()
	httpAPI.ServeHTTP(begin, httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/api/v1/auth/providers/oidc/login?return_to=%2Fexams", nil))
	require.Equal(t, http.StatusSeeOther, begin.Code, "begin = %d %s", begin.Code, begin.Body.String())
	var binding string
	for _, cookie := range begin.Result().Cookies() {
		if cookie.Name == route.BrowserExternalLoginCookieName {
			binding = cookie.Value
		}
	}
	require.NotEmpty(t, binding, "begin set no external login binding")
	require.Len(t, provider.begun(), 1)
	stateToken := provider.begun()[0].State

	request := httptest.NewRequestWithContext(t.Context(), http.MethodGet,
		"/api/v1/auth/providers/oidc/callback?code=accepted&state="+stateToken, nil)
	request.AddCookie(&http.Cookie{Name: route.BrowserExternalLoginCookieName, Value: binding})
	response := httptest.NewRecorder()
	httpAPI.ServeHTTP(response, request)
	require.Equal(t, http.StatusSeeOther, response.Code, "callback redirect = %d %#v %s", response.Code, response.Header(), response.Body.String())
	require.Equal(t, "/exams", response.Header().Get("Location"), "callback redirect = %d %#v %s", response.Code, response.Header(), response.Body.String())

	// The browser binding consumed the state the provider was sent, and the
	// provider exchanged the callback the browser returned with.
	require.Equal(t, []*store.ExternalLoginStateConsumption{{
		Provider: "oidc", StateHash: model.HashToken(stateToken), BindingHash: model.HashToken(binding),
	}}, states.consumptions)
	require.Len(t, provider.completes, 1)
	require.Equal(t, binding, provider.completes[0].Proof)
	require.Equal(t, stateToken, provider.completes[0].State)
	require.Equal(t, []string{"accepted"}, provider.completes[0].Callback.Values["code"])
	require.Len(t, identities.resolutions, 1)
	require.Equal(t, "oidc", identities.resolutions[0].Identity.Provider)
	require.Equal(t, provider.subject, identities.resolutions[0].Identity.Subject)

	require.Len(t, sessions.saves, 1)
	issued := sessions.saves[0]
	require.Equal(t, user.ID, issued.Session.UserID)
	require.Equal(t, model.SessionClientWeb, issued.Session.ClientType)
	require.Equal(t, "oidc", issued.Session.AuthenticationProviderID)
	require.Equal(t, states.saved()[0].State.StateHash, model.HashToken(stateToken))
	require.True(t, issued.ExternalLoginStateID.IsValid())

	require.Len(t, response.Header().Values("Set-Cookie"), 5)
	require.Equal(t, "no-store", response.Header().Get("Cache-Control"))
}

func TestExternalAuthenticationLoginDefaultsToWebAndNeverCreatesDesktopSessionsDirectly(t *testing.T) {
	t.Parallel()

	logger, _ := routetest.NewLogger(t)
	cookies, err := route.NewCookies("http://localhost:8065")
	require.NoError(t, err)
	states := newSigninLoginStates()
	fixture := signinExternalFixture(t, signinProviderPolicy(t, false, "campus"), states, identityStores{}, newSigninProvider("campus"))
	httpAPI := newFocusedResourceAPI(t, logger, routetest.Authenticator{}, externalAuthenticationResource(fixture.service, cookies))
	request := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/api/v1/auth/providers/campus/login", nil)
	response := httptest.NewRecorder()
	httpAPI.ServeHTTP(response, request)
	require.Equal(t, http.StatusSeeOther, response.Code, "login redirect = %d %s", response.Code, response.Body.String())
	require.Len(t, states.saved(), 1)
	require.Equal(t, model.SessionClientWeb, states.saved()[0].State.ClientType, "default external login client type")

	// A Desktop client signs in through Desktop authorization: a browser
	// login begun for it is refused before any login state exists.
	desktop := httptest.NewRecorder()
	httpAPI.ServeHTTP(desktop, httptest.NewRequestWithContext(t.Context(), http.MethodGet,
		"/api/v1/auth/providers/campus/login?client_type=desktop", nil))
	require.Equal(t, http.StatusBadRequest, desktop.Code, "desktop login = %d %s", desktop.Code, desktop.Body.String())
	require.Contains(t, desktop.Body.String(), `"code":"authentication.external.request.invalid"`)
	require.Empty(t, desktop.Header().Values("Set-Cookie"))
	require.Len(t, states.saved(), 1, "desktop login saved a login state")
}

func TestExternalAuthenticationInvitationClaimUsesStrictPOSTBody(t *testing.T) {
	t.Parallel()
	logger, _ := routetest.NewLogger(t)
	cookies, err := route.NewCookies("http://localhost:8065")
	require.NoError(t, err)
	claim := model.NewCredentialToken()
	states := newSigninLoginStates()
	provider := newSigninProvider("campus")
	fixture := signinExternalFixture(t, signinProviderPolicy(t, true, "campus"), states, identityStores{}, provider)
	httpAPI := newFocusedResourceAPI(t, logger, routetest.Authenticator{}, externalAuthenticationResource(fixture.service, cookies))
	request := newJSONRequest(http.MethodPost, "/api/v1/auth/providers/campus/login",
		strings.NewReader(`{"invitation_claim":"`+claim+`","return_to":"/join"}`))
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	httpAPI.ServeHTTP(response, request)
	require.Equal(t, http.StatusSeeOther, response.Code, "invitation start = %d headers=%#v body=%s", response.Code, response.Header(), response.Body.String())
	require.Len(t, states.admissions, 1, "invitation start saved no Invitation admission")
	admission := states.admissions[0]
	require.Equal(t, model.HashInvitationClaim(claim), admission.ClaimHash)
	require.Equal(t, model.ExternalAuthenticationPurposeInvitationAdmission, admission.State.Purpose)
	require.Equal(t, "/join", admission.State.ReturnTo)
	require.Empty(t, states.saved(), "invitation start also saved a plain login state")
	require.Equal(t, provider.redirectURL(), response.Header().Get("Location"))
	require.NotContains(t, response.Body.String(), claim, "invitation start echoed the claim")
	require.NotContains(t, response.Header().Get("Location"), claim, "invitation start put the claim in the redirect")
	for _, begun := range provider.begun() {
		require.NotContains(t, begun.State+begun.Proof+begun.CallbackURL, claim, "invitation start sent the claim to the provider")
	}

	invalid := httptest.NewRecorder()
	httpAPI.ServeHTTP(invalid, newJSONRequest(http.MethodPost, "/api/v1/auth/providers/campus/login",
		strings.NewReader(`{"invitation_claim":"`+claim+`","unknown":true}`)))
	require.Equal(t, http.StatusBadRequest, invalid.Code, "unknown invitation start field = %d %s", invalid.Code, invalid.Body.String())
	missing := httptest.NewRecorder()
	httpAPI.ServeHTTP(missing, newJSONRequest(http.MethodPost, "/api/v1/auth/providers/campus/login",
		strings.NewReader(`{"return_to":"/join"}`)))
	require.Equal(t, http.StatusBadRequest, missing.Code, "missing invitation claim = %d %s", missing.Code, missing.Body.String())
	require.Len(t, states.admissions, 1, "a refused start reached the Store")
}

func TestAuthenticationResourceRunsPublicAndSessionEntriesThroughKernel(t *testing.T) {
	t.Parallel()

	logger, _ := routetest.NewLogger(t)
	user, passwords := signinStudent(t, "long-private-password")
	now := time.Now()
	principal := model.Principal{
		UserID: user.ID, SessionID: model.NewSessionID(),
		CredentialID: model.PrincipalCredentialID(model.NewID()), CredentialType: model.CredentialSessionAccess,
		AuthenticationMethod: "password", AuthenticationStrength: model.AuthenticationSingleFactor,
		ClientType: model.SessionClientCLI, AuthenticatedAt: now,
	}
	sessions := &signinSessionStore{}
	fixture := newIdentityFixture(t, identityOptions{Stores: identityStores{
		Users: newIdentityUsers(user), Passwords: passwords, Sessions: sessions, MFA: &signinMFAStore{},
	}})
	cookies, err := route.NewCookies("http://localhost:8065")
	require.NoError(t, err)
	httpAPI := newFocusedResourceAPI(
		t, logger, routetest.Authenticator{Principal: principal},
		authenticationResource(newOnboardingFixture(t).service, fixture.service, cookies),
	)

	loginRequest := newJSONRequest(
		http.MethodPost,
		"/api/v1/auth/login",
		strings.NewReader(`{"login_id":"student@example.edu","password":"long-private-password","client_type":"web"}`),
	)
	loginRequest.Header.Set("Content-Type", "application/json")
	loginResponse := httptest.NewRecorder()
	httpAPI.ServeHTTP(loginResponse, loginRequest)
	require.Equal(t, http.StatusOK, loginResponse.Code, "login status = %d: %s", loginResponse.Code, loginResponse.Body.String())
	require.Len(t, sessions.saves, 1)
	issued := sessions.saves[0]
	require.Equal(t, user.ID, issued.Session.UserID, "the login ID did not resolve to the student")
	require.Equal(t, model.SessionClientWeb, issued.Session.ClientType)
	require.Equal(t, "password", issued.Session.AuthenticationMethod)
	credential := passwords.byUser[user.ID]
	require.Equal(t, store.PasswordCredentialProof{ID: credential.ID, Revision: credential.Revision}, issued.PasswordProof)
	attempts := fixture.access.Audits.Attempts()
	require.Len(t, attempts, 1)
	require.Equal(t, attempts[0].ID, issued.AuditEventID, "the Session does not complete the login audit")
	require.Equal(t, "no-store", loginResponse.Header().Get("Cache-Control"))
	require.Len(t, loginResponse.Header().Values("Set-Cookie"), 4)
	var loginBody authenticationResponse
	require.NoError(t, json.Unmarshal(loginResponse.Body.Bytes(), &loginBody))
	require.Nil(t, loginBody.Tokens)
	require.NotNil(t, loginBody.Session)

	logoutRequest := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/api/v1/auth/logout", nil)
	logoutRequest.Header.Set("Authorization", "Bearer access")
	logoutResponse := httptest.NewRecorder()
	httpAPI.ServeHTTP(logoutResponse, logoutRequest)
	require.Equal(t, http.StatusNoContent, logoutResponse.Code, "logout status/revocations = %d/%d: %s", logoutResponse.Code, len(sessions.revocations), logoutResponse.Body.String())
	require.Len(t, sessions.revocations, 1, "logout status/revocations = %d/%d: %s", logoutResponse.Code, len(sessions.revocations), logoutResponse.Body.String())
	revocation := sessions.revocations[0]
	require.Equal(t, principal.SessionID, revocation.SessionID)
	require.Equal(t, principal.UserID, revocation.UserID)
	require.Equal(t, model.SessionRevocationUserLogout, revocation.Reason)
	attempts = fixture.access.Audits.Attempts()
	require.Len(t, attempts, 2)
	require.Equal(t, attempts[1].ID, revocation.AuditEventID, "the revocation does not complete the logout audit")
	require.Equal(t, string(model.ActionSessionManage), attempts[1].Action)
	calls := fixture.publisher.Calls()
	require.Len(t, calls, 1)
	require.Equal(t, realtimetest.SessionsRevoked, calls[0].Method)
	require.Equal(t, []model.SessionID{principal.SessionID}, calls[0].SessionIDs)
}

func TestExternalAuthenticationProviderListFailsClosedWhenPolicyIsUnavailable(t *testing.T) {
	t.Parallel()

	logger, _ := routetest.NewLogger(t)
	cookies, err := route.NewCookies("http://localhost:8065")
	require.NoError(t, err)
	policy := signinProviderPolicy(t, false, "configured-but-unknown")
	fixture := signinExternalFixture(t, policy, newSigninLoginStates(), identityStores{}, newSigninProvider("configured-but-unknown"))
	policy.mu.Lock()
	policy.unavailable = true
	policy.mu.Unlock()
	httpAPI := newFocusedResourceAPI(t, logger, routetest.Authenticator{}, externalAuthenticationResource(fixture.service, cookies))
	response := httptest.NewRecorder()
	httpAPI.ServeHTTP(response, httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/api/v1/auth/providers", nil))
	require.Equal(t, http.StatusInternalServerError, response.Code, "provider-list failure")
	require.Contains(t, response.Body.String(), `"code":"authentication.internal"`, "provider-list failure")
	require.NotContains(t, response.Body.String(), "configured-but-unknown", "provider-list failure")
}

func TestAuthenticationResourceKernelRejectsMissingCredentialAndInvalidJSON(t *testing.T) {
	t.Parallel()

	logger, _ := routetest.NewLogger(t)
	cookies, err := route.NewCookies("http://localhost:8065")
	require.NoError(t, err)
	user, passwords := signinStudent(t, "long-private-password")
	mfa := &signinMFAStore{credential: &model.MFACredential{
		ID: model.NewMFACredentialID(), UserID: user.ID, State: model.MFAStateActive,
	}}
	sessions := &signinSessionStore{}
	fixture := newIdentityFixture(t, identityOptions{MFA: true, Stores: identityStores{
		Users: newIdentityUsers(user), Passwords: passwords, MFA: mfa, Sessions: sessions,
	}})
	httpAPI := newFocusedResourceAPI(
		t, logger, routetest.Authenticator{},
		authenticationResource(newOnboardingFixture(t).service, fixture.service, cookies),
	)

	refresh := httptest.NewRecorder()
	httpAPI.ServeHTTP(refresh, httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/api/v1/auth/refresh", nil))
	require.Equal(t, http.StatusUnauthorized, refresh.Code, "missing refresh credential")
	require.Contains(t, refresh.Body.String(), `"code":"authentication.required"`, "missing refresh credential")

	invalid := httptest.NewRecorder()
	httpAPI.ServeHTTP(
		invalid,
		newJSONRequest(http.MethodPost, "/api/v1/auth/login", strings.NewReader(`{"login_id":"student","unknown":true}`)),
	)
	require.Equal(t, http.StatusBadRequest, invalid.Code, "invalid login body")
	require.Contains(t, invalid.Body.String(), `"code":"request.invalid"`, "invalid login body")

	// A well-formed recovery code, so the factor reaches the Store.
	recoveryCode := "abcd-efgh-ijkl-mnop-qrst-uvwx"

	// The MFA Store's generation conflict maps to a code the login route does
	// not declare, so the boundary conceals it.
	mfa.consumeErr = store.ErrAuthenticationGenerationChanged
	undeclared := httptest.NewRecorder()
	httpAPI.ServeHTTP(
		undeclared,
		newJSONRequest(
			http.MethodPost,
			"/api/v1/auth/login",
			strings.NewReader(`{"login_id":"student","password":"long-private-password","client_type":"web","mfa_code":"`+recoveryCode+`"}`),
		),
	)
	require.Equal(t, http.StatusInternalServerError, undeclared.Code, "undeclared login error")
	require.Contains(t, undeclared.Body.String(), `"code":"server.internal"`, "undeclared login error")
	require.NotContains(t, undeclared.Body.String(), "authentication.mfa.conflict", "undeclared login error")

	// An already-used recovery code is no longer in the Store.
	mfa.consumeErr = store.NewErrNotFound("mfa_recovery_code", "")
	invalidSecondFactor := httptest.NewRecorder()
	httpAPI.ServeHTTP(
		invalidSecondFactor,
		newJSONRequest(
			http.MethodPost,
			"/api/v1/auth/login",
			strings.NewReader(`{"login_id":"student","password":"long-private-password","client_type":"web","mfa_code":"`+recoveryCode+`"}`),
		),
	)
	require.Equal(t, http.StatusUnauthorized, invalidSecondFactor.Code, "invalid login second factor")
	require.Contains(t, invalidSecondFactor.Body.String(), `"code":"authentication.mfa.code_invalid"`, "invalid login second factor")

	require.Len(t, mfa.consumptions, 2)
	for _, consumption := range mfa.consumptions {
		require.Equal(t, user.ID, consumption.UserID)
		require.Equal(t, model.HashToken(strings.ReplaceAll(recoveryCode, "-", "")), consumption.RecoveryCodeHash)
	}
	require.Empty(t, sessions.saves, "a refused second factor issued a Session")
	events := fixture.access.Audits.Events()
	require.NotEmpty(t, events)
	require.Equal(t, "authentication.mfa.code_invalid", events[len(events)-1].ErrorCode, "the refused second factor was not audited")
}

func TestPublicRegistrationUsesStrictBodyAndReturnsNoAccountProjection(t *testing.T) {
	t.Parallel()

	logger, _ := routetest.NewLogger(t)
	registration := newOnboardingFixture(t)
	fixture := newIdentityFixture(t, identityOptions{})
	httpAPI := newFocusedResourceAPI(t, logger, routetest.Authenticator{}, authenticationResource(registration.service, fixture.service, route.Cookies{}))

	request := newJSONRequest(http.MethodPost, "/api/v1/auth/register",
		strings.NewReader(`{"username":"student","email":"student@example.edu","first_name":"New","last_name":"Student","password":"long-private-password"}`))
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	httpAPI.ServeHTTP(response, request)
	require.Equal(t, http.StatusAccepted, response.Code, "registration response = %d headers=%#v body=%s", response.Code, response.Header(), response.Body.String())
	require.Equal(t, 0, response.Body.Len(), "registration response = %d headers=%#v body=%s", response.Code, response.Header(), response.Body.String())
	require.Equal(t, "no-store", response.Header().Get("Cache-Control"), "registration response = %d headers=%#v body=%s", response.Code, response.Header(), response.Body.String())
	registered := registration.users.registered()
	require.Len(t, registered, 1, "the registration reached the Store")
	account := registered[0].User
	require.Equal(t, "student", account.Username)
	require.Equal(t, "student@example.edu", account.Email)
	require.Equal(t, "New", account.FirstName)
	require.Equal(t, "Student", account.LastName)
	hasher, err := identity.NewPasswordHasher(identity.PasswordHasherDependencies{Settings: identityPasswordPolicy()})
	require.NoError(t, err)
	require.NoError(t, hasher.Verify(t.Context(), registered[0].PasswordCredential.PasswordHash, "long-private-password"),
		"the stored credential verifies the submitted password")
	require.Equal(t, "student@example.edu", registered[0].VerificationToken.Target, "the mailbox is sent a verification")
	require.True(t, registration.sourceCounted(t, identity.AttemptPurposePublicRegistration, "register", "192.0.2.1"),
		"the registration attempt was counted against the request's source")

	invalid := httptest.NewRecorder()
	httpAPI.ServeHTTP(invalid, newJSONRequest(http.MethodPost, "/api/v1/auth/register",
		strings.NewReader(`{"username":"student","email":"student@example.edu","first_name":"New","last_name":"Student","password":"private","display_name":"New Student"}`)))
	require.Equal(t, http.StatusBadRequest, invalid.Code, "display-name registration = %d body=%s", invalid.Code, invalid.Body.String())
	require.Len(t, registration.users.registered(), 1, "a display-name registration reached the Store")

	// The Access Policy no longer admits public registration.
	registration.accessPolicy.policy.PublicRegistrationEnabled = false
	disabled := httptest.NewRecorder()
	httpAPI.ServeHTTP(disabled, newJSONRequest(http.MethodPost, "/api/v1/auth/register",
		strings.NewReader(`{"username":"student","email":"student@example.edu","first_name":"New","last_name":"Student","password":"private"}`)))
	require.Equal(t, http.StatusForbidden, disabled.Code, "disabled registration")
	require.Contains(t, disabled.Body.String(), `"code":"authentication.registration.invitation_required"`, "disabled registration")
	require.NotContains(t, disabled.Body.String(), "student@example.edu", "disabled registration")
	require.Len(t, registration.users.registered(), 1, "a disabled registration reached the Store")
}

func TestExternalAuthenticationRedirectIsNamedKernelProtocolOperation(t *testing.T) {
	t.Parallel()

	logger, _ := routetest.NewLogger(t)
	cookies, err := route.NewCookies("http://localhost:8065")
	require.NoError(t, err)
	provider := newSigninProvider("oidc")
	states := newSigninLoginStates()
	fixture := signinExternalFixture(t, signinProviderPolicy(t, false, "oidc"), states, identityStores{}, provider)
	httpAPI := newFocusedResourceAPI(
		t, logger, routetest.Authenticator{},
		externalAuthenticationResource(fixture.service, cookies),
	)

	providers := httptest.NewRecorder()
	httpAPI.ServeHTTP(providers, httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/api/v1/auth/providers", nil))
	require.Equal(t, http.StatusOK, providers.Code, "providers response")
	require.Contains(t, providers.Body.String(), `"id":"oidc"`, "providers response")

	begin := httptest.NewRecorder()
	httpAPI.ServeHTTP(
		begin,
		httptest.NewRequestWithContext(t.Context(),
			http.MethodGet,
			"/api/v1/auth/providers/oidc/login?return_to=%2Fexams&device_id=device-a",
			nil,
		),
	)
	require.Equal(t, http.StatusSeeOther, begin.Code, "external login redirect = %d %#v %s", begin.Code, begin.Header(), begin.Body.String())
	require.Equal(t, provider.redirectURL(), begin.Header().Get("Location"), "external login redirect = %d %#v", begin.Code, begin.Header())
	require.Len(t, states.saved(), 1)
	state := states.saved()[0].State
	require.Equal(t, "oidc", state.Provider)
	require.Equal(t, "/exams", state.ReturnTo)
	require.Equal(t, "device-a", state.DeviceID)
	require.Len(t, provider.begun(), 1)
	require.Equal(t, identityPublicURL+"/api/v1/auth/providers/oidc/callback", provider.begun()[0].CallbackURL)
	require.Equal(t, model.HashToken(provider.begun()[0].State), state.StateHash)
	require.Len(t, begin.Header().Values("Set-Cookie"), 1)
	require.Equal(t, "no-store", begin.Header().Get("Cache-Control"))
}
