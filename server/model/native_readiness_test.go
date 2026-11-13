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

// factsChecked accepts every fact, standing in for the release's compiled
// fact schemas, which internal/factschema tests against Desktop's registry.
type factsChecked struct{ refuse NativeCapabilityID }

func (f factsChecked) ValidateFact(capability NativeCapabilityID, _ string, _ []byte) error {
	if capability == f.refuse {
		return errNativeRegistry
	}
	return nil
}

func readinessOf(capability NativeCapabilityID, certainty NativeFactCertainty, state, summary string) NativeCapabilityReadiness {
	return NativeCapabilityReadiness{
		Capability: capability, SourceInstanceID: "instance-" + string(capability), Sequence: 1,
		Permission: NativePermissionGranted, Health: NativeHealthHealthy, Certainty: certainty,
		State: json.RawMessage(state), Summary: json.RawMessage(summary),
	}
}

func compliantStart() []NativeCapabilityReadiness {
	return []NativeCapabilityReadiness{
		readinessOf("candidate_window", NativeCertaintyComplete, `{"presence":"absent"}`, `{}`),
		readinessOf("display", NativeCertaintyComplete, `{"targets":[]}`, `{"active_physical":1,"connected_total":1}`),
		readinessOf("interactive_session", NativeCertaintyComplete,
			`{"console_owner":"candidate","lock":"unlocked","remote_attachment":"absent","other_human_sessions":0}`, `{}`),
	}
}

func checkedRegistry(t *testing.T) *NativeCapabilityRegistry {
	t.Helper()
	registry, err := ParseNativeCapabilityRegistry(readNativeCapabilityRegistry(t))
	require.NoError(t, err)
	checked, err := registry.WithFactValidator(factsChecked{})
	require.NoError(t, err)
	return checked
}

func TestTheBaselineCompilesIntoEveryPolicy(t *testing.T) {
	t.Parallel()

	registry, err := ParseNativeCapabilityRegistry(readNativeCapabilityRegistry(t))
	require.NoError(t, err)
	activations, rules, err := compileNativePolicy(registry, DefaultNativeSecurityPolicy().Families, NativeScreenRecordingPolicy{})
	require.NoError(t, err)
	require.Equal(t, []NativeActivation{
		{Capability: "candidate_window", Categories: []string{}},
		{Capability: "display", Categories: []string{}},
		{Capability: "interactive_session", Categories: []string{}},
	}, activations, "every capability the baseline needs runs, in a stable order")
	require.Len(t, rules, 3)

	none, noRules, err := compileNativePolicy(nil, DefaultNativeSecurityPolicy().Families, NativeScreenRecordingPolicy{})
	require.NoError(t, err, "with every family off, no registry is needed")
	require.Empty(t, none)
	require.Empty(t, noRules)
}

func TestTheServerDecidesReadinessFromTheReportedState(t *testing.T) {
	t.Parallel()

	registry := checkedRegistry(t)
	activations, rules, err := compileNativePolicy(registry, DefaultNativeSecurityPolicy().Families, NativeScreenRecordingPolicy{})
	require.NoError(t, err)

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
	second[1] = readinessOf("display", NativeCertaintyComplete, `{"targets":[]}`, `{"active_physical":2,"connected_total":2}`)
	statuses, blocked, ok = evaluateNativeReadiness(registry, activations, rules, second)
	require.True(t, ok)
	require.True(t, blocked)
	require.Contains(t, statuses, NativeRuleReport{RuleID: "baseline.single_display", Status: NativeRuleNotMet})

	uncounted := compliantStart()
	uncounted[2] = readinessOf("interactive_session", NativeCertaintyComplete,
		`{"console_owner":"candidate","lock":"unlocked","remote_attachment":"absent"}`, `{}`)
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
			r[1].Summary = json.RawMessage(`{"active_physical":"one","connected_total":1}`)
			return r
		},
		"count past its max": func(r []NativeCapabilityReadiness) []NativeCapabilityReadiness {
			r[1].Summary = json.RawMessage(`{"active_physical":33,"connected_total":1}`)
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

	countsInState := compliantStart()
	countsInState[1] = readinessOf("display", NativeCertaintyComplete,
		`{"targets":[],"summary":{"active_physical":1,"connected_total":1}}`, `{}`)
	statuses, _, ok = evaluateNativeReadiness(registry, activations, rules, countsInState)
	require.True(t, ok)
	require.Contains(t, statuses, NativeRuleReport{RuleID: "baseline.single_display", Status: NativeRuleUnknown, UnknownCause: NativeUnknownUnavailable},
		"summary fields are read from the fact's summary, never from the state")

	refusing, err := registry.WithFactValidator(factsChecked{refuse: "display"})
	require.NoError(t, err)
	_, _, ok = evaluateNativeReadiness(refusing, activations, rules, compliantStart())
	require.False(t, ok, "a fact that misses its published schema refuses the report")
	unchecked, err := ParseNativeCapabilityRegistry(readNativeCapabilityRegistry(t))
	require.NoError(t, err)
	_, _, ok = evaluateNativeReadiness(unchecked, activations, rules, compliantStart())
	require.False(t, ok, "a registry with no schema validator accepts no fact")
}

