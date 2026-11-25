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

type MFAActivationResult struct {
	Credential        *model.MFACredential
	Session           *model.Session
	AccessTokenHashes []string
}

type MFADisableResult struct {
	AccessTokenHashes []string
}

// MFASecurityNotice is the mail sent with a successful MFA change. It
// deliberately carries no secret, encrypted credential, recovery code or
// recovery-code hash.
type MFASecurityNotice struct {
	Occurrence *model.MailOccurrence
	Delivery   *model.MailDelivery
	Job        *model.Job
}

type MFAActivationMutation struct {
	Principal               model.Principal
	RecentAuthenticationTTL time.Duration
	CredentialID            model.MFACredentialID
	UserID                  model.UserID
	TimeStep                int64
	RecoveryCodes           []*model.MFARecoveryCode
	SessionID               model.SessionID
	At                      time.Time
	AuditEventID            model.AuditEventID
	AuditAt                 time.Time
	Notice                  MFASecurityNotice
}

type MFARecoveryCodesRegeneration struct {
	Principal               model.Principal
	RecentAuthenticationTTL time.Duration
	UserID                  model.UserID
	RecoveryCodes           []*model.MFARecoveryCode
	At                      time.Time
	AuditEventID            model.AuditEventID
	AuditAt                 time.Time
	Notice                  MFASecurityNotice
}

type MFADisablement struct {
	Principal               model.Principal
	RecentAuthenticationTTL time.Duration
	UserID                  model.UserID
	At                      time.Time
	AuditEventID            model.AuditEventID
	AuditAt                 time.Time
	Notice                  MFASecurityNotice
}

// MFASecretKeyUsage counts the live TOTP credentials whose secret is sealed
// with one key of the MFA key ring.
type MFASecretKeyUsage struct {
	KeyID       string
	Credentials int64
}

// MFAStore persists the encrypted TOTP credential, hashed recovery codes and
// used time steps, and changes session strength with them. Activation and
// session upgrade compare at UTC microsecond precision and reject a pending
// credential or session exactly at its expiry.
type MFAStore interface {
	GetRecoveryState(context.Context, model.UserID) (*model.UserMFARecovery, error)
	SavePending(context.Context, *MFAPendingEnrollment) (*model.MFACredential, error)
	Challenge(context.Context, *MFAChallenge) (*SessionReauthenticationResult, error)
	Reset(context.Context, *MFAAssistedReset) (*MFAResetResult, error)
	GetByUser(context.Context, model.UserID) (*model.MFACredential, error)
	Activate(context.Context, *MFAActivationMutation) (*MFAActivationResult, error)
	ConsumeSecondFactor(context.Context, *MFASecondFactorConsumption) error
	ReplaceRecoveryCodes(context.Context, *MFARecoveryCodesRegeneration) error
	SummarizeRecoveryCodes(context.Context, model.UserID) (*MFARecoveryCodeSummary, error)
	// InspectKeyState returns the primary-key fence and, per key in ID order,
	// how many unarchived credentials it seals. Startup uses it to prove the
	// configured key ring can open every secret and write under the fence.
	InspectKeyState(context.Context) (*MFAKeyState, error)
	// StartRekey refuses while another rekey is queued or running, advances
	// the primary-key fence, queues the Job, and completes its audit.
	StartRekey(context.Context, *MFARekeyStart) (*MFARekeyOperation, error)
	// ListRekeyTargets pages live credentials sealed with a key other than the
	// fenced primary; a stale fence is a conflict.
	ListRekeyTargets(context.Context, *MFARekeyTargetPageRequest) (*MFARekeyTargetPage, error)
	// ReplaceRekeyTarget reports whether it re-sealed the credential; a
	// credential archived or re-sealed meanwhile is skipped, and a stale
	// fence is a conflict.
	ReplaceRekeyTarget(context.Context, *MFARekeyReplacement) (bool, error)
	// ProveRekey counts live credentials still sealed with a non-primary key,
	// and with the retiring key.
	ProveRekey(context.Context, *MFARekeyProofRequest) (*MFARekeyProof, error)
	Disable(context.Context, *MFADisablement) (*MFADisableResult, error)
}

// MFASecondFactorConsumption is the input to MFAStore.ConsumeSecondFactor.
type MFASecondFactorConsumption struct {
	UserID           model.UserID
	TimeStep         int64
	RecoveryCodeHash string
	Now              time.Time
}

// MFARecoveryCodeSummary describes a user's unarchived recovery codes. Issued
// counts used codes too, so it stays the size of the set the user was given
// even if the configured count changes. GeneratedAt is zero for an empty set.
type MFARecoveryCodeSummary struct {
	Remaining   int
	Issued      int
	GeneratedAt time.Time
}
