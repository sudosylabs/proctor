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

// Bounds on attempt evidence. An append past either per-attempt bound is
// refused and only counted, so one submission's evidence always fits an
// examination export.
const (
	AttemptEvidenceMaxRecordBytes = IntegrityEvidenceDetailMaxBytes + 2048
	AttemptEvidenceMaximumRecords = 4096
	AttemptEvidenceMaximumBytes   = 8 << 20
)

// ErrAttemptEvidenceInvalid reports a record or inventory outside its bounds.
var ErrAttemptEvidenceInvalid = invalidModelError("AttemptEvidence.Validate", "attempt_evidence", "",
	"invalid Attempt evidence", "")

// AttemptEvidenceKind is the closed set of evidence the core records; a new
// kind is added together with the code that records it.
type AttemptEvidenceKind string

const (
	// AttemptEvidenceProcess is a slice of the execution environment's
	// command log: what processes started and how they ended.
	AttemptEvidenceProcess AttemptEvidenceKind = "execution.process"
	// AttemptEvidenceInput is recorded terminal input, only when the exam opts
	// in. Output is never recorded.
	AttemptEvidenceInput AttemptEvidenceKind = "execution.input"
	// AttemptEvidenceGap says coverage is missing: the environment was lost
	// or restarted, the candidate waited for capacity, the command log broke
	// off, or a settle did not finish before the seal. Detail names why.
	AttemptEvidenceGap AttemptEvidenceKind = "execution.gap"
	// AttemptEvidenceOverwritten is a guest change lost to a newer workspace
	// version.
	AttemptEvidenceOverwritten AttemptEvidenceKind = "execution.overwritten"
	// AttemptEvidenceRefused is a guest change the workspace refused, with the
	// reason.
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

// AttemptEvidence is one append-only record of an attempt's evidence. It never
// raises a flag: it is review context, and a gap says coverage is missing
// without accusing anyone. It can be recorded until the attempt has a
// submission. Sequence numbers the author's stream of one kind so a repeated
// append is recognized. ObservedAt is the author's claim and never later than
// RecordedAt, which PostgreSQL sets. Detail is a bounded JSON object the core
// stores but does not read.
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

// ValidAttemptEvidenceClaim reports whether an author's input is acceptable: a
// known kind, a valid author, a positive sequence and one bounded JSON object.
func ValidAttemptEvidenceClaim(kind AttemptEvidenceKind, author AttemptWorkspaceAuthor, sequence int64, detail json.RawMessage) bool {
	return kind.IsValid() && author.IsValid() && sequence >= 1 && securitySafeInt(sequence) && validEvidenceDetail(detail)
}

// Validate checks the record's IDs, author, bounds and that ObservedAt is not
// after RecordedAt.
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

// DecodeAttemptEvidence decodes a stored canonical record, rejecting unknown
// fields.
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

// AttemptEvidenceInventory is the running count of an attempt's kept records,
// bytes, gaps and refused appends, with a digest chained over every kept
// record in order. The submission seal fixes it, review finalization binds the
// digest, and the examination export carries it.
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

// Validate checks the bounds and that an empty inventory has the empty digest.
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

// Append chains one canonical record into the digest and counts it; a record
// that Admits refuses is an error.
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

// IntegrityReviewAttemptEvidenceDigest binds the sealed evidence inventory into
// a review finalization digest. An attempt with no evidence and no refused
// append leaves base unchanged.
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
