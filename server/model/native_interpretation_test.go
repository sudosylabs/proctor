// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package model

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func interpretationPolicy(t *testing.T, registry *NativeCapabilityRegistry) EffectiveExamSecurityPolicy {
	t.Helper()
	activations, rules := compileNativePolicy(registry)
	return EffectiveExamSecurityPolicy{Activations: activations, Rules: rules}
}

func observed(t *testing.T, registry *NativeCapabilityRegistry, sequence int64, at time.Time, readiness NativeCapabilityReadiness) NativeObservedFact {
	t.Helper()
	readiness.Sequence = sequence
	fact, ok := registry.ObserveFact(NativeCapabilityFact{Kind: "capability_fact", ObservedAt: at, NativeCapabilityReadiness: readiness})
	require.True(t, ok)
	return fact
}

// roundTrip stores and reloads the state, as each delivery batch does.
func roundTrip(t *testing.T, state *NativeInterpretation) *NativeInterpretation {
	t.Helper()
	raw, err := json.Marshal(state)
	require.NoError(t, err)
	var loaded NativeInterpretation
	require.NoError(t, json.Unmarshal(raw, &loaded))
	return &loaded
}

func TestTheServerTracksParticipationRulesOnTheFactStream(t *testing.T) {
	t.Parallel()

	registry := checkedRegistry(t)
	policy := interpretationPolicy(t, registry)
	start := time.Date(2026, 11, 13, 9, 0, 0, 0, time.UTC)

	state, opening, err := StartNativeInterpretation(registry, policy, compliantStart(), start)
	require.NoError(t, err)
	require.Len(t, opening, 2, "every readiness rule gets its first status at Start")
	for _, transition := range opening {
		require.Nil(t, transition.From)
		require.Equal(t, NativeRuleMet, transition.To.Status, transition.RuleID)
		require.NoError(t, transition.Validate())
	}

	mounted := readinessOf("candidate_window", NativeCertaintyComplete,
		`{"presence":"present","placement":"maximized","content_protection":"verified"}`, `{}`)
	require.Empty(t, state.ApplyFact(policy.Rules, observed(t, registry, 2, start, mounted)),
		"the window rule waits for participation")
	confirmed := state.ConfirmParticipation(policy.Rules, start)
	require.Len(t, confirmed, 1)
	require.Equal(t, "baseline.window", confirmed[0].RuleID)
	require.Equal(t, NativeRuleMet, confirmed[0].To.Status)
	require.Empty(t, state.ConfirmParticipation(policy.Rules, start), "confirming twice changes nothing")

	second := readinessOf("display", NativeCertaintyComplete, `{"targets":[]}`, `{"active_physical":2,"connected_total":2}`)
	state = roundTrip(t, state)
	changes := state.ApplyFact(policy.Rules, observed(t, registry, 2, start.Add(time.Second), second))
	require.Equal(t, []NativeRuleTransition{{
		RuleID: "baseline.single_display", Capability: "display", Mode: NativeRuleEnforce, Reason: changes[0].Reason,
		From:       &NativeRuleReport{RuleID: "baseline.single_display", Status: NativeRuleMet},
		To:         NativeRuleReport{RuleID: "baseline.single_display", Status: NativeRuleNotMet},
		At:         start.Add(time.Second),
		Occurrence: 1, Event: NativeOccurrenceOpened,
	}}, changes, "a second display opens the rule's first occurrence")

	require.Empty(t, state.ApplyFact(policy.Rules, observed(t, registry, 2, start.Add(2*time.Second), compliantStart()[1])),
		"a fact no newer than the latest is ignored")

	state = roundTrip(t, state)
	require.Len(t, state.ApplyGap(policy.Rules, "display", start.Add(3*time.Second)), 1)
	displayRule := state.Standings()[1]
	require.Equal(t, NativeRuleStanding{
		Rule:       NativeRuleReport{RuleID: "baseline.single_display", Status: NativeRuleUnknown, UnknownCause: NativeUnknownGap},
		Capability: "display", Mode: NativeRuleEnforce, Reason: displayRule.Reason,
		OpenOccurrence: 1, Occurrences: 1,
	}, displayRule, "an unknown stretch keeps the occurrence open")
	require.Len(t, state.Standings(), 3)

	definition, _ := registry.Capability("display")
	require.Positive(t, definition.Stability)
	metAgain := start.Add(4 * time.Second)
	require.Empty(t, state.ApplyFact(policy.Rules, observed(t, registry, 3, metAgain, compliantStart()[1])),
		"met again waits for the stability time")
	state = roundTrip(t, state)
	early := NativeRuleStatusReport{Kind: "rule_status", RuleID: "baseline.single_display", Status: NativeRuleMet, Contained: false, ObservedAt: metAgain}
	require.Empty(t, state.ApplyDesktopStatus(policy.Rules, early))
	require.Equal(t, int64(1), state.Standings()[1].Discrepancies, "Desktop said met while the server still waits")

	late := NativeRuleStatusReport{Kind: "rule_status", RuleID: "baseline.single_display", Status: NativeRuleMet, Contained: true, ObservedAt: metAgain.Add(definition.Stability)}
	changes = state.ApplyDesktopStatus(policy.Rules, late)
	require.Len(t, changes, 1)
	require.Equal(t, NativeOccurrenceClosed, changes[0].Event, "the stability time passed, so the rule recovers")
	require.Equal(t, NativeRuleStanding{
		Rule:       NativeRuleReport{RuleID: "baseline.single_display", Status: NativeRuleMet},
		Capability: "display", Mode: NativeRuleEnforce, Reason: displayRule.Reason,
		Occurrences: 1, Contained: true, Discrepancies: 1,
	}, state.Standings()[1])

	changes = state.Interrupt(policy.Rules, start)
	require.Len(t, changes, 3, "lost delivery makes every rule unknown")
	require.Equal(t, metAgain.Add(definition.Stability), changes[0].At, "time never moves backwards")

	_, _, err = StartNativeInterpretation(registry, policy, []NativeCapabilityReadiness{readinessOf("network", NativeCertaintyComplete, `{}`, `{}`)}, start)
	require.Error(t, err, "a capability the policy does not activate is refused")
}

