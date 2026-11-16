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

func newTestAttemptEvidence() AttemptEvidence {
	at := time.Date(2026, 11, 14, 9, 30, 0, 125_000_000, time.UTC)
	return AttemptEvidence{ID: NewIntegrityEvidenceID(), AttemptID: NewExamAttemptID(), Kind: AttemptEvidenceProcess,
		Author: NewEnvironmentWorkspaceAuthor(2, "terminal:1"), Sequence: 7,
		Detail: json.RawMessage(`{"argv":["make","test"],"exit":0}`), ObservedAt: at.Add(-time.Second), RecordedAt: at}
}

func TestAttemptEvidenceValidate(t *testing.T) {
	require.NoError(t, newTestAttemptEvidence().Validate())
	gap := newTestAttemptEvidence()
	gap.Kind, gap.Detail = AttemptEvidenceGap, json.RawMessage(`{"reason":"environment_restarted"}`)
	require.NoError(t, gap.Validate(), "a gap is evidence like any other")
	require.True(t, gap.Gap())
	require.False(t, newTestAttemptEvidence().Gap())
	for _, kind := range []AttemptEvidenceKind{AttemptEvidenceProcess, AttemptEvidenceInput, AttemptEvidenceGap,
		AttemptEvidenceOverwritten, AttemptEvidenceRefused} {
		require.True(t, kind.IsValid(), kind)
	}

	tests := map[string]func(*AttemptEvidence){
		"unknown kind":     func(e *AttemptEvidence) { e.Kind = "execution.process_log" },
		"policy kind":      func(e *AttemptEvidence) { e.Kind = AttemptEvidenceKind(IntegrityPolicyFocusLoss) },
		"no author":        func(e *AttemptEvidence) { e.Author = AttemptWorkspaceAuthor{} },
		"malformed author": func(e *AttemptEvidence) { e.Author.Ref = "x" },
		"zero sequence":    func(e *AttemptEvidence) { e.Sequence = 0 },
		"unsafe sequence":  func(e *AttemptEvidence) { e.Sequence = 1 << 60 },
		"array detail":     func(e *AttemptEvidence) { e.Detail = json.RawMessage(`[1]`) },
		"no detail":        func(e *AttemptEvidence) { e.Detail = nil },
		"oversized detail": func(e *AttemptEvidence) { e.Detail = json.RawMessage(`{"a":"` + strings.Repeat("x", 64<<10) + `"}`) },
		"observed later":   func(e *AttemptEvidence) { e.ObservedAt = e.RecordedAt.Add(time.Millisecond) },
		"sub-millisecond":  func(e *AttemptEvidence) { e.RecordedAt = e.RecordedAt.Add(time.Microsecond) },
		"non-UTC observation": func(e *AttemptEvidence) {
			e.ObservedAt = e.ObservedAt.In(time.FixedZone("x", 3600))
		},
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			value := newTestAttemptEvidence()
			mutate(&value)
			require.ErrorIs(t, value.Validate(), ErrAttemptEvidenceInvalid)
		})
	}
}

func TestAttemptEvidenceCanonicalRoundTrips(t *testing.T) {
	value := newTestAttemptEvidence()
	value.Detail = json.RawMessage(`{ "exit": 0, "argv": ["make", "test"] }`)
	raw, err := value.Canonical()
	require.NoError(t, err)
	decoded, err := DecodeAttemptEvidence(raw)
	require.NoError(t, err)
	require.JSONEq(t, string(value.Detail), string(decoded.Detail))
	require.True(t, value.SameClaim(decoded), "canonical form changes only the spelling of Detail")
	again, err := decoded.Canonical()
	require.NoError(t, err)
	require.Equal(t, raw, again, "the canonical form is stable")

	_, err = DecodeAttemptEvidence(append(append([]byte(nil), raw[:len(raw)-1]...), []byte(`,"extra":1}`)...))
	require.Error(t, err, "unknown member")
	_, err = DecodeAttemptEvidence(append(append([]byte(nil), raw...), []byte(` {}`)...))
	require.Error(t, err, "trailing data")
}

