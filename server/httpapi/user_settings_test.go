// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package httpapi

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/sudosylabs/proctor/server/app/apperror"
	"github.com/sudosylabs/proctor/server/httpapi/route"
	"github.com/sudosylabs/proctor/server/httpapi/route/routetest"
	"github.com/sudosylabs/proctor/server/model"
	"github.com/sudosylabs/proctor/server/store"
)

// settingsFixture is the real settings use cases over the caller's stored
// document.
func settingsFixture(t *testing.T, document *model.UserSettingsDocument) *userFixture {
	t.Helper()
	fixture := newUserFixture(t)
	fixture.settings.document = document
	return fixture
}

// settingsDocument is a stored version 1 document of the User.
func settingsDocument(userID model.UserID, source string, revision model.UserSettingsRevision, at time.Time) *model.UserSettingsDocument {
	return &model.UserSettingsDocument{
		UserID: userID, Source: source, FormatVersion: model.UserSettingsFormatVersion1,
		Revision: revision, CreatedAt: at.Add(-time.Hour), UpdatedAt: at.Add(-time.Minute),
	}
}

func TestUserSettingsHTTPReplacesWithoutEchoingSource(t *testing.T) {
	t.Parallel()
	logger, _ := routetest.NewLogger(t)
	at := time.Date(2026, 8, 15, 12, 0, 0, 0, time.UTC)
	principal := model.Principal{
		UserID: model.NewUserID(), SessionID: model.NewSessionID(),
		CredentialID: model.PrincipalCredentialID(model.NewSessionCredentialID()), CredentialType: model.CredentialSessionAccess,
		AuthenticationMethod: "password", AuthenticationStrength: model.AuthenticationSingleFactor,
		ClientType: model.SessionClientDesktop, AuthenticatedAt: at,
	}
	revision := model.NewUserSettingsRevision()
	next := model.NewUserSettingsRevision()
	source := "{\n  // exact\n  \"workbench.colorTheme\": \"Proctor Dark\",\n}\n"
	fixture := settingsFixture(t, settingsDocument(principal.UserID, "{}\n", revision, at))
	fixture.settings.result = &store.UserSettingsReplacementResult{Revision: next, FormatVersion: 1, UpdatedAt: at, Changed: true}
	httpAPI := newFocusedResourceAPI(t, logger, routetest.Authenticator{Principal: principal}, userSettingsResource(fixture.service))
	body, err := json.Marshal(map[string]any{
		"source": source, "format_version": 1, "expected_revision": revision.String(),
	})
	require.NoError(t, err)
	request := httptest.NewRequestWithContext(t.Context(), http.MethodPut, "/api/v1/users/me/settings", bytes.NewReader(body))
	request.Header.Set("Authorization", "Bearer session-credential")
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Idempotency-Key", "settings-save-1")
	response := httptest.NewRecorder()
	httpAPI.ServeHTTP(response, request)
	require.Equal(t, http.StatusOK, response.Code, "status = %d: %s", response.Code, response.Body.String())
	require.Equal(t, "private, no-store", response.Header().Get("Cache-Control"))
	replacement := fixture.settings.replacement
	require.NotNil(t, replacement, "the replacement reached the settings Store")
	require.Equal(t, principal.UserID, replacement.UserID)
	require.Equal(t, source, replacement.Source)
	require.Equal(t, 1, replacement.FormatVersion)
	require.Equal(t, revision, replacement.ExpectedRevision)
	require.NotNil(t, fixture.settings.idempotency)
	require.Equal(t, sha256.Sum256([]byte("settings-save-1")), fixture.settings.idempotency.KeyDigest, "the Idempotency-Key reached the Store")
	var result map[string]any
	require.NoError(t, json.Unmarshal(response.Body.Bytes(), &result))
	_, exposed := result["source"]
	require.False(t, exposed)
	require.Equal(t, next.String(), result["revision"])
	require.Equal(t, true, result["changed"])
}

