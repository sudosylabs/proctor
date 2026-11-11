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
	"time"

	"github.com/sudosylabs/proctor/server/app/apperror"
	"github.com/sudosylabs/proctor/server/model"
	"github.com/sudosylabs/proctor/server/store"
)

// SessionPolicy is the immutable session-lifetime policy consumed by
// authentication. Composition translates deployment configuration into this
// value so authentication does not depend on config.Config.
type SessionPolicy struct {
	AccessTTL              time.Duration
	RefreshTTL             time.Duration
	IdleTTL                time.Duration
	AbsoluteTTL            time.Duration
	ActivityUpdateInterval time.Duration
	MaximumPerUser         int
	// RetiredRetention is how long maintenance keeps a revoked or expired
	// Session, or a superseded credential, before deleting it.
	RetiredRetention time.Duration
}

type sessionIssuance struct {
	AuthenticationGeneration int64
	MFARecoveryRequired      bool
	ExternalLoginStateID     model.ExternalLoginStateID
	PasswordProof            store.PasswordCredentialProof
	User                     *model.User
	ClientType               model.SessionClientType
	DeviceID                 string
	DeviceName               string
	AuthenticationMethod     string
	AuthenticationProviderID string
	ExternalIdentityID       model.ExternalIdentityID
	AuthenticationStrength   model.AuthenticationStrength
	AuthenticatedAt          int64
	MFACompletedAt           int64
	// AuditEventID is the login attempt that Session creation completes
	// atomically with the Session.
	AuditEventID model.AuditEventID
}

// requireSessionAuthentication refuses a Service whose sign-in and Session
// collaborators are incomplete. New calls it once, after setting them.
func (s *Service) requireSessionAuthentication() error {
	if s.personalAccessTokens == nil {
		return errors.New("personal access token store is required")
	}
	if s.diagnostics == nil {
		return errors.New("personal access token diagnostics are required")
	}
	if s.users == nil {
		return errors.New("authentication user store is required")
	}
	if s.passwords == nil {
		return errors.New("authentication password store is required")
	}
	if s.sessions == nil {
		return errors.New("authentication session store is required")
	}
	if s.sessionCredentials == nil {
		return errors.New("authentication session credential store is required")
	}
	if s.accessPolicy == nil {
		return errors.New("authentication access policy is required")
	}
	if s.cache == nil {
		return errors.New("authentication cache is required")
	}
	if s.attempts == nil {
		return errors.New("authentication attempt accounting is required")
	}
	if s.publisher == nil {
		return errors.New("authentication security effects are required")
	}
	if s.auditor == nil {
		return errors.New("authentication audit is required")
	}
	if s.hasher == nil {
		return errors.New("password hasher is required")
	}
	if s.mfa == nil {
		return errors.New("authentication MFA verifier is required")
	}
	if s.newCredential == nil {
		return errors.New("authentication credential generator is required")
	}
	if s.now == nil {
		return errors.New("authentication clock is required")
	}
	if s.desktopRegistrations == nil || s.dpop == nil {
		return errors.New("authentication Desktop dependencies are required")
	}

	return nil
}

