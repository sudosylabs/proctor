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

package app

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/sudosylabs/proctor/server/app/apperror"
	"github.com/sudosylabs/proctor/server/app/invocation"
	"github.com/sudosylabs/proctor/server/model"
	"github.com/sudosylabs/proctor/server/store"
)

// ListSessionsQuery lists the caller's active sessions.
type ListSessionsQuery struct{}

// RevokeSessionCommand revokes one of the caller's sessions by ID.
type RevokeSessionCommand struct {
	SessionID model.SessionID
}

// RevokeAllSessionsCommand revokes every active session for the caller.
type RevokeAllSessionsCommand struct{}

// RevokeOtherSessionsCommand revokes every active session for the caller
// except the Session making the request.
type RevokeOtherSessionsCommand struct{}

// RevokeOtherSessionsResult reports how many of the caller's other Sessions
// this request revoked.
type RevokeOtherSessionsResult struct {
	RevokedSessionCount int
}

// DPoPRequestProof carries the HTTP proof inputs that must be verified for a
// Desktop-bound credential. A nil value means the credential was not
// presented with the DPoP authorization scheme.
type DPoPRequestProof struct {
	Proof  string
	Method string
	Path   string
}

// RefreshSessionCommand rotates access/refresh credentials for a valid refresh token.
type RefreshSessionCommand struct {
	RefreshToken string
	DPoP         *DPoPRequestProof
}

// LogoutCommand ends the caller's current session.
type LogoutCommand struct{}

// selfSessionServiceDependencies are the collaborators of selfSessionService;
// newSelfSessionService validates them once and selfSessionService keeps them in deps.
type selfSessionServiceDependencies struct {
	Sessions store.SessionStore
	Audit    mutationAuditor
	Effects  sessionRevocationEffects
	Now      func() time.Time
}

type selfSessionService struct {
	deps selfSessionServiceDependencies
}

func newSelfSessionService(deps selfSessionServiceDependencies) (*selfSessionService, error) {
	if deps.Sessions == nil {
		return nil, errors.New("self-session store is required")
	}
	if deps.Audit == nil {
		return nil, errors.New("self-session audit is required")
	}
	if deps.Effects == nil {
		return nil, errors.New("self-session effects are required")
	}
	if deps.Now == nil {
		return nil, errors.New("self-session clock is required")
	}
	return &selfSessionService{deps: deps}, nil
}

func (s *selfSessionService) ListSessions(
	ctx context.Context,
	invocation invocation.Invocation,
	_ ListSessionsQuery,
) ([]*model.Session, error) {
	principal := invocation.Principal()
	if principal.Validate() != nil {
		return nil, invalidTokenAppError()
	}
	sessions, err := s.deps.Sessions.ListActiveByUser(
		ctx,
		principal.UserID,
		model.TimeUTC(s.deps.Now()),
	)
	if err != nil {
		return nil, authenticationUnavailable(err)
	}
	return sessions, nil
}

func (s *selfSessionService) RevokeSession(
	ctx context.Context,
	invocation invocation.Invocation,
	command RevokeSessionCommand,
) error {
	principal := invocation.Principal()
	if principal.Validate() != nil {
		return invalidTokenAppError()
	}
	sessionID := strings.TrimSpace(command.SessionID.String())
	if !model.SessionID(sessionID).IsValid() {
		return apperror.New("session.id.invalid").WithField("field", "session_id")
	}
	session, err := s.deps.Sessions.Get(ctx, model.SessionID(sessionID))
	if err != nil {
		if store.IsNotFound(err) {
			return apperror.New("session.not_found")
		}
		return authenticationUnavailable(err)
	}
	if session.UserID != principal.UserID {
		return apperror.New("session.not_found")
	}

	revocations := sessionRevocationCoordinator{sessions: s.deps.Sessions, audit: s.deps.Audit, effects: s.deps.Effects, now: s.deps.Now}
	return revocations.revokeOne(ctx,
		mutationAttempt{
			Invocation: invocation, Action: model.ActionSessionManage,
			Resource:  model.Resource{Type: model.ResourceUser, ID: principal.UserID.String()},
			Operation: "revoke_own_session",
			Value:     map[string]any{"session_id": session.ID.String()},
		},
		&store.SessionRevocation{
			SessionID: session.ID, UserID: principal.UserID,
			Reason: model.SessionRevocationUserSession,
		},
		selfSessionRevocationStoreErrors.Translate,
	)
}

func (s *selfSessionService) RevokeAllSessions(
	ctx context.Context,
	invocation invocation.Invocation,
	_ RevokeAllSessionsCommand,
) error {
	principal := invocation.Principal()
	if principal.Validate() != nil {
		return invalidTokenAppError()
	}
	revocations := sessionRevocationCoordinator{sessions: s.deps.Sessions, audit: s.deps.Audit, effects: s.deps.Effects, now: s.deps.Now}
	return revocations.revokeAll(ctx,
		mutationAttempt{
			Invocation: invocation, Action: model.ActionSessionManage,
			Resource:  model.Resource{Type: model.ResourceUser, ID: principal.UserID.String()},
			Operation: "revoke_own_sessions",
		},
		&store.UserSessionsRevocation{
			UserID: principal.UserID, Reason: model.SessionRevocationUserAllSessions,
		},
		authenticationUnavailable, nil,
	)
}

// RevokeOtherSessions revokes every live Session of the caller except the
// one making the request, which stays signed in. The principal must hold a
// Session; the kept Session is recorded in the audit result.
func (s *selfSessionService) RevokeOtherSessions(
	ctx context.Context,
	invocation invocation.Invocation,
	_ RevokeOtherSessionsCommand,
) (*RevokeOtherSessionsResult, error) {
	principal := invocation.Principal()
	if principal.Validate() != nil || !principal.SessionID.IsValid() {
		return nil, invalidTokenAppError()
	}
	revocations := sessionRevocationCoordinator{sessions: s.deps.Sessions, audit: s.deps.Audit, effects: s.deps.Effects, now: s.deps.Now}
	count, err := revocations.revokeAllCounted(ctx,
		mutationAttempt{
			Invocation: invocation, Action: model.ActionSessionManage,
			Resource:  model.Resource{Type: model.ResourceUser, ID: principal.UserID.String()},
			Operation: "revoke_other_sessions",
			Value:     map[string]any{"kept_session_id": principal.SessionID.String()},
		},
		&store.UserSessionsRevocation{
			UserID: principal.UserID, KeepSessionID: principal.SessionID,
			Reason: model.SessionRevocationUserOtherSessions,
		},
		selfOtherSessionsRevocationStoreErrors.Translate, nil,
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
