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
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/sudosylabs/proctor/server/app/access/accesstest"
	"github.com/sudosylabs/proctor/server/app/realtime/realtimetest"
	"github.com/sudosylabs/proctor/server/httpapi/route"
	"github.com/sudosylabs/proctor/server/httpapi/route/routetest"
	"github.com/sudosylabs/proctor/server/model"
	"github.com/sudosylabs/proctor/server/store"
)

func TestAccountSecurityResourcesRunThroughFocusedKernel(t *testing.T) {
	t.Parallel()

	logger, _ := routetest.NewLogger(t)
	now := time.Now()
	principal := model.Principal{
		UserID: model.NewUserID(), SessionID: model.NewSessionID(),
		CredentialID:         model.PrincipalCredentialID(model.NewID()),
		CredentialType:       model.CredentialSessionAccess,
		AuthenticationMethod: "password", AuthenticationStrength: model.AuthenticationMultiFactor,
		AuthenticatedAt: now, MFACompletedAt: model.OptionalTimeFrom(now),
		ClientType: model.SessionClientWeb,
	}
	otherSessionIDs := []model.SessionID{model.NewSessionID(), model.NewSessionID()}
	sessions := newAccountSessions(
		accountSession(principal.UserID, principal.SessionID, now),
		accountSession(principal.UserID, otherSessionIDs[0], now.Add(-time.Minute)),
		accountSession(principal.UserID, otherSessionIDs[1], now.Add(-2*time.Minute)),
	)
	mfa := newAccountMFA(sessions)
	mfa.enable(principal.UserID, now.Add(-time.Hour))
	mfa.summary = &store.MFARecoveryCodeSummary{Remaining: 7, Issued: 10, GeneratedAt: now.Add(-time.Hour)}
	tokens := &accountTokens{}
	fixture := newIdentityFixture(t, identityOptions{
		Stores: identityStores{
			Users:                newIdentityUsers(accountUser(principal.UserID)),
			Passwords:            &identityPasswords{byUser: map[model.UserID]*model.PasswordCredential{}},
			Sessions:             sessions,
			MFA:                  mfa,
			PersonalAccessTokens: tokens,
		},
		Grants: []accesstest.Grant{institutionWide(principal.UserID, model.ActionUserProfilePictureManage)},
		MFA:    true,
	})
	sessions.audits = fixture.access.Audits
	cookies, err := route.NewCookies("http://localhost:8065")
	require.NoError(t, err)
	httpAPI := newFocusedResourceAPI(
		t,
		logger,
		routetest.Authenticator{Principal: principal},
		sessionResource(fixture.service, cookies),
		mfaResource(fixture.service),
		personalAccessTokenResource(fixture.service),
	)

	statusRequest := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/api/v1/users/me/mfa", nil)
	statusRequest.Header.Set("Authorization", "Bearer credential")
	statusResponse := httptest.NewRecorder()
	httpAPI.ServeHTTP(statusResponse, statusRequest)
	require.Equal(t, http.StatusOK, statusResponse.Code, "MFA status = %d, cache = %q: %s", statusResponse.Code, statusResponse.Header().Get("Cache-Control"), statusResponse.Body.String())
	require.Equal(t, "no-store", statusResponse.Header().Get("Cache-Control"), "MFA status = %d, cache = %q: %s", statusResponse.Code, statusResponse.Header().Get("Cache-Control"), statusResponse.Body.String())
	require.Contains(t, statusResponse.Body.String(), `"enabled":true`)
	require.Contains(t, statusResponse.Body.String(), `"recovery_codes_remaining":7`)

	listRequest := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/api/v1/users/me/sessions", nil)
	listRequest.Header.Set("Authorization", "Bearer credential")
	listResponse := httptest.NewRecorder()
	httpAPI.ServeHTTP(listResponse, listRequest)
	require.Equal(t, http.StatusOK, listResponse.Code, "session list: %s", listResponse.Body.String())
	var listed struct {
		Items []struct {
			ID      string `json:"id"`
			Current *bool  `json:"current"`
		} `json:"items"`
	}
	require.NoError(t, json.Unmarshal(listResponse.Body.Bytes(), &listed))
	require.Len(t, listed.Items, 3)
	currentByID := map[string]bool{}
	for _, item := range listed.Items {
		require.NotNil(t, item.Current, "session %s omits current", item.ID)
		currentByID[item.ID] = *item.Current
	}
	require.Equal(t, map[string]bool{
		principal.SessionID.String(): true, otherSessionIDs[0].String(): false, otherSessionIDs[1].String(): false,
	}, currentByID)

	revokeOthersRequest := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/api/v1/users/me/sessions/revoke-others", nil)
	revokeOthersRequest.Header.Set("Authorization", "Bearer credential")
	revokeOthersResponse := httptest.NewRecorder()
	httpAPI.ServeHTTP(revokeOthersResponse, revokeOthersRequest)
	require.Equal(t, http.StatusOK, revokeOthersResponse.Code, "revoke others: %s", revokeOthersResponse.Body.String())
	require.JSONEq(t, `{"revoked_session_count":2}`, revokeOthersResponse.Body.String())
	require.Empty(t, revokeOthersResponse.Header().Values("Set-Cookie"), "revoke others must leave cookies in place")
	revocations := sessions.revocationInputs()
	require.Len(t, revocations, 1)
	require.Equal(t, principal.UserID, revocations[0].UserID)
	require.Equal(t, principal.SessionID, revocations[0].KeepSessionID, "the requesting Session stays signed in")
	require.Equal(t, model.SessionRevocationUserOtherSessions, revocations[0].Reason)
	require.True(t, revocations[0].AuditEventID.IsValid(), "the revocation commits with its audit")
	require.Equal(t, []string{"session.manage:success"}, accountAuditOutcomes(fixture))
	require.Equal(t, []realtimetest.Call{{
		Method: realtimetest.SessionsRevoked, UserID: principal.UserID, SessionIDs: otherSessionIDs,
	}}, fixture.publisher.Calls())

	expiresAt := now.UTC().Add(30 * 24 * time.Hour).Truncate(time.Second).Add(123 * time.Millisecond)
	createBody := `{"description":"editor","scopes":["user.profile_picture.manage"],"expires_at":"` + expiresAt.Format("2006-01-02T15:04:05.000Z07:00") + `"}`
	createRequest := httptest.NewRequestWithContext(t.Context(),
		http.MethodPost,
		"/api/v1/users/me/tokens",
		strings.NewReader(createBody),
	)
	createRequest.Header.Set("Authorization", "Bearer credential")
	createRequest.Header.Set("Content-Type", "application/json")
	createResponse := httptest.NewRecorder()
	httpAPI.ServeHTTP(createResponse, createRequest)
	require.Equal(t, http.StatusCreated, createResponse.Code, "token create = %d, cache = %q: %s", createResponse.Code, createResponse.Header().Get("Cache-Control"), createResponse.Body.String())
	require.Equal(t, "no-store", createResponse.Header().Get("Cache-Control"), "token create = %d, cache = %q: %s", createResponse.Code, createResponse.Header().Get("Cache-Control"), createResponse.Body.String())
	created := tokens.createdTokens()
	require.Len(t, created, 1)
	require.Equal(t, "editor", created[0].Description)
	require.Equal(t, []string{"user.profile_picture.manage"}, created[0].Scopes)
	require.Equal(t, expiresAt.UnixMilli(), created[0].ExpiresAt.UnixMilli(), "expires_at keeps millisecond precision")
	require.Equal(t, []model.Action{model.ActionPersonalAccessTokenCreate}, tokens.preparedActions())
	require.Empty(t, tokens.failures, "the prepared creation committed")
	var createdBody personalAccessTokenCreationResponse
	require.NoError(t, json.Unmarshal(createResponse.Body.Bytes(), &createdBody))
	require.NotEmpty(t, createdBody.Credential, "token create returns its credential once: %s", createResponse.Body.String())
	require.Equal(t, model.HashToken(createdBody.Credential), created[0].TokenHash, "the returned credential is the one stored hashed")
	require.Equal(t, created[0].ID.String(), createdBody.Token.ID)

	invalidRequest := httptest.NewRequestWithContext(t.Context(),
		http.MethodPost,
		"/api/v1/users/me/tokens",
		strings.NewReader(strings.TrimSuffix(createBody, "}")+`,"unknown":true}`),
	)
	invalidRequest.Header.Set("Authorization", "Bearer credential")
	invalidRequest.Header.Set("Content-Type", "application/json")
	invalidResponse := httptest.NewRecorder()
	httpAPI.ServeHTTP(invalidResponse, invalidRequest)
	require.Equal(t, http.StatusBadRequest, invalidResponse.Code, "strict decode status = %d: %s", invalidResponse.Code, invalidResponse.Body.String())
	require.Len(t, tokens.preparedActions(), 1, "a strictly refused body never reaches the token Store")

	missingCredential := httptest.NewRecorder()
	httpAPI.ServeHTTP(missingCredential, httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/api/v1/users/me/sessions", nil))
	require.Equal(t, http.StatusUnauthorized, missingCredential.Code, "missing credential status = %d: %s", missingCredential.Code, missingCredential.Body.String())

	weakPrincipal := principal
	weakPrincipal.AuthenticationStrength = model.AuthenticationSingleFactor
	weakPrincipal.MFACompletedAt = model.OptionalTime{}
	weakAPI := newFocusedResourceAPI(
		t,
		logger,
		routetest.Authenticator{Principal: weakPrincipal},
		mfaResource(fixture.service),
	)
	weakRequest := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/api/v1/users/me/mfa/disable", nil)
	weakRequest.Header.Set("Authorization", "Bearer credential")
	weakResponse := httptest.NewRecorder()
	weakAPI.ServeHTTP(weakResponse, weakRequest)
	require.Equal(t, http.StatusForbidden, weakResponse.Code, "weak assurance response")
	require.Contains(t, weakResponse.Body.String(), `"code":"authentication.strong_required"`, "weak assurance response")
	require.True(t, mfa.credential(principal.UserID).IsActive(), "a weak Session never disables the second factor")

	stalePrincipal := principal
	stalePrincipal.AuthenticatedAt = now.Add(-time.Hour)
	stalePrincipal.MFACompletedAt = model.OptionalTimeFrom(now.Add(-time.Hour))
	staleAPI := newFocusedResourceAPI(
		t,
		logger,
		routetest.Authenticator{Principal: stalePrincipal},
		personalAccessTokenResource(fixture.service),
	)
	staleRequest := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/api/v1/users/me/tokens", strings.NewReader(createBody))
	staleRequest.Header.Set("Authorization", "Bearer credential")
	staleResponse := httptest.NewRecorder()
	staleAPI.ServeHTTP(staleResponse, staleRequest)
	require.Equal(t, http.StatusForbidden, staleResponse.Code, "stale assurance response")
	require.Contains(t, staleResponse.Body.String(), `"code":"authentication.reauthentication_required"`, "stale assurance response")
	require.Len(t, tokens.preparedActions(), 1, "a stale Session never reaches the token Store")
}

// accountUser is an active local User valid enough to receive mail.
func accountUser(id model.UserID) *model.User {
	return &model.User{
		ID: id, Revision: 1, Username: "student", Email: "student@example.edu", Locale: "en", Timezone: "UTC",
		DefaultProfilePictureSeed: strings.Repeat("a", model.ProfilePictureSeedLength),
		CreatedAt:                 time.UnixMilli(1_000), UpdatedAt: time.UnixMilli(1_000),
	}
}

// accountSession is a live password Web Session created at createdAt.
func accountSession(userID model.UserID, id model.SessionID, createdAt time.Time) *model.Session {
	createdAt = model.TimeUTC(createdAt)
	return &model.Session{
		ID: id, CreatedAt: createdAt, UpdatedAt: createdAt, UserID: userID, ClientType: model.SessionClientWeb,
		AuthenticationMethod: "password", AuthenticationStrength: model.AuthenticationSingleFactor,
		AuthenticatedAt: createdAt, LastActivityAt: createdAt,
		IdleExpiresAt: createdAt.Add(2 * time.Hour), ExpiresAt: createdAt.Add(24 * time.Hour),
	}
}

// accountAuditOutcomes is each recorded mutation attempt as action:status.
func accountAuditOutcomes(fixture *identityFixture) []string {
	outcomes := []string{}
	for _, attempt := range fixture.access.Audits.Attempts() {
		outcomes = append(outcomes, attempt.Action+":"+string(attempt.Status))
	}
	return outcomes
}

// accountSessions holds the Sessions of the signed-in User and records the
// lifecycle changes requests make to them.
type accountSessions struct {
	store.SessionStore
	accountAudit
	mu                sync.Mutex
	sessions          []*model.Session
	revocations       []store.UserSessionsRevocation
	reauthentications []store.SessionPasswordReauthentication
}

func newAccountSessions(sessions ...*model.Session) *accountSessions {
	return &accountSessions{sessions: sessions}
}

func (s *accountSessions) revocationInputs() []store.UserSessionsRevocation {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]store.UserSessionsRevocation(nil), s.revocations...)
}

