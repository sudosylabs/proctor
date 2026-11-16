// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package httpapi

import (
	"cmp"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/sudosylabs/proctor/server/app/access/accesstest"
	"github.com/sudosylabs/proctor/server/app/realtime/realtimetest"
	"github.com/sudosylabs/proctor/server/httpapi/route/routetest"
	"github.com/sudosylabs/proctor/server/model"
	"github.com/sudosylabs/proctor/server/store"
)

// adminPrincipal is the administrator's single-factor CLI Session.
func adminPrincipal() model.Principal {
	return model.Principal{
		UserID: model.NewUserID(), SessionID: model.NewSessionID(), CredentialID: model.PrincipalCredentialID(model.NewID()),
		CredentialType: model.CredentialSessionAccess, AuthenticationMethod: "password",
		AuthenticationStrength: model.AuthenticationSingleFactor,
		ClientType:             model.SessionClientCLI, AuthenticatedAt: time.Now(),
	}
}

// adminSubject is the User an administrator acts on, with the fields the
// real mail Composer needs to address a notice to them.
func adminSubject(username string) *model.User {
	user := &model.User{Username: username, Email: username + "@example.edu", DisplayName: "Subject " + username,
		DefaultProfilePictureSeed: "0000000000000000000000000000000000000000000000000000000000000000"}
	user.PrepareCreate(model.NewUserID(), time.UnixMilli(100))
	return user
}

// adminSession is an unrevoked CLI Session of userID created at created.
func adminSession(userID model.UserID, created time.Time) *model.Session {
	session := &model.Session{
		UserID: userID, ClientType: model.SessionClientCLI,
		AuthenticationMethod: "password", AuthenticationStrength: model.AuthenticationSingleFactor,
		AuthenticatedAt: created, LastActivityAt: created,
		IdleExpiresAt: created.Add(time.Hour), ExpiresAt: created.Add(2 * time.Hour),
	}
	session.PrepareCreate(model.NewSessionID(), created)
	return session
}

// adminSessions is the Session Store the administration and sign-in
// handlers reach. It pages a User's Sessions newest first as the PostgreSQL
// Store does, revokes them, saves new ones with their credential hashes, and
// records every page query and revocation it is given.
type adminSessions struct {
	store.SessionStore
	mu          sync.Mutex
	byID        map[model.SessionID]*model.Session
	hashes      map[model.SessionID][]string
	queries     []store.SessionPageQuery
	revocations []store.SessionRevocation
	saved       []store.SessionCreation
}

// newAdminSessions holds sessions, each with one access credential.
func newAdminSessions(sessions ...*model.Session) *adminSessions {
	s := &adminSessions{byID: map[model.SessionID]*model.Session{}, hashes: map[model.SessionID][]string{}}
	for _, session := range sessions {
		s.byID[session.ID] = session
		s.hashes[session.ID] = []string{model.HashToken("access-" + session.ID.String())}
	}
	return s
}

// lastQuery is the page query the Store answered last.
func (s *adminSessions) lastQuery(t *testing.T) store.SessionPageQuery {
	t.Helper()
	s.mu.Lock()
	defer s.mu.Unlock()
	require.NotEmpty(t, s.queries, "no Session page reached the Store")
	return s.queries[len(s.queries)-1]
}

func (s *adminSessions) Get(_ context.Context, id model.SessionID) (*model.Session, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	session, ok := s.byID[id]
	if !ok {
		return nil, store.NewErrNotFound("session", id.String())
	}
	copied := *session
	return &copied, nil
}

func (s *adminSessions) ListByUser(_ context.Context, query *store.SessionPageQuery) ([]*model.Session, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.queries = append(s.queries, *query)
	var page []*model.Session
	for _, session := range s.byID {
		active := !session.RevokedAt.Valid && session.IdleExpiresAt.After(query.ActiveAt) && session.ExpiresAt.After(query.ActiveAt)
		before := query.BeforeID.IsZero() || session.CreatedAt.Before(query.BeforeCreatedAt) ||
			(session.CreatedAt.Equal(query.BeforeCreatedAt) && session.ID < query.BeforeID)
		if session.UserID == query.UserID && (query.ActiveAt.IsZero() || active) && before {
			copied := *session
			page = append(page, &copied)
		}
	}
	slices.SortFunc(page, func(a, b *model.Session) int {
		return cmp.Or(b.CreatedAt.Compare(a.CreatedAt), cmp.Compare(b.ID, a.ID))
	})
	return page[:min(len(page), query.Limit)], nil
}

