// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package model

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestIntegrityPolicyKindAcceptsCoreKindsAndCapabilityKinds(t *testing.T) {
	t.Parallel()

	for _, kind := range []IntegrityPolicyKind{
		IntegrityPolicyConnectionLoss,
		IntegrityPolicyFocusLoss,
		IntegrityPolicyBrowserNavigation,
	} {
		require.True(t, kind.IsValid(), kind)
		require.True(t, kind.IsCore(), kind)
		require.False(t, kind.IsCapability(), kind)
	}

	for _, kind := range []IntegrityPolicyKind{
		"display.second_screen",
		"proctor.display-topology.monitor_added",
		IntegrityPolicyKind(strings.Repeat("a", 64) + "." + strings.Repeat("b", 32)),
	} {
		require.True(t, kind.IsValid(), kind)
		require.True(t, kind.IsCapability(), kind)
		require.False(t, kind.IsCore(), kind)
	}
	require.Len(t, strings.Repeat("a", 64)+"."+strings.Repeat("b", 32), IntegrityPolicyKindMaxLength)

	for _, kind := range []IntegrityPolicyKind{
		"",
		"unknown_core_kind",
		"Display.second_screen",
		"display.Second",
		"display.second-screen",
		"display.",
		".second",
		"1display.second",
		"display.2nd",
		IntegrityPolicyKind(strings.Repeat("a", 65) + ".b"),
		IntegrityPolicyKind("a." + strings.Repeat("b", 33)),
	} {
		require.False(t, kind.IsValid(), kind)
	}
}

func TestCoreIntegrityPoliciesDeclareTheirFailModeAndSuspensionReason(t *testing.T) {
	t.Parallel()

	connection, ok := coreIntegrityPolicies[IntegrityPolicyConnectionLoss]
	require.True(t, ok)
	require.Equal(t, IntegrityFailContain, connection.FailMode)
	require.Equal(t, AttemptSuspensionCandidateReasonSecureContinuityLost, connection.SuspensionReason)

	focus, ok := coreIntegrityPolicies[IntegrityPolicyFocusLoss]
	require.True(t, ok)
	require.Equal(t, IntegrityFailRecordGap, focus.FailMode)
	require.Equal(t, AttemptSuspensionCandidateReasonFocusLossPolicy, focus.SuspensionReason)

	browser, ok := coreIntegrityPolicies[IntegrityPolicyBrowserNavigation]
	require.True(t, ok)
	require.Empty(t, browser.SuspensionReason, "browser navigation flags but never suspends")

	_, ok = coreIntegrityPolicies["display.second_screen"]
	require.False(t, ok)

	require.True(t, AttemptSuspensionCandidateReasonSecureContinuityLost.IsPolicy())
	require.True(t, AttemptSuspensionCandidateReasonFocusLossPolicy.IsPolicy())
	require.False(t, AttemptSuspensionCandidateReasonManagerSuspended.IsPolicy())
	require.False(t, AttemptSuspensionCandidateReason("").IsPolicy())
}

func TestIntegrityOutcomeIsAClosedList(t *testing.T) {
	t.Parallel()

	for _, outcome := range []IntegrityOutcome{IntegrityOutcomeFlag, IntegrityOutcomeFlagAndWarn, IntegrityOutcomeFlagAndSuspend} {
		require.True(t, outcome.IsValid(), outcome)
	}
	for _, outcome := range []IntegrityOutcome{"", "record", "end_attempt", "FLAG"} {
		require.False(t, outcome.IsValid(), outcome)
	}
}

