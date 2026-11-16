// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package httpapi

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/sudosylabs/proctor/server/app/identity"
	"github.com/sudosylabs/proctor/server/httpapi/route"
	"github.com/sudosylabs/proctor/server/httpapi/route/routetest"
	"github.com/sudosylabs/proctor/server/model"
	"github.com/sudosylabs/proctor/server/store"
)

// desktopAuthorization is one browser transaction a Desktop started, as
// PostgreSQL keeps it.
type desktopAuthorization struct {
	creation    store.DesktopAuthorizationCreation
	state       model.BrowserAuthenticationState
	createdAt   time.Time
	expiresAt   time.Time
	bindingHash string
	userID      model.UserID
	codeHash    string
}

// desktopBrowserAuthentications is the browser-transaction Store of the
// Desktop authorization handoff. Each step finds its transaction by the
// hashes PostgreSQL matches, moves it on, and records its input.
type desktopBrowserAuthentications struct {
	store.BrowserAuthenticationStore
	mu           sync.Mutex
	transactions []*desktopAuthorization
	binds        []store.DesktopAuthorizationBinding
	issues       []store.DesktopAuthorizationCodeIssue
	cancels      []store.DesktopAuthorizationCancellation
	exchanges    []store.DesktopAuthorizationExchange
}

func (s *desktopBrowserAuthentications) CreateDesktopAuthorization(
	_ context.Context,
	creation *store.DesktopAuthorizationCreation,
) (*store.DesktopAuthorizationCreated, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	at := time.Now().UTC().Truncate(time.Millisecond)
	transaction := &desktopAuthorization{
		creation: *creation, state: model.BrowserAuthenticationStatePending,
		createdAt: at, expiresAt: at.Add(creation.Lifetime),
	}
	s.transactions = append(s.transactions, transaction)
	return &store.DesktopAuthorizationCreated{ID: creation.ID, ExpiresAt: transaction.expiresAt}, nil
}

func (s *desktopBrowserAuthentications) BindDesktopAuthorization(
	_ context.Context,
	input *store.DesktopAuthorizationBinding,
) (*store.DesktopAuthorizationBound, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.binds = append(s.binds, *input)
	for _, transaction := range s.transactions {
		if transaction.state == model.BrowserAuthenticationStatePending &&
			transaction.creation.HandleHash == input.HandleHash &&
			transaction.creation.BrowserProofHash == input.BrowserProofHash &&
			transaction.creation.StateHash == input.StateHash {
			transaction.state, transaction.bindingHash = model.BrowserAuthenticationStateBound, input.BindingHash
			return &store.DesktopAuthorizationBound{ExpiresAt: transaction.expiresAt}, nil
		}
	}
	return nil, store.NewErrNotFound("browser_authentication_transaction", "")
}

func (s *desktopBrowserAuthentications) GetDesktopAuthorizationContext(
	_ context.Context,
	bindingHash string,
) (*store.DesktopAuthorizationContext, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	transaction := s.bound(bindingHash)
	if transaction == nil {
		return nil, store.NewErrNotFound("browser_authentication_transaction", "")
	}
	return &store.DesktopAuthorizationContext{
		ID: transaction.creation.ID, State: transaction.state, UserID: transaction.userID,
		DeviceName: transaction.creation.DeviceName, CreatedAt: transaction.createdAt, ExpiresAt: transaction.expiresAt,
	}, nil
}

func (s *desktopBrowserAuthentications) IssueCode(
	_ context.Context,
	input *store.DesktopAuthorizationCodeIssue,
) (*store.DesktopAuthorizationCodeIssued, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.issues = append(s.issues, *input)
	transaction := s.bound(input.BindingHash)
	if transaction == nil ||
		transaction.creation.StateHash != input.StateHash ||
		transaction.state != model.BrowserAuthenticationStateAuthenticated ||
		transaction.userID != input.ExpectedUserID {
		return nil, store.NewErrNotFound("browser_authentication_transaction", "")
	}
	transaction.state, transaction.codeHash = model.BrowserAuthenticationStateCodeIssued, input.CodeHash
	return &store.DesktopAuthorizationCodeIssued{
		CallbackURL: transaction.creation.CallbackURL, CodeExpiresAt: input.AuditAt.Add(input.CodeLifetime),
	}, nil
}