func TestUserSettingsHTTPReplacementIsStrictBoundedAndIdempotent(t *testing.T) {
	t.Parallel()
	logger, _ := routetest.NewLogger(t)
	principal := model.Principal{
		UserID: model.NewUserID(), SessionID: model.NewSessionID(),
		CredentialID: model.PrincipalCredentialID(model.NewSessionCredentialID()), CredentialType: model.CredentialSessionAccess,
		AuthenticationMethod: "password", AuthenticationStrength: model.AuthenticationSingleFactor,
		ClientType: model.SessionClientWeb, AuthenticatedAt: time.Now(),
	}
	revision := model.NewUserSettingsRevision()
	fixture := settingsFixture(t, settingsDocument(principal.UserID, "{}\n", revision, time.Now()))
	fixture.settings.result = &store.UserSettingsReplacementResult{Revision: model.NewUserSettingsRevision(), FormatVersion: 1, UpdatedAt: time.Now(), Changed: true}
	newAPI := func() *route.API {
		return newFocusedResourceAPI(t, logger, routetest.Authenticator{Principal: principal}, userSettingsResource(fixture.service))
	}
	for name, configure := range map[string]func(*http.Request){
		"missing idempotency": func(request *http.Request) {},
		"unknown outer field": func(request *http.Request) {
			request.Header.Set("Idempotency-Key", "strict-save")
		},
		"trailing value": func(request *http.Request) {
			request.Header.Set("Idempotency-Key", "strict-save")
		},
	} {
		t.Run(name, func(t *testing.T) {
			body := `{"source":"{}\\n","format_version":1,"expected_revision":"` + revision.String() + `"}`
			if name == "unknown outer field" {
				body = `{"source":"{}\\n","format_version":1,"expected_revision":"` + revision.String() + `","unknown":true}`
			}
			if name == "trailing value" {
				body += `{}`
			}
			request := httptest.NewRequestWithContext(t.Context(), http.MethodPut, "/api/v1/users/me/settings", bytes.NewBufferString(body))
			request.Header.Set("Authorization", "Bearer session-credential")
			request.Header.Set("Content-Type", "application/json")
			configure(request)
			response := httptest.NewRecorder()
			newAPI().ServeHTTP(response, request)
			require.Equal(t, http.StatusBadRequest, response.Code, "status = %d: %s", response.Code, response.Body.String())
		})
	}
	require.Zero(t, fixture.settings.reads, "invalid requests reached the application")
	require.Nil(t, fixture.settings.replacement, "invalid requests reached the settings Store")

	maxSource := strings.Repeat(" ", model.UserSettingsSourceMaxBytes-3) + "{}\n"
	body, err := json.Marshal(userSettingsReplaceRequest{
		Source: maxSource, FormatVersion: 1, ExpectedRevision: revision.String(),
	})
	require.NoError(t, err)
	request := httptest.NewRequestWithContext(t.Context(), http.MethodPut, "/api/v1/users/me/settings", bytes.NewReader(body))
	request.Header.Set("Authorization", "Bearer session-credential")
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Idempotency-Key", "maximum-save")
	response := httptest.NewRecorder()
	newAPI().ServeHTTP(response, request)
	require.Equal(t, http.StatusOK, response.Code, "maximum source status = %d: %s", response.Code, response.Body.String())
	require.NotNil(t, fixture.settings.replacement, "the maximum source reached the settings Store")
	require.Equal(t, maxSource, fixture.settings.replacement.Source)
}

func TestUserSettingsHTTPReadsExactSelfDocumentWithoutCaching(t *testing.T) {
	t.Parallel()
	logger, _ := routetest.NewLogger(t)
	at := time.Date(2026, 8, 15, 12, 0, 0, 0, time.UTC)
	principal := model.Principal{
		UserID: model.NewUserID(), SessionID: model.NewSessionID(),
		CredentialID:         model.PrincipalCredentialID(model.NewSessionCredentialID()),
		CredentialType:       model.CredentialSessionAccess,
		AuthenticationMethod: "password", AuthenticationStrength: model.AuthenticationSingleFactor,
		ClientType: model.SessionClientDesktop, AuthenticatedAt: at,
	}
	source := "{\n  // exact\n  \"workbench.colorTheme\": \"Proctor Dark\",\n}\n"
	stored := settingsDocument(principal.UserID, source, model.NewUserSettingsRevision(), at)
	stored.UpdatedAt = at
	fixture := settingsFixture(t, stored)
	httpAPI := newFocusedResourceAPI(
		t,
		logger,
		routetest.Authenticator{Principal: principal},
		userSettingsResource(fixture.service),
	)
	request := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/api/v1/users/me/settings", nil)
	request.Header.Set("Authorization", "Bearer session-credential")
	response := httptest.NewRecorder()
	httpAPI.ServeHTTP(response, request)
	require.Equal(t, http.StatusOK, response.Code, "status = %d: %s", response.Code, response.Body.String())
	require.Equal(t, "private, no-store", response.Header().Get("Cache-Control"))
	require.Equal(t, 1, fixture.settings.reads, "the caller's own document is read once")
	var body userSettingsResponse
	require.NoError(t, json.Unmarshal(response.Body.Bytes(), &body))
	require.Equal(t, source, body.Source)
	require.Equal(t, 1, body.FormatVersion)
	require.Equal(t, stored.Revision.String(), body.Revision)
	require.True(t, body.Writable)
	require.Equal(t, model.FormatRFC3339(at), body.UpdatedAt)
}

