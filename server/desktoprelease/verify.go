// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

// Package desktoprelease checks that a packaged Desktop release is one this
// server build trusts: its artifacts match the pinned Expectation byte for byte
// and its certification list carries a valid Ed25519 signature from an
// admitted key. Keys and expectations are compiled in; no request, institution
// setting or upload can add to them.
package desktoprelease

import (
	"bytes"
	"crypto/ed25519"
	"encoding/json"
	"errors"
	"time"
	"unicode/utf8"

	"github.com/sudosylabs/proctor/server/internal/canonicaljson"
	"github.com/sudosylabs/proctor/server/internal/factschema"
	"github.com/sudosylabs/proctor/server/model"
)

const (
	MatrixMaxBytes   = 64 << 10
	ArtifactMaxBytes = 1 << 20
)

var ErrInvalidArtifact = errors.New("desktop release artifact is invalid")

// SignedMatrix is the release's certification list as the exact canonical
// bytes that were signed. Signature is the raw 64-byte Ed25519 signature, not a
// DPoP proof.
type SignedMatrix struct {
	Payload   []byte
	Signature []byte
	KeyID     string
}

// Expectation is compiled into the server, never decoded from a request: the
// exact digests the release's registry and certification list must match.
type Expectation struct {
	Build model.DesktopBuildTuple
	// ReleasedAt is when the release was published. It is pinned with the
	// rest so the server can list its catalogue with dates.
	ReleasedAt           time.Time
	ApplicationReleaseID string
	RegistryDigest       string
	MatrixDigest         string
}

type Artifacts struct {
	// Registry includes the fact schemas and application catalog.
	Registry              []byte
	ConfigurationManifest []byte
	Matrix                SignedMatrix
}

// Verify returns the build only when every artifact matches expected exactly
// and the signature checks out; any mismatch is ErrInvalidArtifact. The result
// does not alias the caller's buffers.
func Verify(expected Expectation, artifacts Artifacts, admittedKeys map[string]ed25519.PublicKey) (model.DesktopBuildTuple, error) {
	if expected.Build.Validate() != nil ||
		expected.ReleasedAt.IsZero() ||
		!model.IsValidAgreementID(expected.ApplicationReleaseID) ||
		!model.IsValidAgreementID(artifacts.Matrix.KeyID) ||
		len(artifacts.Matrix.Payload) == 0 ||
		len(artifacts.Matrix.Payload) > MatrixMaxBytes {
		return model.DesktopBuildTuple{}, ErrInvalidArtifact
	}

	key := admittedKeys[artifacts.Matrix.KeyID]
	if len(key) != ed25519.PublicKeySize ||
		len(artifacts.Matrix.Signature) != ed25519.SignatureSize ||
		!ed25519.Verify(key, artifacts.Matrix.Payload, artifacts.Matrix.Signature) {
		return model.DesktopBuildTuple{}, ErrInvalidArtifact
	}

	if !exactArtifact(artifacts.Registry, expected.RegistryDigest) ||
		!exactArtifact(artifacts.Matrix.Payload, expected.MatrixDigest) ||
		!exactArtifact(artifacts.ConfigurationManifest, expected.Build.AttemptConfigurationManifestFingerprint) ||
		!bytes.Equal(artifacts.ConfigurationManifest, expected.Build.ConfigurationManifest.Canonical()) {
		return model.DesktopBuildTuple{}, ErrInvalidArtifact
	}

	var matrix model.NativeCapabilityMatrix
	if decodeCanonicalClosed(artifacts.Matrix.Payload, &matrix, MatrixMaxBytes) != nil ||
		matrix.ReleaseID != expected.ApplicationReleaseID ||
		matrix.MatrixID != expected.Build.CapabilityMatrixIdentity ||
		matrix.TargetTuple != expected.Build.TargetTuple() ||
		matrix.RegistryDigest != expected.RegistryDigest {
		return model.DesktopBuildTuple{}, ErrInvalidArtifact
	}

	capabilities, err := model.ParseNativeCapabilityRegistry(artifacts.Registry)
	if err != nil {
		return model.DesktopBuildTuple{}, ErrInvalidArtifact
	}
	validator, err := factschema.Compile(capabilities.FactSchemas())
	if err == nil {
		capabilities, err = capabilities.WithFactValidator(validator)
	}
	if err != nil {
		return model.DesktopBuildTuple{}, ErrInvalidArtifact
	}

	agreement, err := model.NewDesktopNativeAgreement(expected.RegistryDigest, expected.MatrixDigest, matrix, capabilities)
	if err != nil {
		return model.DesktopBuildTuple{}, ErrInvalidArtifact
	}

	build := expected.Build
	build.ReleasedAt = model.TimeUTC(expected.ReleasedAt)
	build.NativeAgreement = agreement
	return build, nil
}

func exactArtifact(document []byte, digest string) bool {
	return len(document) > 0 &&
		len(document) <= ArtifactMaxBytes &&
		model.IsValidSHA256Fingerprint(digest) &&
		utf8.Valid(document) &&
		json.Valid(document) &&
		model.SHA256Fingerprint(document) == digest
}

// Canonical re-encoding also rejects case aliases and omitted required fields,
// which encoding/json's unknown-field check alone does not detect.
func decodeCanonicalClosed(document []byte, value any, limit int) error {
	canonical, err := canonicaljson.Canonicalize(document, limit)
	if err != nil || !bytes.Equal(canonical, document) {
		return ErrInvalidArtifact
	}
	decoder := json.NewDecoder(bytes.NewReader(document))
	decoder.DisallowUnknownFields()
	if decoder.Decode(value) != nil {
		return ErrInvalidArtifact
	}
	encoded, err := json.Marshal(value)
	if err != nil {
		return ErrInvalidArtifact
	}
	encoded, err = canonicaljson.Canonicalize(encoded, limit)
	if err != nil || !bytes.Equal(encoded, document) {
		return ErrInvalidArtifact
	}
	return nil
}
