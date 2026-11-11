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
	"errors"

	"github.com/sudosylabs/proctor/server/app/access"
	"github.com/sudosylabs/proctor/server/app/invocation"
	"github.com/sudosylabs/proctor/server/model"
	"github.com/sudosylabs/proctor/server/store"
)

// RefreshSessionCommand rotates access/refresh credentials for a valid refresh token.
type RefreshSessionCommand struct {
	RefreshToken string
	DPoP         *DPoPRequestProof
}

// RefreshSession rotates a Session's access and refresh credentials for a
// valid refresh token, verifying the DPoP proof of a Desktop Session.
func (s *Service) RefreshSession(
	ctx context.Context,
	_ invocation.Invocation,
	command RefreshSessionCommand,
) (_ *model.Session, _ *model.AuthenticationTokens, resultErr error) {
	defer func() { recordOperational(s.operational, "authentication", "refresh", resultErr) }()
	rawRefreshToken := command.RefreshToken
	if !validRawCredential(rawRefreshToken) {
		return nil, nil, access.InvalidTokenAppError()
	}

	_, session, err := s.sessionCredentials.GetSessionByTokenHash(
		ctx,
		model.HashToken(rawRefreshToken),
		model.SessionCredentialRefresh,
	)
	if err != nil {
		if store.IsNotFound(err) {
			return nil, nil, access.InvalidTokenAppError()
		}
		return nil, nil, authenticationUnavailable(err)
	}

	if session.ClientType == model.SessionClientDesktop {
		if command.DPoP == nil {
			return nil, nil, access.InvalidTokenAppError()
		}
		if err = s.validateDPoPRefresh(ctx, rawRefreshToken, command.DPoP.Proof, command.DPoP.Method, command.DPoP.Path); err != nil {
			return nil, nil, err
		}
	} else if command.DPoP != nil {
		return nil, nil, access.InvalidTokenAppError()
	}

	now := model.TimeUTC(s.now())
	mfaRequired := false
	if session.AuthenticationStrength != model.AuthenticationMultiFactor {
		// A User who became a role holder mid-Session, or whose Session predates the
		// policy, is restricted here; a Desktop Session is refused instead.
		mfaRequired, err = s.accessPolicy.RequiresMFA(ctx, session.UserID)
		if err != nil {
			return nil, nil, authenticationUnavailable(err)
		}
		if mfaRequired && session.ClientType != model.SessionClientWeb {
			return nil, nil, mfaEnrollmentRequiredAppError()
		}
	}

	settings := s.sessionPolicy
	lifetimes, err := s.sessionLifetimes(ctx)
	if err != nil {
		return nil, nil, err
	}

	accessToken := s.newCredential()
	refreshToken := s.newCredential()
	rotation, err := s.sessionCredentials.RotateRefresh(
		ctx,
		&store.SessionRefreshRotation{TokenHash: model.HashToken(rawRefreshToken), Access: &model.SessionCredential{
			TokenHash: model.HashToken(accessToken),
			ExpiresAt: model.TimeUTC(now.Add(settings.AccessTTL)),
		}, Refresh: &model.SessionCredential{
			TokenHash: model.HashToken(refreshToken),
			ExpiresAt: model.TimeUTC(now.Add(settings.RefreshTTL)),
		}, Now: now, IdleExpiresAt: model.TimeUTC(now.Add(lifetimes.Idle)), MFARequiredByPolicy: mfaRequired},
	)
	if err != nil {
		if errors.Is(err, store.ErrMFAEnrollmentRequired) {
			return nil, nil, mfaEnrollmentRequiredAppError()
		}

		var conflict *store.ErrConflict
		if store.IsNotFound(err) ||
			errors.As(err, &conflict) ||
			errors.Is(err, store.ErrAuthenticationGenerationChanged) ||
			errors.Is(err, store.ErrMFAReenrollmentRequired) {
			return nil, nil, access.InvalidTokenAppError()
		}

		return nil, nil, authenticationUnavailable(err)
	}

	if rotation.ReplayDetected || rotation.Expired {
		s.sessionsRevoked(
			ctx,
			rotation.Session.UserID,
			[]model.SessionID{rotation.Session.ID},
			rotation.RevokedAccessHashes,
		)
	} else {
		s.authenticationCacheInvalidated(
			ctx,
			rotation.Session.UserID.String(),
			rotation.RevokedAccessHashes,
		)
	}

	if rotation.ReplayDetected || rotation.Expired {
		return nil, nil, access.InvalidTokenAppError()
	}

	account, err := s.users.Get(ctx, rotation.Session.UserID)
	if err != nil || !account.IsActive() {
		if err == nil {
			var revokedAccessHashes []string
			revokedAccessHashes, err = s.sessions.Withdraw(
				ctx,
				&store.SessionWithdrawal{
					ID:        rotation.Session.ID,
					UserID:    rotation.Session.UserID,
					RevokedAt: now,
					Reason:    model.SessionRevocationInactiveUser,
				},
			)
			if err == nil {
				s.sessionsRevoked(
					ctx,
					rotation.Session.UserID,
					[]model.SessionID{rotation.Session.ID},
					revokedAccessHashes,
				)
			}
		}

		return nil, nil, access.InvalidTokenAppError()
	}

	return rotation.Session, &model.AuthenticationTokens{
		TokenType:        tokenTypeForSession(rotation.Session),
		AccessToken:      accessToken,
		RefreshToken:     refreshToken,
		AccessExpiresAt:  rotation.AccessCredential.ExpiresAt,
		RefreshExpiresAt: rotation.RefreshCredential.ExpiresAt,
	}, nil
}

func tokenTypeForSession(session *model.Session) string {
	if session != nil && session.ClientType == model.SessionClientDesktop {
		return "DPoP"
	}
	return "Bearer"
}
