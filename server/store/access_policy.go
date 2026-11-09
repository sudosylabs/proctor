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

// AccessDeploymentCapabilities is an ephemeral validated deployment snapshot.
// It deliberately contains no protocol, claim, SMTP, or credential material.
type AccessDeploymentCapabilities struct {
	Providers   map[string]AccessProviderCapability
	DurableMail bool
	// MFA reports whether the server configuration enables two-step
	// verification.
	MFA bool
}

type AccessPolicyBlockerCode string

const (
	AccessPolicyBlockerProviderUnavailable          AccessPolicyBlockerCode = "provider_unavailable"
	AccessPolicyBlockerProviderAdmissionUnsupported AccessPolicyBlockerCode = "provider_admission_unsupported"
	AccessPolicyBlockerInvitationMailUnavailable    AccessPolicyBlockerCode = "invitation_delivery_unavailable"
	AccessPolicyBlockerLastAdministratorPath        AccessPolicyBlockerCode = "last_system_administrator_path"
	AccessPolicyBlockerMFAUnavailable               AccessPolicyBlockerCode = "mfa_unavailable"
)

// AccessPolicyBlocker contains only safe operator-facing facts. ProviderID is
// an already-public configured identifier; user identities are never exposed.
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
	CheckedAt              time.Time
}

// AccessPolicyPreflightResult reports the blockers of a proposed replacement
// and the safe counts an administrator weighs before applying it.
type AccessPolicyPreflightResult struct {
	Blockers []AccessPolicyBlocker
	// RoleHoldersWithoutMFACount counts active role holders, Users with a
	// current Role Binding at any scope, who have no active second factor.
	RoleHoldersWithoutMFACount int
	// RoleHolderSingleFactorSessionCount counts the live Sessions of active
	// role holders that are not multi-factor: those revoke_existing_sessions
	// signs out when the replacement starts requiring two-step verification
	// of role holders.
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
}

// AccessPolicySessionRevocation contains post-commit invalidation facts for
// one affected User. Raw credentials never leave persistence.
type AccessPolicySessionRevocation struct {
	UserID            model.UserID
	SessionIDs        []model.SessionID
	AccessTokenHashes []string
}

type AccessPolicyStore interface {
	Get(context.Context, int) (*AccessPolicySnapshot, error)
	Preflight(context.Context, *AccessPolicyPreflight) (*AccessPolicyPreflightResult, error)
	Replace(context.Context, *AccessPolicyReplacement, *CommandIdempotency) (*AccessPolicyReplacementResult, error)
	// HasRoleHolderMFARequirement reports whether the current Access Policy
	// requires two-step verification of role holders and the active User holds, at
	// the given instant, a current Role Binding of an unarchived Role whose
	// scope is still live.
	HasRoleHolderMFARequirement(context.Context, model.UserID, time.Time) (bool, error)
}

type ErrAccessPolicyRevisionConflict struct{ CurrentRevision int64 }

func (e *ErrAccessPolicyRevisionConflict) Error() string { return "access policy revision conflict" }

type ErrAccessPolicyBlocked struct{ Blockers []AccessPolicyBlocker }

func (e *ErrAccessPolicyBlocked) Error() string { return "access policy transition is blocked" }
