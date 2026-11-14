// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package model

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestDesktopPublicJWKThumbprintAndValidation(t *testing.T) {
	privateKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)
	jwk := DesktopPublicJWK{
		Kty: "EC", Crv: "P-256",
		X: rawBase64URL.EncodeToString(privateKey.X.FillBytes(make([]byte, 32))),
		Y: rawBase64URL.EncodeToString(privateKey.Y.FillBytes(make([]byte, 32))),
	}
	thumbprint, err := jwk.Thumbprint()
	require.NoError(t, err)
	require.True(t, IsValidDPoPKeyThumbprint(thumbprint), "Thumbprint()")
	_, err = (DesktopPublicJWK{Kty: "EC", Crv: "P-256", X: jwk.X, Y: jwk.X}).Thumbprint()
	require.Error(t, err, "Thumbprint() accepted a point that is not on P-256")
}

func TestDesktopRegistrationValidation(t *testing.T) {
	privateKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)
	jwk := DesktopPublicJWK{Kty: "EC", Crv: "P-256",
		X: rawBase64URL.EncodeToString(privateKey.X.FillBytes(make([]byte, 32))),
		Y: rawBase64URL.EncodeToString(privateKey.Y.FillBytes(make([]byte, 32)))}
	at := time.Date(2026, time.August, 28, 10, 0, 0, 0, time.UTC)
	registration := DesktopRegistration{UserID: NewUserID(), InstitutionID: NewInstitutionID(), PublicJWK: jwk,
		DisplayName: "Candidate laptop", DesktopRelease: "0.1.0", DesktopBuildID: "build-1",
		Platform: DesktopPlatformDarwin, Architecture: DesktopArchitectureARM64, RealtimeProtocol: 1}
	registration.PrepareCreate(NewDesktopRegistrationID(), at)
	err = registration.Validate()
	require.NoError(t, err)
	renamed := registration
	renamed.RenamedAt = OptionalTimeFrom(at.Add(-time.Second))
	require.Error(t, renamed.Validate(), "Validate() accepted a rename before creation")
	renamed.RenamedAt = OptionalTimeFrom(at)
	require.NoError(t, renamed.Validate())
	registration.KeyThumbprint = "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"
	err = registration.Validate()
	require.Error(t, err, "Validate() accepted a mismatched thumbprint")
}

func TestDeviceLabelNormalization(t *testing.T) {
	normalizers := map[string]struct {
		normalize func(string) (string, error)
		maxRunes  int
	}{
		"DesktopRegistrationDisplayName": {NormalizeDesktopRegistrationDisplayName, DesktopRegistrationDisplayNameMaxRunes},
		"SessionDeviceName":              {NormalizeSessionDeviceName, SessionDeviceNameMaxRunes},
	}
	for name, normalizer := range normalizers {
		t.Run(name, func(t *testing.T) {
			label, err := normalizer.normalize("  Lab laptop \u202Eright\u202C  ")
			require.NoError(t, err)
			require.Equal(t, "Lab laptop right", label)

			label, err = normalizer.normalize(strings.Repeat("é", normalizer.maxRunes))
			require.NoError(t, err)
			require.Equal(t, strings.Repeat("é", normalizer.maxRunes), label)

			for input, reason := range map[string]string{
				"":             "blank",
				" \t ":         "whitespace only",
				"\u2066\u2069": "only stripped code points",
				strings.Repeat("x", normalizer.maxRunes+1): "too long",
				"line\nbreak": "control character",
				"bad\xffbyte": "invalid UTF-8",
			} {
				_, err = normalizer.normalize(input)
				var invalid *ValidationError
				require.ErrorAs(t, err, &invalid, "%s label %q", reason, input)
			}
		})
	}
}

func TestDesktopKeysVerifyRawES256Signatures(t *testing.T) {
	t.Parallel()

	private, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)
	public := DesktopPublicJWK{Kty: "EC", Crv: "P-256",
		X: base64.RawURLEncoding.EncodeToString(private.X.FillBytes(make([]byte, 32))),
		Y: base64.RawURLEncoding.EncodeToString(private.Y.FillBytes(make([]byte, 32)))}
	message := []byte(strings.Repeat("ab", 32))
	digest := sha256.Sum256(message)
	r, s, err := ecdsa.Sign(rand.Reader, private, digest[:])
	require.NoError(t, err)
	signature := base64.RawURLEncoding.EncodeToString(append(r.FillBytes(make([]byte, 32)), s.FillBytes(make([]byte, 32))...))

	require.True(t, public.VerifySignature(message, signature))
	require.False(t, public.VerifySignature([]byte("another digest"), signature), "the signature binds its message")
	tampered := []byte(signature)
	tampered[10] ^= 1
	require.False(t, public.VerifySignature(message, string(tampered)), "a changed signature fails")
	last := strings.IndexByte(base64URLAlphabet, signature[DesktopSignatureLength-1])
	respelled := signature[:DesktopSignatureLength-1] + string(base64URLAlphabet[last|1])
	if respelled != signature {
		require.False(t, public.VerifySignature(message, respelled), "a second spelling of the same bytes is refused")
	}
	require.False(t, public.VerifySignature(message, signature+"=="), "padding is not accepted")
	other, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)
	otherPublic := DesktopPublicJWK{Kty: "EC", Crv: "P-256",
		X: base64.RawURLEncoding.EncodeToString(other.X.FillBytes(make([]byte, 32))),
		Y: base64.RawURLEncoding.EncodeToString(other.Y.FillBytes(make([]byte, 32)))}
	require.False(t, otherPublic.VerifySignature(message, signature), "only the signing installation's key verifies")
}

const base64URLAlphabet = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789-_"
