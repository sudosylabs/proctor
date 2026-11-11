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
	"fmt"

	"github.com/sudosylabs/proctor/server/app/access"
	"github.com/sudosylabs/proctor/server/app/invocation"
	"github.com/sudosylabs/proctor/server/app/mail"
	"github.com/sudosylabs/proctor/server/model"
	"github.com/sudosylabs/proctor/server/store"
)

// RequestEmailVerificationCommand issues a verification token for the caller.
type RequestEmailVerificationCommand struct {
	Source string
}

// CompleteEmailVerificationCommand consumes a verification credential.
type CompleteEmailVerificationCommand struct {
	Token  string
	Source string
}

// RequestEmailVerification issues a verification token for the caller's
// current email address and prepares the mail that delivers it.
func (s *Service) RequestEmailVerification(
	ctx context.Context,
	invocation invocation.Invocation,
	command RequestEmailVerificationCommand,
) error {
	if !s.mail.Enabled() {
		return accountRecoveryUnavailable(fmt.Errorf("mail delivery is disabled"))
	}

	principal := invocation.Principal()
	if err := s.checkAccountRecoveryRateLimit(
		ctx,
		accountRecoveryAttemptEmailVerificationRequest,
		principal.UserID.String(),
		command.Source,
	); err != nil {
		return err
	}

	user, err := s.users.Get(ctx, principal.UserID)
	if err != nil {
		return accountRecoveryStoreFailure(err)
	}
	if !user.IsActive() {
		return access.InvalidTokenAppError()
	}
	if user.EmailVerified {
		return nil
	}

	institution, err := s.accountRecoveryInstitution(ctx)
	if err != nil {
		return err
	}

	rawToken := s.newCredential()
	now := s.now()
	token := &model.UserToken{
		UserID:    user.ID,
		Purpose:   model.UserTokenEmailVerification,
		TokenHash: model.HashToken(rawToken),
		Target:    user.Email,
		ExpiresAt: now.Add(s.accountRecoveryPolicy.EmailVerificationTTL),
	}
	token.PrepareCreate(model.NewUserTokenID(), now)
	event := s.recoveryAuditEvent(
		model.ActionAuthenticationEmailVerificationRequest,
		model.Resource{Type: model.ResourceUser, ID: user.ID.String()},
		institution.ID,
		invocation,
		&principal,
		"session",
	)
	link, err := AccountCredentialLink(
		s.publicURL,
		"/account/verify-email",
		rawToken,
	)
	if err != nil {
		return accountRecoveryUnavailable(err)
	}

	prepared, err := s.mail.PrepareEmailVerification(mail.AccountTokenPreparation{
		Recipient:    user,
		OccurrenceID: model.MailOccurrenceID(token.ID.String()),
		ActionURL:    link,
		At:           now,
		Deadline:     token.ExpiresAt,
	})
	if err != nil {
		return accountRecoveryUnavailable(err)
	}

	if _, err = s.userTokens.Issue(ctx, &store.UserTokenMailIssue{
		Token:      token,
		Occurrence: prepared.Occurrence,
		Delivery:   prepared.Delivery,
		Job:        prepared.Job,
		AuditEvent: event,
	}); err != nil {
		return accountRecoveryStoreFailure(err)
	}

	return nil
}

// CompleteEmailVerification consumes an email verification token and returns
// the verified User.
func (s *Service) CompleteEmailVerification(
	ctx context.Context,
	invocation invocation.Invocation,
	command CompleteEmailVerificationCommand,
) (*model.User, error) {
	if err := s.checkAccountRecoveryRateLimit(
		ctx,
		accountRecoveryAttemptEmailVerificationComplete,
		recoveryCredentialRateIdentity(command.Token),
		command.Source,
	); err != nil {
		return nil, err
	}

	if !validRawCredential(command.Token) {
		return nil, invalidAccountCredential()
	}

	institution, err := s.accountRecoveryInstitution(ctx)
	if err != nil {
		return nil, err
	}

	event := s.recoveryAuditEvent(
		model.ActionAuthenticationEmailVerificationComplete,
		model.Resource{Type: model.ResourceUser},
		institution.ID,
		invocation,
		nil,
		"email_verification_token",
	)
	result, err := s.userTokens.ConsumeEmailVerification(
		ctx,
		&store.EmailVerificationConsumption{TokenHash: model.HashToken(command.Token), Now: s.now(), AuditEvent: event},
	)
	if err != nil {
		if store.IsNotFound(err) {
			return nil, invalidAccountCredential()
		}
		return nil, accountRecoveryStoreFailure(err)
	}

	return result.User, nil
}