func (s *desktopBrowserAuthentications) Cancel(_ context.Context, input *store.DesktopAuthorizationCancellation) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.cancels = append(s.cancels, *input)
	transaction := s.bound(input.BindingHash)
	if transaction == nil || transaction.creation.StateHash != input.StateHash {
		return store.NewErrNotFound("browser_authentication_transaction", "")
	}
	transaction.state = model.BrowserAuthenticationStateCanceled
	return nil
}

func (s *desktopBrowserAuthentications) ResolveDesktopAuthorizationExchange(
	_ context.Context,
	proof *store.DesktopAuthorizationExchangeProof,
) (*store.DesktopAuthorizationExchangeTarget, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	transaction := s.redeemable(proof.CodeHash, proof.StateHash, proof.CodeChallenge, proof.ExpectedKeyThumbprint)
	if transaction == nil {
		return nil, store.NewErrNotFound("browser_authentication_transaction", "")
	}
	return &store.DesktopAuthorizationExchangeTarget{ID: transaction.creation.ID, UserID: transaction.userID}, nil
}

func (s *desktopBrowserAuthentications) Exchange(
	_ context.Context,
	input *store.DesktopAuthorizationExchange,
) (*store.DesktopAuthorizationExchangeResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.exchanges = append(s.exchanges, *input)
	transaction := s.redeemable(input.CodeHash, input.StateHash, input.CodeChallenge, input.ExpectedKeyThumbprint)
	if transaction == nil {
		return nil, store.NewErrNotFound("browser_authentication_transaction", "")
	}
	transaction.state = model.BrowserAuthenticationStateExchanged
	at := input.AuditAt
	registration := &model.DesktopRegistration{
		UserID: transaction.userID, InstitutionID: transaction.creation.InstitutionID,
		PublicJWK: input.ExpectedPublicJWK, KeyThumbprint: input.ExpectedKeyThumbprint,
		DisplayName: transaction.creation.DeviceName, DesktopRelease: input.DesktopRelease, DesktopBuildID: input.DesktopBuildID,
		Platform: input.DesktopPlatform, Architecture: input.DesktopArchitecture, RealtimeProtocol: input.DesktopRealtimeProtocol,
	}
	registration.PrepareCreate(model.NewDesktopRegistrationID(), at)
	session := &model.Session{
		UserID: transaction.userID, ClientType: model.SessionClientDesktop,
		DesktopRegistrationID: registration.ID, DPoPKeyThumbprint: input.ExpectedKeyThumbprint,
		DesktopRelease: input.DesktopRelease, DesktopBuildID: input.DesktopBuildID,
		DesktopPlatform: input.DesktopPlatform, DesktopArchitecture: input.DesktopArchitecture,
		DesktopRealtimeProtocol: input.DesktopRealtimeProtocol, DeviceID: transaction.creation.DeviceID,
		DeviceName:           transaction.creation.DeviceName,
		AuthenticationMethod: "password", AuthenticationStrength: model.AuthenticationSingleFactor,
		AuthenticatedAt: at, LastActivityAt: at,
		IdleExpiresAt: at.Add(input.IdleLifetime), ExpiresAt: at.Add(input.AbsoluteLifetime),
	}
	session.PrepareCreate(model.NewSessionID(), at)
	return &store.DesktopAuthorizationExchangeResult{
		Session: session, Registration: registration,
		AccessExpiresAt: at.Add(input.AccessLifetime), RefreshExpiresAt: at.Add(input.RefreshLifetime),
	}, nil
}

// bound is the transaction the browser binding hash names.
func (s *desktopBrowserAuthentications) bound(bindingHash string) *desktopAuthorization {
	for _, transaction := range s.transactions {
		if transaction.bindingHash != "" && transaction.bindingHash == bindingHash {
			return transaction
		}
	}
	return nil
}

// redeemable is the transaction whose issued code, state, PKCE challenge and
// proposed key all match.
func (s *desktopBrowserAuthentications) redeemable(codeHash, stateHash, challenge, thumbprint string) *desktopAuthorization {
	for _, transaction := range s.transactions {
		if transaction.state == model.BrowserAuthenticationStateCodeIssued &&
			transaction.codeHash == codeHash &&
			transaction.creation.StateHash == stateHash &&
			transaction.creation.CodeChallenge == challenge &&
			transaction.creation.ProposedKeyThumbprint == thumbprint {
			return transaction
		}
	}
	return nil
}