func (s *adminSessions) Revoke(_ context.Context, input *store.SessionRevocation) (*store.SessionRevocationResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.revocations = append(s.revocations, *input)
	session, ok := s.byID[input.SessionID]
	if !ok || session.UserID != input.UserID || session.RevokedAt.Valid {
		return nil, store.NewErrNotFound("session", input.SessionID.String())
	}
	session.RevokedAt = model.OptionalTimeFrom(input.RevokedAt)
	session.RevocationReason = input.Reason
	copied := *session
	return &store.SessionRevocationResult{Session: &copied, TokenHashes: s.hashes[session.ID]}, nil
}

func (s *adminSessions) Save(_ context.Context, input *store.SessionCreation) (*model.Session, []*model.SessionCredential, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.saved = append(s.saved, *input)
	session := *input.Session
	session.PrepareCreate(model.NewSessionID(), input.AuditAt)
	s.byID[session.ID] = &session
	credentials := make([]*model.SessionCredential, 0, len(input.Credentials))
	for _, credential := range input.Credentials {
		saved := *credential
		saved.SessionID = session.ID
		credentials = append(credentials, &saved)
		s.hashes[session.ID] = append(s.hashes[session.ID], credential.TokenHash)
	}
	copied := session
	return &copied, credentials, nil
}

func TestAdminSessionListUsesApplicationQueryAndOmitsCredentials(t *testing.T) {
	t.Parallel()
	logger, _ := routetest.NewLogger(t)
	principal := adminPrincipal()
	subject := adminSubject("student")
	revoked := adminSession(subject.ID, model.TimeFromMillis(100))
	revoked.RevokedAt = model.OptionalTimeFromMillis(150)
	sessions := newAdminSessions(revoked, adminSession(model.NewUserID(), model.TimeFromMillis(200)))
	fixture := newIdentityFixture(t, identityOptions{
		Stores: identityStores{Users: newIdentityUsers(subject), Sessions: sessions},
		Grants: []accesstest.Grant{institutionWide(principal.UserID, model.ActionSessionView)},
	})
	httpAPI := newFocusedResourceAPI(t, logger, routetest.Authenticator{Principal: principal}, userAdministrationResource(fixture.service))
	request := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/api/v1/users/"+subject.ID.String()+"/sessions?include_revoked=true", nil)
	request.Header.Set("Authorization", "Bearer credential")
	response := httptest.NewRecorder()
	httpAPI.ServeHTTP(response, request)
	require.Equal(t, http.StatusOK, response.Code, "status = %d: %s", response.Code, response.Body.String())
	query := sessions.lastQuery(t)
	require.Equal(t, subject.ID, query.UserID)
	require.True(t, query.ActiveAt.IsZero(), "include_revoked reads revoked and expired Sessions too")
	require.Equal(t, 51, query.Limit, "the default page of 50 reads one probe row")
	require.True(t, query.BeforeCreatedAt.IsZero() && query.BeforeID.IsZero(), "a first page has no cursor")
	var body struct {
		Items      []map[string]any `json:"items"`
		NextCursor *string          `json:"next_cursor"`
	}
	require.NoError(t, json.Unmarshal(response.Body.Bytes(), &body))
	require.Len(t, body.Items, 1)
	require.Equal(t, revoked.ID.String(), body.Items[0]["id"])
	require.Nil(t, body.NextCursor, "a short page is the final page")
	for _, key := range []string{"token", "access_token", "refresh_token", "credential"} {
		_, exposed := body.Items[0][key]
		require.False(t, exposed, "credential field")
	}
}

func TestAdminSessionListPagesWithAnOpaqueCursor(t *testing.T) {
	t.Parallel()
	logger, _ := routetest.NewLogger(t)
	principal := adminPrincipal()
	subject := adminSubject("student")
	created := time.Date(2026, 8, 12, 9, 30, 0, 123_456_000, time.UTC)
	last := adminSession(subject.ID, created)
	older := adminSession(subject.ID, created.Add(-time.Minute))
	sessions := newAdminSessions(last, older)
	fixture := newIdentityFixture(t, identityOptions{
		Stores: identityStores{Users: newIdentityUsers(subject), Sessions: sessions},
		Grants: []accesstest.Grant{institutionWide(principal.UserID, model.ActionSessionView)},
	})
	httpAPI := newFocusedResourceAPI(t, logger, routetest.Authenticator{Principal: principal}, userAdministrationResource(fixture.service))
	serve := func(target string) *httptest.ResponseRecorder {
		request := httptest.NewRequestWithContext(t.Context(), http.MethodGet, target, nil)
		request.Header.Set("Authorization", "Bearer credential")
		response := httptest.NewRecorder()
		httpAPI.ServeHTTP(response, request)
		return response
	}
	type page struct {
		Items      []map[string]any `json:"items"`
		NextCursor string           `json:"next_cursor"`
	}
	response := serve("/api/v1/users/" + subject.ID.String() + "/sessions?include_revoked=true&limit=1")
	require.Equal(t, http.StatusOK, response.Code, response.Body.String())
	var body page
	require.NoError(t, json.Unmarshal(response.Body.Bytes(), &body))
	require.NotEmpty(t, body.NextCursor, "a page with more Sessions continues")
	require.Len(t, body.Items, 1)
	require.Equal(t, last.ID.String(), body.Items[0]["id"])

	response = serve("/api/v1/users/" + subject.ID.String() + "/sessions?include_revoked=true&limit=1&cursor=" + body.NextCursor)
	require.Equal(t, http.StatusOK, response.Code, response.Body.String())
	query := sessions.lastQuery(t)
	require.True(t, query.BeforeCreatedAt.Equal(created), "cursor keeps microseconds: %v", query.BeforeCreatedAt)
	require.Equal(t, last.ID, query.BeforeID)
	var next page
	require.NoError(t, json.Unmarshal(response.Body.Bytes(), &next))
	require.Len(t, next.Items, 1)
	require.Equal(t, older.ID.String(), next.Items[0]["id"], "the cursor continues after the last Session")
	require.Empty(t, next.NextCursor, "the second page is the final page")

	for _, query := range []string{"limit=0", "limit=201", "cursor=not-a-cursor"} {
		response = serve("/api/v1/users/" + subject.ID.String() + "/sessions?" + query)
		require.Equal(t, http.StatusBadRequest, response.Code, "%s: %s", query, response.Body.String())
	}
	sessions.mu.Lock()
	defer sessions.mu.Unlock()
	require.Len(t, sessions.queries, 2, "an invalid page request reaches no Store")
}

