// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package httpapi

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/sudosylabs/proctor/server/httpapi/route"
	"github.com/sudosylabs/proctor/server/httpapi/route/routetest"
	"github.com/sudosylabs/proctor/server/model"
	"github.com/sudosylabs/proctor/server/store"
)

// signinLoginLookups finds only the slow User, counts the sign-in lookups of
// each login ID, and fails the lookup of failing.
type signinLoginLookups struct {
	store.UserStore
	slow    *model.User
	failing string
	mu      sync.Mutex
	byLogin map[string]int
}

func (s *signinLoginLookups) GetByUsername(_ context.Context, username string) (*model.User, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.byLogin[username]++
	switch username {
	case s.slow.Username:
		copied := *s.slow
		return &copied, nil
	case s.failing:
		return nil, errors.New("private user store failure")
	}
	return nil, store.NewErrNotFound("user", "")
}

func (s *signinLoginLookups) lookups(loginID string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.byLogin[loginID]
}

// signinRecoveryReads signals each read of a User's MFA recovery state, the
// last Store read before sign-in verifies the password.
type signinRecoveryReads struct {
	signinMFAStore
	reads chan model.UserID
}

func (s *signinRecoveryReads) GetRecoveryState(ctx context.Context, userID model.UserID) (*model.UserMFARecovery, error) {
	s.reads <- userID
	return s.signinMFAStore.GetRecoveryState(ctx, userID)
}

// passwordPermits signals each permit the hasher grants.
type passwordPermits struct{ started chan struct{} }

func (p passwordPermits) Started() {
	select {
	case p.started <- struct{}{}:
	default:
	}
}
func (p passwordPermits) Finished(time.Duration) {}
func (p passwordPermits) Rejected()              {}

func TestLoginCapacityRefusalIsRetryableAndDoesNotIssueCredentials(t *testing.T) {
	logger, logs := routetest.NewLogger(t)
	// The slow User's stored hash asks for twenty Argon2id passes, so each
	// verification of it holds one of the hasher's two permits for a while.
	slow := &model.User{ID: model.NewUserID(), Username: "slow", Email: "slow@example.edu"}
	slowHash := strings.Replace(hashPassword(t, "another-password"), ",t=1,", ",t=20,", 1)
	require.Contains(t, slowHash, ",t=20,")
	users := &signinLoginLookups{slow: slow, failing: "broken", byLogin: map[string]int{}}
	recovery := &signinRecoveryReads{reads: make(chan model.UserID, 2)}
	permits := passwordPermits{started: make(chan struct{}, 64)}
	fixture := newIdentityFixture(t, identityOptions{PasswordWork: permits, Stores: identityStores{
		Users: users, MFA: recovery,
		Passwords: &identityPasswords{byUser: map[model.UserID]*model.PasswordCredential{
			slow.ID: {ID: model.NewPasswordCredentialID(), UserID: slow.ID, Revision: 1, PasswordHash: slowHash},
		}},
	}})
	api := newFocusedResourceAPI(t, logger, routetest.Authenticator{}, authenticationResource(nil, fixture.service, route.Cookies{}))
	login := func(loginID, remoteAddr string) *httptest.ResponseRecorder {
		request := newJSONRequest(http.MethodPost, "/api/v1/auth/login",
			strings.NewReader(`{"login_id":"`+loginID+`","password":"example-password","client_type":"web"}`))
		request.RemoteAddr = remoteAddr
		response := httptest.NewRecorder()
		api.ServeHTTP(response, request)
		return response
	}

	var slowLogins sync.WaitGroup
	slowDone := make(chan struct{})
	for index := range 2 {
		slowLogins.Go(func() {
			response := login("slow", fmt.Sprintf("198.51.100.%d:1234", index+1))
			assert.Equal(t, http.StatusUnauthorized, response.Code, "slow login: %s", response.Body.String())
		})
	}
	go func() { slowLogins.Wait(); close(slowDone) }()
	<-recovery.reads
	<-recovery.reads
	// Wait until both slow verifications hold their permits, so no unknown
	// login below can take one first and leave a slow login refused.
	<-permits.started
	<-permits.started

	// Both slow logins are verifying or about to. An unknown login still
	// spends one dummy verification, so once both permits are held it is
	// refused at capacity. Each try names its own account from its own
	// address, so no rate limit interferes.
	var refused *httptest.ResponseRecorder
	var refusedLogin string
	for try := 0; ; try++ {
		select {
		case <-slowDone:
			t.Fatal("the slow logins finished before any login was refused at capacity")
		default:
		}
		loginID := fmt.Sprintf("student-%d", try)
		response := login(loginID, fmt.Sprintf("203.0.%d.%d:1234", try/250, try%250+1))
		if response.Code == http.StatusServiceUnavailable {
			refused, refusedLogin = response, loginID
			break
		}
		require.Equal(t, http.StatusUnauthorized, response.Code, "admitted unknown login: %s", response.Body.String())
	}
	<-slowDone

	require.Equal(t, http.StatusServiceUnavailable, refused.Code, "capacity refusal")
	require.Equal(t, "1", refused.Header().Get("Retry-After"), "capacity refusal")
	require.Equal(t, 1, users.lookups(refusedLogin), "capacity refusal retried internally, issued cookies, or was cacheable")
	require.Empty(t, refused.Result().Cookies(), "capacity refusal retried internally, issued cookies, or was cacheable")
	require.Equal(t, "no-store", refused.Header().Get("Cache-Control"), "capacity refusal retried internally, issued cookies, or was cacheable")
	require.Contains(t, refused.Body.String(), `"code":"service.busy"`)
	require.NotContains(t, logs.String(), "application request failed", "capacity refusals must use bounded counters rather than per-request error logging")

	// A User Store failure is an unexpected authentication failure.
	response := login("broken", "192.0.2.9:1234")
	require.Equal(t, http.StatusInternalServerError, response.Code, "unexpected failure")
	require.Contains(t, response.Body.String(), `"code":"authentication.internal"`)
	require.NotContains(t, response.Body.String(), "private user store failure")
	require.Contains(t, logs.String(), "application request failed", "unexpected failures lost their operator diagnostics")
}

