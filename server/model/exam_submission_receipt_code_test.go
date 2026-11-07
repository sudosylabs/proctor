// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package model

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestNewSubmissionReceiptCodeIsCanonicalAndRandom(t *testing.T) {
	t.Parallel()

	seen := make(map[SubmissionReceiptCode]struct{}, 256)
	for range 256 {
		code := NewSubmissionReceiptCode()
		require.True(t, code.IsValid(), "NewSubmissionReceiptCode() = %q", code)
		require.Len(t, code.String(), SubmissionReceiptCodeLength)
		parsed, err := ParseSubmissionReceiptCode(code.Display())
		require.NoError(t, err, "parse display form %q", code.Display())
		require.Equal(t, code, parsed)
		_, duplicate := seen[code]
		require.False(t, duplicate, "generated receipt code repeated")
		seen[code] = struct{}{}
	}
}

func TestSubmissionReceiptCodeDisplayGroupsInFours(t *testing.T) {
	t.Parallel()

	code := SubmissionReceiptCode("MBY3KBMNEB83TFW1")
	require.True(t, code.IsValid())
	require.Equal(t, "MBY3-KBMN-EB83-TFW1", code.Display())
	require.Equal(t, "MBY3KBMNEB83TFW1", code.String())
	require.Empty(t, SubmissionReceiptCode("MBY3").Display(), "invalid code displayed")
}

func TestParseSubmissionReceiptCodeNormalizesHumanInput(t *testing.T) {
	t.Parallel()

	for input, want := range map[string]SubmissionReceiptCode{
		"MBY3-KBMN-EB83-TFW1":          "MBY3KBMNEB83TFW1",
		"MBY3KBMNEB83TFW1":             "MBY3KBMNEB83TFW1",
		"  mby3-kbmn-eb83-tfw1\t\n":    "MBY3KBMNEB83TFW1",
		"mby3 kbmn eb83 tfw1":          "MBY3KBMNEB83TFW1",
		"MBY3-KBMN-EB83-TFWI":          "MBY3KBMNEB83TFW1",
		"MBY3-KBMN-EB83-TFWl":          "MBY3KBMNEB83TFW1",
		"oOoO-0000-1111-iIlL":          "0000000011111111",
		"M-B-Y-3-K-B-M-N-E-B-8-3-TFW1": "MBY3KBMNEB83TFW1",
	} {
		got, err := ParseSubmissionReceiptCode(input)
		require.NoError(t, err, "ParseSubmissionReceiptCode(%q)", input)
		require.Equal(t, want, got, "ParseSubmissionReceiptCode(%q)", input)
	}
}

func TestParseSubmissionReceiptCodeRejectsInvalidInput(t *testing.T) {
	t.Parallel()

	for _, input := range []string{
		"",
		"   ",
		"MBY3-KBMN-EB83-TFW",
		"MBY3-KBMN-EB83-TFW1-0",
		"MBY3-KBMN-EB83-TFWU",
		"mby3-kbmn-eb83-tfwu",
		"MBY3_KBMN_EB83_TFW1",
		"MBY3-KBMN-EB83-TFW!",
		"MBY3-KBMN-EB83-TFWı",
		"MBY3\tKBMNEB83TFW1",
	} {
		_, err := ParseSubmissionReceiptCode(input)
		require.Error(t, err, "ParseSubmissionReceiptCode(%q) accepted", input)
	}
}

func TestSubmissionReceiptCodeValidityIsCanonicalOnly(t *testing.T) {
	t.Parallel()

	for _, code := range []SubmissionReceiptCode{"", "mby3kbmneb83tfw1", "MBY3-KBMN-EB83-TF", "MBY3KBMNEB83TFWI", "MBY3KBMNEB83TFWO", "MBY3KBMNEB83TFWL", "MBY3KBMNEB83TFWU"} {
		require.False(t, code.IsValid(), "%q is valid", code)
	}
	require.True(t, SubmissionReceiptCode("").IsZero())
}

func TestSubmissionReceiptCodeTextRoundTripIsStrict(t *testing.T) {
	t.Parallel()

	code := NewSubmissionReceiptCode()
	data, err := json.Marshal(struct {
		Code SubmissionReceiptCode `json:"code"`
	}{code})
	require.NoError(t, err)
	var decoded struct {
		Code SubmissionReceiptCode `json:"code"`
	}
	require.NoError(t, json.Unmarshal(data, &decoded))
	require.Equal(t, code, decoded.Code)
	require.Error(t, json.Unmarshal([]byte(`{"code":"`+code.Display()+`"}`), &decoded), "display form decoded as canonical")
	_, err = json.Marshal(struct {
		Code SubmissionReceiptCode `json:"code"`
	}{"bad"})
	require.Error(t, err, "invalid code marshaled")
}