func TestAdminSessionRevokeUsesApplicationCommand(t *testing.T) {
	t.Parallel()
	logger, _ := routetest.NewLogger(t)
	principal := adminPrincipal()
	subject := adminSubject("student")
	session := adminSession(subject.ID, model.TimeFromMillis(100))
	sessions := newAdminSessions(session)
	fixture := newIdentityFixture(t, identityOptions{
		Stores: identityStores{Users: newIdentityUsers(subject), Sessions: sessions},
		Grants: []accesstest.Grant{institutionWide(principal.UserID, model.ActionSessionManage)},
	})
	httpAPI := newFocusedResourceAPI(t, logger, routetest.Authenticator{Principal: principal}, userAdministrationResource(fixture.service))
	request := httptest.NewRequestWithContext(t.Context(), http.MethodDelete, "/api/v1/users/"+subject.ID.String()+"/sessions/"+session.ID.String(), nil)
	request.Header.Set("Authorization", "Bearer credential")
	response := httptest.NewRecorder()
	httpAPI.ServeHTTP(response, request)
	require.Equal(t, http.StatusNoContent, response.Code, "status = %d: %s", response.Code, response.Body.String())

	attempts := fixture.access.Audits.Attempts()
	require.Len(t, attempts, 1)
	require.Equal(t, "revoke_session", attempts[0].Operation())
	require.Len(t, sessions.revocations, 1)
	revocation := sessions.revocations[0]
	require.Equal(t, subject.ID, revocation.UserID)
	require.Equal(t, session.ID, revocation.SessionID)
	require.Equal(t, model.SessionRevocationAdministratorSession, revocation.Reason)
	require.Equal(t, attempts[0].ID, revocation.AuditEventID)
	require.NotNil(t, revocation.Delivery, "the User is told an administrator signed a Session out")
	require.Equal(t, 1, fixture.mail.renders(), "one notice was rendered")
	require.Equal(t, model.MailTemplateIdentitySessionsRevokedByAdmin, revocation.Delivery.TemplateKey)
	require.Equal(t, []realtimetest.Call{{
		Method: realtimetest.SessionsRevoked, UserID: subject.ID,
		SessionIDs: []model.SessionID{session.ID}, AccessTokenHashes: sessions.hashes[session.ID],
	}}, fixture.publisher.Calls())
}

func TestSessionAdministrationOpenAPIDocument(t *testing.T) {
	t.Parallel()

	document := readOpenAPIDocument(t)
	list := document.Components.Schemas["SessionAdministrationListResponse"]
	require.Equal(t, "object", list.Type)
	var items struct {
		Items struct {
			Ref string `json:"$ref"`
		} `json:"items"`
	}
	require.NoError(t, json.Unmarshal(list.Properties["items"], &items))
	require.Equal(t, "#/components/schemas/SessionAdministrationResponse", items.Items.Ref)

	// Administrative session projections must never expose usable credentials
	// or their hashes, even when the underlying Session model gains fields.
	sessionSchema := document.Components.Schemas["SessionAdministrationResponse"]
	for _, forbidden := range []string{"token", "access_token", "refresh_token", "credential", "token_hash"} {
		_, exists := sessionSchema.Properties[forbidden]
		require.False(t, exists, "session schema exposes %q", forbidden)
	}
}
