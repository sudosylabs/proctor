// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package identity

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/sudosylabs/proctor/server/app/access"
	"github.com/sudosylabs/proctor/server/app/apperror"
	"github.com/sudosylabs/proctor/server/app/invocation"
	"github.com/sudosylabs/proctor/server/app/realtime"
	"github.com/sudosylabs/proctor/server/app/realtime/realtimetest"
	"github.com/sudosylabs/proctor/server/model"
	"github.com/sudosylabs/proctor/server/store"
)

// renameSessionStoreFake serves one Session and records each rename.
type renameSessionStoreFake struct {
	store.SessionStore
	events    *[]string
	session   *model.Session
	sessions  []*model.Session
	renames   []store.SessionRename
	renameErr error
}

func (s *renameSessionStoreFake) Get(context.Context, model.SessionID) (*model.Session, error) {
	*s.events = append(*s.events, "get")
	if s.session == nil {
		return nil, store.NewErrNotFound("session", "")
	}
	return s.session, nil
}

func (s *renameSessionStoreFake) ListActiveByUser(context.Context, model.UserID, time.Time) ([]*model.Session, error) {
	*s.events = append(*s.events, "list sessions")
	return s.sessions, nil
}

func (s *renameSessionStoreFake) Rename(_ context.Context, input *store.SessionRename) (*model.Session, error) {
	*s.events = append(*s.events, "rename")
	s.renames = append(s.renames, *input)
	if s.renameErr != nil {
		return nil, s.renameErr
	}
	renamed := *s.session
	renamed.DeviceName = input.DeviceName
	return &renamed, nil
}

// renameDesktopRegistrationStoreFake records each rename and answers the
// caller's registrations.
type renameDesktopRegistrationStoreFake struct {
	store.DesktopRegistrationStore
	events        *[]string
	registrations []*model.DesktopRegistration
	renames       []store.DesktopRegistrationRename
	renameErr     error
}

func (s *renameDesktopRegistrationStoreFake) ListByUser(context.Context, model.UserID) ([]*model.DesktopRegistration, error) {
	*s.events = append(*s.events, "list registrations")
	return s.registrations, nil
}

func (s *renameDesktopRegistrationStoreFake) Rename(
	_ context.Context,
	input *store.DesktopRegistrationRename,
) (*model.DesktopRegistration, error) {
	*s.events = append(*s.events, "rename")
	s.renames = append(s.renames, *input)
	if s.renameErr != nil {
		return nil, s.renameErr
	}
	return &model.DesktopRegistration{ID: input.RegistrationID, UserID: input.UserID, DisplayName: input.DisplayName}, nil
}

type deviceRenameFixture struct {
	now           time.Time
	principal     model.Principal
	events        []string
	sessions      *renameSessionStoreFake
	registrations *renameDesktopRegistrationStoreFake
	auditor       *mutationAttemptAuditorFake
	publisher     *realtimetest.Publisher
	service       *Service
}

// newDeviceRenameFixture authenticated its Session a day ago, so nothing it
// does can rely on recent authentication.
func newDeviceRenameFixture() *deviceRenameFixture {
	now := time.Date(2026, 8, 12, 9, 30, 0, 0, time.UTC)
	f := &deviceRenameFixture{now: now, principal: selfSessionPrincipal(now.Add(-24 * time.Hour))}
	f.sessions = &renameSessionStoreFake{events: &f.events}
	f.registrations = &renameDesktopRegistrationStoreFake{events: &f.events}
	f.auditor = &mutationAttemptAuditorFake{events: &f.events, beginID: model.NewID()}
	f.publisher = &realtimetest.Publisher{Observe: func(call realtimetest.Call) {
		f.events = append(f.events, string(call.Method)+" "+call.Event.Name)
	}}
	f.service = &Service{
		sessions: f.sessions, desktopRegistrations: f.registrations, auditor: f.auditor,
		publisher: f.publisher, recentAuthenticationTTL: 5 * time.Minute,
		now: func() time.Time { return now },
	}
	return f
}

func (f *deviceRenameFixture) inv() invocation.Invocation {
	return invocation.New(f.principal, model.RequestMetadata{})
}

