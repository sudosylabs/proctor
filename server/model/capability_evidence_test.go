// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package model

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func newTestCapabilityEvidence() CapabilityEvidence {
	at := time.Date(2026, 11, 14, 9, 30, 0, 125_000_000, time.UTC)
	return CapabilityEvidence{ID: NewIntegrityEvidenceID(), AttemptID: NewExamAttemptID(), ParticipationID: NewAttemptParticipationID(),
		Generation: 2, BindingID: NewAttemptEnvironmentBindingID(), Kind: "execution.process_log", Sequence: 7,
		Detail: json.RawMessage(`{"argv":["make","test"],"exit":0}`), ObservedAt: at.Add(-time.Second), RecordedAt: at}
}

func TestCapabilityEvidenceValidate(t *testing.T) {
	require.NoError(t, newTestCapabilityEvidence().Validate())
	gap := newTestCapabilityEvidence()
	gap.Gap, gap.Detail = true, json.RawMessage(`{"reason":"environment_restarted"}`)
	require.NoError(t, gap.Validate(), "a gap is evidence like any other")

	tests := map[string]func(*CapabilityEvidence){
		"core kind":        func(e *CapabilityEvidence) { e.Kind = IntegrityPolicyFocusLoss },
		"native kind":      func(e *CapabilityEvidence) { e.Kind = "native.display_topology" },
		"zero sequence":    func(e *CapabilityEvidence) { e.Sequence = 0 },
		"unsafe sequence":  func(e *CapabilityEvidence) { e.Sequence = 1 << 60 },
		"no generation":    func(e *CapabilityEvidence) { e.Generation = 0 },
		"no binding":       func(e *CapabilityEvidence) { e.BindingID = "" },
		"array detail":     func(e *CapabilityEvidence) { e.Detail = json.RawMessage(`[1]`) },
		"no detail":        func(e *CapabilityEvidence) { e.Detail = nil },
		"oversized detail": func(e *CapabilityEvidence) { e.Detail = json.RawMessage(`{"a":"` + strings.Repeat("x", 64<<10) + `"}`) },
		"observed later":   func(e *CapabilityEvidence) { e.ObservedAt = e.RecordedAt.Add(time.Millisecond) },
		"sub-millisecond":  func(e *CapabilityEvidence) { e.RecordedAt = e.RecordedAt.Add(time.Microsecond) },
		"non-UTC observation": func(e *CapabilityEvidence) {
			e.ObservedAt = e.ObservedAt.In(time.FixedZone("x", 3600))
		},
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			value := newTestCapabilityEvidence()
			mutate(&value)
			require.ErrorIs(t, value.Validate(), ErrCapabilityEvidenceInvalid)
		})
	}
}

func TestCapabilityEvidenceCanonicalRoundTrips(t *testing.T) {
	value := newTestCapabilityEvidence()
	value.Detail = json.RawMessage(`{ "exit": 0, "argv": ["make", "test"] }`)
	raw, err := value.Canonical()
	require.NoError(t, err)
	decoded, err := DecodeCapabilityEvidence(raw)
	require.NoError(t, err)
	require.JSONEq(t, string(value.Detail), string(decoded.Detail))
	require.True(t, value.SameClaim(decoded), "canonical form changes only the spelling of Detail")
	again, err := decoded.Canonical()
	require.NoError(t, err)
	require.Equal(t, raw, again, "the canonical form is stable")

	_, err = DecodeCapabilityEvidence(append(append([]byte(nil), raw[:len(raw)-1]...), []byte(`,"extra":1}`)...))
	require.Error(t, err, "unknown member")
	_, err = DecodeCapabilityEvidence(append(append([]byte(nil), raw...), []byte(` {}`)...))
	require.Error(t, err, "trailing data")
}

