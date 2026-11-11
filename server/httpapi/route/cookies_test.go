// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package route

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/sudosylabs/proctor/server/model"
)

func TestBrowserCookiesAreBoundedSecureAndCSRFSigned(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	cookies, err := NewCookies("https://proctor.example.edu")
	require.NoError(t, err)
	cookies.now = func() time.Time { return now }
	tokens := &model.AuthenticationTokens{
		AccessToken: "access-secret", RefreshToken: "refresh-secret",
		AccessExpiresAt:  now.Add(15 * time.Minute),
		RefreshExpiresAt: now.Add(24 * time.Hour),
	}
	response := httptest.NewRecorder()
	cookies.Attach(response, tokens)
	attached := response.Result().Cookies()
	require.Len(t, attached, 4)
	byName := make(map[string]*http.Cookie, len(attached))
	for _, cookie := range attached {
		byName[cookie.Name] = cookie
		require.True(t, cookie.Secure)
		require.Equal(t, http.SameSiteLaxMode, cookie.SameSite)
		require.Positive(t, cookie.MaxAge)
	}
	require.True(t, byName[BrowserAccessCookieName].HttpOnly)
	require.Equal(t, "/", byName[BrowserAccessCookieName].Path)
	require.True(t, byName[BrowserRefreshCookieName].HttpOnly)
	require.Equal(t, model.APIURLSuffix+"/auth/refresh", byName[BrowserRefreshCookieName].Path)
	require.True(t, byName[BrowserCSRFBindingCookieName].HttpOnly)
	require.False(t, byName[BrowserCSRFCookieName].HttpOnly)

	request := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/api/v1/roles", nil)
	for _, cookie := range attached {
		request.AddCookie(cookie)
	}
	request.Header.Set(BrowserCSRFHeader, byName[BrowserCSRFCookieName].Value)
	require.NoError(t, cookies.verifyCSRF(request))
	request.Header.Set(BrowserCSRFHeader, "tampered")
	require.Equal(t, "authentication.csrf.invalid", applicationErrorCode(cookies.verifyCSRF(request)), "tampered CSRF error")

	clearedResponse := httptest.NewRecorder()
	cookies.Clear(clearedResponse)
	for _, cookie := range clearedResponse.Result().Cookies() {
		require.Negative(t, cookie.MaxAge)
	}
}

func TestRequestCredentialRejectsAmbiguousSourcesAndDuplicateCookies(t *testing.T) {
	request := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/api/v1/users/me", nil)
	request.Header.Set("Authorization", "Bearer header-token")
	request.AddCookie(&http.Cookie{Name: BrowserAccessCookieName, Value: "cookie-token", Secure: true, HttpOnly: true, SameSite: http.SameSiteLaxMode})
	_, appErr := requestAccessCredential(request)
	require.Equal(t, "authentication.credential_ambiguous", applicationErrorCode(appErr), "header and cookie error")

	request = httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/api/v1/users/me", nil)
	request.AddCookie(&http.Cookie{Name: BrowserAccessCookieName, Value: "first", Secure: true, HttpOnly: true, SameSite: http.SameSiteLaxMode})
	request.AddCookie(&http.Cookie{Name: BrowserAccessCookieName, Value: "second", Secure: true, HttpOnly: true, SameSite: http.SameSiteLaxMode})
	_, appErr = requestAccessCredential(request)
	require.Equal(t, "authentication.credential_ambiguous", applicationErrorCode(appErr), "duplicate cookie error")
}
