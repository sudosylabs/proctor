// ---------------------------------------------------------------------------------------------
// Copyright (c) 2015-present Mattermost, Inc. All Rights Reserved.
// Modifications Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------
//
// Adapted from Mattermost server/channels/app/session.go and
// server/channels/app/platform/session.go. Proctor retains application-owned
// session listing and revocation, while adapting cache invalidation to split,
// hashed access/refresh credentials and enforcing self-service ownership at
// the use-case boundary.

package identity

import (
	"context"
	"slices"
	"strings"

	"github.com/sudosylabs/proctor/server/app/access"
	"github.com/sudosylabs/proctor/server/app/apperror"
	"github.com/sudosylabs/proctor/server/app/invocation"
	"github.com/sudosylabs/proctor/server/model"
	"github.com/sudosylabs/proctor/server/store"
)

// RevokeOtherSessionsResult reports how many of the caller's other Sessions
// this request revoked.
type RevokeOtherSessionsResult struct {
	RevokedSessionCount int
}

// ListSessionsResult is the caller's active Sessions with the name of the
// Desktop Registration each Desktop Session is bound to.
type ListSessionsResult struct {
	Sessions []*model.Session
	// DesktopRegistrationNames holds the display name of every Desktop
	// Registration a listed Desktop Session is bound to, keyed by its ID.
	DesktopRegistrationNames map[model.DesktopRegistrationID]string
}

// ListSessions lists the caller's active Sessions. A Desktop Session is named
// by its Desktop Registration, so the names are read with the list in one
// further read of the caller's registrations, only when a Desktop Session is
// listed.
func (s *Service) ListSessions(
	ctx context.Context,
	inv invocation.Invocation,
) (*ListSessionsResult, error) {
	principal := inv.Principal()
	if principal.Validate() != nil {
		return nil, access.InvalidTokenAppError()
	}
	sessions, err := s.sessions.ListActiveByUser(
		ctx,
		principal.UserID,
		model.TimeUTC(s.now()),
	)
	if err != nil {
		return nil, authenticationUnavailable(err)
	}

	result := &ListSessionsResult{Sessions: sessions, DesktopRegistrationNames: map[model.DesktopRegistrationID]string{}}
	if !slices.ContainsFunc(sessions, func(session *model.Session) bool {
		return session.ClientType == model.SessionClientDesktop
	}) {
		return result, nil
	}

	registrations, err := s.desktopRegistrations.ListByUser(ctx, principal.UserID)
	if err != nil {
		return nil, authenticationUnavailable(err)
	}
	for _, registration := range registrations {
		result.DesktopRegistrationNames[registration.ID] = registration.DisplayName
	}
	return result, nil
}

type RenameSessionCommand struct {
	SessionID  model.SessionID
	DeviceName string
}

// RenameSession gives one of the caller's own live browser or CLI Sessions
// the name they chose. A Desktop Session is named by its Desktop
// Registration, so renaming one is refused with a pointer to that
// registration. A rename changes no credential, so any interactive Session
// may make it.
func (s *Service) RenameSession(
	ctx context.Context,
	inv invocation.Invocation,
	command RenameSessionCommand,
) (*model.Session, error) {
	principal := inv.Principal()
	at := model.TimeUTC(s.now())
	if err := requireInteractiveSession(principal, false, at, s.recentAuthenticationTTL); err != nil {
		return nil, err
	}

	if !command.SessionID.IsValid() {
		return nil, apperror.Invalid("session.id.invalid", "session_id")
	}
	deviceName, err := model.NormalizeSessionDeviceName(command.DeviceName)
	if err != nil {
		return nil, apperror.Invalid("session.invalid", "device_name").Wrap(err)
	}

	session, err := s.sessions.Get(ctx, command.SessionID)
	if err != nil {
		if store.IsNotFound(err) {
			return nil, apperror.New("session.not_found")
		}
		return nil, authenticationUnavailable(err)
	}
	if session.UserID != principal.UserID {
		return nil, apperror.New("session.not_found")
	}
	if session.ClientType == model.SessionClientDesktop {
		return nil, apperror.New("session.desktop_named_by_registration").
			WithField("desktop_registration_id", session.DesktopRegistrationID.String())
	}

	renamed, err := access.RunAudited(
		ctx,
		s.auditor,
		access.Mutation{
			Invocation: inv,
			Action:     model.ActionSessionManage,
			Resource:   model.Resource{Type: model.ResourceUser, ID: principal.UserID.String()},
			Operation:  model.AuditOperationRenameOwnSession,
			Value:      map[string]any{"session_id": session.ID.String()},
			At:         at,
		},
		func(ctx context.Context, reference access.MutationReference) (*model.Session, error) {
			return s.sessions.Rename(ctx, &store.SessionRename{
				SessionID:    session.ID,
				UserID:       principal.UserID,
				DeviceName:   deviceName,
				AuditEventID: reference.ID,
				AuditAt:      reference.MutationAt,
			})
		},
		selfSessionRenameStoreErrors.Translate,
	)
	if err != nil {
		return nil, err
	}
	s.publishSessionsChanged(ctx, principal.UserID, "session.renamed")
	return renamed, nil
}