func TestCapabilityEvidenceSameClaim(t *testing.T) {
	value := newTestCapabilityEvidence()
	for name, mutate := range map[string]func(*CapabilityEvidence){
		"gap":      func(e *CapabilityEvidence) { e.Gap = true },
		"detail":   func(e *CapabilityEvidence) { e.Detail = json.RawMessage(`{"exit":1}`) },
		"observed": func(e *CapabilityEvidence) { e.ObservedAt = e.ObservedAt.Add(time.Millisecond) },
		"sequence": func(e *CapabilityEvidence) { e.Sequence++ },
		"kind":     func(e *CapabilityEvidence) { e.Kind = "execution.terminal_input" },
	} {
		other := value
		mutate(&other)
		require.False(t, value.SameClaim(other), name)
	}
	other := value
	other.ID, other.RecordedAt = NewIntegrityEvidenceID(), value.RecordedAt.Add(time.Hour)
	require.True(t, value.SameClaim(other), "identity and receipt time are not part of the claim")
}

func TestCapabilityEvidenceInventoryChainsRecords(t *testing.T) {
	empty := NewCapabilityEvidenceInventory()
	require.NoError(t, empty.Validate())
	require.Equal(t, SHA256Fingerprint([]byte("proctor.capability_evidence")), empty.Digest)

	first, err := empty.Append([]byte(`{"a":1}`), false)
	require.NoError(t, err)
	require.Equal(t, CapabilityEvidenceInventory{Records: 1, Bytes: 7, Digest: first.Digest}, first)
	second, err := first.Append([]byte(`{"b":2}`), true)
	require.NoError(t, err)
	require.Equal(t, int64(2), second.Records)
	require.Equal(t, int64(1), second.Gaps)
	require.NotEqual(t, first.Digest, second.Digest)

	reordered, err := empty.Append([]byte(`{"b":2}`), true)
	require.NoError(t, err)
	reordered, err = reordered.Append([]byte(`{"a":1}`), false)
	require.NoError(t, err)
	require.NotEqual(t, second.Digest, reordered.Digest, "the digest fixes the order")

	full := CapabilityEvidenceInventory{Records: CapabilityEvidenceMaximumRecords, Bytes: 10, Digest: first.Digest}
	require.NoError(t, full.Validate())
	require.False(t, full.Admits(1))
	_, err = full.Append([]byte(`{}`), false)
	require.Error(t, err)
	heavy := CapabilityEvidenceInventory{Records: 1, Bytes: CapabilityEvidenceMaximumBytes - 1, Digest: first.Digest}
	require.True(t, heavy.Admits(1))
	require.False(t, heavy.Admits(2))

	for name, invalid := range map[string]CapabilityEvidenceInventory{
		"empty with bytes":   {Bytes: 1, Digest: empty.Digest},
		"empty other digest": {Digest: first.Digest},
		"more gaps":          {Records: 1, Bytes: 1, Gaps: 2, Digest: first.Digest},
		"bare digest":        {Records: 1, Bytes: 1, Digest: strings.TrimPrefix(first.Digest, "sha256:")},
		"records no bytes":   {Records: 1, Digest: first.Digest},
	} {
		require.Error(t, invalid.Validate(), name)
	}
}

func TestIntegrityReviewCapabilityDigest(t *testing.T) {
	base := strings.Repeat("a", 64)
	same, err := IntegrityReviewCapabilityDigest(base, NewCapabilityEvidenceInventory())
	require.NoError(t, err)
	require.Equal(t, base, same, "no capability evidence leaves earlier finalizations unchanged")

	overflowOnly := NewCapabilityEvidenceInventory()
	overflowOnly.Overflow = 3
	bound, err := IntegrityReviewCapabilityDigest(base, overflowOnly)
	require.NoError(t, err)
	require.NotEqual(t, base, bound, "a refused append is part of what Review binds")

	one, err := NewCapabilityEvidenceInventory().Append([]byte(`{"a":1}`), false)
	require.NoError(t, err)
	withRecord, err := IntegrityReviewCapabilityDigest(base, one)
	require.NoError(t, err)
	require.Len(t, withRecord, 64)
	require.NotEqual(t, bound, withRecord)

	_, err = IntegrityReviewCapabilityDigest("bad", one)
	require.Error(t, err)
}