func TestUserSettingsHTTPReadsUnsupportedFormatAsExactReadOnlySource(t *testing.T) {
	t.Parallel()
	logger, _ := routetest.NewLogger(t)
	at := time.Date(2026, 8, 15, 12, 0, 0, 0, time.UTC)
	principal := model.Principal{
		UserID: model.NewUserID(), SessionID: model.NewSessionID(),
		CredentialID:         model.PrincipalCredentialID(model.NewSessionCredentialID()),
		CredentialType:       model.CredentialSessionAccess,
		AuthenticationMethod: "password", AuthenticationStrength: model.AuthenticationSingleFactor,
		ClientType: model.SessionClientDesktop, AuthenticatedAt: at,
	}
	const source = "future-format(source => remains.exact);\n"
	stored := settingsDocument(principal.UserID, source, model.NewUserSettingsRevision(), at)
	stored.FormatVersion, stored.UpdatedAt = 2, at
	fixture := settingsFixture(t, stored)
	httpAPI := newFocusedResourceAPI(
		t,
		logger,
		routetest.Authenticator{Principal: principal},
		userSettingsResource(fixture.service),
	)
	request := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/api/v1/users/me/settings", nil)
	request.Header.Set("Authorization", "Bearer session-credential")
	response := httptest.NewRecorder()
	httpAPI.ServeHTTP(response, request)
	require.Equal(t, http.StatusOK, response.Code, "status = %d: %s", response.Code, response.Body.String())
	var body userSettingsResponse
	require.NoError(t, json.Unmarshal(response.Body.Bytes(), &body))
	require.Equal(t, source, body.Source)
	require.Equal(t, 2, body.FormatVersion)
	require.False(t, body.Writable)
	require.Equal(t, stored.Revision.String(), body.Revision)
	require.Equal(t, model.FormatRFC3339(at), body.UpdatedAt)
}

func TestUserSettingsHTTPRejectsNonSessionAndArbitraryUserPaths(t *testing.T) {
	t.Parallel()
	logger, _ := routetest.NewLogger(t)
	fixture := settingsFixture(t, nil)
	patAPI := newFocusedResourceAPI(
		t,
		logger,
		routetest.Authenticator{Err: apperror.New("authentication.token_invalid")},
		userSettingsResource(fixture.service),
	)
	request := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/api/v1/users/me/settings", nil)
	request.Header.Set("Authorization", "Bearer personal-access-token")
	response := httptest.NewRecorder()
	patAPI.ServeHTTP(response, request)
	require.Equal(t, http.StatusUnauthorized, response.Code, "PAT status = %d: %s", response.Code, response.Body.String())

	principal := model.Principal{
		UserID: model.NewUserID(), SessionID: model.NewSessionID(),
		CredentialID: model.PrincipalCredentialID(model.NewSessionCredentialID()), CredentialType: model.CredentialSessionAccess,
		AuthenticationMethod: "password", AuthenticationStrength: model.AuthenticationSingleFactor,
		ClientType: model.SessionClientWeb, AuthenticatedAt: time.Now(),
	}
	httpAPI := newFocusedResourceAPI(t, logger, routetest.Authenticator{Principal: principal}, userSettingsResource(fixture.service))
	for name, path := range map[string]string{
		"arbitrary user":   "/api/v1/users/" + model.NewUserID().String() + "/settings",
		"settings listing": "/api/v1/users/settings",
	} {
		t.Run(name, func(t *testing.T) {
			request := httptest.NewRequestWithContext(t.Context(), http.MethodGet, path, nil)
			request.Header.Set("Authorization", "Bearer credential")
			response := httptest.NewRecorder()
			httpAPI.ServeHTTP(response, request)
			require.Equal(t, http.StatusNotFound, response.Code, "status = %d: %s", response.Code, response.Body.String())
		})
	}
	for _, method := range []string{http.MethodPatch, http.MethodDelete} {
		request := httptest.NewRequestWithContext(t.Context(), method, "/api/v1/users/me/settings", nil)
		request.Header.Set("Authorization", "Bearer session-credential")
		response := httptest.NewRecorder()
		httpAPI.ServeHTTP(response, request)
		require.Equal(t, http.StatusMethodNotAllowed, response.Code, "%s status = %d: %s", method, response.Code, response.Body.String())
	}
	require.Zero(t, fixture.settings.reads, "rejected requests reached the application")
}

func TestUserSettingsOpenAPIDocument(t *testing.T) {
	t.Parallel()

	document := readOpenAPIDocument(t)
	response := document.Components.Responses["UserSettingsOK"]
	require.Equal(t, "#/components/headers/PrivateNoStore", response.Headers["Cache-Control"].Ref)
	replaced := document.Components.Responses["UserSettingsReplaced"]
	require.Equal(t, "#/components/headers/PrivateNoStore", replaced.Headers["Cache-Control"].Ref)
}
