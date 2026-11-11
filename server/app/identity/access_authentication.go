// ---------------------------------------------------------------------------------------------
// Copyright (c) 2015-present Mattermost, Inc. All Rights Reserved.
// Modifications Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------
//
// Adapted from Mattermost server/channels/app/authentication.go,
// server/channels/app/login.go, and server/channels/app/session.go. Proctor
// keeps the single application authentication flow, generic login failures,
// server-side revocable sessions, activity debouncing, and boundary-safe
// errors while using split hashed access/refresh credentials and rotation.

package identity

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/sudosylabs/proctor/server/app/access"
	"github.com/sudosylabs/proctor/server/app/apperror"
	"github.com/sudosylabs/proctor/server/logging"
	"github.com/sudosylabs/proctor/server/model"
	"github.com/sudosylabs/proctor/server/store"
)

const activityCachePrefix = "authentication/activity/"

type resolvedAuthentication struct {
	Credential *model.SessionCredential
	Session    *model.Session
	User       *model.User
}

// AuthenticateAccess resolves a session access token into its Principal.
func (s *Service) AuthenticateAccess(
	ctx context.Context,
	rawToken string,
) (*model.Principal, error) {
	principal, err := s.authenticateAccess(ctx, rawToken)
	recordOperational(s.operational, "authentication", "access_token", err)
	return principal, err
}

// AuthenticateBearer accepts the two Authorization-header credential classes:
// a CLI session access credential or a personal access token. Cookie
// authentication remains session-only and therefore calls AuthenticateAccess.
func (s *Service) AuthenticateBearer(
	ctx context.Context,
	rawToken string,
) (*model.Principal, error) {
	principal, err := s.authenticateAccess(ctx, rawToken)
	if err == nil {
		recordOperational(s.operational, "authentication", "bearer", nil)
		return principal, nil
	}
	if failure, ok := apperror.As(err); !ok || failure.Code() != "authentication.token_invalid" {
		recordOperational(s.operational, "authentication", "bearer", err)
		return nil, err
	}
	principal, err = s.resolvePersonalAccessTokenBearer(ctx, rawToken, s.now())
	recordOperational(s.operational, "authentication", "bearer", err)
	return principal, err
}

func (s *Service) authenticateAccess(
	ctx context.Context,
	rawToken string,
) (*model.Principal, error) {
	if !validRawCredential(rawToken) {
		return nil, access.InvalidTokenAppError()
	}

	now := model.TimeUTC(s.now())
	tokenHash := model.HashToken(rawToken)
	// Resolve every new authentication decision from the authoritative Store.
	// Positive cache entries cannot preserve access after committed revocation,
	// credential rotation, or account disablement when invalidation is lost.
	credential, session, err := s.sessionCredentials.GetSessionByTokenHash(
		ctx, tokenHash, model.SessionCredentialAccess,
	)
	if err != nil {
		if store.IsNotFound(err) {
			return nil, access.InvalidTokenAppError()
		}
		return nil, authenticationUnavailable(err)
	}

	account, err := s.users.Get(ctx, session.UserID)
	if err != nil {
		if store.IsNotFound(err) {
			return nil, access.InvalidTokenAppError()
		}
		return nil, authenticationUnavailable(err)
	}

	resolved := &resolvedAuthentication{Credential: credential, Session: session, User: account}
	if !resolved.User.IsActive() || resolved.Credential.IsExpiredAt(now) {
		return nil, access.InvalidTokenAppError()
	}

	resolved.Session, err = s.enforceSessionExpiry(ctx, resolved.Session, now)
	if err != nil {
		return nil, err
	}
	if resolved.Session.ClientType == model.SessionClientDesktop {
		return nil, access.InvalidTokenAppError()
	}
	if err := s.updateActivity(ctx, resolved, now); err != nil {
		return nil, err
	}

	principal := &model.Principal{
		UserID:                   resolved.User.ID,
		AuthenticationGeneration: resolved.Session.AuthenticationGeneration,
		MFARecoveryRequired:      resolved.Session.MFARecoveryRequired,
		MFAEnrollmentRequired:    resolved.Session.MFAEnrollmentRequired,
		SessionID:                resolved.Session.ID,
		CredentialID:             model.PrincipalCredentialID(resolved.Credential.ID),
		CredentialType:           model.CredentialSessionAccess,
		AuthenticationMethod:     resolved.Session.AuthenticationMethod,
		AuthenticationProviderID: resolved.Session.AuthenticationProviderID,
		ExternalIdentityID:       resolved.Session.ExternalIdentityID,
		AuthenticationStrength:   resolved.Session.AuthenticationStrength,
		ClientType:               resolved.Session.ClientType,
		AuthenticatedAt:          resolved.Session.AuthenticatedAt,
		ReauthenticatedAt:        resolved.Session.ReauthenticatedAt,
		MFACompletedAt:           resolved.Session.MFACompletedAt,
	}
	if principal.ValidateMFARecovery() != nil {
		return nil, authenticationUnavailable(
			errors.New("resolved principal is invalid"),
		)
	}

	return principal, nil
}

