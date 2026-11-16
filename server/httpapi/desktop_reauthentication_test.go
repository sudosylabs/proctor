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
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/sudosylabs/proctor/server/httpapi/route"
	"github.com/sudosylabs/proctor/server/httpapi/route/routetest"
	"github.com/sudosylabs/proctor/server/model"
	"github.com/sudosylabs/proctor/server/store"
)

// desktopReauthentication is one Desktop Session's reauthentication
// handoff, as PostgreSQL keeps it.
type desktopReauthentication struct {
	creation    store.DesktopReauthenticationCreation
	state       model.BrowserAuthenticationState
	expiresAt   time.Time
	bindingHash string
	proof       *store.DesktopReauthenticationAuthentication
	codeHash    string
}

// desktopReauthentications is the browser-transaction Store of Desktop
// reauthentication over the one Desktop Session it reauthenticates. Each
// step finds its handoff by the hashes PostgreSQL matches, moves it on and
// records its input.
type desktopReauthentications struct {
	store.BrowserAuthenticationStore
	mu              sync.Mutex
	session         *model.Session
	handoffs        []*desktopReauthentication
	contextReads    int
	authentications []store.DesktopReauthenticationAuthentication
	completions     []store.DesktopReauthenticationCompletion
}

func (s *desktopReauthentications) CreateDesktopReauthentication(
	_ context.Context,
	creation *store.DesktopReauthenticationCreation,
) (*store.DesktopAuthorizationCreated, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if creation.SessionID != s.session.ID || creation.UserID != s.session.UserID ||
		creation.DesktopRegistrationID != s.session.DesktopRegistrationID || creation.KeyThumbprint != s.session.DPoPKeyThumbprint {
		return nil, store.NewErrNotFound("session", creation.SessionID.String())
	}
	handoff := &desktopReauthentication{
		creation: *creation, state: model.BrowserAuthenticationStatePending,
		expiresAt: time.Now().UTC().Truncate(time.Millisecond).Add(creation.Lifetime),
	}
	s.handoffs = append(s.handoffs, handoff)
	return &store.DesktopAuthorizationCreated{ID: creation.ID, ExpiresAt: handoff.expiresAt}, nil
}

func (s *desktopReauthentications) BindDesktopReauthentication(
	_ context.Context,
	input *store.DesktopAuthorizationBinding,
) (*store.DesktopAuthorizationBound, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, handoff := range s.handoffs {
		if handoff.state == model.BrowserAuthenticationStatePending &&
			handoff.creation.HandleHash == input.HandleHash &&
			handoff.creation.BrowserProofHash == input.BrowserProofHash &&
			handoff.creation.StateHash == input.StateHash {
			handoff.state, handoff.bindingHash = model.BrowserAuthenticationStateBound, input.BindingHash
			return &store.DesktopAuthorizationBound{ExpiresAt: handoff.expiresAt}, nil
		}
	}
	return nil, store.NewErrNotFound("browser_authentication_transaction", "")
}

// GetDesktopReauthenticationContext pins the method the Session signed in
// with: the Desktop Session's password.
func (s *desktopReauthentications) GetDesktopReauthenticationContext(
	_ context.Context,
	bindingHash string,
) (*store.DesktopReauthenticationContext, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.contextReads++
	handoff := s.bound(bindingHash)
	if handoff == nil {
		return nil, store.NewErrNotFound("browser_authentication_transaction", "")
	}
	return &store.DesktopReauthenticationContext{
		ID: handoff.creation.ID, State: handoff.state, UserID: handoff.creation.UserID,
		ExpectedAuthenticationMethod: s.session.AuthenticationMethod, ExpiresAt: handoff.expiresAt,
	}, nil
}

func (s *desktopReauthentications) AuthenticateDesktopReauthentication(
	_ context.Context,
	input *store.DesktopReauthenticationAuthentication,
) (*store.DesktopReauthenticationAuthenticationResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.authentications = append(s.authentications, *input)
	handoff := s.bound(input.BindingHash)
	if handoff == nil || handoff.state != model.BrowserAuthenticationStateBound {
		return nil, store.NewErrNotFound("browser_authentication_transaction", "")
	}
	if input.UserID != handoff.creation.UserID {
		handoff.state = model.BrowserAuthenticationStateDenied
		return &store.DesktopReauthenticationAuthenticationResult{Denied: true, UserID: handoff.creation.UserID}, nil
	}
	recorded := *input
	handoff.state, handoff.proof = model.BrowserAuthenticationStateAuthenticated, &recorded
	return &store.DesktopReauthenticationAuthenticationResult{UserID: input.UserID}, nil
}

