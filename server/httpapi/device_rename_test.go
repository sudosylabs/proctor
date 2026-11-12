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
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/sudosylabs/proctor/server/app/apperror"
	"github.com/sudosylabs/proctor/server/app/identity"
	"github.com/sudosylabs/proctor/server/app/invocation"
	"github.com/sudosylabs/proctor/server/httpapi/route"
	"github.com/sudosylabs/proctor/server/httpapi/route/routetest"
	"github.com/sudosylabs/proctor/server/model"
)

// deviceRenameHTTPApplication answers the rename use cases with a fixed
// result or error and records the command each received.
type deviceRenameHTTPApplication struct {
	accountSecurityHTTPApplication

	renameSession      []identity.RenameSessionCommand
	renamedSession     *model.Session
	renameSessionErr   error
	renameRegistration []identity.RenameDesktopRegistrationCommand
	renamedDesktop     *model.DesktopRegistration
	renameDesktopErr   error
}

func (a *deviceRenameHTTPApplication) RenameSession(
	_ context.Context,
	_ invocation.Invocation,
	command identity.RenameSessionCommand,
) (*model.Session, error) {
	a.renameSession = append(a.renameSession, command)
	return a.renamedSession, a.renameSessionErr
}

func (a *deviceRenameHTTPApplication) ListDesktopRegistrations(
	context.Context,
	invocation.Invocation,
) ([]*model.DesktopRegistration, error) {
	return nil, nil
}

func (a *deviceRenameHTTPApplication) RenameDesktopRegistration(
	_ context.Context,
	_ invocation.Invocation,
	command identity.RenameDesktopRegistrationCommand,
) (*model.DesktopRegistration, error) {
	a.renameRegistration = append(a.renameRegistration, command)
	return a.renamedDesktop, a.renameDesktopErr
}

func (a *deviceRenameHTTPApplication) RevokeDesktopRegistration(
	context.Context,
	invocation.Invocation,
	model.DesktopRegistrationID,
) error {
	return nil
}

type deviceRenameProblem struct {
	Code   string            `json:"code"`
	Fields map[string]string `json:"fields"`
}

func deviceRenamePrincipal(now time.Time) model.Principal {
	return model.Principal{
		UserID: model.NewUserID(), SessionID: model.NewSessionID(),
		CredentialID:         model.PrincipalCredentialID(model.NewID()),
		CredentialType:       model.CredentialSessionAccess,
		AuthenticationMethod: "password", AuthenticationStrength: model.AuthenticationSingleFactor,
		AuthenticatedAt: now.Add(-24 * time.Hour), ClientType: model.SessionClientWeb,
	}
}

func deviceRenameAPI(t *testing.T, principal model.Principal, application *deviceRenameHTTPApplication) http.Handler {
	t.Helper()
	logger, _ := routetest.NewLogger(t)
	cookies, err := route.NewCookies("http://localhost:8065")
	require.NoError(t, err)
	return newFocusedResourceAPI(
		t,
		logger,
		routetest.Authenticator{Principal: principal},
		sessionResource(application, cookies),
		desktopRegistrationResource(application),
	)
}

func serveDeviceRename(t *testing.T, handler http.Handler, method, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	request := httptest.NewRequestWithContext(t.Context(), method, path, strings.NewReader(body))
	request.Header.Set("Authorization", "Bearer credential")
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	return response
}

