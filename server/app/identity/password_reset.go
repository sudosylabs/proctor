// ---------------------------------------------------------------------------------------------
// Copyright (c) 2015-present Mattermost, Inc. All Rights Reserved.
// Modifications Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------
//
// Adapted from Mattermost server/channels/app/email/email.go and
// authentication.go. Proctor uses purpose-specific hashed credentials,
// target-bound single-use consumption, generic public recovery responses,
// fail-closed rate limiting, and durable transactional security audits.

package identity

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/sudosylabs/proctor/server/app/apperror"
	"github.com/sudosylabs/proctor/server/app/invocation"
	"github.com/sudosylabs/proctor/server/app/mail"
	"github.com/sudosylabs/proctor/server/model"
	"github.com/sudosylabs/proctor/server/store"
)

// RequestPasswordResetCommand issues a password-reset token when eligible.
type RequestPasswordResetCommand struct {
	Email  string
	Source string
}

// CompletePasswordResetCommand consumes a reset token and sets a new password.
type CompletePasswordResetCommand struct {
	Token    string
	Password string
	Source   string
}

// RequestPasswordReset deliberately returns success for unknown, disabled, or
// external-only accounts and for per-account persistence/delivery failures.
// Operational failures are logged without the requested email or raw token.
func (s *Service) RequestPasswordReset(
	ctx context.Context,
	invocation invocation.Invocation,
	command RequestPasswordResetCommand,
) error {
	localLoginAllowed, err := s.accessPolicy.AllowsLocalLogin(ctx)
	if err != nil {
		s.logHiddenRecoveryFailure(ctx, "password reset access policy lookup failed", err)
		return nil
	}

	if !localLoginAllowed {
		return nil
	}
	if !s.mail.Enabled() {
		return accountRecoveryUnavailable(fmt.Errorf("mail delivery is disabled"))
	}

	normalizedEmail := strings.ToLower(strings.TrimSpace(model.SanitizeUnicode(command.Email)))
	if limitErr := s.checkAccountRecoveryRateLimit(
		ctx,
		accountRecoveryAttemptPasswordResetRequest,
		normalizedEmail,
		command.Source,
	); limitErr != nil {
		return limitErr
	}

	if !model.IsValidEmail(normalizedEmail) {
		return nil
	}

	user, err := s.users.GetByEmail(ctx, normalizedEmail)
	if err != nil {
		if !store.IsNotFound(err) {
			s.logHiddenRecoveryFailure(ctx, "password reset user lookup failed", err)
		}
		return nil
	}

	if !user.IsActive() {
		return nil
	}

	if _, err = s.passwords.GetByUser(ctx, user.ID); err != nil {
		if !store.IsNotFound(err) {
			s.logHiddenRecoveryFailure(ctx, "password credential lookup failed", err)
		}
		return nil
	}

	institution, err := s.accountRecoveryInstitution(ctx)
	if err != nil {
		s.logHiddenRecoveryFailure(ctx, "password reset institution lookup failed", err)
		return nil
	}

	rawToken := s.newCredential()
	now := s.now()
	token := &model.UserToken{
		UserID:    user.ID,
		Purpose:   model.UserTokenPasswordReset,
		TokenHash: model.HashToken(rawToken),
		Target:    user.Email,
		ExpiresAt: now.Add(s.accountRecoveryPolicy.PasswordResetTTL),
	}
	token.PrepareCreate(model.NewUserTokenID(), now)
	event := s.recoveryAuditEvent(
		model.ActionAuthenticationPasswordResetRequest,
		model.Resource{Type: model.ResourceUser, ID: user.ID.String()},
		institution.ID,
		invocation,
		nil,
		"anonymous",
	)
	link, err := AccountCredentialLink(
		s.publicURL,
		"/account/reset-password",
		rawToken,
	)
	if err != nil {
		s.logHiddenRecoveryFailure(ctx, "password reset link generation failed", err)
		return nil
	}

	prepared, err := s.mail.PreparePasswordReset(mail.AccountTokenPreparation{
		Recipient:    user,
		OccurrenceID: model.MailOccurrenceID(token.ID.String()),
		ActionURL:    link,
		At:           now,
		Deadline:     token.ExpiresAt,
	})
	if err != nil {
		s.logHiddenRecoveryFailure(ctx, "password reset delivery preparation failed", err)
		return nil
	}

	if _, err = s.userTokens.Issue(ctx, &store.UserTokenMailIssue{
		Token:      token,
		Occurrence: prepared.Occurrence,
		Delivery:   prepared.Delivery,
		Job:        prepared.Job,
		AuditEvent: event,
	}); err != nil {
		s.logHiddenRecoveryFailure(ctx, "password reset token and delivery issue failed", err)
	}

	return nil
}

