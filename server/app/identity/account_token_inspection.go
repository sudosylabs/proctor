// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package identity

import (
	"context"
	"time"

	"github.com/sudosylabs/proctor/server/app/invocation"
	"github.com/sudosylabs/proctor/server/app/mail"
	"github.com/sudosylabs/proctor/server/model"
	"github.com/sudosylabs/proctor/server/store"
)

// InspectAccountTokenCommand names one fragment credential a hosted page
// holds so it can say which address is involved before the person acts.
type InspectAccountTokenCommand struct {
	Token  string
	Source string
}

// EmailVerificationInspection is the address an active verification token
// would confirm. It is the mailbox the link was delivered to.
type EmailVerificationInspection struct {
	Email string
}

// EmailChangeUndoInspection describes the change an active undo token would
// reverse: the previous address it restores, the current address masked as
// in the warning mail, and when the change was applied.
type EmailChangeUndoInspection struct {
	RestoredEmail      string
	MaskedCurrentEmail string
	ChangedAt          time.Time
}

// InspectEmailVerification reads, without consuming, extending, or auditing,
// the address an active verification token names. It is rate-limited by token
// and source with the completion policy and conceals unknown, expired,
// superseded, consumed, and mismatched tokens behind the completion's generic
// invalid-credential outcome. The result is presentation only; completion
// rechecks everything against authoritative PostgreSQL time.
func (s *Service) InspectEmailVerification(ctx context.Context, _ invocation.Invocation, command InspectAccountTokenCommand) (*EmailVerificationInspection, error) {
	token, user, err := s.inspectAccountToken(ctx, accountRecoveryAttemptEmailVerificationInspect, model.UserTokenEmailVerification, command)
	if err != nil {
		return nil, err
	}
	if user.Email != token.Target {
		return nil, invalidAccountCredential()
	}
	return &EmailVerificationInspection{Email: token.Target}, nil
}

// InspectEmailChangeUndo reads, under the same rules as
// InspectEmailVerification, the change an active undo token would reverse.
func (s *Service) InspectEmailChangeUndo(ctx context.Context, _ invocation.Invocation, command InspectAccountTokenCommand) (*EmailChangeUndoInspection, error) {
	token, user, err := s.inspectAccountToken(ctx, accountRecoveryAttemptEmailChangeUndoInspect, model.UserTokenEmailChangeUndo, command)
	if err != nil {
		return nil, err
	}
	return &EmailChangeUndoInspection{RestoredEmail: token.Target, MaskedCurrentEmail: mail.MaskAddress(user.Email), ChangedAt: model.TimeUTC(token.CreatedAt)}, nil
}

func (s *Service) inspectAccountToken(ctx context.Context, operation accountRecoveryAttemptOperation, purpose model.UserTokenPurpose, command InspectAccountTokenCommand) (*model.UserToken, *model.User, error) {
	if err := s.checkAccountRecoveryRateLimit(ctx, operation, recoveryCredentialRateIdentity(command.Token), command.Source); err != nil {
		return nil, nil, err
	}
	if !validRawCredential(command.Token) {
		return nil, nil, invalidAccountCredential()
	}
	token, err := s.userTokens.GetByHash(ctx, model.HashToken(command.Token), purpose)
	if err != nil {
		if store.IsNotFound(err) {
			return nil, nil, invalidAccountCredential()
		}
		return nil, nil, accountRecoveryStoreFailure(err)
	}
	if token == nil || token.Purpose != purpose || !token.IsActiveAt(s.now()) {
		return nil, nil, invalidAccountCredential()
	}
	user, err := s.users.Get(ctx, token.UserID)
	if err != nil {
		if store.IsNotFound(err) {
			return nil, nil, invalidAccountCredential()
		}
		return nil, nil, accountRecoveryStoreFailure(err)
	}
	if user == nil || !user.IsActive() {
		return nil, nil, invalidAccountCredential()
	}
	return token, user, nil
}
