// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package model

import (
	"crypto/rand"
	"encoding/base32"
	"strings"
)

const (
	// SubmissionReceiptCodeLength is the length of the canonical stored form.
	SubmissionReceiptCodeLength = 16
	// SubmissionReceiptCodeAlphabet is Crockford's base32 alphabet, which
	// omits I, L, O, and U so a code survives being read aloud or retyped.
	SubmissionReceiptCodeAlphabet = "0123456789ABCDEFGHJKMNPQRSTVWXYZ"

	submissionReceiptCodeRandomBytes = 10
	submissionReceiptCodeGroupLength = 4
)

var submissionReceiptCodeEncoding = base32.NewEncoding(SubmissionReceiptCodeAlphabet).WithPadding(base32.NoPadding)

// SubmissionReceiptCode is the short human-readable code a candidate can
// quote for one sealed Submission. It carries 80 random bits as 16 uppercase
// Crockford base32 characters. It is minted once when the Submission seals,
// is unique across Submissions, and is not an entity identifier: it must not
// be used as a persistence relationship or an authorization selector.
//
// The canonical form (String) is the 16 characters; Display groups them in
// fours with hyphens. ParseSubmissionReceiptCode accepts either form as typed
// by a person.
type SubmissionReceiptCode string

// NewSubmissionReceiptCode returns a fresh random receipt code. It panics
// only if the operating system random source fails.
func NewSubmissionReceiptCode() SubmissionReceiptCode {
	value := make([]byte, submissionReceiptCodeRandomBytes)
	if _, err := rand.Read(value); err != nil {
		panic("model: operating system random source failed: " + err.Error())
	}
	return SubmissionReceiptCode(submissionReceiptCodeEncoding.EncodeToString(value))
}

// ParseSubmissionReceiptCode normalizes human input to the canonical form. It
// trims surrounding whitespace, uppercases ASCII letters, removes hyphens and
// spaces, and reads I and L as 1 and O as 0. U and every other character
// outside the alphabet are rejected.
func ParseSubmissionReceiptCode(value string) (SubmissionReceiptCode, error) {
	value = strings.TrimSpace(value)
	var canonical strings.Builder
	canonical.Grow(SubmissionReceiptCodeLength)
	for index := 0; index < len(value); index++ {
		character := value[index]
		if character >= 'a' && character <= 'z' {
			character -= 'a' - 'A'
		}
		switch character {
		case '-', ' ':
			continue
		case 'I', 'L':
			character = '1'
		case 'O':
			character = '0'
		}
		if strings.IndexByte(SubmissionReceiptCodeAlphabet, character) < 0 || canonical.Len() == SubmissionReceiptCodeLength {
			return "", invalidSubmissionReceiptCode()
		}
		canonical.WriteByte(character)
	}
	code := SubmissionReceiptCode(canonical.String())
	if !code.IsValid() {
		return "", invalidSubmissionReceiptCode()
	}
	return code, nil
}

func invalidSubmissionReceiptCode() error {
	return invalidModelError("ParseSubmissionReceiptCode", "exam_submission", "receipt_code", "invalid Submission receipt code", "")
}

// IsValid reports whether c is in canonical form.
func (c SubmissionReceiptCode) IsValid() bool {
	if len(c) != SubmissionReceiptCodeLength {
		return false
	}
	for index := 0; index < len(c); index++ {
		if strings.IndexByte(SubmissionReceiptCodeAlphabet, c[index]) < 0 {
			return false
		}
	}
	return true
}

// IsZero reports whether c is absent.
func (c SubmissionReceiptCode) IsZero() bool { return c == "" }

// String returns the canonical 16-character form.
func (c SubmissionReceiptCode) String() string { return string(c) }

// Display returns the grouped form XXXX-XXXX-XXXX-XXXX, or the empty string
// for an invalid code.
func (c SubmissionReceiptCode) Display() string {
	if !c.IsValid() {
		return ""
	}
	var display strings.Builder
	display.Grow(SubmissionReceiptCodeLength + SubmissionReceiptCodeLength/submissionReceiptCodeGroupLength - 1)
	for start := 0; start < SubmissionReceiptCodeLength; start += submissionReceiptCodeGroupLength {
		if start > 0 {
			display.WriteByte('-')
		}
		display.WriteString(string(c[start : start+submissionReceiptCodeGroupLength]))
	}
	return display.String()
}

// MarshalText encodes the canonical form and refuses an invalid code.
func (c SubmissionReceiptCode) MarshalText() ([]byte, error) {
	if !c.IsValid() {
		return nil, invalidModelError("SubmissionReceiptCode.MarshalText", "exam_submission", "receipt_code", "cannot marshal invalid Submission receipt code", "")
	}
	return []byte(c), nil
}

// UnmarshalText accepts only the canonical form; persisted and wire spellings
// are never normalized.
func (c *SubmissionReceiptCode) UnmarshalText(data []byte) error {
	if c == nil {
		return invalidModelError("SubmissionReceiptCode.UnmarshalText", "exam_submission", "receipt_code", "receipt code unmarshal target is nil", "")
	}
	code := SubmissionReceiptCode(data)
	if !code.IsValid() {
		return invalidSubmissionReceiptCode()
	}
	*c = code
	return nil
}
