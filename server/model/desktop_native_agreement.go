// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package model

import (
	"errors"
	"slices"
	"strings"
)

// NativeCapabilityCertification says whether a release proved a capability
// can enforce on one target. A capability that is not certified may still be
// observed: its rules record but never block Start or suspend anyone.
type NativeCapabilityCertification struct {
	Capability       NativeCapabilityID `json:"capability"`
	EnforceCertified bool               `json:"enforce_certified"`
}

// NativeCapabilityMatrix is a release's signed certification list for one
// target: one entry for every registered capability, in capability order.
// The release-evidence step produces it from packaged verification on
// certification rigs; nothing is certified that was not proved there.
type NativeCapabilityMatrix struct {
	RegistryDigest string                          `json:"registry_digest"`
	MatrixID       string                          `json:"matrix_id"`
	ReleaseID      string                          `json:"release_id"`
	TargetTuple    string                          `json:"target_tuple"`
	Capabilities   []NativeCapabilityCertification `json:"capabilities"`
}

// DesktopNativeAgreement is immutable verified-release meaning: the
// release's capability registry and its certification list. Signature and
// exact artifact-byte verification belong to the packaging adapter.
type DesktopNativeAgreement struct {
	registryDigest string
	matrixDigest   string
	matrix         NativeCapabilityMatrix
	// capabilities is the release's parsed capability registry: the
	// capabilities, fields and reasons its rules may name, and its baseline.
	capabilities *NativeCapabilityRegistry
}

var errNativeAgreement = errors.New("desktop native agreement is invalid")

// NewDesktopNativeAgreement checks the certification list against the
// registry it was made for: every registered capability listed exactly once,
// in order, and nothing certified on Linux.
func NewDesktopNativeAgreement(
	registryDigest, matrixDigest string,
	matrix NativeCapabilityMatrix,
	registry *NativeCapabilityRegistry,
) (*DesktopNativeAgreement, error) {
	if !IsValidSHA256Fingerprint(registryDigest) || !IsValidSHA256Fingerprint(matrixDigest) || registry == nil ||
		matrix.RegistryDigest != registryDigest || !IsValidAgreementID(matrix.MatrixID) ||
		!IsValidAgreementID(matrix.ReleaseID) || !IsValidAgreementID(matrix.TargetTuple) ||
		len(matrix.Capabilities) != len(registry.capabilities) {
		return nil, errNativeAgreement
	}
	linux := strings.HasPrefix(matrix.TargetTuple, "linux")
	for i, entry := range matrix.Capabilities {
		if _, registered := registry.Capability(entry.Capability); !registered ||
			i > 0 && matrix.Capabilities[i-1].Capability >= entry.Capability ||
			linux && entry.EnforceCertified {
			return nil, errNativeAgreement
		}
	}
	matrix.Capabilities = slices.Clone(matrix.Capabilities)
	return &DesktopNativeAgreement{
		registryDigest: registryDigest, matrixDigest: matrixDigest, matrix: matrix, capabilities: registry,
	}, nil
}

// CapabilityRegistry is the release's parsed capability registry.
func (a *DesktopNativeAgreement) CapabilityRegistry() *NativeCapabilityRegistry {
	if a == nil {
		return nil
	}
	return a.capabilities
}

func (a *DesktopNativeAgreement) RegistryDigest() string {
	if a == nil {
		return ""
	}
	return a.registryDigest
}

func (a *DesktopNativeAgreement) MatrixDigest() string {
	if a == nil {
		return ""
	}
	return a.matrixDigest
}

func (a *DesktopNativeAgreement) Matrix() NativeCapabilityMatrix {
	if a == nil {
		return NativeCapabilityMatrix{}
	}
	matrix := a.matrix
	matrix.Capabilities = slices.Clone(matrix.Capabilities)
	return matrix
}

// EnforceCertified reports whether the release proved the capability can
// enforce on its target.
func (a *DesktopNativeAgreement) EnforceCertified(capability NativeCapabilityID) bool {
	if a == nil {
		return false
	}
	for _, entry := range a.matrix.Capabilities {
		if entry.Capability == capability {
			return entry.EnforceCertified
		}
	}
	return false
}
