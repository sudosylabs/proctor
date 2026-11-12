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

	"github.com/stretchr/testify/require"
)

func readinessOf(capability NativeCapabilityID, certainty NativeFactCertainty, state string) NativeCapabilityReadiness {
	return NativeCapabilityReadiness{
		Capability: capability, SourceInstanceID: "instance-" + string(capability), Sequence: 1,
		Permission: NativePermissionGranted, Health: NativeHealthHealthy, Certainty: certainty, State: json.RawMessage(state),
	}
}

func compliantStart() []NativeCapabilityReadiness {
	return []NativeCapabilityReadiness{
		readinessOf("candidate_window", NativeCertaintyComplete, `{"presence":"absent"}`),
		readinessOf("display", NativeCertaintyComplete, `{"summary":{"active_physical":1,"connected_total":1}}`),
		readinessOf("interactive_session", NativeCertaintyComplete,
			`{"console_owner":"candidate","lock":"unlocked","remote_attachment":"absent","other_human_sessions":0}`),
	}
}

func TestTheBaselineCompilesIntoEveryPolicy(t *testing.T) {
	t.Parallel()

	registry, err := ParseNativeCapabilityRegistry(readNativeCapabilityRegistry(t))
	require.NoError(t, err)
	activations, rules := compileNativePolicy(registry)
	require.Equal(t, []NativeActivation{
		{Capability: "candidate_window", Categories: []string{}},
		{Capability: "display", Categories: []string{}},
		{Capability: "interactive_session", Categories: []string{}},
	}, activations, "every capability the baseline needs runs, in a stable order")
	require.Len(t, rules, 3)

	none, noRules := compileNativePolicy(nil)
	require.Empty(t, none)
	require.Empty(t, noRules)
}

func TestTheServerDecidesReadinessFromTheReportedState(t *testing.T) {
	t.Parallel()

	registry, err := ParseNativeCapabilityRegistry(readNativeCapabilityRegistry(t))
	require.NoError(t, err)
	activations, rules := compileNativePolicy(registry)

	statuses, blocked, ok := evaluateNativeReadiness(registry, activations, rules, compliantStart())
	require.True(t, ok)
	require.False(t, blocked)
	require.Equal(t, []NativeRuleReport{
		{RuleID: "baseline.single_display", Status: NativeRuleMet},
		{RuleID: "baseline.session", Status: NativeRuleMet},
	}, statuses, "the window rule applies only during participation")

	partial := compliantStart()
	partial[2].Certainty = NativeCertaintyPartial
	statuses, blocked, ok = evaluateNativeReadiness(registry, activations, rules, partial)
	require.True(t, ok)
	require.True(t, blocked, "an enforce rule that cannot be checked blocks Start")
	require.Contains(t, statuses, NativeRuleReport{RuleID: "baseline.session", Status: NativeRuleUnknown, UnknownCause: NativeUnknownPartial})

	second := compliantStart()
	second[1] = readinessOf("display", NativeCertaintyComplete, `{"summary":{"active_physical":2,"connected_total":2}}`)
	statuses, blocked, ok = evaluateNativeReadiness(registry, activations, rules, second)
	require.True(t, ok)
	require.True(t, blocked)
	require.Contains(t, statuses, NativeRuleReport{RuleID: "baseline.single_display", Status: NativeRuleNotMet})

	uncounted := compliantStart()
	uncounted[2] = readinessOf("interactive_session", NativeCertaintyComplete,
		`{"console_owner":"candidate","lock":"unlocked","remote_attachment":"absent"}`)
	statuses, _, ok = evaluateNativeReadiness(registry, activations, rules, uncounted)
	require.True(t, ok)
	require.Contains(t, statuses, NativeRuleReport{RuleID: "baseline.session", Status: NativeRuleUnknown, UnknownCause: NativeUnknownUnavailable},
		"a count the platform cannot give is left out, never read as zero")

	for name, change := range map[string]func([]NativeCapabilityReadiness) []NativeCapabilityReadiness{
		"missing capability": func(r []NativeCapabilityReadiness) []NativeCapabilityReadiness { return r[1:] },
		"out of order": func(r []NativeCapabilityReadiness) []NativeCapabilityReadiness {
			r[0], r[1] = r[1], r[0]
			return r
		},
		"wrong value type": func(r []NativeCapabilityReadiness) []NativeCapabilityReadiness {
			r[1].State = json.RawMessage(`{"summary":{"active_physical":"one","connected_total":1}}`)
			return r
		},
		"count past its max": func(r []NativeCapabilityReadiness) []NativeCapabilityReadiness {
			r[1].State = json.RawMessage(`{"summary":{"active_physical":33,"connected_total":1}}`)
			return r
		},
		"unregistered symbol": func(r []NativeCapabilityReadiness) []NativeCapabilityReadiness {
			r[2].State = json.RawMessage(`{"console_owner":"someone","lock":"unlocked","remote_attachment":"absent","other_human_sessions":0}`)
			return r
		},
	} {
		_, _, ok = evaluateNativeReadiness(registry, activations, rules, change(compliantStart()))
		require.False(t, ok, name)
	}
}

func TestReadinessReportsAreBounded(t *testing.T) {
	t.Parallel()

	valid := readinessOf("display", NativeCertaintyComplete, `{"summary":{}}`)
	require.True(t, valid.validate())
	for name, change := range map[string]func(*NativeCapabilityReadiness){
		"array state":       func(r *NativeCapabilityReadiness) { r.State = json.RawMessage(`[]`) },
		"oversized state":   func(r *NativeCapabilityReadiness) { r.State = make(json.RawMessage, NativeCapabilityStateMaxBytes+1) },
		"unknown health":    func(r *NativeCapabilityReadiness) { r.Health = "fine" },
		"unknown certainty": func(r *NativeCapabilityReadiness) { r.Certainty = "mostly" },
	} {
		changed := valid
		change(&changed)
		require.False(t, changed.validate(), name)
	}

	require.True(t, NativeRuleReport{RuleID: "baseline.session", Status: NativeRuleUnknown, UnknownCause: NativeUnknownPartial}.validate())
	require.False(t, NativeRuleReport{RuleID: "baseline.session", Status: NativeRuleUnknown}.validate(), "unknown names its cause")
	require.False(t, NativeRuleReport{RuleID: "baseline.session", Status: NativeRuleMet, UnknownCause: NativeUnknownGap}.validate())
}
