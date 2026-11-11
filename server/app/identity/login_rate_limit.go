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
)

// LoginRateLimitPolicy bounds attempts in fixed windows by identity and
// source. The account-failure fields apply only to local login: once one
// account has AccountFailuresBeforeDelay failed password verifications in the
// window, from any source and through any of its identifiers, each further
// attempt against it waits a delay that doubles per failure up to
// MaximumAccountFailureDelay. The delay never refuses a correct password, so
// failures sent by someone else cannot lock the account out.
type LoginRateLimitPolicy struct {
	Window                     time.Duration
	MaximumAttempts            int
	MaximumSourceAttempts      int
	AccountFailuresBeforeDelay int
	MaximumAccountFailureDelay time.Duration
}

// loginFailureDelayBase is the first delay applied once an account reaches
// its failure threshold; each further failure doubles it.
const loginFailureDelayBase = 250 * time.Millisecond

// loginFailureDelay returns the wait applied before verifying a password for
// an account that already has failures recorded in the current window.
func loginFailureDelay(failures int64, threshold int, maximum time.Duration) time.Duration {
	if threshold <= 0 || maximum <= 0 || failures < int64(threshold) {
		return 0
	}
	delay := loginFailureDelayBase
	for steps := failures - int64(threshold); steps > 0 && delay < maximum; steps-- {
		delay *= 2
	}
	return min(delay, maximum)
}

// waitContext waits for delay or until ctx ends.
func waitContext(ctx context.Context, delay time.Duration) error {
	if delay <= 0 {
		return ctx.Err()
	}
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func (s *Service) checkLoginRateLimit(
	ctx context.Context,
	loginID string,
	source string,
) (authenticationAttemptReceipt, error) {
	settings := s.loginRateLimit
	receipt, limited, err := s.attempts.account(ctx, authenticationAttemptIntent{
		purpose: authenticationAttemptPurposeLocalLogin,
		window:  settings.Window,
		limits: []AttemptLimit{
			{
				dimension: authenticationAttemptDimensionIdentitySource,
				maximum:   settings.MaximumAttempts,
				identity:  loginID,
				source:    source,
			},
			{
				dimension: authenticationAttemptDimensionSource,
				maximum:   settings.MaximumSourceAttempts,
				source:    source,
			},
		},
	})
	if err != nil {
		return authenticationAttemptReceipt{}, RateLimitUnavailableAppError(err)
	}
	if limited {
		return authenticationAttemptReceipt{}, apperror.New("authentication.rate_limited")
	}
	return receipt, nil
}

// delayAfterLoginFailures waits the progressive delay earned by earlier failed
// verifications and returns the failure subject for this attempt. The subject
// is the resolved account, so every identifier of one account shares one
// budget; an identifier that resolves to no account is its own subject and
// waits by the same rule, so the delay does not reveal whether it exists. The
// wait happens before password work, so it holds no hashing permit.
func (s *Service) delayAfterLoginFailures(
	ctx context.Context,
	loginID string,
	account *model.User,
) (authenticationFailureSubject, error) {
	identity := "identifier:" + normalizeLoginIdentity(loginID)
	if account != nil {
		identity = "account:" + account.ID.String()
	}

	subject, err := s.attempts.failureSubject(authenticationAttemptPurposeLocalLogin, identity)
	if err != nil {
		return authenticationFailureSubject{}, RateLimitUnavailableAppError(err)
	}

	failures, err := s.attempts.failures(ctx, subject, s.loginRateLimit.Window)
	if err != nil {
		return authenticationFailureSubject{}, RateLimitUnavailableAppError(err)
	}

	delay := loginFailureDelay(
		failures,
		s.loginRateLimit.AccountFailuresBeforeDelay,
		s.loginRateLimit.MaximumAccountFailureDelay,
	)
	if delay > 0 {
		if err = s.wait(ctx, delay); err != nil {
			return authenticationFailureSubject{}, authenticationUnavailable(err)
		}
	}

	return subject, nil
}

// recordLoginFailure counts one failed verification and returns the public
// credential failure. Losing the count only shortens a later delay, so a cache
// failure is diagnostic rather than a different answer.
func (s *Service) recordLoginFailure(
	ctx context.Context,
	subject authenticationFailureSubject,
) error {
	if err := s.attempts.recordFailure(ctx, subject, s.loginRateLimit.Window); err != nil {
		s.warn(ctx, "login failure count failed", err)
	}
	return invalidCredentialsAppError()
}

func (s *Service) resetLocalAuthenticationAttempts(ctx context.Context, proof *localProof) {
	if err := errors.Join(
		s.attempts.reset(ctx, proof.receipt, authenticationAttemptDimensionIdentitySource),
		s.attempts.clearFailures(ctx, proof.failures),
	); err != nil {
		s.warn(ctx, "login rate-limit reset failed", err)
	}
}