// authenticate binds the started transaction to a fresh browser binding and
// records it as authenticated as userID, as the hosted page's sign-in does.
// It returns the raw binding the browser holds.
func (s *desktopBrowserAuthentications) authenticate(t *testing.T, index int, userID model.UserID) string {
	t.Helper()
	s.mu.Lock()
	defer s.mu.Unlock()
	require.Less(t, index, len(s.transactions), "no such Desktop authorization")
	binding := model.NewCredentialToken()
	transaction := s.transactions[index]
	transaction.state, transaction.bindingHash, transaction.userID = model.BrowserAuthenticationStateAuthenticated, model.HashToken(binding), userID
	return binding
}

func (s *desktopBrowserAuthentications) created() []store.DesktopAuthorizationCreation {
	s.mu.Lock()
	defer s.mu.Unlock()
	creations := make([]store.DesktopAuthorizationCreation, 0, len(s.transactions))
	for _, transaction := range s.transactions {
		creations = append(creations, transaction.creation)
	}
	return creations
}

// desktopKey is a Desktop's ES256 DPoP key.
type desktopKey struct {
	private *ecdsa.PrivateKey
	public  model.DesktopPublicJWK
}

func newDesktopKey(t *testing.T) desktopKey {
	t.Helper()
	private, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)
	return desktopKey{private: private, public: model.DesktopPublicJWK{
		Kty: "EC", Crv: "P-256",
		X: base64.RawURLEncoding.EncodeToString(private.X.FillBytes(make([]byte, 32))),
		Y: base64.RawURLEncoding.EncodeToString(private.Y.FillBytes(make([]byte, 32))),
	}}
}

// proof is a DPoP proof for one request to target under the server nonce.
func (k desktopKey) proof(t *testing.T, method, target, nonce string) string {
	t.Helper()
	header, err := json.Marshal(map[string]any{"typ": "dpop+jwt", "alg": "ES256", "jwk": k.public})
	require.NoError(t, err)
	payload, err := json.Marshal(map[string]any{
		"jti": model.NewCredentialToken(), "htm": method, "htu": target, "iat": time.Now().Unix(), "nonce": nonce,
	})
	require.NoError(t, err)
	signed := base64.RawURLEncoding.EncodeToString(header) + "." + base64.RawURLEncoding.EncodeToString(payload)
	digest := sha256.Sum256([]byte(signed))
	r, sig, err := ecdsa.Sign(rand.Reader, k.private, digest[:])
	require.NoError(t, err)
	return signed + "." + base64.RawURLEncoding.EncodeToString(append(r.FillBytes(make([]byte, 32)), sig.FillBytes(make([]byte, 32))...))
}

// desktopClient is one Desktop's public-client request: its loopback
// callback, state, PKCE verifier, DPoP key and catalog build.
type desktopClient struct {
	callback string
	state    string
	verifier string
	key      desktopKey
}

func newDesktopClient(t *testing.T) desktopClient {
	t.Helper()
	return desktopClient{
		callback: "http://127.0.0.1:49152/" + model.NewCredentialToken(),
		state:    model.NewCredentialToken(),
		verifier: model.NewCredentialToken(),
		key:      newDesktopKey(t),
	}
}

func (c desktopClient) build() map[string]any {
	return map[string]any{
		"public_jwk": c.key.public, "desktop_release": "1.4.0", "desktop_build_id": "darwin-42",
		"platform": "darwin", "architecture": "arm64", "realtime_protocol": 2,
	}
}

func (c desktopClient) startBody(t *testing.T) string {
	t.Helper()
	body := c.build()
	body["callback_url"], body["state"], body["code_challenge"] = c.callback, c.state, model.PKCES256Challenge(c.verifier)
	body["device_id"], body["device_name"] = "device", "Exam laptop"
	encoded, err := json.Marshal(body)
	require.NoError(t, err)
	return string(encoded)
}

func (c desktopClient) exchangeBody(t *testing.T, code string) string {
	t.Helper()
	body := c.build()
	body["code"], body["state"], body["code_verifier"] = code, c.state, c.verifier
	encoded, err := json.Marshal(body)
	require.NoError(t, err)
	return string(encoded)
}

// desktopAuthorizationFixture serves the Desktop authorization resource from
// the real service over the browser-transaction Store.
type desktopAuthorizationFixture struct {
	browser *desktopBrowserAuthentications
	fixture *identityFixture
	api     http.Handler
}

