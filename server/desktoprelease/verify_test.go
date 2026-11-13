// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package desktoprelease

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/sudosylabs/proctor/server/internal/canonicaljson"
	"github.com/sudosylabs/proctor/server/model"
)

type releaseFixture struct {
	expected  Expectation
	artifacts Artifacts
	keys      map[string]ed25519.PublicKey
	private   ed25519.PrivateKey
	matrix    model.NativeCapabilityMatrix
}

func fixture(t *testing.T) releaseFixture {
	t.Helper()
	public, private, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	manifest := model.EmptyAttemptConfigurationManifest()
	f := releaseFixture{private: private, keys: map[string]ed25519.PublicKey{"synthetic-test-only": public}}
	f.expected = Expectation{Build: model.DesktopBuildTuple{DesktopRelease: "1.0.0", DesktopBuildID: "synthetic-test-build",
		DesktopTarget: "synthetic-darwin-arm64", Platform: model.DesktopPlatformDarwin, Architecture: model.DesktopArchitectureARM64, RealtimeProtocol: 1,
		DesktopSettingsRegistryFingerprint: "fnv1a64:aaaaaaaaaaaaaaaa", ConfigurationManifest: manifest,
		AttemptConfigurationManifestFingerprint: manifest.Fingerprint(), CapabilityMatrixIdentity: "synthetic-test-matrix"}, ApplicationReleaseID: "synthetic-release"}
	registry, err := os.ReadFile("../model/testdata/native_capability_registry.json")
	require.NoError(t, err)
	f.artifacts = Artifacts{Registry: registry, ConfigurationManifest: manifest.Canonical(), Matrix: SignedMatrix{KeyID: "synthetic-test-only"}}
	f.expected.RegistryDigest = model.SHA256Fingerprint(f.artifacts.Registry)
	f.matrix = model.NativeCapabilityMatrix{RegistryDigest: f.expected.RegistryDigest, MatrixID: f.expected.Build.CapabilityMatrixIdentity, ReleaseID: f.expected.ApplicationReleaseID, TargetTuple: f.expected.Build.TargetTuple()}
	parsed, err := model.ParseNativeCapabilityRegistry(registry)
	require.NoError(t, err)
	for _, capability := range []model.NativeCapabilityID{"camera", "candidate_window", "clipboard", "display", "interactive_session",
		"microphone", "network", "printing", "process_application", "removable_storage", "virtualization"} {
		_, registered := parsed.Capability(capability)
		require.True(t, registered, capability)
		f.matrix.Capabilities = append(f.matrix.Capabilities, model.NativeCapabilityCertification{
			Capability: capability, EnforceCertified: capability != "camera",
		})
	}
	f.sign(t)
	return f
}
func canonical(t *testing.T, value any) []byte {
	t.Helper()
	raw, err := json.Marshal(value)
	require.NoError(t, err)
	result, err := canonicaljson.Canonicalize(raw, ArtifactMaxBytes)
	require.NoError(t, err)
	return result
}
func (f *releaseFixture) sign(t *testing.T) {
	t.Helper()
	f.artifacts.Matrix.Payload = canonical(t, f.matrix)
	f.signPayload()
}
func (f *releaseFixture) signPayload() {
	f.artifacts.Matrix.Signature = ed25519.Sign(f.private, f.artifacts.Matrix.Payload)
	f.expected.MatrixDigest = model.SHA256Fingerprint(f.artifacts.Matrix.Payload)
}

func TestVerifyExactReleaseAndItsCertification(t *testing.T) {
	t.Parallel()
	f := fixture(t)
	build, err := Verify(f.expected, f.artifacts, f.keys)
	require.NoError(t, err)
	agreement := build.NativeAgreement
	require.True(t, agreement.EnforceCertified("display"))
	require.False(t, agreement.EnforceCertified("camera"), "an uncertified capability is reported as such")
	_, known := agreement.CapabilityRegistry().Application("teamviewer")
	require.True(t, known, "the registry's application catalog travels with the agreement")
	copied := agreement.Matrix()
	copied.Capabilities[0].EnforceCertified = !copied.Capabilities[0].EnforceCertified
	require.Equal(t, f.matrix.Capabilities[0].EnforceCertified, agreement.Matrix().Capabilities[0].EnforceCertified,
		"verified agreement mutated through caller")
}

