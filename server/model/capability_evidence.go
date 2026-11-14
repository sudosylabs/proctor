// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package model

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"strings"
	"time"
)

// The bounds of capability evidence. One record's Detail is at most
// IntegrityEvidenceDetailMaxBytes, as for every capability's evidence. One
// Attempt keeps at most CapabilityEvidenceMaximumRecords records and
// CapabilityEvidenceMaximumBytes of canonical records; an append past either
// is refused and only counted, so one Submission's capability evidence always
// fits an Examination Export.
const (
	CapabilityEvidenceMaxRecordBytes = IntegrityEvidenceDetailMaxBytes + 2048
	CapabilityEvidenceMaximumRecords = 4096
	CapabilityEvidenceMaximumBytes   = 8 << 20
)

// ErrCapabilityEvidenceInvalid reports capability evidence or an inventory
// outside its contract.
var ErrCapabilityEvidenceInvalid = invalidModelError("CapabilityEvidence.Validate", "capability_evidence", "",
	"invalid capability evidence", "")

// CapabilityEvidence is one record a capability plugin appended for an
// Attempt through its execution environment binding, such as a slice of the
// process log. It never raises a Flag: it is review context, and a Gap record
// says that coverage is missing (the environment was unavailable or
// restarted, the log broke off) rather than accusing anyone.
//
// Kind is the plugin's own "<plugin-id>.<kind>"; the native namespace is the
// core's. Sequence numbers the plugin's stream for that kind within the
// binding, so a repeated append is recognized. ObservedAt is the plugin's
// claim, never later than RecordedAt, which is PostgreSQL's.
//
// Detail is the bounded JSON object the core stores and presents but does
// not read. Large payloads, such as a terminal input recording, are meant to
// become chunks in VFS that a later field references by object identity and
// SHA-256; today a record carries only its bounded Detail.
type CapabilityEvidence struct {
	ID              IntegrityEvidenceID         `json:"id"`
	AttemptID       ExamAttemptID               `json:"attempt_id"`
	ParticipationID AttemptParticipationID      `json:"participation_id"`
	Generation      int64                       `json:"generation"`
	BindingID       AttemptEnvironmentBindingID `json:"environment_binding_id"`
	Kind            IntegrityPolicyKind         `json:"policy_kind"`
	Sequence        int64                       `json:"sequence"`
	Gap             bool                        `json:"gap"`
	Detail          json.RawMessage             `json:"detail"`
	ObservedAt      time.Time                   `json:"observed_at"`
	RecordedAt      time.Time                   `json:"recorded_at"`
}

// IsPluginCapabilityKind reports whether kind is a capability plugin's
// "<plugin-id>.<kind>", outside the core's native namespace.
func IsPluginCapabilityKind(kind IntegrityPolicyKind) bool {
	return kind.IsCapability() && !strings.HasPrefix(string(kind), NativeIntegrityPolicyNamespace+".")
}

// ValidCapabilityEvidenceClaim reports whether what a plugin supplies for a
// record is within the contract: a plugin's kind, a positive sequence and one
// bounded JSON object as Detail.
func ValidCapabilityEvidenceClaim(kind IntegrityPolicyKind, sequence int64, detail json.RawMessage) bool {
	return IsPluginCapabilityKind(kind) && sequence >= 1 && securitySafeInt(sequence) && validCapabilityEvidenceDetail(detail)
}

// Validate checks identity, provenance, bounds and instants.
func (e CapabilityEvidence) Validate() error {
	if !e.ID.IsValid() ||
		!e.AttemptID.IsValid() ||
		!e.ParticipationID.IsValid() ||
		e.Generation < 1 ||
		!securitySafeInt(e.Generation) ||
		!e.BindingID.IsValid() ||
		!ValidCapabilityEvidenceClaim(e.Kind, e.Sequence, e.Detail) ||
		!securityInstant(e.ObservedAt) ||
		!securityInstant(e.RecordedAt) ||
		e.ObservedAt.After(e.RecordedAt) {
		return ErrCapabilityEvidenceInvalid
	}
	return nil
}

// Canonical returns the record's stored form: canonical JSON within
// CapabilityEvidenceMaxRecordBytes.
func (e CapabilityEvidence) Canonical() ([]byte, error) {
	if e.Validate() != nil {
		return nil, ErrCapabilityEvidenceInvalid
	}
	raw, err := encodeCanonicalExamDocument(e)
	if err != nil || len(raw) > CapabilityEvidenceMaxRecordBytes {
		return nil, ErrCapabilityEvidenceInvalid
	}
	return raw, nil
}

// DecodeCapabilityEvidence strictly decodes a stored canonical record.
func DecodeCapabilityEvidence(raw []byte) (CapabilityEvidence, error) {
	var value CapabilityEvidence
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if len(raw) > CapabilityEvidenceMaxRecordBytes || decoder.Decode(&value) != nil || decoder.Decode(&struct{}{}) == nil ||
		value.Validate() != nil {
		return CapabilityEvidence{}, ErrCapabilityEvidenceInvalid
	}
	value.ObservedAt, value.RecordedAt = value.ObservedAt.UTC(), value.RecordedAt.UTC()
	return value, nil
}

