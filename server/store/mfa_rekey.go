// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package store

import (
	"time"

	"github.com/sudosylabs/proctor/server/model"
)

// MFARekeyStart fences the key ring on PrimaryKeyID and queues the Job that
// re-seals every live TOTP secret with it.
type MFARekeyStart struct {
	PrimaryKeyID  string
	RetiringKeyID string
	Job           *model.Job
	AuditEventID  model.AuditEventID
	AuditAt       time.Time
}

// MFARekeyOperation is the rekey queued by MFAStore.StartRekey.
type MFARekeyOperation struct {
	JobID         model.JobID
	PrimaryKeyID  string
	RetiringKeyID string
	CreatedAt     time.Time
}

// MFAKeyState is the primary-key fence and the live credentials per key.
// RequiredPrimaryKeyID is empty until the first rekey; after it, every new
// secret must be sealed with that key.
type MFAKeyState struct {
	RequiredPrimaryKeyID string
	ActiveRekeyJobID     model.JobID
	Active               []MFASecretKeyUsage
}

// MFARekeyTarget is a live credential sealed with a non-primary key.
// EncryptedSecret must never leave the rekey Job.
type MFARekeyTarget struct {
	CredentialID    model.MFACredentialID
	UserID          model.UserID
	KeyID           string
	EncryptedSecret string
}

// MFARekeyTargetPageRequest continues a rekey Job's scan after
// AfterCredentialID, in credential ID order.
type MFARekeyTargetPageRequest struct {
	JobID             model.JobID
	PrimaryKeyID      string
	AfterCredentialID model.MFACredentialID
	Limit             int
}

// MFARekeyTargetPage is one page of rekey targets; More means another follows.
type MFARekeyTargetPage struct {
	Targets []MFARekeyTarget
	More    bool
}

// MFARekeyReplacement swaps a credential's envelope for the same secret sealed
// with PrimaryKeyID, only while the credential is live and sealed with
// ExpectedKeyID and the Job still holds the fence.
type MFARekeyReplacement struct {
	JobID           model.JobID
	CredentialID    model.MFACredentialID
	ExpectedKeyID   string
	PrimaryKeyID    string
	EncryptedSecret string
}

// MFARekeyProofRequest asks how many live credentials a rekey Job still has to
// move.
type MFARekeyProofRequest struct {
	JobID         model.JobID
	PrimaryKeyID  string
	RetiringKeyID string
}

// MFARekeyProof holds key IDs and counts only, never key material, owners or
// secrets.
type MFARekeyProof struct {
	PrimaryKeyID         string
	RetiringKeyID        string
	NonPrimaryReferences int64
	RetiringReferences   int64
	RetirementSafe       bool
}