func TestVerifyRejectsUntrustedOrInconsistentArtifacts(t *testing.T) {
	t.Parallel()
	tests := map[string]func(*releaseFixture, *testing.T){
		"unknown key":        func(f *releaseFixture, t *testing.T) { f.artifacts.Matrix.KeyID = "unknown" },
		"malformed key":      func(f *releaseFixture, t *testing.T) { f.keys[f.artifacts.Matrix.KeyID] = []byte{1} },
		"tampered signature": func(f *releaseFixture, t *testing.T) { f.artifacts.Matrix.Signature[0] ^= 1 },
		"changed signed bytes": func(f *releaseFixture, t *testing.T) {
			f.artifacts.Matrix.Payload = append(f.artifacts.Matrix.Payload, ' ')
		},
		"registry bytes": func(f *releaseFixture, t *testing.T) {
			f.artifacts.Registry = []byte(`{"synthetic_test_registry":false}`)
		},
		"configuration manifest": func(f *releaseFixture, t *testing.T) { f.artifacts.ConfigurationManifest = []byte(`{}`) },
		"release mismatch":       func(f *releaseFixture, t *testing.T) { f.matrix.ReleaseID = "another-release"; f.sign(t) },
		"target mismatch":        func(f *releaseFixture, t *testing.T) { f.matrix.TargetTuple = "another-target"; f.sign(t) },
		"matrix ID mismatch":     func(f *releaseFixture, t *testing.T) { f.matrix.MatrixID = "another-matrix"; f.sign(t) },
		"registry mismatch": func(f *releaseFixture, t *testing.T) {
			f.matrix.RegistryDigest = model.SHA256Fingerprint([]byte(`{}`))
			f.sign(t)
		},
		"missing capability": func(f *releaseFixture, t *testing.T) { f.matrix.Capabilities = f.matrix.Capabilities[1:]; f.sign(t) },
		"unregistered capability": func(f *releaseFixture, t *testing.T) {
			f.matrix.Capabilities[0].Capability = "teleport"
			f.sign(t)
		},
		"out of order": func(f *releaseFixture, t *testing.T) {
			f.matrix.Capabilities[0], f.matrix.Capabilities[1] = f.matrix.Capabilities[1], f.matrix.Capabilities[0]
			f.sign(t)
		},
		"case alias": func(f *releaseFixture, t *testing.T) {
			f.artifacts.Matrix.Payload = []byte(strings.Replace(string(f.artifacts.Matrix.Payload), `"matrix_id"`, `"MATRIX_ID"`, 1))
			f.signPayload()
		},
		"duplicate key": func(f *releaseFixture, t *testing.T) {
			f.artifacts.Matrix.Payload = []byte(strings.Replace(string(f.artifacts.Matrix.Payload), `"matrix_id":`, `"matrix_id":"duplicate","matrix_id":`, 1))
			f.signPayload()
		},
		"old version selector": func(f *releaseFixture, t *testing.T) {
			f.artifacts.Matrix.Payload = append([]byte(`{"schema_version":2,`), f.artifacts.Matrix.Payload[1:]...)
			f.signPayload()
		},
		"omitted required field": func(f *releaseFixture, t *testing.T) {
			f.artifacts.Matrix.Payload = []byte(strings.Replace(string(f.artifacts.Matrix.Payload), `,"enforce_certified":true}`, "}", 1))
			f.signPayload()
		},
		"null list": func(f *releaseFixture, t *testing.T) { f.matrix.Capabilities = nil; f.sign(t) },
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			f := fixture(t)
			mutate(&f, t)
			_, err := Verify(f.expected, f.artifacts, f.keys)
			require.Equal(t, ErrInvalidArtifact, err)
		})
	}
}

func TestVerifyRefusesASignedRegistryItCannotParse(t *testing.T) {
	t.Parallel()
	f := fixture(t)
	f.artifacts.Registry = []byte(`{"description":"a registry in a format the server does not model"}`)
	f.expected.RegistryDigest = model.SHA256Fingerprint(f.artifacts.Registry)
	f.matrix.RegistryDigest = f.expected.RegistryDigest
	f.sign(t)
	_, err := Verify(f.expected, f.artifacts, f.keys)
	require.ErrorIs(t, err, ErrInvalidArtifact, "an exact, signed registry must still be one the server understands")
}
