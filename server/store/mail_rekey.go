// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package store

import (
	"encoding/json"
	"time"

	"github.com/sudosylabs/proctor/server/model"
)

type MailRekeyStart struct {
	PrimaryKeyID  string
	RetiringKeyID string
	Job           *model.Job
	AuditEventID  model.AuditEventID
	AuditAt       time.Time
}

type MailRekeyOperation struct {
	JobID         model.JobID
	PrimaryKeyID  string
	RetiringKeyID string
	CreatedAt     time.Time
}

type MailPayloadKeyUsage struct {
	KeyID            string
	ActiveReferences int64
	// CreatedAt is when the key's current use began.
	CreatedAt time.Time
}

type MailKeyState struct {
	RequiredPrimaryKeyID string
	// PrimaryPromotionAllowed is true only once the rekey that installed
	// RequiredPrimaryKeyID proved no references remain to the retiring key. A
	// node may then stage a new primary while keeping the required key for
	// reads, but its writes are rejected until the next rekey moves the fence.
	PrimaryPromotionAllowed bool
	Active                  []MailPayloadKeyUsage
}

// MailRekeyTargetKind names the kind of encrypted mail value being rekeyed.
type MailRekeyTargetKind string

const (
	MailRekeyTargetDelivery     MailRekeyTargetKind = "delivery"
	MailRekeyTargetFanoutBundle MailRekeyTargetKind = "fanout_bundle"
)

type MailRekeyTarget struct {
	Kind             MailRekeyTargetKind
	ID               string
	KeyID            string
	EncryptedPayload json.RawMessage
}

type MailRekeyTargetPageRequest struct {
	JobID        model.JobID
	PrimaryKeyID string
	AfterKind    MailRekeyTargetKind
	AfterID      string
	Limit        int
}

type MailRekeyTargetPage struct {
	Targets []MailRekeyTarget
	More    bool
}

type MailRekeyReplacement struct {
	JobID            model.JobID
	Kind             MailRekeyTargetKind
	ID               string
	ExpectedKeyID    string
	PrimaryKeyID     string
	EncryptedPayload json.RawMessage
}

type MailRekeyProofRequest struct {
	JobID         model.JobID
	PrimaryKeyID  string
	RetiringKeyID string
}

// MailRekeyProof holds key IDs and reference counts only, never key material,
// owners or payloads.
type MailRekeyProof struct {
	PrimaryKeyID         string
	RetiringKeyID        string
	NonPrimaryReferences int64
	RetiringReferences   int64
	RetirementSafe       bool
}