func TestRenameSessionRenamesTheCallersOwnSessionUnderAudit(t *testing.T) {
	t.Parallel()
	f := newDeviceRenameFixture()
	f.sessions.session = &model.Session{ID: model.NewSessionID(), UserID: f.principal.UserID, ClientType: model.SessionClientCLI}

	renamed, err := f.service.RenameSession(context.Background(), f.inv(), RenameSessionCommand{
		SessionID: f.sessions.session.ID, DeviceName: "  Build \u202Eserver\u202C ",
	})
	require.NoError(t, err)
	require.Equal(t, "Build server", renamed.DeviceName)
	// Contract (guardrail 8): the audit attempt begins before the durable
	// rename, which completes it; the User's connections hear of it after.
	require.Equal(t, []string{"get", "begin", "rename", "Publish current_user.sessions.changed"}, f.events)
	sessionsChanged, err := realtime.NewCurrentUserSessionsChangedEvent(f.principal.UserID)
	require.NoError(t, err)
	require.Equal(t, []realtimetest.Call{{Method: realtimetest.Publish, Event: sessionsChanged}}, f.publisher.Calls())
	require.Equal(t, []store.SessionRename{{
		SessionID: f.sessions.session.ID, UserID: f.principal.UserID, DeviceName: "Build server",
		AuditEventID: model.AuditEventID(f.auditor.beginID), AuditAt: f.now,
	}}, f.sessions.renames)
	require.Equal(t, access.Mutation{
		Invocation: f.inv(),
		Action:     model.ActionSessionManage,
		Resource:   model.Resource{Type: model.ResourceUser, ID: f.principal.UserID.String()},
		Operation:  model.AuditOperationRenameOwnSession,
		Value:      map[string]any{"session_id": f.sessions.session.ID.String()},
		At:         f.now,
	}, f.auditor.attempt)
	event, ok := model.AuditSelfEventFor(string(f.auditor.attempt.Action), f.auditor.attempt.Operation, model.AuditStatusSuccess)
	require.True(t, ok)
	require.Equal(t, model.AuditSelfEventSessionRenamed, event)
}

func TestRenameSessionRefusesBeforeAudit(t *testing.T) {
	t.Parallel()
	registrationID := model.NewDesktopRegistrationID()
	for name, test := range map[string]struct {
		session *model.Session
		command func(*deviceRenameFixture) RenameSessionCommand
		token   bool
		code    string
		fields  map[string]string
		events  []string
	}{
		"another User's Session": {
			session: &model.Session{ID: model.NewSessionID(), UserID: model.NewUserID(), ClientType: model.SessionClientWeb},
			code:    "session.not_found", events: []string{"get"},
		},
		"a missing Session": {code: "session.not_found", events: []string{"get"}},
		"a Desktop Session": {
			session: &model.Session{ClientType: model.SessionClientDesktop, DesktopRegistrationID: registrationID},
			code:    "session.desktop_named_by_registration", fields: map[string]string{"desktop_registration_id": registrationID.String()},
			events: []string{"get"},
		},
		"a blank name": {
			command: func(f *deviceRenameFixture) RenameSessionCommand {
				return RenameSessionCommand{SessionID: f.principal.SessionID, DeviceName: " \t "}
			},
			code: "session.invalid", fields: map[string]string{"field": "device_name"},
		},
		"a name over 128 characters": {
			command: func(f *deviceRenameFixture) RenameSessionCommand {
				return RenameSessionCommand{SessionID: f.principal.SessionID, DeviceName: strings.Repeat("x", model.SessionDeviceNameMaxRunes+1)}
			},
			code: "session.invalid", fields: map[string]string{"field": "device_name"},
		},
		"a malformed Session ID": {
			command: func(*deviceRenameFixture) RenameSessionCommand {
				return RenameSessionCommand{SessionID: "not-an-id", DeviceName: "Laptop"}
			},
			code: "session.id.invalid", fields: map[string]string{"field": "session_id"},
		},
		"a Personal Access Token": {token: true, code: "authentication.session_required"},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			f := newDeviceRenameFixture()
			if test.token {
				f.principal.CredentialType, f.principal.SessionID = model.CredentialPersonalAccessToken, ""
			}
			if test.session != nil {
				if test.session.UserID.IsZero() {
					test.session.UserID = f.principal.UserID
				}
				if test.session.ID.IsZero() {
					test.session.ID = model.NewSessionID()
				}
				f.sessions.session = test.session
			}
			command := RenameSessionCommand{SessionID: model.NewSessionID(), DeviceName: "Laptop"}
			if test.command != nil {
				command = test.command(f)
			}

			_, err := f.service.RenameSession(context.Background(), f.inv(), command)
			failure, ok := apperror.As(err)
			require.True(t, ok, "error = %v", err)
			require.Equal(t, test.code, failure.Code())
			if test.fields != nil {
				require.Equal(t, test.fields, failure.Fields())
			}
			require.Equal(t, test.events, f.events)
		})
	}
}

