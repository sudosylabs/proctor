// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package model

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
)

const TokenHashLength = sha256.Size * 2

// NewCredentialToken returns a 256-bit opaque bearer credential. Show the raw
// value only when required; never persist or log it.
func NewCredentialToken() string {
	value := make([]byte, 32)
	if _, err := rand.Read(value); err != nil {
		panic("model: operating system random source failed: " + err.Error())
	}
	return base64.RawURLEncoding.EncodeToString(value)
}

// HashToken returns the stored form of a high-entropy random token. Unsalted
// SHA-256 is enough for random bearer tokens; passwords need a password hash.
func HashToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

func IsValidCredentialToken(token string) bool {
	decoded, err := base64.RawURLEncoding.Strict().DecodeString(token)
	return err == nil && len(decoded) == 32
}

func IsValidTokenHash(value string) bool {
	if len(value) != TokenHashLength {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil
}