// resolvePersonalAccessTokenBearer resolves a raw personal access token to
// its CLI principal, debouncing the token's last-used write.
func (s *Service) resolvePersonalAccessTokenBearer(
	ctx context.Context,
	rawToken string,
	at time.Time,
) (*model.Principal, error) {
	if !validRawCredential(rawToken) {
		return nil, access.InvalidTokenAppError()
	}

	resolved, err := s.personalAccessTokens.Resolve(
		ctx,
		model.HashToken(rawToken),
		model.TimeUTC(at),
		s.personalAccessTokenPolicy.LastUsedUpdateInterval,
	)
	if err != nil {
		if store.IsNotFound(err) {
			return nil, access.InvalidTokenAppError()
		}
		return nil, authenticationUnavailable(err)
	}

	principal := &model.Principal{
		UserID:               resolved.User.ID,
		CredentialID:         model.PrincipalCredentialID(resolved.Token.ID),
		CredentialType:       model.CredentialPersonalAccessToken,
		AuthenticationMethod: "personal_access_token",
		ClientType:           model.SessionClientCLI,
		CredentialScopes:     append([]string(nil), resolved.Token.Scopes...),
		AcademicUnitID:       resolved.Token.AcademicUnitID,
	}
	if principal.Validate() != nil {
		s.diagnostics.WarnContext(
			ctx,
			"personal access token resolved to invalid principal",
			logging.Err(fmt.Errorf("personal_access_token_id=%s", resolved.Token.ID.String())),
		)
		return nil, access.InvalidTokenAppError()
	}

	return principal, nil
}

func (s *Service) updateActivity(
	ctx context.Context,
	resolved *resolvedAuthentication,
	now time.Time,
) error {
	now = model.TimeUTC(now)
	settings := s.sessionPolicy
	if now.Sub(resolved.Session.LastActivityAt) < settings.ActivityUpdateInterval {
		return nil
	}

	key := activityCachePrefix + resolved.Session.ID.String()
	err := s.cache.SetIfAbsent(
		ctx,
		key,
		[]byte{1},
		settings.ActivityUpdateInterval,
	)
	if errors.Is(err, ErrAuthenticationCacheNotStored) {
		return nil
	}

	if err != nil {
		s.warn(ctx, "session activity debounce cache failed", err)
		return nil
	}

	lifetimes, err := s.sessionLifetimes(ctx)
	if err != nil {
		return err
	}

	idleExpiresAt := sessionDeadline(now, lifetimes.Idle, resolved.Session.ExpiresAt)
	if err := s.sessions.UpdateActivity(
		ctx,
		&store.SessionActivityUpdate{ID: resolved.Session.ID, LastActivityAt: now, IdleExpiresAt: idleExpiresAt},
	); err != nil {
		if store.IsNotFound(err) {
			return access.InvalidTokenAppError()
		}
		return authenticationUnavailable(err)
	}

	resolved.Session.LastActivityAt = now
	resolved.Session.IdleExpiresAt = idleExpiresAt
	if resolved.Session.UpdatedAt.Before(now) {
		resolved.Session.UpdatedAt = now
	}

	return nil
}