func newDesktopAuthorizationFixture(t *testing.T, principal model.Principal, options identityOptions) *desktopAuthorizationFixture {
	t.Helper()
	logger, _ := routetest.NewLogger(t)
	browser := &desktopBrowserAuthentications{}
	options.Stores.BrowserAuthentications = browser
	if options.Stores.DesktopCompatibilityPolicies == nil {
		options.Stores.DesktopCompatibilityPolicies = desktopCompatibilityPolicy(t, nil)
	}
	options.DesktopBuilds = desktopPingBuilds()
	fixture := newIdentityFixture(t, options)
	api := newFocusedResourceAPI(t, logger, routetest.Authenticator{Principal: principal},
		desktopAuthorizationResource(fixture.service, testDesktopAuthorizationCookies(t)))
	return &desktopAuthorizationFixture{browser: browser, fixture: fixture, api: api}
}

func (f *desktopAuthorizationFixture) post(t *testing.T, path, body string, edit func(*http.Request)) *httptest.ResponseRecorder {
	t.Helper()
	request := httptest.NewRequestWithContext(t.Context(), http.MethodPost, path, strings.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	if edit != nil {
		edit(request)
	}
	response := httptest.NewRecorder()
	f.api.ServeHTTP(response, request)
	return response
}

func (f *desktopAuthorizationFixture) start(t *testing.T, client desktopClient, edit func(*http.Request)) *httptest.ResponseRecorder {
	t.Helper()
	return f.post(t, "/api/v1/auth/desktop/authorizations", client.startBody(t), edit)
}

func withBinding(name, binding string) func(*http.Request) {
	return func(request *http.Request) { request.AddCookie(&http.Cookie{Name: name, Value: binding}) }
}

func TestDesktopAuthorizationHTTPExposesBoundedAttemptFailures(t *testing.T) {
	t.Parallel()
	desktop := newDesktopAuthorizationFixture(t, model.Principal{}, identityOptions{})
	client := newDesktopClient(t)

	// One state may start twenty authorizations in the window.
	for attempt := range 20 {
		response := desktop.start(t, client, nil)
		require.Equal(t, http.StatusCreated, response.Code, "start %d = %d: %s", attempt, response.Code, response.Body.String())
	}
	response := desktop.start(t, client, nil)
	require.Equal(t, http.StatusTooManyRequests, response.Code, "rate-limited start = %d: %s", response.Code, response.Body.String())
	require.Contains(t, response.Body.String(), `"code":"authentication.rate_limited"`)

	// One source may make a hundred attempts, whatever their state.
	for attempt := range 79 {
		response = desktop.start(t, newDesktopClient(t), nil)
		require.Equal(t, http.StatusCreated, response.Code, "start %d = %d: %s", attempt, response.Code, response.Body.String())
	}
	response = desktop.start(t, newDesktopClient(t), nil)
	require.Equal(t, http.StatusTooManyRequests, response.Code, "source-limited start = %d: %s", response.Code, response.Body.String())
	response = desktop.start(t, newDesktopClient(t), func(request *http.Request) { request.RemoteAddr = "198.51.100.7:1234" })
	require.Equal(t, http.StatusCreated, response.Code, "another source's start = %d: %s", response.Code, response.Body.String())

	require.Len(t, desktop.browser.created(), 100, "a rate-limited start reached the Store")
}

func TestDesktopAuthorizationHTTPMapsPublicClientProtocolAndDisablesCaching(t *testing.T) {
	t.Parallel()
	principal := model.Principal{UserID: model.NewUserID(), SessionID: model.NewSessionID(),
		CredentialID: model.PrincipalCredentialID(model.NewID()), CredentialType: model.CredentialSessionAccess,
		AuthenticationMethod: "password", AuthenticationStrength: model.AuthenticationSingleFactor,
		ClientType: model.SessionClientWeb, AuthenticatedAt: time.Now()}
	desktop := newDesktopAuthorizationFixture(t, principal, identityOptions{})
	client, canceled := newDesktopClient(t), newDesktopClient(t)

	started := desktop.start(t, client, nil)
	require.Equal(t, http.StatusCreated, started.Code, "start = %d: %s", started.Code, started.Body.String())
	require.Equal(t, "no-store", started.Header().Get("Cache-Control"), "start cache")
	nonce := started.Header().Get("DPoP-Nonce")
	require.NotEmpty(t, nonce, "start issued no DPoP nonce")
	require.Equal(t, http.StatusCreated, desktop.start(t, canceled, nil).Code)
	creations := desktop.browser.created()
	require.Len(t, creations, 2)
	creation := creations[0]
	require.Equal(t, client.callback, creation.CallbackURL)
	require.Equal(t, model.HashToken(client.state), creation.StateHash)
	require.Equal(t, model.PKCES256Challenge(client.verifier), creation.CodeChallenge)
	require.Equal(t, "device", creation.DeviceID)
	require.Equal(t, "Exam laptop", creation.DeviceName)
	require.Equal(t, client.key.public, creation.ProposedPublicJWK)
	require.Equal(t, identityPublicURL, creation.Issuer)

	binding := desktop.browser.authenticate(t, 0, principal.UserID)
	approved := desktop.post(t, "/api/v1/auth/desktop/authorizations/approve", `{"state":"`+client.state+`"}`,
		withBinding(route.BrowserDesktopAuthorizationCookieName, binding))
	require.Equal(t, http.StatusOK, approved.Code, "approve = %d: %s", approved.Code, approved.Body.String())
	require.Equal(t, "no-store", approved.Header().Get("Cache-Control"), "approve cache")
	require.Len(t, desktop.browser.issues, 1)
	require.Equal(t, model.HashToken(binding), desktop.browser.issues[0].BindingHash, "the binding cookie did not select the transaction")
	require.Equal(t, model.HashToken(client.state), desktop.browser.issues[0].StateHash)
	var approval desktopAuthorizationApprovalResponse
	require.NoError(t, json.Unmarshal(approved.Body.Bytes(), &approval))
	redirect, err := url.Parse(approval.RedirectURL)
	require.NoError(t, err)
	require.Equal(t, client.state, redirect.Query().Get("state"))
	code := redirect.Query().Get("code")
	require.Equal(t, desktop.browser.issues[0].CodeHash, model.HashToken(code))

	canceledBinding := desktop.browser.authenticate(t, 1, principal.UserID)
	cancel := desktop.post(t, "/api/v1/auth/desktop/authorizations/cancel", `{"state":"`+canceled.state+`"}`,
		withBinding(route.BrowserDesktopAuthorizationCookieName, canceledBinding))
	require.Equal(t, http.StatusNoContent, cancel.Code, "cancel = %d: %s", cancel.Code, cancel.Body.String())
	require.Equal(t, "no-store", cancel.Header().Get("Cache-Control"), "cancel cache")
	require.Equal(t, []store.DesktopAuthorizationCancellation{{
		BindingHash: model.HashToken(canceledBinding), StateHash: model.HashToken(canceled.state),
	}}, desktop.browser.cancels)

	exchanged := desktop.post(t, "/api/v1/auth/desktop/token", client.exchangeBody(t, code), func(request *http.Request) {
		request.Header.Set("DPoP", client.key.proof(t, http.MethodPost, identityPublicURL+"/api/v1/auth/desktop/token", nonce))
	})
	require.Equal(t, http.StatusOK, exchanged.Code, "exchange = %d: %s", exchanged.Code, exchanged.Body.String())
	require.Equal(t, "no-store", exchanged.Header().Get("Cache-Control"), "exchange cache")
	require.NotEmpty(t, exchanged.Header().Get("DPoP-Nonce"), "exchange issued no Session nonce")
	require.Len(t, desktop.browser.exchanges, 1)
	exchange := desktop.browser.exchanges[0]
	require.Equal(t, model.HashToken(code), exchange.CodeHash)
	require.Equal(t, model.PKCES256Challenge(client.verifier), exchange.CodeChallenge, "the code verifier did not reach the exchange")
	require.Equal(t, client.key.public, exchange.ExpectedPublicJWK)
	require.Equal(t, "darwin-42", exchange.DesktopBuildID)
	require.Contains(t, exchanged.Body.String(), `"token_type":"DPoP"`)

	// The request's source is the network identity of both audited steps,
	// as it is the key of their rate limits.
	sources := map[string]string{}
	for _, event := range desktop.fixture.access.Audits.Events() {
		sources[event.Action] = event.IPAddress
	}
	require.Equal(t, "192.0.2.1", sources[string(model.ActionAuthenticationDesktopAuthorization)], "approval audit = %#v", sources)
	require.Equal(t, "192.0.2.1", sources[string(model.ActionAuthenticationDesktopSessionIssue)], "exchange audit = %#v", sources)
}

// desktopProvider is a configured external identity provider whose sign-in
// begins at a fixed page.
type desktopProvider struct {
	mu     sync.Mutex
	begins []identity.ExternalProviderBeginRequest
}

func (p *desktopProvider) Descriptor() model.ExternalAuthenticationProvider {
	return model.ExternalAuthenticationProvider{ID: "provider", DisplayName: "Campus", Type: "oidc"}
}

func (p *desktopProvider) AutoProvision() bool { return false }

func (p *desktopProvider) Begin(_ context.Context, request identity.ExternalProviderBeginRequest) (*identity.ExternalProviderBeginResponse, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.begins = append(p.begins, request)
	return &identity.ExternalProviderBeginResponse{RedirectURL: "https://identity.example/login"}, nil
}

func (p *desktopProvider) State(model.ExternalAuthenticationCallback) (string, error) {
	return "", errors.New("no provider callback is expected")
}

func (p *desktopProvider) Complete(context.Context, identity.ExternalProviderCompleteRequest) (*model.ExternalAuthenticationAssertion, error) {
	return nil, errors.New("no provider callback is expected")
}

// desktopExternalLoginStates saves each external login state it is given.
type desktopExternalLoginStates struct {
	store.ExternalLoginStateStore
	mu    sync.Mutex
	saved []*model.ExternalLoginState
}

func (s *desktopExternalLoginStates) Save(_ context.Context, creation *store.ExternalLoginStateCreation) (*model.ExternalLoginState, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	saved := *creation.State
	at := time.Now().UTC().Truncate(time.Millisecond)
	saved.ID, saved.CreatedAt, saved.ExpiresAt = model.NewExternalLoginStateID(), at, at.Add(creation.Lifetime)
	s.saved = append(s.saved, &saved)
	return &saved, nil
}

func TestDesktopAuthorizationHTTPExchangesFragmentProofForScopedCookies(t *testing.T) {
	t.Parallel()
	provider := &desktopProvider{}
	logins := &desktopExternalLoginStates{}
	desktop := newDesktopAuthorizationFixture(t, model.Principal{}, identityOptions{
		Stores: identityStores{
			ExternalLoginStates: logins,
			AccessPolicy: newIdentityAccessPolicy(t, func(policy *model.AccessPolicy) {
				policy.ProviderAdmissions = map[string]model.ProviderAdmissionMode{"provider": model.ProviderAdmissionLinkedOnly}
			}),
		},
		Providers: []identity.ExternalIdentityProvider{provider},
	})
	client := newDesktopClient(t)
	started := desktop.start(t, client, nil)
	require.Equal(t, http.StatusCreated, started.Code, "start = %d: %s", started.Code, started.Body.String())
	var start desktopAuthorizationStartResponse
	require.NoError(t, json.Unmarshal(started.Body.Bytes(), &start))
	hosted, err := url.Parse(start.AuthorizationURL)
	require.NoError(t, err)
	handle, proof := hosted.Query().Get("request"), strings.TrimPrefix(hosted.Fragment, "proof=")

	bindResponse := desktop.post(t, "/api/v1/auth/desktop/authorizations/bind",
		fmt.Sprintf(`{"handle":%q,"browser_proof":%q,"state":%q}`, handle, proof, client.state), nil)
	require.Equal(t, http.StatusNoContent, bindResponse.Code, "bind = %d cache=%q body=%s", bindResponse.Code, bindResponse.Header().Get("Cache-Control"), bindResponse.Body.String())
	require.Equal(t, "no-store", bindResponse.Header().Get("Cache-Control"), "bind = %d cache=%q body=%s", bindResponse.Code, bindResponse.Header().Get("Cache-Control"), bindResponse.Body.String())
	bindingCookie := responseCookie(t, bindResponse.Result().Cookies(), route.BrowserDesktopAuthorizationCookieName)
	require.Equal(t, []store.DesktopAuthorizationBinding{{
		HandleHash: model.HashToken(handle), BrowserProofHash: model.HashToken(proof),
		StateHash: model.HashToken(client.state), BindingHash: model.HashToken(bindingCookie.Value),
	}}, desktop.browser.binds, "the cookie is not the binding the Store recorded")
	require.Equal(t, "/api/v1/auth/desktop/authorizations", bindingCookie.Path)
	require.True(t, bindingCookie.HttpOnly)
	require.True(t, bindingCookie.Secure)
	require.Equal(t, http.SameSiteLaxMode, bindingCookie.SameSite)

	providerRequest := httptest.NewRequestWithContext(t.Context(), http.MethodGet,
		"/api/v1/auth/desktop/authorizations/authenticate/providers/provider/login?state="+url.QueryEscape(client.state), nil)
	providerRequest.AddCookie(bindingCookie)
	providerResponse := httptest.NewRecorder()
	desktop.api.ServeHTTP(providerResponse, providerRequest)
	require.Equal(t, http.StatusSeeOther, providerResponse.Code, "provider start = %d location=%q cache=%q body=%s", providerResponse.Code, providerResponse.Header().Get("Location"), providerResponse.Header().Get("Cache-Control"), providerResponse.Body.String())
	require.Equal(t, "https://identity.example/login", providerResponse.Header().Get("Location"), "provider start = %d location=%q cache=%q body=%s", providerResponse.Code, providerResponse.Header().Get("Location"), providerResponse.Header().Get("Cache-Control"), providerResponse.Body.String())
	require.Equal(t, "no-store", providerResponse.Header().Get("Cache-Control"), "provider start = %d location=%q cache=%q body=%s", providerResponse.Code, providerResponse.Header().Get("Location"), providerResponse.Header().Get("Cache-Control"), providerResponse.Body.String())
	externalCookie := responseCookie(t, providerResponse.Result().Cookies(), route.BrowserExternalLoginCookieName)
	require.Len(t, logins.saved, 1)
	saved := logins.saved[0]
	require.Equal(t, model.HashToken(externalCookie.Value), saved.BindingHash, "the cookie is not the binding of the saved login state")
	require.Equal(t, desktop.browser.created()[0].ID, saved.BrowserAuthenticationTransactionID, "the login state is not bound to the Desktop authorization")
	require.Equal(t, model.ExternalAuthenticationPurposeDesktopAuthorization, saved.Purpose)
	require.Equal(t, "/authorize/desktop?state="+url.QueryEscape(client.state), saved.ReturnTo)
	require.Len(t, provider.begins, 1)
	require.Equal(t, externalCookie.Value, provider.begins[0].Proof)
	require.Equal(t, "/api/v1/auth/providers/", externalCookie.Path)
	require.True(t, externalCookie.HttpOnly)
	require.True(t, externalCookie.Secure)
	require.Equal(t, http.SameSiteLaxMode, externalCookie.SameSite)
}

func responseCookie(t *testing.T, cookies []*http.Cookie, name string) *http.Cookie {
	t.Helper()
	for _, cookie := range cookies {
		if cookie.Name == name {
			return cookie
		}
	}
	require.FailNow(t, fmt.Sprintf("response cookie %q was not set", name))
	return nil
}

func testDesktopAuthorizationCookies(t *testing.T) route.Cookies {
	t.Helper()
	cookies, err := route.NewCookies("https://proctor.example.edu")
	require.NoError(t, err)
	return cookies
}

func TestDesktopAuthorizationContextResponseNamesAccountEmailAndRequestTime(t *testing.T) {
	t.Parallel()
	requestedAt := time.Date(2026, 8, 18, 11, 58, 0, 0, time.UTC)
	userID := model.NewUserID()
	response := desktopAuthorizationContextResponseFromApplication(&identity.DesktopAuthorizationBrowserContext{
		State: model.BrowserAuthenticationStateAuthenticated, DeviceName: "Lab Mac",
		Account:     &identity.DesktopAuthorizationAccount{ID: userID, Username: "ada", DisplayName: "Ada Okafor", Email: "ada@example.edu"},
		RequestedAt: requestedAt.UnixMilli(), ExpiresAt: requestedAt.Add(5 * time.Minute).UnixMilli(),
	})
	require.Equal(t, "2026-08-18T11:58:00Z", response.RequestedAt)
	require.NotNil(t, response.Account)
	require.Equal(t, "ada@example.edu", response.Account.Email)
	require.Equal(t, userID.String(), response.Account.ID)
}
