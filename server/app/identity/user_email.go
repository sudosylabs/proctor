// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package identity

import (
	"context"
	"strings"
	"time"

	"github.com/sudosylabs/proctor/server/app/access"
	"github.com/sudosylabs/proctor/server/app/apperror"
	"github.com/sudosylabs/proctor/server/app/invocation"
	"github.com/sudosylabs/proctor/server/app/mail"
	"github.com/sudosylabs/proctor/server/app/user"
	"github.com/sudosylabs/proctor/server/model"
	"github.com/sudosylabs/proctor/server/store"
)

type ChangeUserEmailCommand struct {
	UserID model.UserID
	Email  string
}

// VerifyUserEmailPrivilegedCommand asks an administrator to mark a User's
// email verified without a verification token. It requires a strong recent
// Session and is a no-op when the email is already verified.
type VerifyUserEmailPrivilegedCommand struct{ UserID model.UserID }

// UserEmailState is the narrow result of an explicit email-security mutation.
// It deliberately omits the mailbox and unrelated profile/account metadata.
type UserEmailState struct {
	UserID        model.UserID
	EmailVerified bool
}

func userEmailState(user *model.User) *UserEmailState {
	if user == nil {
		return nil
	}
	return &UserEmailState{UserID: user.ID, EmailVerified: user.EmailVerified}
}

// ChangeUserEmail changes a User's email address as an administrator and
// sends the warning and verification mail.
func (s *Service) ChangeUserEmail(
	ctx context.Context,
	invocation invocation.Invocation,
	command ChangeUserEmailCommand,
) (*UserEmailState, error) {
	if s.authorizer == nil || s.auditor == nil {
		return nil, apperror.New("administration.unavailable")
	}
	if err := RequireStrongRecentSession(invocation.Principal(), s.now(), s.recentAuthenticationTTL); err != nil {
		return nil, err
	}

	if err := s.authorizer.Authorize(
		ctx,
		invocation,
		model.ActionUserManage,
		model.Resource{Type: model.ResourceUser, ID: command.UserID.String()},
	); err != nil {
		return nil, err
	}

	id := command.UserID
	newEmail := strings.ToLower(strings.TrimSpace(model.SanitizeUnicode(command.Email)))
	if !id.IsValid() || !model.IsValidEmail(newEmail) {
		return nil, apperror.New("user.invalid").WithField("field", "email")
	}

	current, err := s.users.Get(ctx, id)
	if err != nil || current == nil || !current.IsActive() {
		return nil, user.StoreErrors.Translate(err)
	}
	if current.Email == newEmail {
		return userEmailState(current), nil
	}

	// Each applied change mails an undo link to the previous address, so the
	// changes one account can receive are bounded like other recovery mail.
	if err := s.checkAccountRecoveryRateLimit(
		ctx,
		accountRecoveryAttemptEmailChangeNotice,
		id.String(),
		invocation.RequestMetadata().IPAddress,
	); err != nil {
		return nil, err
	}

	at := model.TimeUTC(s.now())
	return access.RunAudited(
		ctx,
		s.auditor,
		access.Mutation{
			Invocation: invocation,
			Action:     model.ActionUserManage,
			Resource:   model.Resource{Type: model.ResourceUser, ID: id.String()},
			Operation:  "change_email",
			Value:      map[string]any{"email_changed": true, "email_verified": false},
			Prior:      current.Auditable(),
		},
		func() time.Time { return at },
		func(ctx context.Context, reference access.MutationReference) (*UserEmailState, error) {
			updated, err := s.changeUserEmail(ctx, current, newEmail, at, reference)
			return userEmailState(updated), err
		},
		user.StoreErrors.Translate,
	)
}

// VerifyUserEmailPrivileged marks a User's email address verified as an
// administrator and notifies the User.
func (s *Service) VerifyUserEmailPrivileged(
	ctx context.Context,
	invocation invocation.Invocation,
	command VerifyUserEmailPrivilegedCommand,
) (*UserEmailState, error) {
	if s.authorizer == nil || s.auditor == nil {
		return nil, apperror.New("administration.unavailable")
	}
	if err := RequireStrongRecentSession(invocation.Principal(), s.now(), s.recentAuthenticationTTL); err != nil {
		return nil, err
	}

	if err := s.authorizer.Authorize(
		ctx,
		invocation,
		model.ActionUserManage,
		model.Resource{Type: model.ResourceUser, ID: command.UserID.String()},
	); err != nil {
		return nil, err
	}

	id := command.UserID
	if !id.IsValid() {
		return nil, apperror.New("request.invalid").WithField("field", "user_id")
	}

	current, err := s.users.Get(ctx, id)
	if err != nil || current == nil || !current.IsActive() {
		return nil, user.StoreErrors.Translate(err)
	}
	if current.EmailVerified {
		return userEmailState(current), nil
	}

	at := model.TimeUTC(s.now())
	return access.RunAudited(
		ctx,
		s.auditor,
		access.Mutation{
			Invocation: invocation,
			Action:     model.ActionUserManage,
			Resource:   model.Resource{Type: model.ResourceUser, ID: id.String()},
			Operation:  "verify_email_privileged",
			Value:      map[string]any{"email_verified": true},
			Prior:      current.Auditable(),
		},
		func() time.Time { return at },
		func(ctx context.Context, reference access.MutationReference) (*UserEmailState, error) {
			updated, err := s.verifyUserEmailPrivileged(ctx, current, at, reference)
			return userEmailState(updated), err
		},
		user.StoreErrors.Translate,
	)
}

