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

// ListSessions lists the caller's active Sessions.
func (s *Service) ListSessions(
	ctx context.Context,
	inv invocation.Invocation,
) ([]*model.Session, error) {
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
	return sessions, nil
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

var selfSessionRevocationStoreErrors = apperror.StoreTable{
	NotFound:    "session.not_found",
	Unavailable: "authentication.internal",
}