func (s *accountSessions) reauthenticationInputs() []store.SessionPasswordReauthentication {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]store.SessionPasswordReauthentication(nil), s.reauthentications...)
}

func (s *accountSessions) live(session *model.Session, at time.Time) bool {
	return !session.RevokedAt.Valid && !session.ArchivedAt.Valid && at.Before(session.IdleExpiresAt) && at.Before(session.ExpiresAt)
}

func (s *accountSessions) ListActiveByUser(_ context.Context, userID model.UserID, at time.Time) ([]*model.Session, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	listed := []*model.Session{}
	for _, session := range s.sessions {
		if session.UserID == userID && s.live(session, at) {
			copied := *session
			listed = append(listed, &copied)
		}
	}
	return listed, nil
}

func (s *accountSessions) RevokeAllForUser(ctx context.Context, input *store.UserSessionsRevocation) (*store.UserSessionsRevocationResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.succeed(ctx, input.AuditEventID); err != nil {
		return nil, err
	}
	s.revocations = append(s.revocations, *input)
	result := &store.UserSessionsRevocationResult{}
	for _, session := range s.sessions {
		if session.UserID != input.UserID || session.ID == input.KeepSessionID || !s.live(session, input.RevokedAt) {
			continue
		}
		session.RevokedAt, session.RevocationReason = model.OptionalTimeFrom(input.RevokedAt), input.Reason
		copied := *session
		result.Sessions = append(result.Sessions, &copied)
	}
	return result, nil
}