func TestReadinessReportsAreBounded(t *testing.T) {
	t.Parallel()

	valid := readinessOf("display", NativeCertaintyComplete, `{"targets":[]}`, `{}`)
	require.True(t, valid.validate())
	for name, change := range map[string]func(*NativeCapabilityReadiness){
		"array state":       func(r *NativeCapabilityReadiness) { r.State = json.RawMessage(`[]`) },
		"no summary":        func(r *NativeCapabilityReadiness) { r.Summary = nil },
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

func TestARecordingExamStartsOnlyWhenTheRecorderIsPermittedAndHasRoom(t *testing.T) {
	t.Parallel()

	registry := checkedRegistry(t)
	activations, rules, err := compileNativePolicy(registry, DefaultNativeSecurityPolicy().Families, NativeScreenRecordingPolicy{Enabled: true})
	require.NoError(t, err)
	require.Contains(t, activations, NativeActivation{Capability: "screen_recording", Categories: []string{}})
	require.Contains(t, rules, NativeRule{
		RuleID: "screen_recording.free_space", Capability: "screen_recording", Mode: NativeRuleEnforce,
		Phase: NativeRuleReadiness, Reason: "screen_recording.low_disk_space",
		Requirement: NativeCondition{Compare: &NativeComparison{
			Field: "screen_recording.summary.free_space_mib", Op: NativeCompareGe, Value: NativeCount(3072),
		}},
	})

	start := func(change func(*NativeCapabilityReadiness)) []NativeCapabilityReadiness {
		recorder := readinessOf("screen_recording", NativeCertaintyComplete, `{"free_space_mib":8192}`, `{"free_space_mib":8192}`)
		if change != nil {
			change(&recorder)
		}
		facts := compliantStart()
		return append(facts[:2], append([]NativeCapabilityReadiness{facts[2]}, recorder)...)
	}
	recorder := func(statuses []NativeRuleReport) NativeRuleReport {
		for _, status := range statuses {
			if status.RuleID == "screen_recording.free_space" {
				return status
			}
		}
		require.FailNow(t, "the recorder rule was not evaluated")
		return NativeRuleReport{}
	}

	statuses, blocked, ok := evaluateNativeReadiness(registry, activations, rules, start(nil))
	require.True(t, ok)
	require.False(t, blocked, "a permitted recorder with room starts")
	require.Equal(t, NativeRuleMet, recorder(statuses).Status)

	for name, test := range map[string]struct {
		change func(*NativeCapabilityReadiness)
		want   NativeRuleReport
	}{
		"low disk": {func(r *NativeCapabilityReadiness) {
			r.State, r.Summary = json.RawMessage(`{"free_space_mib":3071}`), json.RawMessage(`{"free_space_mib":3071}`)
		}, NativeRuleReport{RuleID: "screen_recording.free_space", Status: NativeRuleNotMet}},
		"permission denied": {func(r *NativeCapabilityReadiness) { r.Permission = NativePermissionDenied },
			NativeRuleReport{RuleID: "screen_recording.free_space", Status: NativeRuleUnknown, UnknownCause: NativeUnknownPermission}},
		"free space unreadable": {func(r *NativeCapabilityReadiness) {
			r.Certainty = NativeCertaintyPartial
			r.State, r.Summary = json.RawMessage(`{}`), json.RawMessage(`{}`)
		}, NativeRuleReport{RuleID: "screen_recording.free_space", Status: NativeRuleUnknown, UnknownCause: NativeUnknownUnavailable}},
	} {
		statuses, blocked, ok := evaluateNativeReadiness(registry, activations, rules, start(test.change))
		require.True(t, ok, name)
		require.True(t, blocked, "%s blocks Start", name)
		require.Equal(t, test.want, recorder(statuses), name)
	}

	_, noRecorder, err := compileNativePolicy(registry, DefaultNativeSecurityPolicy().Families, NativeScreenRecordingPolicy{})
	require.NoError(t, err)
	require.NotContains(t, noRecorder, NativeRule{RuleID: "screen_recording.free_space"})
	for _, rule := range noRecorder {
		require.NotEqual(t, NativeCapabilityID("screen_recording"), rule.Capability, "an Exam that does not record checks no recorder")
	}

	_, _, err = compileNativePolicy(nil, DefaultNativeSecurityPolicy().Families, NativeScreenRecordingPolicy{Enabled: true})
	require.ErrorIs(t, err, ErrNativeCoverageUnsupported, "recording without a registry cannot check the recorder")
}
