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

type AccessProviderCapability struct {
	AutoProvision bool
}

// AccessDeploymentCapabilities is what the deployment's configuration allows.
// It deliberately carries no protocol, claim, SMTP or credential material.
type AccessDeploymentCapabilities struct {
	Providers   map[string]AccessProviderCapability
	DurableMail bool
	MFA         bool
}

type AccessPolicyBlockerCode string

const (
	AccessPolicyBlockerProviderUnavailable          AccessPolicyBlockerCode = "provider_unavailable"
	AccessPolicyBlockerProviderAdmissionUnsupported AccessPolicyBlockerCode = "provider_admission_unsupported"
	AccessPolicyBlockerInvitationMailUnavailable    AccessPolicyBlockerCode = "invitation_delivery_unavailable"
	AccessPolicyBlockerLastAdministratorPath        AccessPolicyBlockerCode = "last_system_administrator_path"
	AccessPolicyBlockerMFAUnavailable               AccessPolicyBlockerCode = "mfa_unavailable"
)

// AccessPolicyBlocker is safe to show an operator: ProviderID is already
// public and no user identity is included.
type AccessPolicyBlocker struct {
	Code       AccessPolicyBlockerCode
	ProviderID string
}

type AccessPolicySnapshot struct {
	Policy  *model.AccessPolicy
	History []*model.AccessPolicyTransition
}

type AccessPolicyPreflight struct {
	ExpectedRevision       int64
	Settings               model.AccessPolicySettings
	RevokeExistingSessions bool
	Capabilities           AccessDeploymentCapabilities
	// SessionLimits bound the policy's session lengths; replacement also uses
	// them to pull live session deadlines in.
	SessionLimits model.SessionLengthLimits
	CheckedAt     time.Time
}

// AccessPolicyPreflightResult is the blockers of a proposed replacement and
// counts an administrator weighs before applying it; it names no user.
type AccessPolicyPreflightResult struct {
	Blockers []AccessPolicyBlocker
	// RoleHoldersWithoutMFACount counts active users with a current role
	// binding at any scope and no active second factor.
	RoleHoldersWithoutMFACount int
	// RoleHolderSingleFactorSessionCount counts role holders' live
	// single-factor sessions: the ones revoke_existing_sessions signs out
	// when the replacement starts requiring two-step verification.
	RoleHolderSingleFactorSessionCount int
}

type AccessPolicyReplacement struct {
	Preflight    AccessPolicyPreflight
	ActorID      model.UserID
	AuditEventID model.AuditEventID
	AuditAt      time.Time
}

type AccessPolicyReplacementResult struct {
	Snapshot           *AccessPolicySnapshot
	Changed            bool
	Replayed           bool
	SessionRevocations []AccessPolicySessionRevocation
	// SessionDeadlineChanges name the live sessions a shorter session length
	// pulled in, so cached authentication can be invalidated.
	SessionDeadlineChanges []AccessPolicySessionDeadlineChange
}

// AccessPolicySessionDeadlineChange is what post-commit cache invalidation
// needs for one user whose sessions now end sooner. It carries token hashes,
// never raw credentials.
type AccessPolicySessionDeadlineChange struct {
	UserID            model.UserID
	SessionIDs        []model.SessionID
	AccessTokenHashes []string
}

// AccessPolicySessionRevocation is what post-commit cache invalidation needs
// for one user whose sessions were revoked. It carries token hashes, never
// raw credentials.
type AccessPolicySessionRevocation struct {
	UserID            model.UserID
	SessionIDs        []model.SessionID
	AccessTokenHashes []string
}

type AccessPolicyStore interface {
	Get(context.Context, int) (*AccessPolicySnapshot, error)
	Preflight(context.Context, *AccessPolicyPreflight) (*AccessPolicyPreflightResult, error)
	Replace(context.Context, *AccessPolicyReplacement, *CommandIdempotency) (*AccessPolicyReplacementResult, error)
	// HasRoleHolderMFARequirement reports whether the access policy requires
	// two-step verification of role holders and the active user holds, at the
	// instant, a current binding of an unarchived role whose scope is live.
	HasRoleHolderMFARequirement(context.Context, model.UserID, time.Time) (bool, error)
}

type ErrAccessPolicyRevisionConflict struct{ CurrentRevision int64 }

func (e *ErrAccessPolicyRevisionConflict) Error() string { return "access policy revision conflict" }

type ErrAccessPolicyBlocked struct{ Blockers []AccessPolicyBlocker }

func (e *ErrAccessPolicyBlocked) Error() string { return "access policy transition is blocked" }
