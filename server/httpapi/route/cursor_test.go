// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package route

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

type opaqueCursorFixture struct {
	Key string `json:"key"`
}

func opaqueCursorFixtureSpec() CursorSpec[opaqueCursorFixture] {
	return CursorSpec[opaqueCursorFixture]{
		Label: "fixture", MaximumEncodedLength: 256,
		Members: []string{"key"},
		Validate: func(cursor opaqueCursorFixture) error {
			if cursor.Key == "" {
				return errors.New("key is required")
			}
			return nil
		},
	}
}

func TestOpaqueCursorCanonicalRoundTrip(t *testing.T) {
	t.Parallel()
	spec := opaqueCursorFixtureSpec()
	want := opaqueCursorFixture{Key: "keyset"}
	first, err := EncodeCursor(want, spec)
	require.NoError(t, err)
	second, err := EncodeCursor(want, spec)
	require.NoError(t, err)
	require.Equal(t, second, first, "non-deterministic or non-raw-URL encoding")
	require.False(t, strings.ContainsAny(first, "+/="), "non-deterministic or non-raw-URL encoding")
	decoded, err := DecodeCursor(first, spec)
	require.NoError(t, err, "round trip")
	require.Equal(t, want, decoded, "round trip")
	document, err := base64.RawURLEncoding.Strict().DecodeString(first)
	require.NoError(t, err, "encoded document")
	require.JSONEq(t, `{"key":"keyset"}`, string(document), "encoded document carries no version")
}

func TestOpaqueCursorRejectsMalformedDocuments(t *testing.T) {
	t.Parallel()
	spec := opaqueCursorFixtureSpec()
	encodeDocument := func(document string) string {
		return base64.RawURLEncoding.EncodeToString([]byte(document))
	}
	valid := `{"key":"keyset"}`
	tests := map[string]string{
		"empty":              "",
		"over bound":         strings.Repeat("a", spec.MaximumEncodedLength+1),
		"at bound invalid":   strings.Repeat("a", spec.MaximumEncodedLength),
		"malformed alphabet": "%%%",
		"padded base64":      encodeDocument(valid) + "=",
		"trailing bits":      "Zh",
		"invalid UTF-8":      base64.RawURLEncoding.EncodeToString([]byte{0xff}),
		"array":              encodeDocument(`[]`),
		"scalar":             encodeDocument(`1`),
		"null":               encodeDocument(`null`),
		"case alias key":     encodeDocument(`{"Key":"keyset"}`),
		"duplicate key":      encodeDocument(`{"key":"a","key":"b"}`),
		"unknown member":     encodeDocument(`{"key":"keyset","unknown":true}`),
		"version member":     encodeDocument(`{"version":1,"key":"keyset"}`),
		"missing key":        encodeDocument(`{}`),
		"concatenated":       encodeDocument(valid + `{}`),
		"trailing token":     encodeDocument(valid + ` true`),
		"trailing garbage":   encodeDocument(valid + ` garbage`),
	}
	for name, raw := range tests {
		name, raw := name, raw
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			_, err := DecodeCursor(raw, spec)
			require.Error(t, err, "decode(%q) error", raw)
			require.True(t, strings.HasPrefix(err.Error(), "invalid fixture cursor"), "decode(%q) error", raw)
		})
	}
}

func TestOpaqueCursorRejectsEncoderAndSpecFailures(t *testing.T) {
	t.Parallel()
	spec := opaqueCursorFixtureSpec()
	_, err := EncodeCursor(opaqueCursorFixture{}, spec)
	require.Error(t, err, "resource-invalid cursor encoded")

	type marshalFailureCursor struct {
		Value any `json:"value"`
	}
	marshalSpec := CursorSpec[marshalFailureCursor]{
		Label: "marshal fixture", MaximumEncodedLength: 256,
		Members:  []string{"value"},
		Validate: func(marshalFailureCursor) error { return nil },
	}
	_, err = EncodeCursor(marshalFailureCursor{Value: make(chan struct{})}, marshalSpec)
	require.Error(t, err, "JSON marshal failure was ignored")

	invalidSpec := spec
	invalidSpec.Validate = nil
	_, err = EncodeCursor(opaqueCursorFixture{Key: "keyset"}, invalidSpec)
	require.Error(t, err, "invalid encoder specification accepted")
	_, err = DecodeCursor("anything", invalidSpec)
	require.Error(t, err, "invalid decoder specification accepted")
	duplicateMembers := spec
	duplicateMembers.Members = []string{"key", "key"}
	_, err = EncodeCursor(opaqueCursorFixture{Key: "keyset"}, duplicateMembers)
	require.Error(t, err, "duplicate specification members accepted")
	undeclaredMember := spec
	undeclaredMember.Members = []string{"key", "extra"}
	_, err = EncodeCursor(opaqueCursorFixture{Key: "keyset"}, undeclaredMember)
	require.Error(t, err, "specification member absent from the cursor type accepted")
}

// TestOpaqueCursorSpecRejectsVersionMember plants a versioned cursor: cursors
// are unversioned, so a "version" member fails every encode and decode.
func TestOpaqueCursorSpecRejectsVersionMember(t *testing.T) {
	t.Parallel()
	type versionedCursor struct {
		Version int    `json:"version"`
		Key     string `json:"key"`
	}
	spec := CursorSpec[versionedCursor]{
		Label: "versioned fixture", MaximumEncodedLength: 256,
		Members:  []string{"version", "key"},
		Validate: func(versionedCursor) error { return nil },
	}
	_, err := EncodeCursor(versionedCursor{Version: 1, Key: "keyset"}, spec)
	require.Error(t, err, "versioned cursor specification accepted")
	_, err = DecodeCursor(base64.RawURLEncoding.EncodeToString([]byte(`{"version":1,"key":"keyset"}`)), spec)
	require.Error(t, err, "versioned cursor specification accepted")
}

func FuzzOpaqueCursorDecode(f *testing.F) {
	spec := opaqueCursorFixtureSpec()
	valid, err := EncodeCursor(opaqueCursorFixture{Key: "seed"}, spec)
	require.NoError(f, err)
	for _, seed := range []string{"", valid, "%%%", "Zh", base64.RawURLEncoding.EncodeToString([]byte(`null`))} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, raw string) {
		_, _ = DecodeCursor(raw, spec)
	})
}

func TestOpaqueCursorDocumentIsJSON(t *testing.T) {
	// Keep json imported in this focused test so the fixture also proves the
	// emitted document is a normal object consumable by standard decoders.
	raw, err := EncodeCursor(opaqueCursorFixture{Key: "keyset"}, opaqueCursorFixtureSpec())
	require.NoError(t, err)
	document, err := base64.RawURLEncoding.DecodeString(raw)
	require.NoError(t, err, "document")
	require.True(t, json.Valid(document), "document")
}