func (s *desktopReauthentications) IssueDesktopReauthenticationCode(
	_ context.Context,
	input *store.DesktopReauthenticationCodeIssue,
) (*store.DesktopAuthorizationCodeIssued, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	handoff := s.bound(input.BindingHash)
	if handoff == nil || handoff.creation.StateHash != input.StateHash || handoff.state != model.BrowserAuthenticationStateAuthenticated {
		return nil, store.NewErrNotFound("browser_authentication_transaction", "")
	}
	handoff.state, handoff.codeHash = model.BrowserAuthenticationStateCodeIssued, input.CodeHash
	return &store.DesktopAuthorizationCodeIssued{
		CallbackURL: handoff.creation.CallbackURL, CodeExpiresAt: time.Now().Add(input.CodeLifetime),
	}, nil
}

// CompleteDesktopReauthentication consumes the code for the exact Session
// and key that started the handoff and refreshes the Session's primary
// proof to the instant the browser proved it.
func (s *desktopReauthentications) CompleteDesktopReauthentication(
	_ context.Context,
	input *store.DesktopReauthenticationCompletion,
) (*store.SessionReauthenticationResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.completions = append(s.completions, *input)
	for _, handoff := range s.handoffs {
		if handoff.state == model.BrowserAuthenticationStateCodeIssued &&
			handoff.codeHash == input.CodeHash &&
			handoff.creation.StateHash == input.StateHash &&
			handoff.creation.CodeChallenge == input.CodeChallenge &&
			handoff.creation.SessionID == input.SessionID &&
			handoff.creation.CredentialID == input.CredentialID &&
			handoff.creation.DesktopRegistrationID == input.DesktopRegistrationID &&
			handoff.creation.KeyThumbprint == input.KeyThumbprint {
			handoff.state = model.BrowserAuthenticationStateCompleted
			s.session.ReauthenticatedAt = model.OptionalTimeFrom(handoff.proof.AuthenticatedAt)
			session := *s.session
			return &store.SessionReauthenticationResult{Session: &session, AccessTokenHashes: []string{model.HashToken("access")}}, nil
		}
	}
	return nil, store.NewErrNotFound("browser_authentication_transaction", "")
}

func (s *desktopReauthentications) bound(bindingHash string) *desktopReauthentication {
	for _, handoff := range s.handoffs {
		if handoff.bindingHash != "" && handoff.bindingHash == bindingHash {
			return handoff
		}
	}
	return nil
}

func (s *desktopReauthentications) reached() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.contextReads > 0 || len(s.authentications) > 0
}

