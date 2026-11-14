// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package model

import (
	"bytes"
	"encoding/json"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// syntheticAgreement is a release agreement on the fixture registry for
// target, certifying enforcement for every capability except the uncertified.
func syntheticAgreement(t *testing.T, target string, uncertified ...NativeCapabilityID) *DesktopNativeAgreement {
	t.Helper()
	registry := checkedRegistry(t)
	matrix := NativeCapabilityMatrix{RegistryDigest: NativeRegistryDigest, MatrixID: "synthetic-matrix", ReleaseID: "synthetic-release", TargetTuple: target}
	for _, capability := range registry.capabilities {
		matrix.Capabilities = append(matrix.Capabilities, NativeCapabilityCertification{
			Capability: capability.ID, EnforceCertified: !slices.Contains(uncertified, capability.ID),
		})
	}
	slices.SortFunc(matrix.Capabilities, func(a, b NativeCapabilityCertification) int {
		return strings.Compare(string(a.Capability), string(b.Capability))
	})
	agreement, err := NewDesktopNativeAgreement(NativeRegistryDigest, SHA256Fingerprint([]byte("matrix")), matrix, registry)
	require.NoError(t, err)
	return agreement
}

// Synthetic certification exercises resolution only, never a production claim.
func nativeResolutionFixture(t *testing.T, uncertified ...NativeCapabilityID) NativePolicyResolution {
	t.Helper()
	agreement := syntheticAgreement(t, "synthetic-darwin-arm64", uncertified...)
	matrix := agreement.Matrix()
	manifest := EmptyAttemptConfigurationManifest()
	at := time.Date(2026, 9, 8, 8, 0, 0, 123000000, time.UTC)
	return NativePolicyResolution{PolicyID: "synthetic-policy", Revision: "one", Ordinal: 1, InstitutionID: NewInstitutionID(), ExamRevisionID: NewExamRevisionID(), SittingID: NewExamSittingID(), Scope: SecurityPolicyScope{Kind: "admission", AdmissionScopeID: "synthetic-admission"}, IssuedAt: at, ActiveFrom: at, ActivationTime: at, Selections: DefaultExamPolicySet(), Build: DesktopBuildTuple{DesktopRelease: "1.0.0", DesktopBuildID: "synthetic-build", Platform: DesktopPlatformDarwin, Architecture: DesktopArchitectureARM64, RealtimeProtocol: 1, AttemptConfigurationManifestFingerprint: manifest.Fingerprint(), DesktopSettingsRegistryFingerprint: "fnv1a64:aaaaaaaaaaaaaaaa", CapabilityMatrixIdentity: matrix.MatrixID, ConfigurationManifest: manifest, DesktopTarget: matrix.TargetTuple, NativeAgreement: agreement}}
}
func setNativeFamily(t *testing.T, input *NativePolicyResolution, index int, raw string) {
	t.Helper()
	require.NoError(t, json.Unmarshal([]byte(raw), &input.Selections.Native.Families[index]))
}
func TestEffectiveNativePolicyCarriesTheScreenRecorderSwitch(t *testing.T) {
	input := nativeResolutionFixture(t)
	off, err := ResolveNativePolicy(input)
	require.NoError(t, err)
	require.False(t, off.Policy.ScreenRecording.Enabled)

	input.Selections.Native.ScreenRecording.Enabled = true
	on, err := ResolveNativePolicy(input)
	require.NoError(t, err)
	require.True(t, on.Policy.ScreenRecording.Enabled, "Desktop learns from the policy whether to record")
	require.NotEqual(t, off.PolicyContentDigest, on.PolicyContentDigest, "the switch is part of the content Desktop verifies")
}

func TestEffectiveNativePolicyContentDigestSurvivesOnlyScopeRebind(t *testing.T) {
	input := nativeResolutionFixture(t)
	resolved, err := ResolveNativePolicy(input)
	require.NoError(t, err)
	require.Len(t, resolved.Policy.Rules, 3, "only the baseline runs when every family is off")
	rebound, err := resolved.Policy.RebindAttempt(NewExamAttemptID(), input.ActivationTime)
	require.NoError(t, err)
	digest, err := rebound.ContentDigest(resolved.CatalogBindings, resolved.CapabilityMatrixDigest)
	require.NoError(t, err, "scope rebinding changed content or retained full identity")
	require.Equal(t, resolved.PolicyContentDigest, digest, "scope rebinding changed content or retained full identity")
	require.NotEqual(t, resolved.Policy.Digest, rebound.Digest, "scope rebinding changed content or retained full identity")
	raw, err := encodeCanonicalExamDocument(rebound)
	require.NoError(t, err)
	require.False(t, bytes.Contains(raw, []byte("expires_at")), "policy carries an independent expiry")
	var decoded EffectiveExamSecurityPolicy
	require.NoError(t, json.Unmarshal(raw, &decoded))
	require.NoError(t, decoded.Validate(input.ActivationTime.Add(48*time.Hour)), "provenance was incorrectly treated as live authority expiry")
	for _, field := range []string{"matrix_id", "evidence_class_id", "revision"} {
		changed := rebound
		document, _ := changed.document()
		document[field] = json.RawMessage(`"changed"`)
		raw, _ := encodeCanonicalExamDocument(document)
		require.Error(t, json.Unmarshal(raw, &decoded), "unbound %s", field)
	}
	changed := resolved.Policy
	changed.Revision = "two"
	require.NoError(t, changed.seal(input.ActivationTime), "seal")
	digest, _ = changed.ContentDigest(resolved.CatalogBindings, resolved.CapabilityMatrixDigest)
	require.NotEqual(t, resolved.PolicyContentDigest, digest, "revision omitted from content identity")
}
func TestOnlyACertifiedFamilyMayEnforce(t *testing.T) {
	enforced := `{"id":"virtualization","mode":"enforce","rule":"block_recognized_guest","outcome":"flag_and_suspend"}`
	input := nativeResolutionFixture(t)
	setNativeFamily(t, &input, 5, enforced)
	_, err := ResolveNativePolicy(input)
	require.NoError(t, err, "a certified family enforces")

	input = nativeResolutionFixture(t, "virtualization")
	setNativeFamily(t, &input, 5, enforced)
	_, err = ResolveNativePolicy(input)
	require.ErrorIs(t, err, ErrNativeCoverageUnsupported, "an uncertified family may not enforce or suspend")

	setNativeFamily(t, &input, 5, `{"id":"virtualization","mode":"observe","rule":"block_recognized_guest","outcome":"flag"}`)
	resolved, err := ResolveNativePolicy(input)
	require.NoError(t, err, "an uncertified family may still be observed")
	require.Contains(t, resolved.Policy.Activations, NativeActivation{Capability: "virtualization", Categories: []string{}})

	input = nativeResolutionFixture(t, "candidate_window", "display", "interactive_session")
	_, err = ResolveNativePolicy(input)
	require.NoError(t, err, "the release's baseline runs whatever it certifies")
}

func TestNothingIsCertifiedOnLinux(t *testing.T) {
	registry := checkedRegistry(t)
	matrix := NativeCapabilityMatrix{RegistryDigest: NativeRegistryDigest, MatrixID: "linux-matrix", ReleaseID: "synthetic-release", TargetTuple: "linux-x64"}
	for _, capability := range syntheticAgreement(t, "synthetic-darwin-arm64").Matrix().Capabilities {
		matrix.Capabilities = append(matrix.Capabilities, NativeCapabilityCertification{Capability: capability.Capability})
	}
	_, err := NewDesktopNativeAgreement(NativeRegistryDigest, SHA256Fingerprint([]byte("matrix")), matrix, registry)
	require.NoError(t, err, "Linux may run uncertified")
	matrix.Capabilities[0].EnforceCertified = true
	_, err = NewDesktopNativeAgreement(NativeRegistryDigest, SHA256Fingerprint([]byte("matrix")), matrix, registry)
	require.Error(t, err, "nothing is certified on Linux")
	matrix.Capabilities = matrix.Capabilities[1:]
	_, err = NewDesktopNativeAgreement(NativeRegistryDigest, SHA256Fingerprint([]byte("matrix")), matrix, registry)
	require.Error(t, err, "every registered capability is listed")
}
func TestNativePolicyResolutionRejectsUnknownCatalogsAndBadProvenance(t *testing.T) {
	input := nativeResolutionFixture(t)
	setNativeFamily(t, &input, 0, `{"id":"process_application","mode":"observe","allowed_application_ids":["application-id"],"outcome":"flag"}`)
	_, err := ResolveNativePolicy(input)
	require.ErrorIs(t, err, ErrNativeCatalogUnavailable, "invented catalog ownership")
	for _, mutate := range []func(*NativePolicyResolution){func(i *NativePolicyResolution) { i.Build.NativeAgreement = nil }, func(i *NativePolicyResolution) { i.ActiveFrom = i.ActivationTime.Add(time.Millisecond) }, func(i *NativePolicyResolution) { i.IssuedAt = i.IssuedAt.Add(time.Nanosecond) }, func(i *NativePolicyResolution) { i.Scope.AttemptID = NewExamAttemptID() }, func(i *NativePolicyResolution) { i.Build.CapabilityMatrixIdentity = "other" }} {
		input = nativeResolutionFixture(t)
		mutate(&input)
		_, err := ResolveNativePolicy(input)
		require.Error(t, err, "invalid resolution accepted")
	}
}
func TestEffectivePolicyRejectsRemovedAndUnknownMembers(t *testing.T) {
	input := nativeResolutionFixture(t)
	resolved, err := ResolveNativePolicy(input)
	require.NoError(t, err)
	raw, err := encodeCanonicalExamDocument(resolved.Policy)
	require.NoError(t, err)
	for _, document := range []string{strings.Replace(string(raw), `"scope":`, `"expires_at":"2026-09-09T00:00:00Z","scope":`, 1), strings.Replace(string(raw), `"scope":`, `"scope":{},"scope":`, 1), strings.Replace(string(raw), `"scope":`, `"SCOPE":`, 1), strings.Replace(string(raw), `"resolved_exception_refs":[]`, `"resolved_exception_refs":["exception"]`, 1)} {
		var value EffectiveExamSecurityPolicy
		require.Error(t, json.Unmarshal([]byte(document), &value), "invalid policy document accepted")
	}
}

func TestReadinessDigestIgnoresPolicyIdentityButNotContent(t *testing.T) {
	input := nativeResolutionFixture(t)
	first, err := ResolveNativePolicy(input)
	require.NoError(t, err)
	input.PolicyID, input.Ordinal = "another-policy", 2
	second, err := ResolveNativePolicy(input)
	require.NoError(t, err)
	firstDigest, err := first.ReadinessDigest()
	require.NoError(t, err)
	secondDigest, err := second.ReadinessDigest()
	require.NoError(t, err)
	require.Equal(t, firstDigest, secondDigest, "a fresh resolution of the same policy reads the same")
	require.NotEqual(t, first.PolicyContentDigest, second.PolicyContentDigest, "the content digest binds the policy's identity")

	input.Selections.Native.ScreenRecording.Enabled = true
	changed, err := ResolveNativePolicy(input)
	require.NoError(t, err)
	changedDigest, err := changed.ReadinessDigest()
	require.NoError(t, err)
	require.NotEqual(t, firstDigest, changedDigest, "turning the recorder on changes what the overview shows")
}