func TestRenameSessionFailsItsAuditWhenTheStoreRefuses(t *testing.T) {
	t.Parallel()
	for name, test := range map[string]struct {
		err  error
		code string
	}{
		"Session no longer live": {store.NewErrNotFound("session", ""), "session.not_found"},
		"Store unavailable":      {errors.New("connection reset"), "authentication.internal"},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			f := newDeviceRenameFixture()
			f.sessions.session = &model.Session{ID: model.NewSessionID(), UserID: f.principal.UserID, ClientType: model.SessionClientWeb}
			f.sessions.renameErr = test.err

			_, err := f.service.RenameSession(context.Background(), f.inv(), RenameSessionCommand{SessionID: f.sessions.session.ID, DeviceName: "Laptop"})
			require.True(t, apperror.Is(err, apperror.Code(test.code)), "error = %v", err)
			require.Equal(t, []string{"get", "begin", "rename", "fail"}, f.events)
			require.Equal(t, f.auditor.beginID, f.auditor.failID)
			require.Equal(t, test.code, f.auditor.failCode)
		})
	}
}

func TestRenameDesktopRegistrationRenamesUnderAuditWithoutRecentAuthentication(t *testing.T) {
	t.Parallel()
	f := newDeviceRenameFixture()
	registrationID := model.NewDesktopRegistrationID()

	renamed, err := f.service.RenameDesktopRegistration(context.Background(), f.inv(), RenameDesktopRegistrationCommand{
		RegistrationID: registrationID, DisplayName: " Library laptop\u2066 ",
	})
	require.NoError(t, err)
	require.Equal(t, "Library laptop", renamed.DisplayName)
	require.Equal(t, []string{"begin", "rename", "Publish current_user.sessions.changed"}, f.events)
	sessionsChanged, err := realtime.NewCurrentUserSessionsChangedEvent(f.principal.UserID)
	require.NoError(t, err)
	require.Equal(t, []realtimetest.Call{{Method: realtimetest.Publish, Event: sessionsChanged}}, f.publisher.Calls())
	require.Equal(t, []store.DesktopRegistrationRename{{
		RegistrationID: registrationID, UserID: f.principal.UserID, DisplayName: "Library laptop",
		AuditEventID: model.AuditEventID(f.auditor.beginID), AuditAt: f.now,
	}}, f.registrations.renames)
	require.Equal(t, access.Mutation{
		Invocation: f.inv(),
		Action:     model.ActionDesktopRegistrationRename,
		Resource:   model.Resource{Type: model.ResourceUser, ID: f.principal.UserID.String()},
		Operation:  model.AuditOperationRenameDesktopRegistration,
		Value:      map[string]any{"registration_id": registrationID.String()},
		At:         f.now,
	}, f.auditor.attempt)
	event, ok := model.AuditSelfEventFor(string(f.auditor.attempt.Action), f.auditor.attempt.Operation, model.AuditStatusSuccess)
	require.True(t, ok)
	require.Equal(t, model.AuditSelfEventDesktopRegistrationRenamed, event)
}

