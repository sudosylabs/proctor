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

	"github.com/sudosylabs/proctor/server/httpapi/route"
	"github.com/sudosylabs/proctor/server/httpapi/route/routetest"
	"github.com/sudosylabs/proctor/server/model"
	"github.com/sudosylabs/proctor/server/store"
)

// desktopDeviceSessions holds the Sessions a rename reads and renames.
// Rename applies the change to the stored Session; a missing or revoked
// Session is not found and a Desktop Session is a conflict, as in
// PostgreSQL.
type desktopDeviceSessions struct {
	store.SessionStore
	mu      sync.Mutex
	byID    map[model.SessionID]*model.Session
	gets    int
	renames []store.SessionRename
}

func newDesktopDeviceSessions(sessions ...*model.Session) *desktopDeviceSessions {
	s := &desktopDeviceSessions{byID: map[model.SessionID]*model.Session{}}
	for _, session := range sessions {
		s.byID[session.ID] = session
	}
	return s
}

func (s *desktopDeviceSessions) Get(_ context.Context, id model.SessionID) (*model.Session, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.gets++
	session, ok := s.byID[id]
	if !ok {
		return nil, store.NewErrNotFound("session", id.String())
	}
	copied := *session
	return &copied, nil
}

func (s *desktopDeviceSessions) ListActiveByUser(_ context.Context, userID model.UserID, at time.Time) ([]*model.Session, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var sessions []*model.Session
	for _, session := range s.byID {
		if session.UserID == userID && !session.RevokedAt.Valid {
			copied := *session
			sessions = append(sessions, &copied)
		}
	}
	return sessions, nil
}

func (s *desktopDeviceSessions) Rename(_ context.Context, input *store.SessionRename) (*model.Session, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.renames = append(s.renames, *input)
	session, ok := s.byID[input.SessionID]
	if !ok || session.UserID != input.UserID || session.RevokedAt.Valid {
		return nil, store.NewErrNotFound("session", input.SessionID.String())
	}
	if session.ClientType == model.SessionClientDesktop {
		return nil, store.NewErrConflict("session", "desktop_session_named_by_registration", nil)
	}
	session.DeviceName, session.UpdatedAt = input.DeviceName, input.AuditAt
	copied := *session
	return &copied, nil
}

// reached reports whether a rename read or wrote any Session.
func (s *desktopDeviceSessions) reached() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.gets > 0 || len(s.renames) > 0
}

// desktopDeviceRegistrations holds the caller's Desktop Registrations.
// Rename applies the change; a missing or another User's registration is not
// found and a revoked one a conflict, as in PostgreSQL.
type desktopDeviceRegistrations struct {
	store.DesktopRegistrationStore
	mu      sync.Mutex
	byID    map[model.DesktopRegistrationID]*model.DesktopRegistration
	renames []store.DesktopRegistrationRename
}

func newDesktopDeviceRegistrations(registrations ...*model.DesktopRegistration) *desktopDeviceRegistrations {
	s := &desktopDeviceRegistrations{byID: map[model.DesktopRegistrationID]*model.DesktopRegistration{}}
	for _, registration := range registrations {
		s.byID[registration.ID] = registration
	}
	return s
}

func (s *desktopDeviceRegistrations) ListByUser(_ context.Context, userID model.UserID) ([]*model.DesktopRegistration, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var registrations []*model.DesktopRegistration
	for _, registration := range s.byID {
		if registration.UserID == userID {
			copied := *registration
			registrations = append(registrations, &copied)
		}
	}
	return registrations, nil
}

func (s *desktopDeviceRegistrations) Rename(_ context.Context, input *store.DesktopRegistrationRename) (*model.DesktopRegistration, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.renames = append(s.renames, *input)
	registration, ok := s.byID[input.RegistrationID]
	if !ok || registration.UserID != input.UserID {
		return nil, store.NewErrNotFound("desktop_registration", input.RegistrationID.String())
	}
	if registration.RevokedAt.Valid {
		return nil, store.NewErrConflict("desktop_registration", "desktop_registration_revoked", nil)
	}
	registration.DisplayName = input.DisplayName
	registration.UpdatedAt, registration.RenamedAt = input.AuditAt, model.OptionalTimeFrom(input.AuditAt)
	copied := *registration
	return &copied, nil
}

func (s *desktopDeviceRegistrations) renamed() []store.DesktopRegistrationRename {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]store.DesktopRegistrationRename(nil), s.renames...)
}

// desktopDevices are the caller's Sessions and Desktop Registrations the
// real service renames.
type desktopDevices struct {
	sessions      *desktopDeviceSessions
	registrations *desktopDeviceRegistrations
	fixture       *identityFixture
}

func newDesktopDevices(t *testing.T, sessions *desktopDeviceSessions, registrations *desktopDeviceRegistrations) *desktopDevices {
	t.Helper()
	if sessions == nil {
		sessions = newDesktopDeviceSessions()
	}
	if registrations == nil {
		registrations = newDesktopDeviceRegistrations()
	}
	fixture := newIdentityFixture(t, identityOptions{Stores: identityStores{Sessions: sessions, DesktopRegistrations: registrations}})
	return &desktopDevices{sessions: sessions, registrations: registrations, fixture: fixture}
}