func (s *accountSessions) ReauthenticatePassword(ctx context.Context, input *store.SessionPasswordReauthentication) (*store.SessionReauthenticationResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.reauthentications = append(s.reauthentications, *input)
	for _, session := range s.sessions {
		if session.ID == input.SessionID && session.UserID == input.UserID {
			if err := s.succeed(ctx, input.AuditEventID); err != nil {
				return nil, err
			}
			session.ReauthenticatedAt = model.OptionalTimeFrom(model.TimeUTC(time.Now()))
			copied := *session
			return &store.SessionReauthenticationResult{Session: &copied}, nil
		}
	}
	return nil, store.NewErrNotFound("session", input.SessionID.String())
}

// upgrade completes a second factor on the Session, as an MFA challenge
// commits it.
func (s *accountSessions) upgrade(id model.SessionID, at time.Time) (*model.Session, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, session := range s.sessions {
		if session.ID == id {
			session.AuthenticationStrength = model.AuthenticationMultiFactor
			session.MFACompletedAt = model.OptionalTimeFrom(at)
			session.MFAEnrollmentRequired = false
			copied := *session
			return &copied, true
		}
	}
	return nil, false
}

// accountTokens records the personal access token mutations a request
// prepares and commits.
type accountTokens struct {
	store.PersonalAccessTokenStore
	mu           sync.Mutex
	preparations []store.PersonalAccessTokenMutationPreparation
	created      []*model.PersonalAccessToken
	failures     []string
}

