// ---------------------------------------------------------------------------------------------
// Copyright (c) 2015-present Mattermost, Inc. All Rights Reserved.
// Modifications Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------
//
// Adapted from Mattermost server/platform/shared/mfa/mfa.go and
// server/channels/app/user.go. Proctor retains the proven TOTP setup,
// activation, validation, and replay-prevention flow while separating the
// encrypted credential aggregate, hashed recovery codes, session assurance,
// and durable security audit.

package identity

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

// ChallengeMFACommand upgrades the current session to multi-factor assurance.
type ChallengeMFACommand struct {
	Code string
}

// ChallengeMFA upgrades the caller's Session to multi-factor assurance with a
// TOTP or recovery code.
func (s *Service) ChallengeMFA(
	ctx context.Context,
	inv invocation.Invocation,
	command ChallengeMFACommand,
) (*model.Session, error) {
	principal := inv.Principal()
	// A Session the Access Policy restricts to MFA enrollment may satisfy the
	// policy with a challenge, for example after an external sign-in by a
	// User who already has a local second factor. Assisted recovery may not.
	if principal.MFARecoveryRequired {
		return nil, apperror.New("authentication.session_required")
	}
	if err := s.requireMFARecoverySession(principal, false); err != nil {
		return nil, err
	}
	if err := s.requireMFAEnabled(); err != nil {
		return nil, err
	}
	if err := s.limitMFAFactorAttempt(ctx, inv); err != nil {
		return nil, err
	}

	resource, appErr := s.mfaAuditResource(ctx)
	if appErr != nil {
		return nil, appErr
	}

	auditID, appErr := s.beginMFAAudit(ctx, inv, model.ActionMFAChallenge, resource, nil)
	if appErr != nil {
		return nil, appErr
	}

	now := model.TimeUTC(s.now())
	proof, err := s.mfa.verifySecondFactor(ctx, s.mfaCredentials, principal.UserID.String(), command.Code, now)
	if err != nil {
		code := "authentication.mfa.code_invalid"
		if failure, ok := apperror.As(err); ok {
			code = failure.Code()
		}
		if auditErr := s.completeMFAAudit(ctx, auditID, model.AuditStatusFail, code, nil); auditErr != nil {
			return nil, errors.Join(err, auditErr)
		}

		return nil, err
	}

	result, err := s.mfaCredentials.Challenge(ctx, &store.MFAChallenge{
		Principal:        principal,
		CredentialID:     proof.credentialID,
		TimeStep:         proof.step,
		RecoveryCodeHash: proof.recoveryHash,
		VerifiedAt:       now,
		AuditEventID:     auditID,
	})
	if err != nil {
		if store.IsNotFound(err) {
			if auditErr := s.completeMFAAudit(ctx, auditID, model.AuditStatusFail, "authentication.mfa.code_invalid", nil); auditErr != nil {
				return nil, errors.Join(mfaInvalidCodeError(), auditErr)
			}
			return nil, mfaInvalidCodeError()
		}

		return nil, s.failMFAMutation(ctx, auditID, err)
	}

	if result == nil || result.Session == nil {
		return nil, authenticationUnavailable(errors.New("MFA challenge returned no Session"))
	}

	s.publisher.SessionsRevoked(ctx, principal.UserID, []model.SessionID{principal.SessionID}, result.AccessTokenHashes)
	return result.Session, nil
}

// verifyLoginMFA verifies and consumes the second factor of a local
// password sign-in. A User without an active credential signs in with one
// factor.
func (s *Service) verifyLoginMFA(
	ctx context.Context,
	userID string,
	code string,
	at time.Time,
) (model.AuthenticationStrength, int64, error) {
	credential, err := s.mfaCredentials.GetByUser(ctx, model.UserID(userID))
	if store.IsNotFound(err) {
		return model.AuthenticationSingleFactor, 0, nil
	}
	if err != nil {
		return "", 0, authenticationUnavailable(err)
	}
	if !credential.IsActive() {
		return model.AuthenticationSingleFactor, 0, nil
	}
	if !s.mfa.deps.Settings.Enabled {
		return "", 0, apperror.New("authentication.mfa.unavailable")
	}
	if strings.TrimSpace(code) == "" {
		return "", 0, apperror.New("authentication.mfa.required")
	}
	if appErr := s.consumeLoginSecondFactor(ctx, userID, code, at); appErr != nil {
		return "", 0, appErr
	}
	return model.AuthenticationMultiFactor, at.UnixMilli(), nil
}

// consumeLoginSecondFactor consumes the sign-in factor against the MFA Store.
func (s *Service) consumeLoginSecondFactor(
	ctx context.Context,
	userID string,
	code string,
	now time.Time,
) error {
	return s.mfa.consumeSecondFactor(ctx, s.mfaCredentials, userID, code, now)
}

// mfaRecoveryState is the two-step verification recovery state sign-in and
// external authentication read for a User before issuing a Session.
func (s *Service) mfaRecoveryState(ctx context.Context, userID model.UserID) (*model.UserMFARecovery, error) {
	state, err := s.mfaCredentials.GetRecoveryState(ctx, userID)
	if err != nil {
		return nil, authenticationUnavailable(err)
	}
	if state.ReenrollmentRequired && !s.mfa.deps.Settings.Enabled {
		return nil, apperror.New("authentication.mfa.unavailable")
	}
	return state, nil
}

func (s *Service) requireMFARecoverySession(principal model.Principal, recent bool) error {
	if principal.ValidateMFARecovery() != nil || principal.CredentialType != model.CredentialSessionAccess {
		return apperror.New("authentication.session_required")
	}
	if recent && !principal.IsRecentlyAuthenticated(s.now(), s.recentAuthenticationTTL) {
		return apperror.New("authentication.reauthentication_required")
	}
	return nil
}

func (s *Service) limitMFAFactorAttempt(ctx context.Context, inv invocation.Invocation) error {
	_, limited, err := s.attempts.account(ctx, authenticationAttemptIntent{
		purpose: authenticationAttemptPurposeMFA,
		window:  s.loginRateLimit.Window,
		limits: []AttemptLimit{
			{
				dimension: authenticationAttemptDimensionIdentity,
				maximum:   s.loginRateLimit.MaximumAttempts,
				identity:  inv.Principal().UserID.String(),
			},
			{
				dimension: authenticationAttemptDimensionSource,
				maximum:   s.loginRateLimit.MaximumSourceAttempts,
				source:    inv.RequestMetadata().IPAddress,
			},
		},
	})
	if err != nil {
		return RateLimitUnavailableAppError(err)
	}
	if limited {
		return apperror.New("authentication.rate_limited")
	}

	return nil
}