func TestSessionRenameRoute(t *testing.T) {
	t.Parallel()
	now := time.Now().UTC()
	principal := deviceRenamePrincipal(now)
	sessionID := model.NewSessionID()
	path := "/api/v1/users/me/sessions/" + sessionID.String()

	t.Run("renames and returns the Session without recent authentication", func(t *testing.T) {
		t.Parallel()
		application := &deviceRenameHTTPApplication{renamedSession: &model.Session{
			ID: sessionID, UserID: principal.UserID, ClientType: model.SessionClientWeb,
			DeviceName: "Office browser", CreatedAt: now, UpdatedAt: now,
		}}
		response := serveDeviceRename(t, deviceRenameAPI(t, principal, application), http.MethodPatch, path, `{"device_name":"  Office browser "}`)
		require.Equal(t, http.StatusOK, response.Code, response.Body.String())
		require.Equal(t, "no-store", response.Header().Get("Cache-Control"))
		require.Equal(t, []identity.RenameSessionCommand{{SessionID: sessionID, DeviceName: "  Office browser "}}, application.renameSession)
		var body map[string]any
		require.NoError(t, json.Unmarshal(response.Body.Bytes(), &body))
		require.Equal(t, sessionID.String(), body["id"])
		require.Equal(t, "Office browser", body["device_name"])
		require.Equal(t, false, body["current"])
		require.NotContains(t, body, "desktop_registration_display_name")
	})

	t.Run("refuses a Desktop Session with the registration to rename", func(t *testing.T) {
		t.Parallel()
		registrationID := model.NewDesktopRegistrationID()
		application := &deviceRenameHTTPApplication{renameSessionErr: apperror.New("session.desktop_named_by_registration").
			WithField("desktop_registration_id", registrationID.String())}
		response := serveDeviceRename(t, deviceRenameAPI(t, principal, application), http.MethodPatch, path, `{"device_name":"Laptop"}`)
		require.Equal(t, http.StatusConflict, response.Code, response.Body.String())
		var problem deviceRenameProblem
		require.NoError(t, json.Unmarshal(response.Body.Bytes(), &problem))
		require.Equal(t, deviceRenameProblem{
			Code:   "session.desktop_named_by_registration",
			Fields: map[string]string{"desktop_registration_id": registrationID.String()},
		}, problem)
	})

	t.Run("presents an invalid name and another User's Session", func(t *testing.T) {
		t.Parallel()
		for code, status := range map[string]int{
			"session.invalid":   http.StatusBadRequest,
			"session.not_found": http.StatusNotFound,
		} {
			application := &deviceRenameHTTPApplication{renameSessionErr: apperror.New(apperror.Code(code))}
			response := serveDeviceRename(t, deviceRenameAPI(t, principal, application), http.MethodPatch, path, `{"device_name":"Laptop"}`)
			require.Equal(t, status, response.Code, "%s: %s", code, response.Body.String())
			require.Contains(t, response.Body.String(), `"code":"`+code+`"`)
		}
	})

	t.Run("rejects a malformed request before the use case", func(t *testing.T) {
		t.Parallel()
		application := &deviceRenameHTTPApplication{}
		handler := deviceRenameAPI(t, principal, application)
		for name, request := range map[string]struct{ path, body string }{
			"unknown member":      {path, `{"device_name":"Laptop","label":"x"}`},
			"server-owned member": {path, `{"device_name":"Laptop","id":"` + sessionID.String() + `"}`},
		} {
			response := serveDeviceRename(t, handler, http.MethodPatch, request.path, request.body)
			require.Equal(t, http.StatusBadRequest, response.Code, "%s: %s", name, response.Body.String())
		}
		malformed := serveDeviceRename(t, handler, http.MethodPatch, "/api/v1/users/me/sessions/not-an-id", `{"device_name":"Laptop"}`)
		require.Equal(t, http.StatusNotFound, malformed.Code, malformed.Body.String())
		require.Contains(t, malformed.Body.String(), `"code":"route.not_found"`)
		require.Empty(t, application.renameSession)
	})

	t.Run("refuses a Personal Access Token", func(t *testing.T) {
		t.Parallel()
		token := principal
		token.CredentialType, token.SessionID = model.CredentialPersonalAccessToken, ""
		application := &deviceRenameHTTPApplication{}
		response := serveDeviceRename(t, deviceRenameAPI(t, token, application), http.MethodPatch, path, `{"device_name":"Laptop"}`)
		require.Equal(t, http.StatusUnauthorized, response.Code, response.Body.String())
		require.Contains(t, response.Body.String(), `"code":"authentication.token_invalid"`)
		require.Empty(t, application.renameSession)
	})
}

func TestSessionListNamesDesktopSessionsByRegistration(t *testing.T) {
	t.Parallel()
	now := time.Now().UTC()
	principal := deviceRenamePrincipal(now)
	registrationID := model.NewDesktopRegistrationID()
	webID, desktopID := model.NewSessionID(), model.NewSessionID()
	application := &deviceRenameHTTPApplication{accountSecurityHTTPApplication: accountSecurityHTTPApplication{
		sessions: []*model.Session{
			{ID: webID, UserID: principal.UserID, ClientType: model.SessionClientWeb, DeviceName: "Office browser", CreatedAt: now},
			{ID: desktopID, UserID: principal.UserID, ClientType: model.SessionClientDesktop, DesktopRegistrationID: registrationID,
				DeviceName: "reported-host", CreatedAt: now.Add(-time.Minute)},
		},
		desktopRegistrationNames: map[model.DesktopRegistrationID]string{registrationID: "Library laptop"},
	}}
	response := serveDeviceRename(t, deviceRenameAPI(t, principal, application), http.MethodGet, "/api/v1/users/me/sessions", "")
	require.Equal(t, http.StatusOK, response.Code, response.Body.String())
	var listed struct {
		Items []struct {
			ID                             string  `json:"id"`
			DesktopRegistrationDisplayName *string `json:"desktop_registration_display_name"`
		} `json:"items"`
	}
	require.NoError(t, json.Unmarshal(response.Body.Bytes(), &listed))
	names := map[string]*string{}
	for _, item := range listed.Items {
		names[item.ID] = item.DesktopRegistrationDisplayName
	}
	desktopName := "Library laptop"
	require.Equal(t, map[string]*string{webID.String(): nil, desktopID.String(): &desktopName}, names)
}

