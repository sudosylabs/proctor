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

// The bounds of Attempt evidence. One record's Detail is at most
// IntegrityEvidenceDetailMaxBytes. One Attempt keeps at most
// AttemptEvidenceMaximumRecords records and AttemptEvidenceMaximumBytes of
// canonical records; an append past either is refused and only counted, so
// one Submission's Attempt evidence always fits an Examination Export.
const (
	AttemptEvidenceMaxRecordBytes = IntegrityEvidenceDetailMaxBytes + 2048
	AttemptEvidenceMaximumRecords = 4096
	AttemptEvidenceMaximumBytes   = 8 << 20
)

// ErrAttemptEvidenceInvalid reports Attempt evidence or an inventory outside
// its contract.
var ErrAttemptEvidenceInvalid = invalidModelError("AttemptEvidence.Validate", "attempt_evidence", "",
	"invalid Attempt evidence", "")

// AttemptEvidenceKind is the closed set of Attempt evidence the core records.
// A new kind is added here, with the code that records it.
type AttemptEvidenceKind string

const (
	// AttemptEvidenceProcess is a slice of the execution environment's
	// command log: what processes started and how they ended.
	AttemptEvidenceProcess AttemptEvidenceKind = "execution.process"
	// AttemptEvidenceInput is recorded terminal input, only when the Exam
	// opts in. Output is never recorded.
	AttemptEvidenceInput AttemptEvidenceKind = "execution.input"
	// AttemptEvidenceGap says coverage is missing: the environment was lost
	// or restarted, the candidate waited for capacity, the command log broke
	// off, or a settle did not finish before the seal. Detail names why.
	AttemptEvidenceGap AttemptEvidenceKind = "execution.gap"
	// AttemptEvidenceOverwritten is a guest change lost to a newer Workspace
	// version.
	AttemptEvidenceOverwritten AttemptEvidenceKind = "execution.overwritten"
	// AttemptEvidenceRefused is a guest change the Workspace refused, with
	// the reason.
	AttemptEvidenceRefused AttemptEvidenceKind = "execution.refused"
)

func (k AttemptEvidenceKind) IsValid() bool {
	switch k {
	case AttemptEvidenceProcess, AttemptEvidenceInput, AttemptEvidenceGap,
		AttemptEvidenceOverwritten, AttemptEvidenceRefused:
		return true
	default:
		return false
	}
}

// AttemptEvidence is one append-only record of an Attempt's evidence, such as
// a slice of the command log. It never raises a Flag: it is review context,
// and a gap record says that coverage is missing rather than accusing anyone.
//
// The record belongs to the Attempt, not to a Participation, so it can be
// recorded whenever the Attempt has no Submission yet. Author is the actor
// that produced it, such as one environment incarnation. Sequence numbers the
// author's stream of that kind, so a repeated append is recognized.
// ObservedAt is the author's claim, never later than RecordedAt, which is
// PostgreSQL's.
//
// Detail is the bounded JSON object the core stores and presents but does
// not read.
type AttemptEvidence struct {
	ID         IntegrityEvidenceID    `json:"id"`
	AttemptID  ExamAttemptID          `json:"attempt_id"`
	Kind       AttemptEvidenceKind    `json:"kind"`
	Author     AttemptWorkspaceAuthor `json:"author"`
	Sequence   int64                  `json:"sequence"`
	Detail     json.RawMessage        `json:"detail"`
	ObservedAt time.Time              `json:"observed_at"`
	RecordedAt time.Time              `json:"recorded_at"`
}

// Gap reports whether the record says that coverage is missing.
func (e AttemptEvidence) Gap() bool { return e.Kind == AttemptEvidenceGap }

// ValidAttemptEvidenceClaim reports whether what an author supplies for a
// record is within the contract: a known kind, a valid author, a positive
// sequence and one bounded JSON object as Detail.
func ValidAttemptEvidenceClaim(kind AttemptEvidenceKind, author AttemptWorkspaceAuthor, sequence int64, detail json.RawMessage) bool {
	return kind.IsValid() && author.IsValid() && sequence >= 1 && securitySafeInt(sequence) && validEvidenceDetail(detail)
}

// Validate checks identity, provenance, bounds and instants.
func (e AttemptEvidence) Validate() error {
	if !e.ID.IsValid() ||
		!e.AttemptID.IsValid() ||
		!ValidAttemptEvidenceClaim(e.Kind, e.Author, e.Sequence, e.Detail) ||
		!securityInstant(e.ObservedAt) ||
		!securityInstant(e.RecordedAt) ||
		e.ObservedAt.After(e.RecordedAt) {
		return ErrAttemptEvidenceInvalid
	}
	return nil
}

// Canonical returns the record's stored form: canonical JSON within
// AttemptEvidenceMaxRecordBytes.
func (e AttemptEvidence) Canonical() ([]byte, error) {
	if e.Validate() != nil {
		return nil, ErrAttemptEvidenceInvalid
	}
	raw, err := encodeCanonicalExamDocument(e)
	if err != nil || len(raw) > AttemptEvidenceMaxRecordBytes {
		return nil, ErrAttemptEvidenceInvalid
	}
	return raw, nil
}