// CompletePasswordReset consumes a password reset token, replaces the
// password, and revokes the User's Sessions after the reset commits.
func (s *Service) CompletePasswordReset(
	ctx context.Context,
	invocation invocation.Invocation,
	command CompletePasswordResetCommand,
) (*model.User, error) {
	if err := s.checkAccountRecoveryRateLimit(
		ctx,
		accountRecoveryAttemptPasswordResetComplete,
		recoveryCredentialRateIdentity(command.Token),
		command.Source,
	); err != nil {
		return nil, err
	}

	localLoginAllowed, err := s.accessPolicy.AllowsLocalLogin(ctx)
	if err != nil {
		return nil, accountRecoveryStoreFailure(err)
	}
	if !localLoginAllowed {
		return nil, invalidAccountCredential()
	}
	if !validRawCredential(command.Token) {
		return nil, invalidAccountCredential()
	}

	institution, err := s.accountRecoveryInstitution(ctx)
	if err != nil {
		return nil, err
	}

	event := s.recoveryAuditEvent(
		model.ActionAuthenticationPasswordResetComplete,
		model.Resource{Type: model.ResourceUser},
		institution.ID,
		invocation,
		nil,
		"password_reset_token",
	)
	now := model.TimeUTC(s.now())
	token, err := s.userTokens.GetByHash(ctx, model.HashToken(command.Token), model.UserTokenPasswordReset)
	if err != nil {
		if store.IsNotFound(err) {
			return nil, invalidAccountCredential()
		}
		return nil, accountRecoveryStoreFailure(err)
	}

	user, err := s.users.Get(ctx, token.UserID)
	if err != nil {
		if store.IsNotFound(err) {
			return nil, invalidAccountCredential()
		}
		return nil, accountRecoveryStoreFailure(err)
	}

	// Hash only for a live token's account. The hashing permit is shared with
	// login, so guessed tokens must not be able to consume it.
	if err = CheckNewPassword(command.Password, passwordIdentityOf(user)); err != nil {
		return nil, err
	}

	passwordHash, err := s.hasher.Hash(ctx, command.Password)
	if err != nil {
		return nil, PasswordHashError(err, "authentication.account_recovery.unavailable")
	}

	prepared, err := s.mail.PreparePasswordChanged(mail.NoticePreparation{Recipient: user, At: now})
	if err != nil {
		return nil, accountRecoveryUnavailable(err)
	}

	result, err := s.userTokens.ConsumePasswordReset(ctx, &store.PasswordResetCompletion{
		TokenHash:        model.HashToken(command.Token),
		PasswordHash:     passwordHash,
		Reuse:            s.hasher.ReuseCheck(command.Password),
		At:               now,
		RevocationReason: model.SessionRevocationPasswordReset,
		AuditEvent:       event,
		Occurrence:       prepared.Occurrence,
		Delivery:         prepared.Delivery,
		Job:              prepared.Job,
	})
	if err != nil {
		if errors.Is(err, store.ErrAuthenticationMethodDisabled) {
			return nil, invalidAccountCredential()
		}
		if errors.Is(err, store.ErrPasswordReused) {
			return nil, passwordReused()
		}
		if apperror.Is(err, "service.busy") {
			return nil, err
		}
		if store.IsNotFound(err) {
			return nil, invalidAccountCredential()
		}

		return nil, accountRecoveryStoreFailure(err)
	}

	s.publisher.SessionsRevoked(
		ctx,
		result.User.ID,
		sessionIDs(result.RevokedSessions),
		result.RevokedAccessHashes,
	)
	return result.User, nil
}