func TestOnlyExpensiveWorkRoutesDeclareCapacityRefusal(t *testing.T) {
	expected := map[string]bool{
		"POST /api/v1/auth/login":                                                            true,
		"POST /api/v1/auth/reauthenticate/password":                                          true,
		"POST /api/v1/auth/register":                                                         true,
		"POST /api/v1/auth/password-reset/complete":                                          true,
		"POST /api/v1/bootstrap":                                                             true,
		"PUT /api/v1/authentication-methods/password":                                        true,
		"POST /api/v1/authentication-methods/password/change":                                true,
		"POST /api/v1/auth/browser/invitations/accept":                                       true,
		"POST /api/v1/invitations/student-class/accept":                                      true,
		"POST /api/v1/invitations/teacher-academic-unit/accept":                              true,
		"POST /api/v1/auth/desktop/authorizations/authenticate/password":                     true,
		"POST /api/v1/auth/desktop/reauthentications/authenticate/password":                  true,
		"GET /api/v1/users/{user_id}/profile-picture":                                        true,
		"PUT /api/v1/users/{user_id}/profile-picture":                                        true,
		"POST /api/v1/exams/{exam_id}/draft/resources":                                       true,
		"PUT /api/v1/exams/{exam_id}/draft/resources/{exam_resource_id}/content":             true,
		"POST /api/v1/exams/{exam_id}/sittings/{exam_sitting_id}/correction-resource-stages": true,
	}
	api := newProductionCatalogAPI(t)
	for _, entry := range api.Routes() {
		key := entry.Method + " " + normalizeRuntimeRoutePath(entry.Path)
		assert.Equal(t, expected[key], slices.Contains(entry.ErrorCodes, "service.busy"), "%s has incorrect capacity error declaration", key)
		delete(expected, key)
	}
	require.Empty(t, expected)
}
