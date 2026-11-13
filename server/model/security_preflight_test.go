// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package model

import (
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func preflightFixture(t *testing.T) (NativePolicyResolution, ResolvedNativePolicy, SecurityPreflightChallenge, SecurityPreflightReport) {
	t.Helper()
	input := nativeResolutionFixture(t)
	setNativeFamily(t, &input, 2, `{"id":"clipboard","mode":"observe","strategy":"disabled","observe_os_clipboard_changes":true,"outcome":"flag"}`)
	resolved, err := ResolveNativePolicy(input)
	require.NoError(t, err)
	challenge := SecurityPreflightChallenge{PreflightID: "synthetic-preflight", Challenge: base64.RawURLEncoding.EncodeToString(make([]byte, 32)), IssuedAt: input.ActivationTime, ExpiresAt: input.ActivationTime.Add(SecurityPreflightLifetime)}
	report := SecurityPreflightReport{Challenge: challenge.Challenge, PolicyDigest: resolved.Policy.Digest, PolicyContentDigest: resolved.PolicyContentDigest, SecuritySessionID: "synthetic-security-session", Capabilities: readinessFor(resolved.Policy.Activations), Rules: []NativeRuleReport{}, ReportedAt: input.ActivationTime}
	return input, resolved, challenge, report
}

// readinessFor reports every activated capability as compliant, in
// activation order: the baseline's from compliantStart, and an unchanged
// clipboard.
func readinessFor(activations []NativeActivation) []NativeCapabilityReadiness {
	baseline := map[NativeCapabilityID]NativeCapabilityReadiness{}
	for _, readiness := range compliantStart() {
		baseline[readiness.Capability] = readiness
	}
	reported := []NativeCapabilityReadiness{}
	for _, activation := range activations {
		readiness, ok := baseline[activation.Capability]
		if !ok {
			readiness = readinessOf(activation.Capability, NativeCertaintyComplete, `{"changes":0}`, `{}`)
		}
		reported = append(reported, readiness)
	}
	return reported
}

func TestSecurityPreflightEligibleDegradedObservation(t *testing.T) {
	input, resolved, challenge, report := preflightFixture(t)
	result, err := EvaluateSecurityPreflight(challenge, resolved, input.Build.NativeAgreement, report, input.ActivationTime)
	require.NoError(t, err, "result = %#v", result)
	require.Equal(t, "eligible", result.Admission, "result = %#v", result)
	require.Empty(t, result.ReasonCodes, "result = %#v", result)
	document, err := report.Canonical()
	require.NoError(t, err)
	require.Equal(t, SHA256Fingerprint(document), result.ReportDigest, "client-selected digest")
	var decoded SecurityPreflightReport
	require.NoError(t, json.Unmarshal(document, &decoded), "strict report round trip")
	// Client time is provenance; it cannot extend the server's challenge lifetime.
	report.ReportedAt = report.ReportedAt.Add(24 * time.Hour)
	result, err = EvaluateSecurityPreflight(challenge, resolved, input.Build.NativeAgreement, report, challenge.ExpiresAt)
	require.NoError(t, err, "client time extended the challenge")
	require.Equal(t, "blocked", result.Admission, "client time extended the challenge")
	require.Contains(t, result.ReasonCodes, SecurityReasonPreflightExpired, "client time extended the challenge")
	require.True(t, result.ExpiresAt.Equal(challenge.ExpiresAt), "client time extended the challenge")
}
func TestSecurityPreflightBlocksWithoutInventingEvidence(t *testing.T) {
	displayAt := func(r *SecurityPreflightReport) *NativeCapabilityReadiness {
		for i := range r.Capabilities {
			if r.Capabilities[i].Capability == "display" {
				return &r.Capabilities[i]
			}
		}
		t.Fatal("no display in the report")
		return nil
	}
	for name, mutate := range map[string]func(*SecurityPreflightReport){
		"second display": func(r *SecurityPreflightReport) {
			displayAt(r).Summary = json.RawMessage(`{"active_physical":2,"connected_total":2}`)
		},
		"unhealthy source": func(r *SecurityPreflightReport) { displayAt(r).Health = NativeHealthFailed },
		"partial state":    func(r *SecurityPreflightReport) { displayAt(r).Certainty = NativeCertaintyPartial },
		"no permission":    func(r *SecurityPreflightReport) { displayAt(r).Permission = NativePermissionDenied },
	} {
		t.Run(name, func(t *testing.T) {
			input, resolved, challenge, report := preflightFixture(t)
			mutate(&report)
			result, err := EvaluateSecurityPreflight(challenge, resolved, input.Build.NativeAgreement, report, input.ActivationTime)
			require.NoError(t, err, "result = %#v", result)
			require.Equal(t, "blocked", result.Admission, "result = %#v", result)
			require.Equal(t, []SecurityReasonCode{SecurityReasonNativeRuleUnmet}, result.ReasonCodes, "result = %#v", result)
		})
	}
}
func TestSecurityPreflightRejectsForgedReports(t *testing.T) {
	for name, mutate := range map[string]func(*SecurityPreflightReport){
		"challenge": func(r *SecurityPreflightReport) {
			r.Challenge = base64.RawURLEncoding.EncodeToString([]byte(strings.Repeat("x", 32)))
		},
		"policy":             func(r *SecurityPreflightReport) { r.PolicyDigest = SHA256Fingerprint([]byte("other")) },
		"content":            func(r *SecurityPreflightReport) { r.PolicyContentDigest = SHA256Fingerprint([]byte("other")) },
		"missing capability": func(r *SecurityPreflightReport) { r.Capabilities = r.Capabilities[1:] },
		"reordered":          func(r *SecurityPreflightReport) { r.Capabilities[0], r.Capabilities[1] = r.Capabilities[1], r.Capabilities[0] },
	} {
		t.Run(name, func(t *testing.T) {
			input, resolved, challenge, report := preflightFixture(t)
			mutate(&report)
			_, err := EvaluateSecurityPreflight(challenge, resolved, input.Build.NativeAgreement, report, input.ActivationTime)
			require.Error(t, err, "forged report accepted")
		})
	}
}
func TestSecurityPreflightExactCodec(t *testing.T) {
	_, _, _, report := preflightFixture(t)
	raw, err := report.Canonical()
	require.NoError(t, err)
	for name, document := range map[string]string{
		"unknown member":   strings.Replace(string(raw), `"sequence":1`, `"sequence":1,"raw_inventory":[]`, 1),
		"unsafe integer":   strings.Replace(string(raw), `"sequence":1`, `"sequence":9007199254740992`, 1),
		"integer exponent": strings.Replace(string(raw), `"sequence":1`, `"sequence":1e0`, 1),
		"duplicate":        strings.Replace(string(raw), `"sequence":1`, `"sequence":1,"sequence":1`, 1),
		"uppercase":        strings.Replace(string(raw), `"certainty":`, `"CERTAINTY":`, 1),
		"null rules":       strings.Replace(string(raw), `"rules":[]`, `"rules":null`, 1),
		"old member":       strings.Replace(string(raw), `"rules":[]`, `"rules":[],"posture":"compliant"`, 1),
	} {
		t.Run(name, func(t *testing.T) {
			var value SecurityPreflightReport
			require.Error(t, json.Unmarshal([]byte(document), &value), "invalid report accepted")
		})
	}
}