func (s *Service) createSession(
	ctx context.Context,
	command sessionIssuance,
) (*model.Session, *model.AuthenticationTokens, error) {
	account := command.User
	clientType := command.ClientType
	strength := command.AuthenticationStrength
	authenticatedAt := command.AuthenticatedAt
	mfaCompletedAt := command.MFACompletedAt
	if account == nil ||
		!account.IsActive() ||
		!clientType.IsValid() ||
		!strength.IsValid() ||
		authenticatedAt <= 0 {
		return nil, nil, apperror.New("authentication.session.invalid")
	}

	now := model.TimeUTC(s.now())
	nowMillis := now.UnixMilli()
	if authenticatedAt > nowMillis {
		authenticatedAt = nowMillis
	}

	if strength == model.AuthenticationMultiFactor {
		if mfaCompletedAt < authenticatedAt || mfaCompletedAt > nowMillis {
			mfaCompletedAt = authenticatedAt
		}
	} else {
		mfaCompletedAt = 0
	}

	enrollmentRequired, err := s.mfaEnrollmentRequired(ctx, account.ID, clientType, strength)
	if err != nil {
		return nil, nil, err
	}

	settings := s.sessionPolicy
	lifetimes, err := s.sessionLifetimes(ctx)
	if err != nil {
		return nil, nil, err
	}

	absoluteExpiresAt := model.TimeUTC(now.Add(lifetimes.Absolute))
	accessExpiresAt := sessionDeadline(now, settings.AccessTTL, absoluteExpiresAt)
	refreshExpiresAt := sessionDeadline(now, settings.RefreshTTL, absoluteExpiresAt)
	session := &model.Session{
		UserID:                   account.ID,
		AuthenticationGeneration: command.AuthenticationGeneration,
		MFARecoveryRequired:      command.MFARecoveryRequired,
		MFAEnrollmentRequired:    enrollmentRequired,
		ClientType:               clientType,
		DeviceID:                 command.DeviceID,
		DeviceName:               command.DeviceName,
		AuthenticationMethod:     command.AuthenticationMethod,
		AuthenticationProviderID: command.AuthenticationProviderID,
		ExternalIdentityID:       command.ExternalIdentityID,
		AuthenticationStrength:   strength,
		AuthenticatedAt:          model.TimeFromMillis(authenticatedAt),
		MFACompletedAt:           model.OptionalTimeFromMillis(mfaCompletedAt),
		LastActivityAt:           now,
		IdleExpiresAt:            sessionDeadline(now, lifetimes.Idle, absoluteExpiresAt),
		ExpiresAt:                absoluteExpiresAt,
	}
	accessToken := s.newCredential()
	refreshToken := s.newCredential()
	savedSession, credentials, saveErr := s.sessions.Save(
		ctx,
		&store.SessionCreation{
			Session:              session,
			PasswordProof:        command.PasswordProof,
			ExternalLoginStateID: command.ExternalLoginStateID,
			Credentials: []*model.SessionCredential{
				{
					Kind:      model.SessionCredentialAccess,
					TokenHash: model.HashToken(accessToken),
					ExpiresAt: accessExpiresAt,
				},
				{
					Kind:      model.SessionCredentialRefresh,
					TokenHash: model.HashToken(refreshToken),
					ExpiresAt: refreshExpiresAt,
				},
			},
			MaximumActive: settings.MaximumPerUser,
			AuditEventID:  command.AuditEventID,
			AuditAt:       now,
		},
	)
	if saveErr != nil {
		return nil, nil, sessionCreationError(command, saveErr)
	}

	if !hasAccessCredential(credentials) {
		return nil, nil, authenticationUnavailable(
			errors.New("saved session has no access credential"),
		)
	}

	return savedSession, &model.AuthenticationTokens{
		TokenType:        "Bearer",
		AccessToken:      accessToken,
		RefreshToken:     refreshToken,
		AccessExpiresAt:  accessExpiresAt,
		RefreshExpiresAt: refreshExpiresAt,
	}, nil
}

// sessionLifetimes returns the idle and absolute lengths a Session gets now:
// the deployment lifetimes, shortened by the current Access Policy.
func (s *Service) sessionLifetimes(ctx context.Context) (sessionLifetimes, error) {
	lifetimes, err := s.accessPolicy.SessionLifetimes(ctx, s.sessionPolicy)
	if err != nil {
		return sessionLifetimes{}, authenticationUnavailable(err)
	}
	return lifetimes, nil
}

// mfaEnrollmentRequired decides whether a new single-factor Session of the
// User must be restricted to MFA enrollment because the Access Policy
// requires role holders to use two-step verification. Only a Web Session can carry
// the restriction; any other client is refused and sent to the browser.
func (s *Service) mfaEnrollmentRequired(
	ctx context.Context,
	userID model.UserID,
	clientType model.SessionClientType,
	strength model.AuthenticationStrength,
) (bool, error) {
	if strength == model.AuthenticationMultiFactor {
		return false, nil
	}

	required, err := s.accessPolicy.RequiresMFA(ctx, userID)
	if err != nil {
		return false, authenticationUnavailable(err)
	}
	if required && clientType != model.SessionClientWeb {
		return false, mfaEnrollmentRequiredAppError()
	}

	return required, nil
}

// sessionCreationError maps a Session persistence failure to the public authentication error.
func sessionCreationError(command sessionIssuance, saveErr error) error {
	if errors.Is(saveErr, store.ErrAuthenticationMethodDisabled) ||
		errors.Is(saveErr, store.ErrPasswordCredentialChanged) ||
		errors.Is(saveErr, store.ErrAuthenticationGenerationChanged) ||
		errors.Is(saveErr, store.ErrMFAReenrollmentRequired) {
		if command.AuthenticationProviderID != "" {
			return invalidExternalAuthenticationError()
		}
		return invalidCredentialsAppError()
	}

	var conflict *store.ErrConflict
	if errors.As(saveErr, &conflict) {
		switch conflict.Constraint {
		case "sessions_maximum_per_user":
			return apperror.New("authentication.sessions.maximum_reached")
		case "active_attempt_session_lock":
			return apperror.New("authentication.desktop_authorization.account_session_locked")
		}
	}

	return authenticationUnavailable(saveErr)
}

// hasAccessCredential reports whether the saved Session credentials include an access credential.
func hasAccessCredential(credentials []*model.SessionCredential) bool {
	for _, savedCredential := range credentials {
		if savedCredential.Kind == model.SessionCredentialAccess {
			return true
		}
	}
	return false
}

func sessionDeadline(now time.Time, lifetime time.Duration, absoluteExpiresAt time.Time) time.Time {
	expiresAt := model.TimeUTC(now.Add(lifetime))
	if expiresAt.After(absoluteExpiresAt) {
		return absoluteExpiresAt
	}
	return expiresAt
}