func (s *Service) changeUserEmail(
	ctx context.Context,
	current *model.User,
	newEmail string,
	now time.Time,
	reference access.MutationReference,
) (*model.User, error) {
	rawToken := s.newCredential()
	token := &model.UserToken{
		UserID:    current.ID,
		Purpose:   model.UserTokenEmailVerification,
		TokenHash: model.HashToken(rawToken),
		Target:    newEmail,
		ExpiresAt: now.Add(s.accountRecoveryPolicy.EmailVerificationTTL),
	}
	token.PrepareCreate(model.NewUserTokenID(), now)
	link, err := AccountCredentialLink(s.publicURL, "/account/verify-email", rawToken)
	if err != nil {
		return nil, accountRecoveryUnavailable(err)
	}

	rawUndo := s.newCredential()
	undo := &model.UserToken{
		UserID:    current.ID,
		Purpose:   model.UserTokenEmailChangeUndo,
		TokenHash: model.HashToken(rawUndo),
		Target:    current.Email,
		ExpiresAt: now.Add(s.accountRecoveryPolicy.EmailChangeUndoTTL),
	}
	undo.PrepareCreate(model.NewUserTokenID(), now)
	undoLink, err := AccountCredentialLink(s.publicURL, "/account/undo-email-change", rawUndo)
	if err != nil {
		return nil, accountRecoveryUnavailable(err)
	}

	oldRecipient, newRecipient := *current, *current
	newRecipient.Email, newRecipient.EmailVerified = newEmail, false
	warning, err := s.mail.PrepareEmailChangeWarning(mail.AccountTokenPreparation{
		Recipient:    &oldRecipient,
		OccurrenceID: model.MailOccurrenceID(undo.ID.String()),
		ActionURL:    undoLink,
		At:           now,
		Deadline:     undo.ExpiresAt,
		NewAddress:   newEmail,
	})
	if err != nil {
		return nil, accountRecoveryUnavailable(err)
	}

	verification, err := s.mail.PrepareEmailChangeVerification(mail.AccountTokenPreparation{
		Recipient:    &newRecipient,
		OccurrenceID: model.MailOccurrenceID(token.ID.String()),
		ActionURL:    link,
		At:           now,
		Deadline:     token.ExpiresAt,
	})
	if err != nil {
		return nil, accountRecoveryUnavailable(err)
	}

	result, err := s.userTokens.ChangeEmail(ctx, &store.UserEmailChange{
		UserID:                 current.ID,
		ExpectedRevision:       current.Revision,
		NewEmail:               newEmail,
		Token:                  token,
		UndoToken:              undo,
		TokenLifetime:          s.accountRecoveryPolicy.EmailVerificationTTL,
		WarningLifetime:        s.accountRecoveryPolicy.EmailChangeUndoTTL,
		WarningOccurrence:      warning.Occurrence,
		WarningDelivery:        warning.Delivery,
		WarningJob:             warning.Job,
		VerificationOccurrence: verification.Occurrence,
		VerificationDelivery:   verification.Delivery,
		VerificationJob:        verification.Job,
		AuditEventID:           reference.ID,
		AuditAt:                reference.MutationAt,
	})
	if err != nil {
		return nil, user.StoreErrors.Translate(err)
	}

	return result.User, nil
}

// CompleteEmailChangeUndoCommand redeems the undo link mailed to the previous
// address of a changed account.
type CompleteEmailChangeUndoCommand struct {
	Token  string
	Source string
}

// CompleteEmailChangeUndo restores the previous address an undo credential
// names, signs every Session of the account out, and audits the undo. The
// response conceals whether the credential ever existed.
func (s *Service) CompleteEmailChangeUndo(
	ctx context.Context,
	invocation invocation.Invocation,
	command CompleteEmailChangeUndoCommand,
) error {
	if err := s.checkAccountRecoveryRateLimit(
		ctx,
		accountRecoveryAttemptEmailChangeUndoComplete,
		recoveryCredentialRateIdentity(command.Token),
		command.Source,
	); err != nil {
		return err
	}

	if !validRawCredential(command.Token) {
		return invalidAccountCredential()
	}

	institution, err := s.accountRecoveryInstitution(ctx)
	if err != nil {
		return err
	}

	event := s.recoveryAuditEvent(
		model.ActionAuthenticationEmailChangeUndo,
		model.Resource{Type: model.ResourceUser},
		institution.ID,
		invocation,
		nil,
		"email_change_undo_token",
	)
	result, err := s.userTokens.ConsumeEmailChangeUndo(ctx, &store.EmailChangeUndo{
		TokenHash:        model.HashToken(command.Token),
		RevocationReason: model.SessionRevocationEmailChangeUndone,
		AuditEvent:       event,
	})
	switch {
	case err == nil:
	case store.IsNotFound(err):
		return invalidAccountCredential()
	case store.IsConflict(err):
		return apperror.New("authentication.email_change_undo.conflict").Wrap(err)
	default:
		return accountRecoveryStoreFailure(err)
	}

	s.effects.SessionsRevoked(ctx, result.User.ID, sessionIDs(result.RevokedSessions), result.RevokedAccessHashes)
	return nil
}

func (s *Service) verifyUserEmailPrivileged(
	ctx context.Context,
	current *model.User,
	now time.Time,
	reference access.MutationReference,
) (*model.User, error) {
	prepared, err := s.mail.PrepareEmailVerifiedByAdministrator(mail.NoticePreparation{Recipient: current, At: now})
	if err != nil {
		return nil, accountRecoveryUnavailable(err)
	}

	updated, err := s.userTokens.VerifyEmailPrivileged(ctx, &store.PrivilegedEmailVerification{
		UserID:           current.ID,
		ExpectedRevision: current.Revision,
		Occurrence:       prepared.Occurrence,
		Delivery:         prepared.Delivery,
		Job:              prepared.Job,
		AuditEventID:     reference.ID,
		AuditAt:          reference.MutationAt,
	})
	if err != nil {
		return nil, err
	}

	return updated, nil
}
