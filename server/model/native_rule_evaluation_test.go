// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package model

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func displayFacts(connected, physical int64, certainty NativeFactCertainty) *NativeCapabilityFacts {
	return &NativeCapabilityFacts{
		Permission: NativePermissionGranted,
		Health:     NativeHealthHealthy,
		Certainty:  certainty,
		Fields: map[string]NativeScalar{
			"display.summary.connected_total": NativeCount(connected),
			"display.summary.active_physical": NativeCount(physical),
		},
	}
}

func TestNativeRuleEvaluationIsThreeValued(t *testing.T) {
	t.Parallel()

	rule := singleDisplayRule()
	require.Equal(t, NativeRuleResult{Status: NativeRuleMet}, EvaluateNativeRule(rule, displayFacts(1, 1, NativeCertaintyComplete)))
	require.Equal(t, NativeRuleResult{Status: NativeRuleNotMet}, EvaluateNativeRule(rule, displayFacts(2, 2, NativeCertaintyComplete)))
	require.Equal(t, NativeRuleResult{Status: NativeRuleUnknown, Cause: NativeUnknownPartial},
		EvaluateNativeRule(rule, displayFacts(2, 2, NativeCertaintyPartial)),
		"partial certainty is \"we can't tell\", never \"a second display is present\"")

	oneMissing := displayFacts(1, 1, NativeCertaintyComplete)
	delete(oneMissing.Fields, "display.summary.active_physical")
	require.Equal(t, NativeRuleResult{Status: NativeRuleUnknown, Cause: NativeUnknownUnavailable}, EvaluateNativeRule(rule, oneMissing),
		"all: met and unknown is unknown")
	notMetAndMissing := displayFacts(2, 1, NativeCertaintyComplete)
	delete(notMetAndMissing.Fields, "display.summary.active_physical")
	require.Equal(t, NativeRuleNotMet, EvaluateNativeRule(rule, notMetAndMissing).Status, "all: not met wins over unknown")

	anyRule := rule
	anyRule.Requirement = NativeCondition{Any: rule.Requirement.All}
	require.Equal(t, NativeRuleMet, EvaluateNativeRule(anyRule, oneMissing).Status, "any: met wins over unknown")
	require.Equal(t, NativeUnknownUnavailable, EvaluateNativeRule(anyRule, notMetAndMissing).Cause, "any: not met and unknown is unknown")

	notRule := rule
	notRule.Requirement = NativeCondition{Not: &NativeCondition{All: rule.Requirement.All}}
	require.Equal(t, NativeRuleNotMet, EvaluateNativeRule(notRule, displayFacts(1, 1, NativeCertaintyComplete)).Status)
	require.Equal(t, NativeRuleMet, EvaluateNativeRule(notRule, displayFacts(2, 1, NativeCertaintyComplete)).Status)
	require.Equal(t, NativeUnknownPartial, EvaluateNativeRule(notRule, displayFacts(1, 1, NativeCertaintyPartial)).Cause,
		"not keeps unknown")
}

func TestNativeUnknownCausesFollowDesktopsOrder(t *testing.T) {
	t.Parallel()

	rule := singleDisplayRule()
	facts := func(change func(*NativeCapabilityFacts)) *NativeCapabilityFacts {
		f := displayFacts(1, 1, NativeCertaintyComplete)
		change(f)
		return f
	}
	for name, test := range map[string]struct {
		facts *NativeCapabilityFacts
		cause NativeUnknownCause
	}{
		"no facts": {nil, NativeUnknownUnavailable},
		"in a gap, whatever else holds": {facts(func(f *NativeCapabilityFacts) {
			f.InGap, f.Health = true, NativeHealthFailed
		}), NativeUnknownGap},
		"unavailable before unhealthy": {facts(func(f *NativeCapabilityFacts) {
			f.Fields = map[string]NativeScalar{}
			f.Health = NativeHealthFailed
		}), NativeUnknownUnavailable},
		"unavailable before a gap": {facts(func(f *NativeCapabilityFacts) {
			f.Fields, f.InGap = map[string]NativeScalar{}, true
		}), NativeUnknownUnavailable},
		"unhealthy before permission": {facts(func(f *NativeCapabilityFacts) {
			f.Health, f.Permission = NativeHealthFailed, NativePermissionDenied
		}), NativeUnknownUnhealthy},
		"degraded is running, its certainty decides": {facts(func(f *NativeCapabilityFacts) {
			f.Health, f.Certainty = NativeHealthDegraded, NativeCertaintyPartial
		}), NativeUnknownPartial},
		"permission before partial": {facts(func(f *NativeCapabilityFacts) {
			f.Permission, f.Certainty = NativePermissionNotDetermined, NativeCertaintyPartial
		}), NativeUnknownPermission},
		"partial": {facts(func(f *NativeCapabilityFacts) { f.Certainty = NativeCertaintyUnknown }), NativeUnknownPartial},
	} {
		require.Equal(t, NativeRuleResult{Status: NativeRuleUnknown, Cause: test.cause}, EvaluateNativeRule(rule, test.facts), name)
	}
}

func TestNativeApplicationConditionsReadTheDevicesMatch(t *testing.T) {
	t.Parallel()

	rule := NativeRule{RuleID: "r1", Capability: "process_application", Mode: NativeRuleEnforce, Phase: NativeRuleBoth,
		Reason: "process.blocked_app_running", Requirement: NativeCondition{AppsAbsent: []string{"discord"}}}
	facts := func(certainty NativeFactCertainty, applications NativeApplicationFacts) *NativeCapabilityFacts {
		return &NativeCapabilityFacts{
			Permission: NativePermissionGranted, Health: NativeHealthHealthy, Certainty: certainty, Applications: applications,
		}
	}
	none := map[string]NativeRuleMatch{"r1": {}}

	require.Equal(t, NativeRuleMet, EvaluateNativeRule(rule, facts(NativeCertaintyComplete, NativeApplicationFacts{Matches: none})).Status)
	require.Equal(t, NativeRuleNotMet, EvaluateNativeRule(rule, facts(NativeCertaintyComplete, NativeApplicationFacts{
		Matches: map[string]NativeRuleMatch{"r1": {Matched: true, CatalogAppIDs: []string{"discord"}}},
	})).Status, "matched means the application condition is violated")
	require.Equal(t, NativeUnknownChecking, EvaluateNativeRule(rule, facts(NativeCertaintyComplete, NativeApplicationFacts{
		Checking: 1, Matches: none,
	})).Cause, "an identity being verified makes the rule unknown")
	require.Equal(t, NativeUnknownPartial, EvaluateNativeRule(rule, facts(NativeCertaintyPartial, NativeApplicationFacts{
		Checking: 1, Matches: none,
	})).Cause, "incomplete process coverage reads as partial, before checking")
	require.Equal(t, NativeUnknownUnavailable, EvaluateNativeRule(rule, facts(NativeCertaintyPartial, NativeApplicationFacts{})).Cause,
		"a rule the device reported no match for is unknown, first of all")
}