func TestDesktopReauthenticationHTTPScopesItsBindingAndReturnsRecency(t *testing.T) {
	t.Parallel()
	logger, _ := routetest.NewLogger(t)
	key := newDesktopKey(t)
	thumbprint, err := key.public.Thumbprint()
	require.NoError(t, err)
	signedInAt := time.Now().Add(-time.Hour).UTC().Truncate(time.Millisecond)
	user := &model.User{ID: model.NewUserID(), Username: "ada", Email: "ada@example.edu", DisplayName: "Ada Candidate"}
	session := &model.Session{
		ID: model.NewSessionID(), UserID: user.ID, ClientType: model.SessionClientDesktop,
		DesktopRegistrationID: model.NewDesktopRegistrationID(), DPoPKeyThumbprint: thumbprint,
		AuthenticationMethod: "password", AuthenticationStrength: model.AuthenticationSingleFactor, AuthenticatedAt: signedInAt,
	}
	principal := model.Principal{UserID: user.ID, SessionID: session.ID,
		CredentialID: model.PrincipalCredentialID(model.NewID()), CredentialType: model.CredentialSessionAccess,
		AuthenticationMethod: "password", AuthenticationStrength: model.AuthenticationSingleFactor,
		ClientType: model.SessionClientDesktop, AuthenticatedAt: signedInAt,
		RegisteredDesktopKey: true, DesktopRegistrationID: session.DesktopRegistrationID, DPoPKeyThumbprint: thumbprint,
		DesktopRelease: "1.4.0", DesktopBuildID: "darwin-42", DesktopPlatform: model.DesktopPlatformDarwin,
		DesktopArchitecture: model.DesktopArchitectureARM64, DesktopRealtimeProtocol: 2}
	credential := &model.PasswordCredential{ID: model.NewPasswordCredentialID(), UserID: user.ID, Revision: 3,
		PasswordHash: hashPassword(t, "synthetic-local-password")}
	handoffs := &desktopReauthentications{session: session}
	fixture := newIdentityFixture(t, identityOptions{Stores: identityStores{
		Users:                  newIdentityUsers(user),
		Passwords:              &identityPasswords{byUser: map[model.UserID]*model.PasswordCredential{user.ID: credential}},
		BrowserAuthentications: handoffs,
	}})
	httpAPI := newFocusedResourceAPI(t, logger, routetest.Authenticator{Principal: principal},
		desktopReauthenticationResource(fixture.service, testDesktopAuthorizationCookies(t)))
	serve := func(path, body string, edit func(*http.Request)) *httptest.ResponseRecorder {
		request := httptest.NewRequestWithContext(t.Context(), http.MethodPost, path, strings.NewReader(body))
		request.Header.Set("Content-Type", "application/json")
		if edit != nil {
			edit(request)
		}
		response := httptest.NewRecorder()
		httpAPI.ServeHTTP(response, request)
		return response
	}
	asDesktop := func(request *http.Request) { request.Header.Set("Authorization", "Bearer access") }

	// The Desktop starts the handoff and opens the hosted page it names.
	client := newDesktopClient(t)
	response := serve("/api/v1/auth/desktop/reauthentications", fmt.Sprintf(`{"callback_url":%q,"state":%q,"code_challenge":%q}`,
		client.callback, client.state, model.PKCES256Challenge(client.verifier)), asDesktop)
	require.Equal(t, http.StatusCreated, response.Code, response.Body.String())
	var start desktopAuthorizationStartResponse
	require.NoError(t, json.Unmarshal(response.Body.Bytes(), &start))
	hosted, err := url.Parse(start.AuthorizationURL)
	require.NoError(t, err)
	handle, proof := hosted.Query().Get("request"), strings.TrimPrefix(hosted.Fragment, "proof=")

	response = serve("/api/v1/auth/desktop/reauthentications/bind",
		fmt.Sprintf(`{"handle":%q,"browser_proof":%q,"state":%q}`, handle, proof, client.state), nil)
	require.Equal(t, http.StatusNoContent, response.Code, response.Body.String())
	cookies := response.Result().Cookies()
	require.Len(t, cookies, 1)
	require.Equal(t, route.BrowserDesktopReauthenticationCookieName, cookies[0].Name)
	require.Equal(t, "/api/v1/auth/desktop/reauthentications", cookies[0].Path, "binding escaped its route family")
	require.True(t, cookies[0].HttpOnly, "binding is readable by page script")
	require.Equal(t, "no-store", response.Header().Get("Cache-Control"))
	binding := cookies[0].Value
	require.Equal(t, model.HashToken(binding), handoffs.handoffs[0].bindingHash, "the cookie is not the binding the Store recorded")

	response = serve("/api/v1/auth/desktop/reauthentications/authenticate/password", `{"password":"synthetic-local-password"}`, nil)
	require.Equal(t, http.StatusBadRequest, response.Code, "password proof without the binding cookie: %s", response.Body.String())
	require.False(t, handoffs.reached(), "a request without the binding reached the handoff")

	response = serve("/api/v1/auth/desktop/reauthentications/authenticate/password", `{"password":"synthetic-local-password"}`,
		withBinding(route.BrowserDesktopReauthenticationCookieName, binding))
	require.Equal(t, http.StatusOK, response.Code, response.Body.String())
	require.Len(t, handoffs.authentications, 1)
	authentication := handoffs.authentications[0]
	require.Equal(t, model.HashToken(binding), authentication.BindingHash)
	require.Equal(t, user.ID, authentication.UserID)
	require.Equal(t, "password", authentication.AuthenticationMethod)
	require.Equal(t, store.PasswordCredentialProof{ID: credential.ID, Revision: credential.Revision}, authentication.PasswordProof)
	require.Contains(t, response.Body.String(), `"authentication_method":"password"`)
	require.Contains(t, response.Body.String(), `"display_name":"Ada Candidate"`)
	require.NotContains(t, response.Body.String(), `"provider"`, "a password reauthentication offered a provider")

	response = serve("/api/v1/auth/desktop/reauthentications/approve", fmt.Sprintf(`{"state":%q}`, client.state),
		withBinding(route.BrowserDesktopReauthenticationCookieName, binding))
	require.Equal(t, http.StatusOK, response.Code, response.Body.String())
	var approval desktopAuthorizationApprovalResponse
	require.NoError(t, json.Unmarshal(response.Body.Bytes(), &approval))
	redirect, err := url.Parse(approval.RedirectURL)
	require.NoError(t, err)
	code := redirect.Query().Get("code")

	response = serve("/api/v1/auth/desktop/reauthentications/complete",
		fmt.Sprintf(`{"code":%q,"code_verifier":%q,"state":%q}`, code, client.verifier, client.state), asDesktop)
	require.Equal(t, http.StatusOK, response.Code, response.Body.String())
	recentUntil := authentication.AuthenticatedAt.Add(15 * time.Minute)
	require.JSONEq(t, `{"recently_authenticated_until":"`+model.FormatRFC3339(recentUntil)+`"}`, response.Body.String())
	attempts := fixture.access.Audits.Attempts()
	require.Len(t, attempts, 1)
	require.Equal(t, []store.DesktopReauthenticationCompletion{{
		CodeHash: model.HashToken(code), StateHash: model.HashToken(client.state),
		CodeChallenge: model.PKCES256Challenge(client.verifier), UserID: user.ID, SessionID: session.ID,
		CredentialID: model.SessionCredentialID(principal.CredentialID), DesktopRegistrationID: session.DesktopRegistrationID,
		KeyThumbprint: thumbprint, AuditEventID: attempts[0].ID,
	}}, handoffs.completions)
}
