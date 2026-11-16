// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package httpapi

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/sudosylabs/proctor/server/app/identity"
	"github.com/sudosylabs/proctor/server/httpapi/route"
	"github.com/sudosylabs/proctor/server/httpapi/route/routetest"
	"github.com/sudosylabs/proctor/server/model"
)

func TestBrowserInvitationHTTPKeepsSecondProofInHttpOnlyCookie(t *testing.T) {
	t.Parallel()
	logger, _ := routetest.NewLogger(t)
	fixture := newOnboardingFixture(t)
	invitation, claim := fixture.pendingStudentInvitation(t, fixture.addClass("Biology 101"), "student.one@example.edu")
	cookies, err := route.NewCookies("https://proctor.example.edu")
	require.NoError(t, err)
	httpAPI := newFocusedResourceAPI(t, logger, routetest.Authenticator{}, browserInvitationResource(fixture.service, cookies))

	start := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/api/v1/auth/browser/invitations", strings.NewReader(`{"claim":"`+claim+`"}`))
	start.Header.Set("Content-Type", "application/json")
	started := httptest.NewRecorder()
	httpAPI.ServeHTTP(started, start)
	require.Equal(t, http.StatusCreated, started.Code, "start = %d cache=%q body=%s", started.Code, started.Header().Get("Cache-Control"), started.Body.String())
	require.Equal(t, "no-store", started.Header().Get("Cache-Control"))
	proofCookies := started.Result().Cookies()
	require.Len(t, proofCookies, 1)
	require.Equal(t, route.BrowserInvitationProofCookieName, proofCookies[0].Name)
	require.True(t, proofCookies[0].HttpOnly)
	require.True(t, proofCookies[0].Secure)
	require.Equal(t, "/api/v1/auth/browser/invitations", proofCookies[0].Path)
	require.NotContains(t, started.Body.String(), proofCookies[0].Value, "browser proof was disclosed in the JSON response")
	require.Contains(t, started.Body.String(), `"email":"student.one@example.edu"`)
	require.Contains(t, started.Body.String(), `"class_display_name":"Biology 101"`)
	require.Len(t, fixture.browser.creations, 1, "the handoff reached the Store")
	creation := fixture.browser.creations[0]
	require.Equal(t, invitation.ID, creation.InvitationID)
	require.Equal(t, model.HashToken(proofCookies[0].Value), creation.BrowserProofHash, "the cookie carries the proof the Store bound")
	require.True(t, fixture.sourceCounted(t, identity.AttemptPurposeInvitation, "claim-accept", "192.0.2.1"), "the claim attempt was counted against the request's source")
	var startBody struct {
		Handle string `json:"handle"`
	}
	require.NoError(t, json.Unmarshal(started.Body.Bytes(), &startBody))

	accept := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/api/v1/auth/browser/invitations/accept", strings.NewReader(`{"handle":"`+startBody.Handle+`","password":"correct horse battery staple","username":"student"}`))
	accept.Header.Set("Content-Type", "application/json")
	accept.AddCookie(proofCookies[0])
	accepted := httptest.NewRecorder()
	httpAPI.ServeHTTP(accepted, accept)
	require.Equal(t, http.StatusOK, accepted.Code, "accept = %d cache=%q body=%s", accepted.Code, accepted.Header().Get("Cache-Control"), accepted.Body.String())
	require.Equal(t, "no-store", accepted.Header().Get("Cache-Control"))
	cleared := accepted.Result().Cookies()
	require.Len(t, cleared, 1)
	require.Equal(t, route.BrowserInvitationProofCookieName, cleared[0].Name)
	require.Negative(t, cleared[0].MaxAge)
	require.Len(t, fixture.invitations.studentAcceptances, 1, "the acceptance reached the Store")
	acceptance := fixture.invitations.studentAcceptances[0]
	require.Equal(t, invitation.ClaimHash, acceptance.ClaimHash)
	require.NotNil(t, acceptance.BrowserTransaction)
	require.Equal(t, creation.ID, acceptance.BrowserTransaction.ID)
	require.Equal(t, model.HashToken(proofCookies[0].Value), acceptance.BrowserTransaction.BrowserProofHash, "the cookie's proof fenced the acceptance")
	require.Equal(t, "student", acceptance.User.Username)
}

func TestBrowserInvitationSessionAcceptanceUsesAuthenticatedPrincipal(t *testing.T) {
	t.Parallel()
	logger, _ := routetest.NewLogger(t)
	fixture := newOnboardingFixture(t)
	invitation, _ := fixture.pendingRoleInvitation(t, model.InvitationPurposeAcademicUnitRole, fixture.addDelegableRole())
	handle, proof := fixture.browser.handoff(invitation)
	cookies, err := route.NewCookies("https://proctor.example.edu")
	require.NoError(t, err)
	principal := fixture.principal(model.AuthenticationSingleFactor)
	httpAPI := newFocusedResourceAPI(t, logger, routetest.Authenticator{Principal: principal}, browserInvitationResource(fixture.service, cookies))
	request := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/api/v1/auth/browser/invitations/accept-session", strings.NewReader(`{"handle":"`+handle+`"}`))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Authorization", "Bearer access")
	request.AddCookie(&http.Cookie{Name: route.BrowserInvitationProofCookieName, Value: proof})
	response := httptest.NewRecorder()
	httpAPI.ServeHTTP(response, request)
	require.Equal(t, http.StatusOK, response.Code, "accept session = %d body=%s", response.Code, response.Body.String())
	require.Len(t, fixture.invitations.scopedAcceptances, 1, "the acceptance reached the Store")
	acceptance := fixture.invitations.scopedAcceptances[0]
	require.Equal(t, principal.UserID, acceptance.UserID, "the Role binds the authenticated Session's User")
	require.NotNil(t, acceptance.BrowserTransaction)
	require.Equal(t, model.HashToken(proof), acceptance.BrowserTransaction.BrowserProofHash, "the cookie's proof fenced the acceptance")
}