// SameClaim reports whether other repeats e's append: the same binding,
// kind and sequence with the same gap marker, observation time and Detail.
func (e CapabilityEvidence) SameClaim(other CapabilityEvidence) bool {
	if e.BindingID != other.BindingID || e.Kind != other.Kind || e.Sequence != other.Sequence ||
		e.Gap != other.Gap || !e.ObservedAt.Equal(other.ObservedAt) {
		return false
	}
	left, leftErr := encodeCanonicalExamDocument(e.Detail)
	right, rightErr := encodeCanonicalExamDocument(other.Detail)
	return leftErr == nil && rightErr == nil && bytes.Equal(left, right)
}

// CapabilityEvidenceInventory is the running account of an Attempt's
// capability evidence: how many records and canonical bytes it keeps, how
// many of them are gaps, how many appends were refused over the bounds, and
// a digest chained over every kept record in order. The Submission seal
// fixes it, Review finalization binds its digest, and the Examination Export
// carries it beside the records.
type CapabilityEvidenceInventory struct {
	Records  int64  `json:"records"`
	Bytes    int64  `json:"bytes"`
	Gaps     int64  `json:"gaps"`
	Overflow int64  `json:"overflow"`
	Digest   string `json:"digest"`
}

const capabilityEvidenceInventorySeed = "proctor.capability_evidence"

// NewCapabilityEvidenceInventory returns the inventory of no records.
func NewCapabilityEvidenceInventory() CapabilityEvidenceInventory {
	return CapabilityEvidenceInventory{Digest: SHA256Fingerprint([]byte(capabilityEvidenceInventorySeed))}
}

// Validate checks the bounds and the empty digest.
func (v CapabilityEvidenceInventory) Validate() error {
	if v.Records < 0 || v.Records > CapabilityEvidenceMaximumRecords ||
		v.Bytes < 0 || v.Bytes > CapabilityEvidenceMaximumBytes ||
		v.Gaps < 0 || v.Gaps > v.Records ||
		v.Overflow < 0 || !securitySafeInt(v.Overflow) ||
		!IsValidSHA256Fingerprint(v.Digest) ||
		(v.Records == 0 && (v.Bytes != 0 || v.Digest != NewCapabilityEvidenceInventory().Digest)) ||
		(v.Records > 0 && v.Bytes == 0) {
		return ErrCapabilityEvidenceInvalid
	}
	return nil
}

// Admits reports whether a canonical record of size bytes still fits.
func (v CapabilityEvidenceInventory) Admits(size int) bool {
	return v.Records < CapabilityEvidenceMaximumRecords && v.Bytes+int64(size) <= CapabilityEvidenceMaximumBytes
}

// Append chains one kept canonical record into the inventory.
func (v CapabilityEvidenceInventory) Append(record []byte, gap bool) (CapabilityEvidenceInventory, error) {
	if v.Validate() != nil || len(record) == 0 || len(record) > CapabilityEvidenceMaxRecordBytes || !v.Admits(len(record)) {
		return v, ErrCapabilityEvidenceInvalid
	}
	raw := append([]byte(capabilityEvidenceInventorySeed+".append\x00"), []byte(v.Digest)...)
	raw = binary.BigEndian.AppendUint64(raw, uint64(v.Records+1)) //nolint:gosec // G115: Validate bounds Records.
	raw = append(raw, record...)
	next := CapabilityEvidenceInventory{Records: v.Records + 1, Bytes: v.Bytes + int64(len(record)), Gaps: v.Gaps,
		Overflow: v.Overflow, Digest: SHA256Fingerprint(raw)}
	if gap {
		next.Gaps++
	}
	return next, nil
}

// IntegrityReviewCapabilityDigest binds the sealed capability evidence
// inventory into a Review finalization digest. An Attempt with no capability
// evidence and no refused append leaves the digest as it was.
func IntegrityReviewCapabilityDigest(base string, inventory CapabilityEvidenceInventory) (string, error) {
	if !validLowerSHA256(base) || inventory.Validate() != nil {
		return "", ErrCapabilityEvidenceInvalid
	}
	if inventory.Records == 0 && inventory.Overflow == 0 {
		return base, nil
	}
	raw := append([]byte("proctor.review.capability_evidence\x00"), []byte(base)...)
	raw = append(raw, []byte(inventory.Digest)...)
	for _, count := range []int64{inventory.Records, inventory.Bytes, inventory.Gaps, inventory.Overflow} {
		raw = binary.BigEndian.AppendUint64(raw, uint64(count)) //nolint:gosec // G115: Validate bounds every count.
	}
	return strings.TrimPrefix(SHA256Fingerprint(raw), "sha256:"), nil
}
