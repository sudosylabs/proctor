// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package httpapi

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/sudosylabs/proctor/server/app/realtime/realtimetest"
	"github.com/sudosylabs/proctor/server/httpapi/route"
	"github.com/sudosylabs/proctor/server/httpapi/route/routetest"
	"github.com/sudosylabs/proctor/server/model"
)

func TestPasswordReauthenticationHTTPRejectsIdentitySelectionAndProtectsResponse(t *testing.T) {
	t.Parallel()
	logger, _ := routetest.NewLogger(t)
	principal := model.Principal{UserID: model.NewUserID(), SessionID: model.NewSessionID(), CredentialID: model.PrincipalCredentialID(model.NewID()), CredentialType: model.CredentialSessionAccess, AuthenticationMethod: "password", AuthenticationStrength: model.AuthenticationSingleFactor, ClientType: model.SessionClientWeb, AuthenticatedAt: time.Now().Add(-time.Hour)}
	password := &model.PasswordCredential{ID: model.NewPasswordCredentialID(), UserID: principal.UserID, Revision: 3, PasswordHash: hashPassword(t, "correct horse battery"), PasswordChangedAt: time.UnixMilli(1_000)}
	sessions := newAccountSessions(accountSession(principal.UserID, principal.SessionID, principal.AuthenticatedAt))
	fixture := newIdentityFixture(t, identityOptions{Stores: identityStores{
		Users:     newIdentityUsers(accountUser(principal.UserID)),
		Passwords: &identityPasswords{byUser: map[model.UserID]*model.PasswordCredential{principal.UserID: password}},
		Sessions:  sessions,
	}})
	sessions.audits = fixture.access.Audits
	api := newFocusedResourceAPI(t, logger, routetest.Authenticator{Principal: principal}, sessionReauthenticationResource(fixture.service, route.Cookies{}))
	for _, test := range []struct {
		body   string
		status int
	}{{`{"password":"correct horse battery","user_id":"other"}`, 400}, {`{"password":"correct horse battery","login_id":"other@example.edu"}`, 400}, {`{"password":"correct horse battery"}`, 200}} {
		t.Run(test.body, func(t *testing.T) {
			request := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/api/v1/auth/reauthenticate/password", strings.NewReader(test.body))
			request.Header.Set("Authorization", "Bearer session")
			request.Header.Set("Content-Type", "application/json")
			response := httptest.NewRecorder()
			api.ServeHTTP(response, request)
			require.Equal(t, test.status, response.Code, "status=%d body=%s", response.Code, response.Body.String())
			if response.Code == 200 {
				require.Equal(t, "no-store", response.Header().Get("Cache-Control"), "fresh-proof response not protected/projected")
				require.Contains(t, response.Body.String(), `"reauthenticated_at"`, "fresh-proof response not protected/projected")
			}
		})
	}
	// Only the body without an identity selector reached the Session Store,
	// for the current Session and its User, with proof of the password the
	// request sent.
	reauthentications := sessions.reauthenticationInputs()
	require.Len(t, reauthentications, 1, "transport did not bind the immutable current identity")
	require.Equal(t, principal.UserID, reauthentications[0].UserID, "transport did not bind the immutable current identity")
	require.Equal(t, principal.SessionID, reauthentications[0].SessionID, "transport did not bind the immutable current identity")
	require.Equal(t, principal.CredentialID.String(), reauthentications[0].CredentialID.String())
	require.Equal(t, password.ID, reauthentications[0].PasswordProof.ID, "the stored password verified")
	require.Equal(t, password.Revision, reauthentications[0].PasswordProof.Revision)
	require.Equal(t, []string{"authentication.reauthenticate:success"}, accountAuditOutcomes(fixture))
	require.Equal(t, []realtimetest.Call{{Method: realtimetest.AuthenticationCacheInvalidated, UserID: principal.UserID}}, fixture.publisher.Calls())
}