func TestStoredInterpretationIsChecked(t *testing.T) {
	t.Parallel()

	for name, raw := range map[string]string{
		"no rules":         `{"capabilities":{},"phase":"readiness","at":"2026-11-13T09:00:00Z"}`,
		"no phase":         `{"capabilities":{},"rules":{},"at":"2026-11-13T09:00:00Z"}`,
		"open without one": `{"capabilities":{},"rules":{"r1":{"capability":"display","mode":"enforce","reason":"display.r","tracker":{"observed":false},"occurrences":0,"open":true,"contained":false,"discrepancies":0}},"phase":"readiness","at":"2026-11-13T09:00:00Z"}`,
		"bad status":       `{"capabilities":{},"rules":{"r1":{"capability":"display","mode":"enforce","reason":"display.r","tracker":{"status":"fine","observed":true},"occurrences":0,"open":false,"contained":false,"discrepancies":0}},"phase":"readiness","at":"2026-11-13T09:00:00Z"}`,
		"no mode":          `{"capabilities":{},"rules":{"r1":{"capability":"display","reason":"display.r","tracker":{"observed":false},"occurrences":0,"open":false,"contained":false,"discrepancies":0}},"phase":"readiness","at":"2026-11-13T09:00:00Z"}`,
		"long stability":   `{"capabilities":{"display":{"source_instance_id":"a","sequence":1,"stability_ms":60001}},"rules":{},"phase":"participation","at":"2026-11-13T09:00:00Z"}`,
	} {
		var state NativeInterpretation
		require.Error(t, json.Unmarshal([]byte(raw), &state), name)
	}
}

func TestOnlyAnOpenedEnforceOccurrenceRaisesAFlag(t *testing.T) {
	t.Parallel()

	at := time.Date(2026, 11, 13, 9, 0, 0, 0, time.UTC)
	opened := NativeRuleTransition{
		RuleID: "baseline.single_display", Capability: "display", Mode: NativeRuleEnforce, Reason: "display.extra_display",
		To: NativeRuleReport{RuleID: "baseline.single_display", Status: NativeRuleNotMet}, At: at.Add(time.Minute),
		Occurrence: 1, Event: NativeOccurrenceOpened,
	}
	require.True(t, opened.RaisesFlag())
	evidence, err := NewNativeRuleEvidence(NewIntegrityEvidenceID(), NewExamAttemptID(), NewAttemptParticipationID(),
		NewIntegrityFlagID(), 1, opened, at)
	require.NoError(t, err)
	require.Equal(t, IntegrityPolicyKind("native.display"), evidence.Kind)
	require.Equal(t, at, evidence.ObservedAt, "a device clock ahead of the server is not trusted for ordering")
	require.JSONEq(t, `{"rule_id":"baseline.single_display","reason":"display.extra_display","occurrence":1,"opened_at":"2026-11-13T09:01:00.000Z"}`,
		string(evidence.Detail))

	observe := opened
	observe.Mode = NativeRuleObserve
	closed := opened
	closed.Event = NativeOccurrenceClosed
	for name, transition := range map[string]NativeRuleTransition{"observe": observe, "closed": closed} {
		require.False(t, transition.RaisesFlag(), name)
		_, err = NewNativeRuleEvidence(NewIntegrityEvidenceID(), NewExamAttemptID(), NewAttemptParticipationID(),
			NewIntegrityFlagID(), 1, transition, at)
		require.Error(t, err, name)
	}
}
