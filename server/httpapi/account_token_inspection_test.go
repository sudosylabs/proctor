// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package httpapi

import (
	"context"
	"net/http"
	"net/http/httptest"
	"slices"
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

// signinUserTokens holds the account tokens a hosted page may inspect, by
// purpose and hash, and records each lookup.
type signinUserTokens struct {
	store.UserTokenStore
	mu      sync.Mutex
	tokens  []*model.UserToken
	lookups []signinUserTokenLookup
}

type signinUserTokenLookup struct {
	hash    string
	purpose model.UserTokenPurpose
}

func (s *signinUserTokens) GetByHash(_ context.Context, hash string, purpose model.UserTokenPurpose) (*model.UserToken, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.lookups = append(s.lookups, signinUserTokenLookup{hash: hash, purpose: purpose})
	for _, token := range s.tokens {
		if token.TokenHash == hash && token.Purpose == purpose {
			copied := *token
			return &copied, nil
		}
	}
	return nil, store.NewErrNotFound("user_token", "")
}

// signinSourceAttempts are the per-source attempt counters the cache holds,
// in ascending order.
func signinSourceAttempts(cache *identityCache) []int64 {
	cache.mu.Lock()
	defer cache.mu.Unlock()
	var counts []int64
	for key, count := range cache.counters {
		if strings.Contains(key, "/source/") {
			counts = append(counts, count)
		}
	}
	slices.Sort(counts)
	return counts
}

func TestAccountTokenInspectionHTTPReadsTheBodyTokenAndIsNotCached(t *testing.T) {
	t.Parallel()
	logger, _ := routetest.NewLogger(t)
	token := model.NewCredentialToken()
	verifying := &model.User{ID: model.NewUserID(), Username: "ada", Email: "ada@example.edu"}
	changed := &model.User{ID: model.NewUserID(), Username: "ada-changed", Email: "ada.new@example.org"}
	expires := time.Now().Add(time.Hour)
	tokens := &signinUserTokens{tokens: []*model.UserToken{
		{
			ID: model.NewUserTokenID(), UserID: verifying.ID, Purpose: model.UserTokenEmailVerification,
			TokenHash: model.HashToken(token), Target: "ada@example.edu", ExpiresAt: expires,
		},
		{
			ID: model.NewUserTokenID(), UserID: changed.ID, Purpose: model.UserTokenEmailChangeUndo,
			TokenHash: model.HashToken(token), Target: "ada.old@example.edu", ExpiresAt: expires,
			CreatedAt: time.Date(2026, 8, 18, 10, 0, 0, 0, time.UTC),
		},
	}}
	fixture := newIdentityFixture(t, identityOptions{Stores: identityStores{
		Users: newIdentityUsers(verifying, changed), UserTokens: tokens,
	}})
	api := newFocusedResourceAPI(t, logger, routetest.Authenticator{}, authenticationResource(nil, fixture.service, route.Cookies{}))
	post := func(path, body, remoteAddr string) *httptest.ResponseRecorder {
		request := httptest.NewRequestWithContext(t.Context(), http.MethodPost, path, strings.NewReader(body))
		request.Header.Set("Content-Type", "application/json")
		if remoteAddr != "" {
			request.RemoteAddr = remoteAddr
		}
		response := httptest.NewRecorder()
		api.ServeHTTP(response, request)
		return response
	}

	verification := post("/api/v1/auth/email-verification/inspect", `{"token":"`+token+`"}`, "")
	require.Equal(t, http.StatusOK, verification.Code, verification.Body.String())
	require.Equal(t, "no-store", verification.Header().Get("Cache-Control"))
	require.JSONEq(t, `{"email":"ada@example.edu"}`, verification.Body.String())

	undo := post("/api/v1/auth/email-change/undo/inspect", `{"token":"`+token+`"}`, "")
	require.Equal(t, http.StatusOK, undo.Code, undo.Body.String())
	require.Equal(t, "no-store", undo.Header().Get("Cache-Control"))
	require.JSONEq(t, `{"restored_email":"ada.old@example.edu","masked_current_email":"a***@example.org","changed_at":"2026-08-18T10:00:00Z"}`, undo.Body.String())

	require.Equal(t, []signinUserTokenLookup{
		{hash: model.HashToken(token), purpose: model.UserTokenEmailVerification},
		{hash: model.HashToken(token), purpose: model.UserTokenEmailChangeUndo},
	}, tokens.lookups)
	require.NotContains(t, verification.Body.String()+undo.Body.String(), token, "inspection echoed the credential")

	unknown := model.NewCredentialToken()
	invalid := post("/api/v1/auth/email-verification/inspect", `{"token":"`+unknown+`"}`, "")
	require.Equal(t, http.StatusBadRequest, invalid.Code, invalid.Body.String())
	require.Contains(t, invalid.Body.String(), "authentication.account_token.invalid")
	require.NotContains(t, invalid.Body.String(), unknown)

	// Inspection is rate-limited by the request's source address: the two
	// verification inspections from 192.0.2.1 share a counter, the undo
	// inspection and one from another address count apart.
	other := post("/api/v1/auth/email-verification/inspect", `{"token":"`+token+`"}`, "198.51.100.7:4321")
	require.Equal(t, http.StatusOK, other.Code, other.Body.String())
	require.Equal(t, []int64{1, 1, 2}, signinSourceAttempts(fixture.cache))
	require.Empty(t, fixture.access.Audits.Events(), "inspection was audited")
}
