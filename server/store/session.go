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

// SessionRevocation revokes one session and completes its recorded audit
// attempt in the same commit. The UserSession and UserLogout reasons forbid the
// notice mail; every other reason requires it. UserLogout of an absent or
// revoked session succeeds as an audited no-op; any other reason returns
// ErrNotFound for an inactive or another user's session.
type SessionRevocation struct {
	SessionID    model.SessionID
	UserID       model.UserID
	Occurrence   *model.MailOccurrence
	Delivery     *model.MailDelivery
	DeliveryJob  *model.Job
	RevokedAt    time.Time
	Reason       model.SessionRevocationReason
	AuditEventID model.AuditEventID
	AuditAt      time.Time
}

// SessionRevocationResult is what post-commit cache and realtime invalidation
// need. Session is nil for a UserLogout no-op, which needs none.
type SessionRevocationResult struct {
	Session     *model.Session
	TokenHashes []string
}

// SessionRename renames a user's browser or CLI session. DeviceName must
// already be normalized with model.NormalizeSessionDeviceName.
type SessionRename struct {
	SessionID    model.SessionID
	UserID       model.UserID
	DeviceName   string
	AuditEventID model.AuditEventID
	AuditAt      time.Time
}

// SessionExpiryEnforcementResult is a session checked against both its
// deadlines. Expired is true only when this call committed the revocation.
type SessionExpiryEnforcementResult struct {
	Session     *model.Session
	TokenHashes []string
	Expired     bool
}

// UserSessionsRevocation revokes every active session of one user. The
// UserAllSessions and UserOtherSessions reasons forbid the notice mail and
// Command; other reasons require the notice. KeepSessionID is required with
// UserOtherSessions and forbidden otherwise; it must name a live session of
// the user at RevokedAt (else ErrNotFound) and that session stays live. With
// nothing to revoke the audit still succeeds and no notice is sent.
type UserSessionsRevocation struct {
	UserID        model.UserID
	KeepSessionID model.SessionID
	Occurrence    *model.MailOccurrence
	Delivery      *model.MailDelivery
	DeliveryJob   *model.Job
	RevokedAt     time.Time
	Reason        model.SessionRevocationReason
	AuditEventID  model.AuditEventID
	AuditAt       time.Time
	Command       *CommandIdempotency
	Replayed      bool
	NoOp          bool
}

// UserSessionsRevocationResult is what post-commit cache and realtime
// invalidation need.
type UserSessionsRevocationResult struct {
	Sessions    []*model.Session
	TokenHashes []string
}

// SessionStore persists sessions. Active reads, activity updates and expiry
// compare at UTC microsecond precision, and a session is inactive at either
// deadline.
type SessionStore interface {
	ReauthenticateExternal(context.Context, *SessionExternalReauthentication) (*SessionReauthenticationResult, error)
	ReauthenticatePassword(context.Context, *SessionPasswordReauthentication) (*SessionReauthenticationResult, error)
	Save(context.Context, *SessionCreation) (*model.Session, []*model.SessionCredential, error)
	Get(context.Context, model.SessionID) (*model.Session, error)
	ListByUser(context.Context, *SessionPageQuery) ([]*model.Session, error)
	ListActiveByUser(context.Context, model.UserID, time.Time) ([]*model.Session, error)
	UpdateActivity(context.Context, *SessionActivityUpdate) error
	EnforceExpiry(context.Context, *SessionExpiryEnforcement) (*SessionExpiryEnforcementResult, error)
	// Withdraw takes back a session the system just issued or refreshed: the
	// Desktop nonce could not be stored after the exchange committed, or the
	// user became inactive at refresh. It is a compensation with no actor and
	// no audit; the revocation reason is the record, and any other reason is
	// rejected.
	Withdraw(context.Context, *SessionWithdrawal) ([]string, error)
	Revoke(context.Context, *SessionRevocation) (*SessionRevocationResult, error)
	RevokeAllForUser(context.Context, *UserSessionsRevocation) (*UserSessionsRevocationResult, error)
	// Rename completes the audit in the same transaction. A missing, another
	// user's, revoked, archived or expired session is ErrNotFound. A Desktop
	// session is ErrConflict, since its name belongs to its registration. The
	// row lock serializes a rename with revocation.
	Rename(context.Context, *SessionRename) (*model.Session, error)
	// PurgeRetired deletes retired sessions and their credentials once past
	// their grace and unreferenced.
	PurgeRetired(context.Context, *SessionPurge) (*SessionPurgeResult, error)
}

