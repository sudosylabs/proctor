// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

// Package nativetest builds native release agreements for tests, on the
// capability registry Desktop publishes.
package nativetest

import (
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/sudosylabs/proctor/server/internal/factschema"
	"github.com/sudosylabs/proctor/server/model"
)

// NativeRegistry is Desktop's published capability registry, its fact
// schemas compiled, as release verification builds it for a signed release.
func NativeRegistry(t testing.TB) *model.NativeCapabilityRegistry {
	t.Helper()
	_, here, _, ok := runtime.Caller(0)
	require.True(t, ok)
	document, err := os.ReadFile(filepath.Join(filepath.Dir(here), "..", "..", "model", "testdata", "native_capability_registry.json"))
	require.NoError(t, err)
	registry, err := model.ParseNativeCapabilityRegistry(document)
	require.NoError(t, err)
	validator, err := factschema.Compile(registry.FactSchemas())
	require.NoError(t, err)
	registry, err = registry.WithFactValidator(validator)
	require.NoError(t, err)
	return registry
}

// NativeAgreement is a release agreement for build on the published
// registry, certifying every capability to enforce except the uncertified.
// It certifies nothing a real release proved; it exercises the server only.
func NativeAgreement(t testing.TB, build model.DesktopBuildTuple, uncertified ...model.NativeCapabilityID) *model.DesktopNativeAgreement {
	t.Helper()
	registry := NativeRegistry(t)
	capabilities := []model.NativeCapabilityID{}
	for capability := range registry.FactSchemas() {
		capabilities = append(capabilities, capability)
	}
	slices.Sort(capabilities)
	matrix := model.NativeCapabilityMatrix{
		RegistryDigest: model.NativeRegistryDigest, MatrixID: build.CapabilityMatrixIdentity,
		ReleaseID: "synthetic-release", TargetTuple: build.DesktopTarget,
	}
	for _, capability := range capabilities {
		matrix.Capabilities = append(matrix.Capabilities, model.NativeCapabilityCertification{
			Capability: capability, EnforceCertified: !slices.Contains(uncertified, capability),
		})
	}
	agreement, err := model.NewDesktopNativeAgreement(model.NativeRegistryDigest, model.SHA256Fingerprint([]byte("synthetic-matrix")), matrix, registry)
	require.NoError(t, err)
	return agreement
}