// renameAttempt is the one audited rename attempt.
func (d *desktopDevices) renameAttempt(t *testing.T) *model.AuditEvent {
	t.Helper()
	attempts := d.fixture.access.Audits.Attempts()
	require.Len(t, attempts, 1)
	return attempts[0]
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

func deviceRenameAPI(t *testing.T, principal model.Principal, devices *desktopDevices) http.Handler {
	t.Helper()
	logger, _ := routetest.NewLogger(t)
	cookies, err := route.NewCookies("http://localhost:8065")
	require.NoError(t, err)
	return newFocusedResourceAPI(
		t,
		logger,
		routetest.Authenticator{Principal: principal},
		sessionResource(devices.fixture.service, cookies),
		desktopRegistrationResource(devices.fixture.service),
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
	webSession := func() *model.Session {
		return &model.Session{
			ID: sessionID, UserID: principal.UserID, ClientType: model.SessionClientWeb,
			DeviceName: "Old browser", CreatedAt: now, UpdatedAt: now,
		}
	}

	t.Run("renames and returns the Session without recent authentication", func(t *testing.T) {
		t.Parallel()
		devices := newDesktopDevices(t, newDesktopDeviceSessions(webSession()), nil)
		response := serveDeviceRename(t, deviceRenameAPI(t, principal, devices), http.MethodPatch, path, `{"device_name":"  Office browser "}`)
		require.Equal(t, http.StatusOK, response.Code, response.Body.String())
		require.Equal(t, "no-store", response.Header().Get("Cache-Control"))
		attempt := devices.renameAttempt(t)
		require.Equal(t, []store.SessionRename{{
			SessionID: sessionID, UserID: principal.UserID, DeviceName: "Office browser",
			AuditEventID: attempt.ID, AuditAt: devices.sessions.renames[0].AuditAt,
		}}, devices.sessions.renames)
		require.Equal(t, string(model.ActionSessionManage), attempt.Action)
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
		desktop := webSession()
		desktop.ClientType, desktop.DesktopRegistrationID = model.SessionClientDesktop, registrationID
		devices := newDesktopDevices(t, newDesktopDeviceSessions(desktop), nil)
		response := serveDeviceRename(t, deviceRenameAPI(t, principal, devices), http.MethodPatch, path, `{"device_name":"Laptop"}`)
		require.Equal(t, http.StatusConflict, response.Code, response.Body.String())
		var problem deviceRenameProblem
		require.NoError(t, json.Unmarshal(response.Body.Bytes(), &problem))
		require.Equal(t, deviceRenameProblem{
			Code:   "session.desktop_named_by_registration",
			Fields: map[string]string{"desktop_registration_id": registrationID.String()},
		}, problem)
		require.Empty(t, devices.sessions.renames)
	})

	t.Run("presents an invalid name and another User's Session", func(t *testing.T) {
		t.Parallel()
		othersSession := webSession()
		othersSession.UserID = model.NewUserID()
		for code, test := range map[string]struct {
			session *model.Session
			body    string
			status  int
		}{
			"session.invalid":   {session: webSession(), body: `{"device_name":"   "}`, status: http.StatusBadRequest},
			"session.not_found": {session: othersSession, body: `{"device_name":"Laptop"}`, status: http.StatusNotFound},
		} {
			devices := newDesktopDevices(t, newDesktopDeviceSessions(test.session), nil)
			response := serveDeviceRename(t, deviceRenameAPI(t, principal, devices), http.MethodPatch, path, test.body)
			require.Equal(t, test.status, response.Code, "%s: %s", code, response.Body.String())
			require.Contains(t, response.Body.String(), `"code":"`+code+`"`)
			require.Empty(t, devices.sessions.renames, code)
		}
	})

	t.Run("rejects a malformed request before the use case", func(t *testing.T) {
		t.Parallel()
		devices := newDesktopDevices(t, newDesktopDeviceSessions(webSession()), nil)
		handler := deviceRenameAPI(t, principal, devices)
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
		require.False(t, devices.sessions.reached(), "a malformed rename reached the Sessions")
	})

	t.Run("refuses a Personal Access Token", func(t *testing.T) {
		t.Parallel()
		token := principal
		token.CredentialType, token.SessionID = model.CredentialPersonalAccessToken, ""
		devices := newDesktopDevices(t, newDesktopDeviceSessions(webSession()), nil)
		response := serveDeviceRename(t, deviceRenameAPI(t, token, devices), http.MethodPatch, path, `{"device_name":"Laptop"}`)
		require.Equal(t, http.StatusUnauthorized, response.Code, response.Body.String())
		require.Contains(t, response.Body.String(), `"code":"authentication.token_invalid"`)
		require.False(t, devices.sessions.reached(), "a Personal Access Token reached the Sessions")
	})
}

func TestSessionListNamesDesktopSessionsByRegistration(t *testing.T) {
	t.Parallel()
	now := time.Now().UTC()
	principal := deviceRenamePrincipal(now)
	registrationID := model.NewDesktopRegistrationID()
	webID, desktopID := model.NewSessionID(), model.NewSessionID()
	devices := newDesktopDevices(t,
		newDesktopDeviceSessions(
			&model.Session{ID: webID, UserID: principal.UserID, ClientType: model.SessionClientWeb, DeviceName: "Office browser", CreatedAt: now},
			&model.Session{ID: desktopID, UserID: principal.UserID, ClientType: model.SessionClientDesktop, DesktopRegistrationID: registrationID,
				DeviceName: "reported-host", CreatedAt: now.Add(-time.Minute)},
		),
		newDesktopDeviceRegistrations(&model.DesktopRegistration{ID: registrationID, UserID: principal.UserID, DisplayName: "Library laptop"}),
	)
	response := serveDeviceRename(t, deviceRenameAPI(t, principal, devices), http.MethodGet, "/api/v1/users/me/sessions", "")
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
	now := time.Now().UTC().Truncate(time.Millisecond)
	principal := deviceRenamePrincipal(now)
	registrationID := model.NewDesktopRegistrationID()
	path := "/api/v1/users/me/desktop-registrations/" + registrationID.String()
	registration := func() *model.DesktopRegistration {
		return &model.DesktopRegistration{
			ID: registrationID, UserID: principal.UserID, DisplayName: "reported-host",
			DesktopRelease: "1.4.0", DesktopBuildID: "build-1", Platform: model.DesktopPlatformDarwin,
			Architecture: model.DesktopArchitectureARM64, RealtimeProtocol: 1, CreatedAt: now, UpdatedAt: now, LastUsedAt: now,
		}
	}

	t.Run("renames without recent authentication and returns the registration", func(t *testing.T) {
		t.Parallel()
		devices := newDesktopDevices(t, nil, newDesktopDeviceRegistrations(registration()))
		response := serveDeviceRename(t, deviceRenameAPI(t, principal, devices), http.MethodPatch, path, `{"display_name":"Library laptop"}`)
		require.Equal(t, http.StatusOK, response.Code, response.Body.String())
		require.Equal(t, "private, no-store", response.Header().Get("Cache-Control"))
		attempt := devices.renameAttempt(t)
		renames := devices.registrations.renamed()
		require.Len(t, renames, 1)
		require.Equal(t, store.DesktopRegistrationRename{
			RegistrationID: registrationID, UserID: principal.UserID, DisplayName: "Library laptop",
			AuditEventID: attempt.ID, AuditAt: renames[0].AuditAt,
		}, renames[0])
		require.Equal(t, string(model.ActionDesktopRegistrationRename), attempt.Action)
		var body desktopRegistrationResponse
		require.NoError(t, json.Unmarshal(response.Body.Bytes(), &body))
		require.Equal(t, desktopRegistrationResponseFromModel(devices.registrations.byID[registrationID], false), body)
		require.Equal(t, "Library laptop", body.DisplayName)
	})

	t.Run("presents application refusals", func(t *testing.T) {
		t.Parallel()
		revoked := registration()
		revoked.RevokedAt = model.OptionalTimeFrom(now)
		for code, test := range map[string]struct {
			registrations *desktopDeviceRegistrations
			body          string
			status        int
		}{
			"desktop_registration.invalid": {newDesktopDeviceRegistrations(registration()), `{"display_name":"   "}`, http.StatusBadRequest},
			"desktop_registration.revoked": {newDesktopDeviceRegistrations(revoked), `{"display_name":"Laptop"}`, http.StatusConflict},
			"resource.not_found":           {newDesktopDeviceRegistrations(), `{"display_name":"Laptop"}`, http.StatusNotFound},
		} {
			devices := newDesktopDevices(t, nil, test.registrations)
			response := serveDeviceRename(t, deviceRenameAPI(t, principal, devices), http.MethodPatch, path, test.body)
			require.Equal(t, test.status, response.Code, "%s: %s", code, response.Body.String())
			require.Contains(t, response.Body.String(), `"code":"`+code+`"`)
		}
	})

	t.Run("rejects a malformed request before the use case", func(t *testing.T) {
		t.Parallel()
		devices := newDesktopDevices(t, nil, newDesktopDeviceRegistrations(registration()))
		handler := deviceRenameAPI(t, principal, devices)
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
		require.Empty(t, devices.registrations.renamed())
	})

	t.Run("refuses a Personal Access Token", func(t *testing.T) {
		t.Parallel()
		token := principal
		token.CredentialType, token.SessionID = model.CredentialPersonalAccessToken, ""
		devices := newDesktopDevices(t, nil, newDesktopDeviceRegistrations(registration()))
		response := serveDeviceRename(t, deviceRenameAPI(t, token, devices), http.MethodPatch, path, `{"display_name":"Laptop"}`)
		require.Equal(t, http.StatusUnauthorized, response.Code, response.Body.String())
		require.Contains(t, response.Body.String(), `"code":"authentication.token_invalid"`)
		require.Empty(t, devices.registrations.renamed())
	})
}