// RevokeSession revokes one of the caller's own Sessions.
func (s *Service) RevokeSession(
	ctx context.Context,
	inv invocation.Invocation,
	id model.SessionID,
) error {
	principal := inv.Principal()
	if principal.Validate() != nil {
		return access.InvalidTokenAppError()
	}

	sessionID := strings.TrimSpace(id.String())
	if !model.SessionID(sessionID).IsValid() {
		return apperror.New("session.id.invalid").WithField("field", "session_id")
	}

	session, err := s.sessions.Get(ctx, model.SessionID(sessionID))
	if err != nil {
		if store.IsNotFound(err) {
			return apperror.New("session.not_found")
		}
		return authenticationUnavailable(err)
	}

	if session.UserID != principal.UserID {
		return apperror.New("session.not_found")
	}

	return s.revokeSession(
		ctx,
		access.Mutation{
			Invocation: inv,
			Action:     model.ActionSessionManage,
			Resource:   model.Resource{Type: model.ResourceUser, ID: principal.UserID.String()},
			Operation:  "revoke_own_session",
			Value:      map[string]any{"session_id": session.ID.String()},
			At:         s.now(),
		},
		&store.SessionRevocation{
			SessionID: session.ID,
			UserID:    principal.UserID,
			Reason:    model.SessionRevocationUserSession,
		},
		selfSessionRevocationStoreErrors.Translate,
	)
}

// RevokeAllSessions revokes every active Session of the caller.
func (s *Service) RevokeAllSessions(
	ctx context.Context,
	inv invocation.Invocation,
) error {
	principal := inv.Principal()
	if principal.Validate() != nil {
		return access.InvalidTokenAppError()
	}

	return s.revokeAllSessions(
		ctx,
		access.Mutation{
			Invocation: inv,
			Action:     model.ActionSessionManage,
			Resource:   model.Resource{Type: model.ResourceUser, ID: principal.UserID.String()},
			Operation:  "revoke_own_sessions",
			At:         s.now(),
		},
		&store.UserSessionsRevocation{
			UserID: principal.UserID,
			Reason: model.SessionRevocationUserAllSessions,
		},
		authenticationUnavailable,
		nil,
	)
}

// RevokeOtherSessions revokes every live Session of the caller except the
// one making the request, which stays signed in. The principal must hold a
// Session; the kept Session is recorded in the audit result.
func (s *Service) RevokeOtherSessions(
	ctx context.Context,
	inv invocation.Invocation,
) (*RevokeOtherSessionsResult, error) {
	principal := inv.Principal()
	if principal.Validate() != nil || !principal.SessionID.IsValid() {
		return nil, access.InvalidTokenAppError()
	}

	count, err := s.revokeAllSessionsCounted(
		ctx,
		access.Mutation{
			Invocation: inv,
			Action:     model.ActionSessionManage,
			Resource:   model.Resource{Type: model.ResourceUser, ID: principal.UserID.String()},
			Operation:  "revoke_other_sessions",
			Value:      map[string]any{"kept_session_id": principal.SessionID.String()},
			At:         s.now(),
		},
		&store.UserSessionsRevocation{
			UserID:        principal.UserID,
			KeepSessionID: principal.SessionID,
			Reason:        model.SessionRevocationUserOtherSessions,
		},
		selfOtherSessionsRevocationStoreErrors.Translate,
		nil,
	)
	if err != nil {
		return nil, err
	}

	return &RevokeOtherSessionsResult{RevokedSessionCount: count}, nil
}

// A kept Session that is no longer live means the caller's own credential
// stopped being valid during the request.
var selfOtherSessionsRevocationStoreErrors = apperror.StoreTable{
	NotFound:    "authentication.token_invalid",
	Unavailable: "authentication.internal",
}

// A Session that stopped being live, or a Desktop Session, after the read
// above is reported as the read would have reported it.
var selfSessionRenameStoreErrors = apperror.StoreTable{
	NotFound:    "session.not_found",
	Conflict:    "session.desktop_named_by_registration",
	Unavailable: "authentication.internal",
}

var selfSessionRevocationStoreErrors = apperror.StoreTable{
	NotFound:    "session.not_found",
	Unavailable: "authentication.internal",
}