func (s *accountTokens) preparedActions() []model.Action {
	s.mu.Lock()
	defer s.mu.Unlock()
	actions := []model.Action{}
	for _, preparation := range s.preparations {
		actions = append(actions, model.Action(preparation.AuditEvent.Action))
	}
	return actions
}

func (s *accountTokens) createdTokens() []*model.PersonalAccessToken {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]*model.PersonalAccessToken(nil), s.created...)
}

func (s *accountTokens) PrepareMutation(_ context.Context, input *store.PersonalAccessTokenMutationPreparation) (*store.PreparedPersonalAccessTokenMutation, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.preparations = append(s.preparations, *input)
	at := model.TimeUTC(time.Now())
	return &store.PreparedPersonalAccessTokenMutation{ID: model.NewPersonalAccessTokenPreparationID(), ActionAt: at, ExpiresAt: at.Add(input.Lifetime)}, nil
}

func (s *accountTokens) FailMutation(_ context.Context, input *store.PersonalAccessTokenMutationFailure) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.failures = append(s.failures, input.ErrorCode)
	return nil
}

func (s *accountTokens) Create(_ context.Context, input *store.PersonalAccessTokenCreationMutation) (*store.PersonalAccessTokenMutationResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	token := *input.Token
	token.PrepareCreate(model.NewPersonalAccessTokenID(), time.Now())
	s.created = append(s.created, &token)
	copied := token
	return &store.PersonalAccessTokenMutationResult{Token: &copied, Fresh: true}, nil
}