// DecodeAttemptEvidence strictly decodes a stored canonical record.
func DecodeAttemptEvidence(raw []byte) (AttemptEvidence, error) {
	var value AttemptEvidence
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if len(raw) > AttemptEvidenceMaxRecordBytes || decoder.Decode(&value) != nil || decoder.Decode(&struct{}{}) == nil ||
		value.Validate() != nil {
		return AttemptEvidence{}, ErrAttemptEvidenceInvalid
	}
	value.ObservedAt, value.RecordedAt = value.ObservedAt.UTC(), value.RecordedAt.UTC()
	return value, nil
}

// SameClaim reports whether other repeats e's append: the same kind, author
// and sequence with the same observation time and Detail.
func (e AttemptEvidence) SameClaim(other AttemptEvidence) bool {
	if e.Kind != other.Kind || e.Author != other.Author || e.Sequence != other.Sequence ||
		!e.ObservedAt.Equal(other.ObservedAt) {
		return false
	}
	left, leftErr := encodeCanonicalExamDocument(e.Detail)
	right, rightErr := encodeCanonicalExamDocument(other.Detail)
	return leftErr == nil && rightErr == nil && bytes.Equal(left, right)
}

// AttemptEvidenceInventory is the running account of an Attempt's evidence:
// how many records and canonical bytes it keeps, how many of them are gaps,
// how many appends were refused over the bounds, and a digest chained over
// every kept record in order. The Submission seal fixes it, Review
// finalization binds its digest, and the Examination Export carries it beside
// the records.
type AttemptEvidenceInventory struct {
	Records  int64  `json:"records"`
	Bytes    int64  `json:"bytes"`
	Gaps     int64  `json:"gaps"`
	Overflow int64  `json:"overflow"`
	Digest   string `json:"digest"`
}

const attemptEvidenceInventorySeed = "proctor.attempt_evidence"

// NewAttemptEvidenceInventory returns the inventory of no records.
func NewAttemptEvidenceInventory() AttemptEvidenceInventory {
	return AttemptEvidenceInventory{Digest: SHA256Fingerprint([]byte(attemptEvidenceInventorySeed))}
}

// Validate checks the bounds and the empty digest.
func (v AttemptEvidenceInventory) Validate() error {
	if v.Records < 0 || v.Records > AttemptEvidenceMaximumRecords ||
		v.Bytes < 0 || v.Bytes > AttemptEvidenceMaximumBytes ||
		v.Gaps < 0 || v.Gaps > v.Records ||
		v.Overflow < 0 || !securitySafeInt(v.Overflow) ||
		!IsValidSHA256Fingerprint(v.Digest) ||
		(v.Records == 0 && (v.Bytes != 0 || v.Digest != NewAttemptEvidenceInventory().Digest)) ||
		(v.Records > 0 && v.Bytes == 0) {
		return ErrAttemptEvidenceInvalid
	}
	return nil
}

// Admits reports whether a canonical record of size bytes still fits.
func (v AttemptEvidenceInventory) Admits(size int) bool {
	return v.Records < AttemptEvidenceMaximumRecords && v.Bytes+int64(size) <= AttemptEvidenceMaximumBytes
}

// Append chains one kept canonical record into the inventory.
func (v AttemptEvidenceInventory) Append(record []byte, gap bool) (AttemptEvidenceInventory, error) {
	if v.Validate() != nil || len(record) == 0 || len(record) > AttemptEvidenceMaxRecordBytes || !v.Admits(len(record)) {
		return v, ErrAttemptEvidenceInvalid
	}
	raw := append([]byte(attemptEvidenceInventorySeed+".append\x00"), []byte(v.Digest)...)
	raw = binary.BigEndian.AppendUint64(raw, uint64(v.Records+1)) //nolint:gosec // G115: Validate bounds Records.
	raw = append(raw, record...)
	next := AttemptEvidenceInventory{Records: v.Records + 1, Bytes: v.Bytes + int64(len(record)), Gaps: v.Gaps,
		Overflow: v.Overflow, Digest: SHA256Fingerprint(raw)}
	if gap {
		next.Gaps++
	}
	return next, nil
}

// IntegrityReviewAttemptEvidenceDigest binds the sealed Attempt evidence
// inventory into a Review finalization digest. An Attempt with no evidence
// and no refused append leaves the digest as it was.
func IntegrityReviewAttemptEvidenceDigest(base string, inventory AttemptEvidenceInventory) (string, error) {
	if !validLowerSHA256(base) || inventory.Validate() != nil {
		return "", ErrAttemptEvidenceInvalid
	}
	if inventory.Records == 0 && inventory.Overflow == 0 {
		return base, nil
	}
	raw := append([]byte("proctor.review.attempt_evidence\x00"), []byte(base)...)
	raw = append(raw, []byte(inventory.Digest)...)
	for _, count := range []int64{inventory.Records, inventory.Bytes, inventory.Gaps, inventory.Overflow} {
		raw = binary.BigEndian.AppendUint64(raw, uint64(count)) //nolint:gosec // G115: Validate bounds every count.
	}
	return strings.TrimPrefix(SHA256Fingerprint(raw), "sha256:"), nil
}
