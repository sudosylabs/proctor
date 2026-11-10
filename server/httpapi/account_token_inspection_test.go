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
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/sudosylabs/proctor/server/app/apperror"
	"github.com/sudosylabs/proctor/server/app/identity"
	"github.com/sudosylabs/proctor/server/app/invocation"
	"github.com/sudosylabs/proctor/server/model"
)

type accountTokenInspectionHTTPApplication struct {
	authenticationEntryApplication
	commands []identity.InspectAccountTokenCommand
	err      error
}

func (a *accountTokenInspectionHTTPApplication) InspectEmailVerification(_ context.Context, _ invocation.Invocation, command identity.InspectAccountTokenCommand) (*identity.EmailVerificationInspection, error) {
	a.commands = append(a.commands, command)
	if a.err != nil {
		return nil, a.err
	}
	return &identity.EmailVerificationInspection{Email: "ada@example.edu"}, nil
}

func (a *accountTokenInspectionHTTPApplication) InspectEmailChangeUndo(_ context.Context, _ invocation.Invocation, command identity.InspectAccountTokenCommand) (*identity.EmailChangeUndoInspection, error) {
	a.commands = append(a.commands, command)
	if a.err != nil {
		return nil, a.err
	}
	return &identity.EmailChangeUndoInspection{RestoredEmail: "ada.old@example.edu", MaskedCurrentEmail: "a***@example.org",
		ChangedAt: time.Date(2026, 8, 18, 10, 0, 0, 0, time.UTC)}, nil
}

func TestAccountTokenInspectionHTTPReadsTheBodyTokenAndIsNotCached(t *testing.T) {
	t.Parallel()
	logger, _ := newTestLogger(t)
	application := &accountTokenInspectionHTTPApplication{}
	api := newFocusedResourceAPI(t, logger, classRouteAuthenticator{}, authenticationResource(nil, application, browserCookies{}))
	token := model.NewCredentialToken()
	post := func(path string) *httptest.ResponseRecorder {
		request := httptest.NewRequestWithContext(t.Context(), http.MethodPost, path, strings.NewReader(`{"token":"`+token+`"}`))
		request.Header.Set("Content-Type", "application/json")
		response := httptest.NewRecorder()
		api.ServeHTTP(response, request)
		return response
	}

	verification := post("/api/v1/auth/email-verification/inspect")
	require.Equal(t, http.StatusOK, verification.Code, verification.Body.String())
	require.Equal(t, "no-store", verification.Header().Get("Cache-Control"))
	require.JSONEq(t, `{"email":"ada@example.edu"}`, verification.Body.String())

	undo := post("/api/v1/auth/email-change/undo/inspect")
	require.Equal(t, http.StatusOK, undo.Code, undo.Body.String())
	require.Equal(t, "no-store", undo.Header().Get("Cache-Control"))
	require.JSONEq(t, `{"restored_email":"ada.old@example.edu","masked_current_email":"a***@example.org","changed_at":"2026-08-18T10:00:00Z"}`, undo.Body.String())

	require.Len(t, application.commands, 2)
	for _, command := range application.commands {
		require.Equal(t, token, command.Token)
		require.Equal(t, "192.0.2.1", command.Source)
	}
	require.NotContains(t, verification.Body.String()+undo.Body.String(), token, "inspection echoed the credential")

	application.err = apperror.New("authentication.account_token.invalid")
	invalid := post("/api/v1/auth/email-verification/inspect")
	require.Equal(t, http.StatusBadRequest, invalid.Code, invalid.Body.String())
	require.Contains(t, invalid.Body.String(), "authentication.account_token.invalid")
	require.NotContains(t, invalid.Body.String(), token)
}