func TestCapabilityIntegrityEvidenceCarriesOnlyItsBoundedDetail(t *testing.T) {
	t.Parallel()

	at := time.Date(2026, 11, 12, 9, 0, 0, 0, time.UTC)
	valid := func() *IntegrityEvidence {
		return &IntegrityEvidence{
			ID:              NewIntegrityEvidenceID(),
			AttemptID:       NewExamAttemptID(),
			ParticipationID: NewAttemptParticipationID(),
			FlagID:          NewIntegrityFlagID(),
			Generation:      1,
			Kind:            "display.second_screen",
			Detail:          json.RawMessage(`{"displays":2}`),
			ObservedAt:      at,
			RecordedAt:      at,
		}
	}
	require.NoError(t, valid().Validate())

	for name, change := range map[string]func(*IntegrityEvidence){
		"no detail":         func(e *IntegrityEvidence) { e.Detail = nil },
		"array detail":      func(e *IntegrityEvidence) { e.Detail = json.RawMessage(`[1]`) },
		"broken detail":     func(e *IntegrityEvidence) { e.Detail = json.RawMessage(`{"displays":`) },
		"oversized detail":  func(e *IntegrityEvidence) { e.Detail = oversizedDetail() },
		"no flag":           func(e *IntegrityEvidence) { e.FlagID = "" },
		"focus loss fields": func(e *IntegrityEvidence) { e.Sequence = 1 },
		"browser fields":    func(e *IntegrityEvidence) { e.Browser = &BrowserIntegrityEvidence{} },
	} {
		evidence := valid()
		change(evidence)
		require.Error(t, evidence.Validate(), name)
	}

	connection, err := NewConnectionLossEvidence(NewIntegrityEvidenceID(), NewExamAttemptID(), NewAttemptParticipationID(),
		NewIntegrityFlagID(), 1, at, at)
	require.NoError(t, err)
	connection.Detail = json.RawMessage(`{}`)
	require.Error(t, connection.Validate(), "core evidence carries no capability detail")
}

func oversizedDetail() json.RawMessage {
	return json.RawMessage(`{"padding":"` + strings.Repeat("x", IntegrityEvidenceDetailMaxBytes) + `"}`)
}

func TestIntegrityPolicySuspensionTakesThePolicysReason(t *testing.T) {
	t.Parallel()

	at := time.Date(2026, 11, 12, 9, 0, 0, 0, time.UTC)
	open := func(kind IntegrityPolicyKind) (*AttemptSuspension, error) {
		return NewIntegrityPolicySuspension(NewAttemptSuspensionID(), NewExamAttemptID(), NewAttemptParticipationID(),
			NewIntegrityFlagID(), 1, kind, at)
	}

	connection, err := open(IntegrityPolicyConnectionLoss)
	require.NoError(t, err)
	require.Equal(t, AttemptSuspensionSourcePolicy, connection.Source)
	require.Equal(t, AttemptSuspensionCandidateReasonSecureContinuityLost, connection.CandidateReason)

	focus, err := open(IntegrityPolicyFocusLoss)
	require.NoError(t, err)
	require.Equal(t, AttemptSuspensionCandidateReasonFocusLossPolicy, focus.CandidateReason)

	_, err = open(IntegrityPolicyBrowserNavigation)
	require.Error(t, err, "browser navigation never suspends")
	_, err = open("display.second_screen")
	require.Error(t, err, "a capability's reasons are not registered yet")
}

func TestNativeCapabilityPoliciesContainAndSuspendWithTheNativeReason(t *testing.T) {
	t.Parallel()

	policy, ok := integrityPolicy("native.display_topology")
	require.True(t, ok)
	require.Equal(t, IntegrityFailContain, policy.FailMode)
	require.Equal(t, AttemptSuspensionCandidateReasonNativePolicy, policy.SuspensionReason)
	require.True(t, IntegrityPolicyKind("native.display_topology").IsValid())
	require.False(t, IntegrityPolicyKind("native.display_topology").IsCore(), "native kinds are capability kinds")

	for _, kind := range []IntegrityPolicyKind{"native.", "native.display.topology", "native.Display", "display.second_screen"} {
		_, ok = integrityPolicy(kind)
		require.False(t, ok, kind)
	}

	suspension, err := NewIntegrityPolicySuspension(NewAttemptSuspensionID(), NewExamAttemptID(), NewAttemptParticipationID(),
		NewIntegrityFlagID(), 1, "native.process_application", time.Date(2026, 11, 12, 9, 0, 0, 0, time.UTC))
	require.NoError(t, err)
	require.Equal(t, AttemptSuspensionCandidateReasonNativePolicy, suspension.CandidateReason)
	require.True(t, AttemptSuspensionCandidateReasonNativePolicy.IsPolicy(), "a native suspension can be re-allowed")
	require.True(t, AttemptSuspensionCandidateReasonNativePolicy.IsValid())
}