// SessionPageQuery pages a user's sessions newest first by creation then ID.
// A non-zero ActiveAt keeps only sessions unrevoked and unexpired at that
// instant.
type SessionPageQuery struct {
	UserID          model.UserID
	ActiveAt        time.Time
	BeforeCreatedAt time.Time
	BeforeID        model.SessionID
	Limit           int
}

// MaximumSessionPageSize is a 200-session page plus the look-ahead row.
const MaximumSessionPageSize = 201

// MaximumSessionPurgeBatch bounds one session or credential purge pass.
const MaximumSessionPurgeBatch = 1000

// SessionPurge examines sessions retired (revoked, expired or idle-expired)
// more than Grace ago on the database clock, oldest first after After, and
// deletes the unreferenced ones with their credentials.
type SessionPurge struct {
	Grace time.Duration
	Limit int
	After SessionPurgePosition
}

// SessionPurgePosition is the last session a purge pass examined.
type SessionPurgePosition struct {
	RetiredAt time.Time
	SessionID model.SessionID
}

// SessionPurgeResult reports one purge pass. Examined below the limit means no
// retired session remains after Next.
type SessionPurgeResult struct {
	Examined    int
	Sessions    int64
	Credentials int64
	Next        SessionPurgePosition
}

// SessionCredentialPurge deletes credentials expired or revoked more than
// Grace ago, including superseded credentials of live sessions.
type SessionCredentialPurge struct {
	Grace time.Duration
	Limit int
}

// SessionCreation creates a session and its credentials atomically. Password
// sign-in requires PasswordProof; external sign-in forbids it. Creation and
// password reset or removal share the per-user lock: either creation commits
// first and the reset revokes it, or the stale proof fails with
// ErrPasswordCredentialChanged. The login audit completes in the same commit,
// so no session exists without its success record.
type SessionCreation struct {
	ExternalLoginStateID model.ExternalLoginStateID
	Session              *model.Session
	Credentials          []*model.SessionCredential
	MaximumActive        int
	PasswordProof        PasswordCredentialProof
	AuditEventID         model.AuditEventID
	AuditAt              time.Time
}

type SessionRotation struct {
	Session             *model.Session
	AccessCredential    *model.SessionCredential
	RefreshCredential   *model.SessionCredential
	RevokedAccessHashes []string
	ReplayDetected      bool
	Expired             bool
}

// SessionCredentialStore resolves bearer credentials and rotates refresh
// credentials with replay detection. Rotation compares Now, at UTC
// microsecond precision, with exclusive credential and session deadlines.
type SessionCredentialStore interface {
	GetSessionByTokenHash(
		context.Context,
		string,
		model.SessionCredentialKind,
	) (*model.SessionCredential, *model.Session, error)
	RotateRefresh(context.Context, *SessionRefreshRotation) (*SessionRotation, error)
	// PurgeRetired also deletes superseded credentials of live sessions once
	// their grace has passed.
	PurgeRetired(context.Context, *SessionCredentialPurge) (int64, error)
}

// SessionRefreshRotation is the input to SessionCredentialStore.RotateRefresh.
type SessionRefreshRotation struct {
	TokenHash     string
	Access        *model.SessionCredential
	Refresh       *model.SessionCredential
	Now           time.Time
	IdleExpiresAt time.Time
	// MFARequiredByPolicy restricts a single-factor web session to MFA
	// enrollment as it rotates; a multi-factor session is unchanged.
	MFARequiredByPolicy bool
}

// SessionExpiryEnforcement is the input to SessionStore.EnforceExpiry.
type SessionExpiryEnforcement struct {
	ID     model.SessionID
	UserID model.UserID
	At     time.Time
}

// SessionActivityUpdate is the input to SessionStore.UpdateActivity.
type SessionActivityUpdate struct {
	ID             model.SessionID
	LastActivityAt time.Time
	IdleExpiresAt  time.Time
}

// SessionWithdrawal is the input to SessionStore.Withdraw.
type SessionWithdrawal struct {
	ID        model.SessionID
	UserID    model.UserID
	RevokedAt time.Time
	Reason    model.SessionRevocationReason
}
