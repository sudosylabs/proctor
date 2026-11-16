// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package store

import (
	"errors"
	"time"

	"github.com/sudosylabs/proctor/server/model"
)

var ErrAuthenticationGenerationChanged = errors.New("authentication generation changed")
var ErrMFAReenrollmentRequired = errors.New("MFA reenrollment required")

// ErrMFAEnrollmentRequired refuses a single-factor proof when the access
// policy requires the user to use two-step verification and no restricted web
// session can be issued instead.
var ErrMFAEnrollmentRequired = errors.New("MFA enrollment required by access policy")

// MFAPendingEnrollment replaces a pending setup only for the same live
// session and authentication generation.
type MFAPendingEnrollment struct {
	Principal               model.Principal
	Credential              *model.MFACredential
	Lifetime                time.Duration
	RecentAuthenticationTTL time.Duration
	AuditEventID            model.AuditEventID
}

// MFAChallenge consumes one factor and upgrades only that session. It cannot
// clear an assisted-reset restriction.
type MFAChallenge struct {
	Principal        model.Principal
	CredentialID     model.MFACredentialID
	TimeStep         int64
	RecoveryCodeHash string
	VerifiedAt       time.Time
	AuditEventID     model.AuditEventID
}

// MFAAssistedReset is an administrator's reset of another user's MFA. The
// store rechecks authority, the administrator's strong recent credential and
// the active target under the same fences as issuance and authority changes.
// Retiring the credential, advancing the generation, requiring reenrollment,
// revoking every session, token and unfinished grant of the target, and the
// audit and notice commit together. A repeated reset is not a replay: it
// advances the generation again.
type MFAAssistedReset struct {
	Principal               model.Principal
	UserID                  model.UserID
	IdentityVerified        bool
	Reason                  string
	VerificationReference   string
	RecentAuthenticationTTL time.Duration
	AuditEventID            model.AuditEventID
	Capabilities            AccessDeploymentCapabilities
	Notice                  MFASecurityNotice
	NoticeAt                time.Time
}

type MFAResetResult struct {
	Recovery          *model.UserMFARecovery
	Sessions          []*model.Session
	AccessTokenHashes []string
}