func TestAttemptEvidenceSameClaim(t *testing.T) {
	value := newTestAttemptEvidence()
	for name, mutate := range map[string]func(*AttemptEvidence){
		"author":   func(e *AttemptEvidence) { e.Author = NewEnvironmentWorkspaceAuthor(3, "") },
		"detail":   func(e *AttemptEvidence) { e.Detail = json.RawMessage(`{"exit":1}`) },
		"observed": func(e *AttemptEvidence) { e.ObservedAt = e.ObservedAt.Add(time.Millisecond) },
		"sequence": func(e *AttemptEvidence) { e.Sequence++ },
		"kind":     func(e *AttemptEvidence) { e.Kind = AttemptEvidenceInput },
	} {
		other := value
		mutate(&other)
		require.False(t, value.SameClaim(other), name)
	}
	other := value
	other.ID, other.RecordedAt = NewIntegrityEvidenceID(), value.RecordedAt.Add(time.Hour)
	require.True(t, value.SameClaim(other), "identity and receipt time are not part of the claim")
}

func TestAttemptEvidenceInventoryChainsRecords(t *testing.T) {
	empty := NewAttemptEvidenceInventory()
	require.NoError(t, empty.Validate())
	require.Equal(t, SHA256Fingerprint([]byte("proctor.attempt_evidence")), empty.Digest)

	first, err := empty.Append([]byte(`{"a":1}`), false)
	require.NoError(t, err)
	require.Equal(t, AttemptEvidenceInventory{Records: 1, Bytes: 7, Digest: first.Digest}, first)
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

	full := AttemptEvidenceInventory{Records: AttemptEvidenceMaximumRecords, Bytes: 10, Digest: first.Digest}
	require.NoError(t, full.Validate())
	require.False(t, full.Admits(1))
	_, err = full.Append([]byte(`{}`), false)
	require.Error(t, err)
	heavy := AttemptEvidenceInventory{Records: 1, Bytes: AttemptEvidenceMaximumBytes - 1, Digest: first.Digest}
	require.True(t, heavy.Admits(1))
	require.False(t, heavy.Admits(2))

	for name, invalid := range map[string]AttemptEvidenceInventory{
		"empty with bytes":   {Bytes: 1, Digest: empty.Digest},
		"empty other digest": {Digest: first.Digest},
		"more gaps":          {Records: 1, Bytes: 1, Gaps: 2, Digest: first.Digest},
		"bare digest":        {Records: 1, Bytes: 1, Digest: strings.TrimPrefix(first.Digest, "sha256:")},
		"records no bytes":   {Records: 1, Digest: first.Digest},
	} {
		require.Error(t, invalid.Validate(), name)
	}
}

func TestIntegrityReviewAttemptEvidenceDigest(t *testing.T) {
	base := strings.Repeat("a", 64)
	same, err := IntegrityReviewAttemptEvidenceDigest(base, NewAttemptEvidenceInventory())
	require.NoError(t, err)
	require.Equal(t, base, same, "no Attempt evidence leaves earlier finalizations unchanged")

	overflowOnly := NewAttemptEvidenceInventory()
	overflowOnly.Overflow = 3
	bound, err := IntegrityReviewAttemptEvidenceDigest(base, overflowOnly)
	require.NoError(t, err)
	require.NotEqual(t, base, bound, "a refused append is part of what Review binds")

	one, err := NewAttemptEvidenceInventory().Append([]byte(`{"a":1}`), false)
	require.NoError(t, err)
	withRecord, err := IntegrityReviewAttemptEvidenceDigest(base, one)
	require.NoError(t, err)
	require.Len(t, withRecord, 64)
	require.NotEqual(t, bound, withRecord)

	_, err = IntegrityReviewAttemptEvidenceDigest("bad", one)
	require.Error(t, err)
}