func TestRenameDesktopRegistrationRefusals(t *testing.T) {
	t.Parallel()
	for name, test := range map[string]struct {
		command  func() RenameDesktopRegistrationCommand
		token    bool
		storeErr error
		code     string
		events   []string
	}{
		"a blank name": {
			command: func() RenameDesktopRegistrationCommand {
				return RenameDesktopRegistrationCommand{RegistrationID: model.NewDesktopRegistrationID(), DisplayName: "\u202E"}
			},
			code: "desktop_registration.invalid",
		},
		"a control character": {
			command: func() RenameDesktopRegistrationCommand {
				return RenameDesktopRegistrationCommand{RegistrationID: model.NewDesktopRegistrationID(), DisplayName: "Lab\x07laptop"}
			},
			code: "desktop_registration.invalid",
		},
		"a malformed registration ID": {
			command: func() RenameDesktopRegistrationCommand {
				return RenameDesktopRegistrationCommand{RegistrationID: "not-an-id", DisplayName: "Laptop"}
			},
			code: "request.invalid",
		},
		"a Personal Access Token":         {token: true, code: "authentication.session_required"},
		"another User's registration":     {storeErr: store.NewErrNotFound("desktop_registration", ""), code: "resource.not_found", events: []string{"begin", "rename", "fail"}},
		"a revoked registration":          {storeErr: store.NewErrConflict("desktop_registration", "desktop_registration_revoked", nil), code: "desktop_registration.revoked", events: []string{"begin", "rename", "fail"}},
		"an unavailable Store":            {storeErr: errors.New("connection reset"), code: "desktop_registration.unavailable", events: []string{"begin", "rename", "fail"}},
		"an unavailable audit (no write)": {code: "audit.unavailable", events: []string{"begin"}},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			f := newDeviceRenameFixture()
			if test.token {
				f.principal.CredentialType, f.principal.SessionID = model.CredentialPersonalAccessToken, ""
			}
			if test.code == "audit.unavailable" {
				f.auditor.beginErr = apperror.New("audit.unavailable")
			}
			f.registrations.renameErr = test.storeErr
			command := RenameDesktopRegistrationCommand{RegistrationID: model.NewDesktopRegistrationID(), DisplayName: "Laptop"}
			if test.command != nil {
				command = test.command()
			}

			_, err := f.service.RenameDesktopRegistration(context.Background(), f.inv(), command)
			require.True(t, apperror.Is(err, apperror.Code(test.code)), "error = %v", err)
			require.Equal(t, test.events, f.events)
			if test.storeErr != nil {
				require.Equal(t, test.code, f.auditor.failCode)
			}
		})
	}
}

func TestListSessionsNamesDesktopSessionsByTheirRegistration(t *testing.T) {
	t.Parallel()
	t.Run("a Desktop Session reads the caller's registrations once", func(t *testing.T) {
		t.Parallel()
		f := newDeviceRenameFixture()
		first, second := model.NewDesktopRegistrationID(), model.NewDesktopRegistrationID()
		f.sessions.sessions = []*model.Session{
			{ID: model.NewSessionID(), UserID: f.principal.UserID, ClientType: model.SessionClientDesktop, DesktopRegistrationID: first},
			{ID: model.NewSessionID(), UserID: f.principal.UserID, ClientType: model.SessionClientDesktop, DesktopRegistrationID: second},
			{ID: model.NewSessionID(), UserID: f.principal.UserID, ClientType: model.SessionClientWeb},
		}
		f.registrations.registrations = []*model.DesktopRegistration{
			{ID: first, DisplayName: "Library laptop"}, {ID: second, DisplayName: "Home desktop"},
		}

		listed, err := f.service.ListSessions(context.Background(), f.inv())
		require.NoError(t, err)
		require.Equal(t, f.sessions.sessions, listed.Sessions)
		require.Equal(t, map[model.DesktopRegistrationID]string{first: "Library laptop", second: "Home desktop"}, listed.DesktopRegistrationNames)
		require.Equal(t, []string{"list sessions", "list registrations"}, f.events)
	})

	t.Run("without a Desktop Session no registration is read", func(t *testing.T) {
		t.Parallel()
		f := newDeviceRenameFixture()
		f.sessions.sessions = []*model.Session{{ID: model.NewSessionID(), UserID: f.principal.UserID, ClientType: model.SessionClientWeb}}

		listed, err := f.service.ListSessions(context.Background(), f.inv())
		require.NoError(t, err)
		require.Empty(t, listed.DesktopRegistrationNames)
		require.Equal(t, []string{"list sessions"}, f.events)
	})
}
