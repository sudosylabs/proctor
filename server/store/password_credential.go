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

// PasswordCredentialStore persists one encoded password hash per local user.
type PasswordCredentialStore interface {
	Save(context.Context, *model.PasswordCredential) (*model.PasswordCredential, error)
	GetByUser(context.Context, model.UserID) (*model.PasswordCredential, error)
	Rehash(context.Context, *PasswordCredentialRehash) error
	Enroll(context.Context, *PasswordCredentialEnrollment) (*AuthenticationMethodMutationResult, error)
	Remove(context.Context, *PasswordCredentialRemoval) (*AuthenticationMethodMutationResult, error)
	Change(context.Context, *PasswordCredentialChange) (*AuthenticationMethodMutationResult, error)
}

// PasswordCredentialRehash replaces the hash of the active credential only if
// it is still ExpectedHash, keeping PasswordChangedAt and the revision. A
// competing rehash, reset, removal or replacement returns
// ErrPasswordCredentialChanged and writes nothing. Do not retry an unknown
// commit outcome automatically.
type PasswordCredentialRehash struct {
	ID               model.PasswordCredentialID
	UserID           model.UserID
	ExpectedHash     string
	ExpectedRevision int64
	PasswordHash     string
}

// PasswordCredentialProof names the verified password without carrying its
// hash. It holds only if ID and Revision still name the active credential
// when the consuming transaction commits.
type PasswordCredentialProof struct {
	ID       model.PasswordCredentialID
	Revision int64
}

func (p PasswordCredentialProof) IsValid() bool { return p.ID.IsValid() && p.Revision > 0 }

// PasswordMatcher reports whether the new password matches an encoded hash.
// An error aborts the credential change.
type PasswordMatcher interface {
	Matches(context.Context, string) (bool, error)
}

// PasswordReuseCheck refuses, with ErrPasswordReused, a new password matching
// one of the user's Limit most recent ones (active, archived and history,
// newest first). The store checks under the transaction's per-user locks, so
// a concurrent change cannot slip between check and write. A replaced hash
// moves into history, which is pruned to Limit in the same transaction. Limit
// zero disables the check and discards the history.
type PasswordReuseCheck struct {
	Limit   int
	Matcher PasswordMatcher
}

// Valid reports whether Limit is in range and Matcher is set when needed.
func (c PasswordReuseCheck) Valid() bool {
	return c.Limit >= 0 && c.Limit <= MaximumPasswordHistory && (c.Limit == 0 || c.Matcher != nil)
}

// MaximumPasswordHistory bounds the retained password history per user.
const MaximumPasswordHistory = 24

type PasswordCredentialEnrollment struct {
	Credential   *model.PasswordCredential
	Reuse        PasswordReuseCheck
	Capabilities AccessDeploymentCapabilities
	AuditEventID model.AuditEventID
	AuditAt      time.Time
}

type PasswordCredentialRemoval struct {
	UserID           model.UserID
	Capabilities     AccessDeploymentCapabilities
	ChangedAt        time.Time
	RevocationReason model.SessionRevocationReason
	AuditEventID     model.AuditEventID
	AuditAt          time.Time
}

// PasswordCredentialChange is a user changing their own password while local
// login is enabled. In one transaction, under the per-user session lock, it
// advances the revision and change time, invalidates outstanding reset
// tokens, revokes every other live session (KeepSessionID stays), queues the
// notice and completes the audit. No active credential is ErrNotFound; an
// ineligible user is a conflict.
type PasswordCredentialChange struct {
	UserID           model.UserID
	KeepSessionID    model.SessionID
	PasswordHash     string
	Reuse            PasswordReuseCheck
	RevocationReason model.SessionRevocationReason
	Occurrence       *model.MailOccurrence
	Delivery         *model.MailDelivery
	Job              *model.Job
	AuditEventID     model.AuditEventID
	AuditAt          time.Time
}
