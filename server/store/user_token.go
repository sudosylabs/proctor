// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package store

import (
	"context"
	"time"

	"github.com/sudosylabs/proctor/server/model"
)

type EmailVerificationResult struct {
	Token *model.UserToken
	User  *model.User
}

type PasswordResetResult struct {
	Token               *model.UserToken
	User                *model.User
	PasswordCredential  *model.PasswordCredential
	RevokedSessions     []*model.Session
	RevokedAccessHashes []string
}

// UserTokenMailIssue replaces the user's active token of that purpose and
// commits it with its audit and mail in one transaction.
type UserTokenMailIssue struct {
	Token      *model.UserToken
	Occurrence *model.MailOccurrence
	Delivery   *model.MailDelivery
	Job        *model.Job
	AuditEvent *model.AuditEvent
}

// UserEmailChange replaces the account email, invalidates earlier
// verification tokens, and commits the new verification token, an undo token
// for the previous address and both mails atomically. The undo token and its
// warning mail live for WarningLifetime. Earlier undo tokens stay usable, so
// every address in a chain of changes can still restore itself. New times
// come from one database instant; existing user and superseded token times
// never move backward, and they do not shift the new deadlines.
type UserEmailChange struct {
	UserID                                    model.UserID
	ExpectedRevision                          int64
	NewEmail                                  string
	Token                                     *model.UserToken
	UndoToken                                 *model.UserToken
	TokenLifetime                             time.Duration
	WarningLifetime                           time.Duration
	WarningOccurrence, VerificationOccurrence *model.MailOccurrence
	WarningDelivery, VerificationDelivery     *model.MailDelivery
	WarningJob, VerificationJob               *model.Job
	AuditEventID                              model.AuditEventID
	AuditAt                                   time.Time
}

type UserEmailChangeResult struct {
	User      *model.User
	Token     *model.UserToken
	UndoToken *model.UserToken
}

// EmailChangeUndo redeems an undo token. In one transaction, under the
// per-user session lock, it restores the token's address as verified,
// archives outstanding verification and reset tokens and suppresses their
// unsent mail, consumes every undo token of the user, and revokes all the
// user's sessions. An unusable token or inactive user is ErrNotFound; an
// address another active user now holds is a conflict.
type EmailChangeUndo struct {
	TokenHash        string
	RevocationReason model.SessionRevocationReason
	AuditEvent       *model.AuditEvent
}

type EmailChangeUndoResult struct {
	User                *model.User
	RevokedSessions     []*model.Session
	RevokedAccessHashes []string
}

// PrivilegedEmailVerification is an administrator marking a user's email
// verified; the user's notice commits with it.
type PrivilegedEmailVerification struct {
	UserID           model.UserID
	ExpectedRevision int64
	Occurrence       *model.MailOccurrence
	Delivery         *model.MailDelivery
	Job              *model.Job
	AuditEventID     model.AuditEventID
	AuditAt          time.Time
}

// PasswordResetCompletion consumes a reset token, changes the password,
// revokes sessions, and records the audit and notice atomically.
type PasswordResetCompletion struct {
	TokenHash        string
	PasswordHash     string
	Reuse            PasswordReuseCheck
	At               time.Time
	RevocationReason model.SessionRevocationReason
	AuditEvent       *model.AuditEvent
	Occurrence       *model.MailOccurrence
	Delivery         *model.MailDelivery
	Job              *model.Job
}

// UserTokenStore issues and consumes single-use account tokens. Each Consume
// method commits its account change, any session revocation and the audit in
// one transaction.
type UserTokenStore interface {
	Issue(
		context.Context,
		*UserTokenMailIssue,
	) (*model.UserToken, error)
	ChangeEmail(context.Context, *UserEmailChange) (*UserEmailChangeResult, error)
	VerifyEmailPrivileged(context.Context, *PrivilegedEmailVerification) (*model.User, error)
	Get(context.Context, model.UserTokenID) (*model.UserToken, error)
	GetByHash(
		context.Context,
		string,
		model.UserTokenPurpose,
	) (*model.UserToken, error)
	ConsumeEmailVerification(context.Context, *EmailVerificationConsumption) (*EmailVerificationResult, error)
	ConsumeEmailChangeUndo(context.Context, *EmailChangeUndo) (*EmailChangeUndoResult, error)
	ConsumePasswordReset(
		context.Context,
		*PasswordResetCompletion,
	) (*PasswordResetResult, error)
}

// EmailVerificationConsumption is the input to UserTokenStore.ConsumeEmailVerification.
type EmailVerificationConsumption struct {
	TokenHash  string
	Now        time.Time
	AuditEvent *model.AuditEvent
}