func TestDesktopRegistrationRenameRoute(t *testing.T) {
	t.Parallel()
	now := time.Now().UTC()
	principal := deviceRenamePrincipal(now)
	registrationID := model.NewDesktopRegistrationID()
	path := "/api/v1/users/me/desktop-registrations/" + registrationID.String()

	t.Run("renames without recent authentication and returns the registration", func(t *testing.T) {
		t.Parallel()
		application := &deviceRenameHTTPApplication{renamedDesktop: &model.DesktopRegistration{
			ID: registrationID, UserID: principal.UserID, DisplayName: "Library laptop",
			DesktopRelease: "1.4.0", DesktopBuildID: "build-1", Platform: model.DesktopPlatformDarwin,
			Architecture: model.DesktopArchitectureARM64, RealtimeProtocol: 1, CreatedAt: now, UpdatedAt: now, LastUsedAt: now,
			RenamedAt: model.OptionalTimeFrom(now),
		}}
		response := serveDeviceRename(t, deviceRenameAPI(t, principal, application), http.MethodPatch, path, `{"display_name":"Library laptop"}`)
		require.Equal(t, http.StatusOK, response.Code, response.Body.String())
		require.Equal(t, "private, no-store", response.Header().Get("Cache-Control"))
		require.Equal(t, []identity.RenameDesktopRegistrationCommand{{RegistrationID: registrationID, DisplayName: "Library laptop"}},
			application.renameRegistration)
		var body desktopRegistrationResponse
		require.NoError(t, json.Unmarshal(response.Body.Bytes(), &body))
		require.Equal(t, desktopRegistrationResponseFromModel(application.renamedDesktop, false), body)
	})

	t.Run("presents application refusals", func(t *testing.T) {
		t.Parallel()
		for code, status := range map[string]int{
			"desktop_registration.invalid": http.StatusBadRequest,
			"desktop_registration.revoked": http.StatusConflict,
			"resource.not_found":           http.StatusNotFound,
		} {
			application := &deviceRenameHTTPApplication{renameDesktopErr: apperror.New(apperror.Code(code))}
			response := serveDeviceRename(t, deviceRenameAPI(t, principal, application), http.MethodPatch, path, `{"display_name":"Laptop"}`)
			require.Equal(t, status, response.Code, "%s: %s", code, response.Body.String())
			require.Contains(t, response.Body.String(), `"code":"`+code+`"`)
		}
	})

	t.Run("rejects a malformed request before the use case", func(t *testing.T) {
		t.Parallel()
		application := &deviceRenameHTTPApplication{}
		handler := deviceRenameAPI(t, principal, application)
		for name, request := range map[string]struct{ path, body string }{
			"unknown member": {path, `{"display_name":"Laptop","platform":"linux"}`},
			"not an object":  {path, `"Laptop"`},
		} {
			response := serveDeviceRename(t, handler, http.MethodPatch, request.path, request.body)
			require.Equal(t, http.StatusBadRequest, response.Code, "%s: %s", name, response.Body.String())
		}
		malformed := serveDeviceRename(t, handler, http.MethodPatch, "/api/v1/users/me/desktop-registrations/not-an-id", `{"display_name":"Laptop"}`)
		require.Equal(t, http.StatusNotFound, malformed.Code, malformed.Body.String())
		require.Contains(t, malformed.Body.String(), `"code":"route.not_found"`)
		require.Empty(t, application.renameRegistration)
	})

	t.Run("refuses a Personal Access Token", func(t *testing.T) {
		t.Parallel()
		token := principal
		token.CredentialType, token.SessionID = model.CredentialPersonalAccessToken, ""
		application := &deviceRenameHTTPApplication{}
		response := serveDeviceRename(t, deviceRenameAPI(t, token, application), http.MethodPatch, path, `{"display_name":"Laptop"}`)
		require.Equal(t, http.StatusUnauthorized, response.Code, response.Body.String())
		require.Contains(t, response.Body.String(), `"code":"authentication.token_invalid"`)
		require.Empty(t, application.renameRegistration)
	})
}