// ValidatePrincipal revalidates the authoritative Session and User state
// behind a principal that was established earlier. WebSocket admission and
// onboarding import call it before they trust that principal again; it grants
// nothing a fresh authentication would not.
func (s *Service) ValidatePrincipal(ctx context.Context, principal model.Principal) error {
	return s.validatePrincipal(ctx, principal)
}

func (s *Service) validatePrincipal(ctx context.Context, principal model.Principal) error {
	if principal.Validate() != nil || principal.CredentialType != model.CredentialSessionAccess {
		return access.InvalidTokenAppError()
	}

	session, err := s.sessions.Get(ctx, principal.SessionID)
	if err != nil {
		if store.IsNotFound(err) {
			return access.InvalidTokenAppError()
		}
		return authenticationUnavailable(err)
	}

	if session.UserID != principal.UserID {
		return access.InvalidTokenAppError()
	}

	recovery, recoveryErr := s.mfaRecoveryState(ctx, principal.UserID)
	if recoveryErr != nil {
		return recoveryErr
	}

	if recovery.ReenrollmentRequired ||
		principal.AuthenticationGeneration != recovery.Generation ||
		session.AuthenticationGeneration != recovery.Generation ||
		session.MFARecoveryRequired ||
		session.MFAEnrollmentRequired {
		return access.InvalidTokenAppError()
	}

	session, err = s.enforceSessionExpiry(ctx, session, s.now().UTC())
	if err != nil {
		return err
	}

	if principal.HasRegisteredDesktopKey() {
		if session.DesktopRegistrationID != principal.DesktopRegistrationID ||
			session.DPoPKeyThumbprint != principal.DPoPKeyThumbprint ||
			s.desktopRegistrations == nil {
			return access.InvalidTokenAppError()
		}

		registration, registrationErr := s.desktopRegistrations.Get(ctx, principal.DesktopRegistrationID)
		if registrationErr != nil ||
			!registration.IsActive() ||
			registration.UserID != principal.UserID ||
			registration.KeyThumbprint != principal.DPoPKeyThumbprint {
			if registrationErr == nil || store.IsNotFound(registrationErr) {
				return access.InvalidTokenAppError()
			}
			return authenticationUnavailable(registrationErr)
		}
	}

	account, err := s.users.Get(ctx, principal.UserID)
	if err != nil {
		if store.IsNotFound(err) {
			return access.InvalidTokenAppError()
		}
		return authenticationUnavailable(err)
	}

	if !account.IsActive() {
		return access.InvalidTokenAppError()
	}

	return nil
}

func (s *Service) enforceSessionExpiry(
	ctx context.Context,
	session *model.Session,
	now time.Time,
) (*model.Session, error) {
	now = model.TimeUTC(now)
	if session == nil {
		return nil, access.InvalidTokenAppError()
	}
	if !session.IsExpiredAt(now) {
		return session, nil
	}

	result, err := s.sessions.EnforceExpiry(
		ctx,
		&store.SessionExpiryEnforcement{ID: session.ID, UserID: session.UserID, At: now},
	)
	if err != nil {
		if store.IsNotFound(err) {
			return nil, access.InvalidTokenAppError()
		}
		return nil, authenticationUnavailable(err)
	}

	if result == nil || result.Session == nil {
		return nil, authenticationUnavailable(errors.New("session expiry result is incomplete"))
	}
	if !result.Expired {
		return result.Session, nil
	}

	s.sessionsRevoked(
		ctx,
		result.Session.UserID,
		[]model.SessionID{result.Session.ID},
		result.TokenHashes,
	)
	return nil, access.InvalidTokenAppError()
}

func validRawCredential(token string) bool {
	return model.IsValidCredentialToken(token)
}
