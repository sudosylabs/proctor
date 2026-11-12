// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package model

import (
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

type testAppCatalog map[string]bool

func (c testAppCatalog) HasApplication(id string) bool { return c[id] }

var testApps = testAppCatalog{"anydesk": true, "discord": true, "teamviewer": true}

func testNativeRegistry(t *testing.T) *NativeCapabilityRegistry {
	t.Helper()
	registry, err := NewNativeCapabilityRegistry([]NativeCapabilityDefinition{
		{
			ID: "display_topology", Stability: 2 * time.Second, Reasons: []string{"display.not_single"},
			Fields: []NativeFieldDefinition{
				{Name: "display.summary.active_physical", Type: NativeFieldCount},
				{Name: "display.summary.connected_total", Type: NativeFieldCount},
			},
		},
		{
			ID: "camera", Stability: time.Second, Reasons: []string{"camera.not_ready"},
			Fields: []NativeFieldDefinition{
				{Name: "camera.physical_devices", Type: NativeFieldCount},
				{Name: "camera.ready", Type: NativeFieldEnum, Values: []string{"ready", "not_ready", "unknown"}},
				{Name: "camera.virtual_present", Type: NativeFieldBoolean},
			},
		},
		{
			ID: "process_application", Stability: time.Second, Reasons: []string{"process.blocked_app_running"},
			AppConditions: true,
		},
	})
	require.NoError(t, err)
	return registry
}

var testActivated = map[NativeCapabilityID]bool{"display_topology": true, "camera": true, "process_application": true}

func singleDisplayRule() NativeRule {
	return NativeRule{
		RuleID: "baseline.single_display", Capability: "display_topology", Mode: NativeRuleEnforce,
		Phase: NativeRuleBoth, Reason: "display.not_single",
		Requirement: NativeCondition{All: []NativeCondition{
			{Compare: &NativeComparison{Field: "display.summary.connected_total", Op: NativeCompareEq, Value: NativeCount(1)}},
			{Compare: &NativeComparison{Field: "display.summary.active_physical", Op: NativeCompareEq, Value: NativeCount(1)}},
		}},
	}
}

func TestNativeRuleRoundTripsThroughDesktopsGrammar(t *testing.T) {
	t.Parallel()

	document := `{"rule_id":"r1","capability":"process_application","mode":"enforce","phase":"both",` +
		`"reason":"process.blocked_app_running",` +
		`"requirement":{"AppsAbsent":{"catalog_app_ids":["anydesk","discord","teamviewer"]}}}`
	var rule NativeRule
	require.NoError(t, json.Unmarshal([]byte(document), &rule))
	require.Equal(t, []string{"anydesk", "discord", "teamviewer"}, rule.Requirement.AppsAbsent)
	require.NoError(t, ValidateNativeRules([]NativeRule{rule}, testNativeRegistry(t), testActivated, testApps))
	encoded, err := json.Marshal(rule)
	require.NoError(t, err)
	require.JSONEq(t, document, string(encoded))

	nested := `{"rule_id":"r2","capability":"camera","mode":"enforce","phase":"readiness","reason":"camera.not_ready",` +
		`"requirement":{"All":{"conditions":[` +
		`{"Compare":{"field":"camera.physical_devices","op":"ge","value":1}},` +
		`{"Not":{"condition":{"Compare":{"field":"camera.virtual_present","op":"eq","value":true}}}},` +
		`{"Compare":{"field":"camera.ready","op":"in","value":["ready"]}}]}}}`
	require.NoError(t, json.Unmarshal([]byte(nested), &rule))
	require.NoError(t, ValidateNativeRules([]NativeRule{rule}, testNativeRegistry(t), testActivated, testApps))
	encoded, err = json.Marshal(rule)
	require.NoError(t, err)
	require.JSONEq(t, nested, string(encoded))
}

func TestNativeRuleDecodingRefusesWhatTheGrammarDoesNotAllow(t *testing.T) {
	t.Parallel()

	head := `{"rule_id":"r1","capability":"camera","mode":"enforce","phase":"both","reason":"camera.not_ready",`
	for name, document := range map[string]string{
		"unknown member":      head + `"requirement":{"Compare":{"field":"camera.physical_devices","op":"ge","value":1}},"x":1}`,
		"aliased key":         head + `"requirement":{"compare":{"field":"camera.physical_devices","op":"ge","value":1}}}`,
		"two members":         head + `"requirement":{"Not":{"condition":{"AppsOnly":{"catalog_app_ids":["discord"]}}},"AppsOnly":{"catalog_app_ids":["discord"]}}}`,
		"no member":           head + `"requirement":{}}`,
		"fractional count":    head + `"requirement":{"Compare":{"field":"camera.physical_devices","op":"ge","value":1.5}}}`,
		"negative count":      head + `"requirement":{"Compare":{"field":"camera.physical_devices","op":"ge","value":-1}}}`,
		"null value":          head + `"requirement":{"Compare":{"field":"camera.physical_devices","op":"ge","value":null}}}`,
		"object value":        head + `"requirement":{"Compare":{"field":"camera.physical_devices","op":"ge","value":{}}}}`,
		"list for in missing": head + `"requirement":{"Compare":{"field":"camera.ready","op":"in","value":"ready"}}}`,
	} {
		var rule NativeRule
		require.Error(t, json.Unmarshal([]byte(document), &rule), name)
	}
}

func TestNativeRuleValidationHoldsTheRegistryAndBounds(t *testing.T) {
	t.Parallel()

	registry := testNativeRegistry(t)
	valid := singleDisplayRule()
	require.NoError(t, ValidateNativeRules([]NativeRule{valid}, registry, testActivated, testApps))

	compare := func(field string, op NativeCompareOp, value NativeScalar) NativeCondition {
		return NativeCondition{Compare: &NativeComparison{Field: field, Op: op, Value: value}}
	}
	for name, change := range map[string]func(*NativeRule){
		"unregistered field": func(r *NativeRule) {
			r.Requirement = compare("display.summary.secret", NativeCompareEq, NativeCount(1))
		},
		"another's field": func(r *NativeRule) {
			r.Requirement = compare("camera.physical_devices", NativeCompareEq, NativeCount(1))
		},
		"wrong value type": func(r *NativeRule) {
			r.Requirement = compare("display.summary.active_physical", NativeCompareEq, NativeEnum("one"))
		},
		"unregistered enum": func(r *NativeRule) {
			r.Capability, r.Reason = "camera", "camera.not_ready"
			r.Requirement = compare("camera.ready", NativeCompareEq, NativeEnum("mostly"))
		},
		"ordered enum": func(r *NativeRule) {
			r.Capability, r.Reason = "camera", "camera.not_ready"
			r.Requirement = compare("camera.ready", NativeCompareLt, NativeEnum("ready"))
		},
		"ordered boolean": func(r *NativeRule) {
			r.Capability, r.Reason = "camera", "camera.not_ready"
			r.Requirement = compare("camera.virtual_present", NativeCompareGt, NativeBoolean(false))
		},
		"unregistered reason": func(r *NativeRule) { r.Reason = "camera.not_ready" },
		"not activated":       func(r *NativeRule) { r.Capability = "printing" },
		"bad mode":            func(r *NativeRule) { r.Mode = "contain" },
		"bad phase":           func(r *NativeRule) { r.Phase = "always" },
		"apps off process":    func(r *NativeRule) { r.Requirement = NativeCondition{AppsAbsent: []string{"discord"}} },
		"empty all":           func(r *NativeRule) { r.Requirement = NativeCondition{All: []NativeCondition{}} },
		"too deep": func(r *NativeRule) {
			leaf := compare("display.summary.active_physical", NativeCompareEq, NativeCount(1))
			r.Requirement = NativeCondition{Not: &NativeCondition{Not: &NativeCondition{Not: &leaf}}}
		},
		"too many comparisons": func(r *NativeRule) {
			var all []NativeCondition
			for range NativeRuleComparisonLimit + 1 {
				all = append(all, compare("display.summary.active_physical", NativeCompareEq, NativeCount(1)))
			}
			r.Requirement = NativeCondition{All: all}
		},
	} {
		rule := singleDisplayRule()
		change(&rule)
		require.Error(t, ValidateNativeRules([]NativeRule{rule}, registry, testActivated, testApps), name)
	}

	apps := func(ids ...string) NativeRule {
		return NativeRule{RuleID: "r1", Capability: "process_application", Mode: NativeRuleEnforce, Phase: NativeRuleBoth,
			Reason: "process.blocked_app_running", Requirement: NativeCondition{AppsAbsent: ids}}
	}
	require.NoError(t, ValidateNativeRules([]NativeRule{apps("anydesk", "discord")}, registry, testActivated, testApps))
	require.Error(t, ValidateNativeRules([]NativeRule{apps("zoom")}, registry, testActivated, testApps), "not in the catalog")
	require.Error(t, ValidateNativeRules([]NativeRule{apps("discord", "anydesk")}, registry, testActivated, testApps), "unsorted")
	require.Error(t, ValidateNativeRules([]NativeRule{apps("discord", "discord")}, registry, testActivated, testApps), "duplicate")
	twoApps := apps("discord")
	twoApps.Requirement = NativeCondition{Any: []NativeCondition{{AppsAbsent: []string{"discord"}}, {AppsOnly: []string{"anydesk"}}}}
	require.Error(t, ValidateNativeRules([]NativeRule{twoApps}, registry, testActivated, testApps), "one application condition per rule")

	require.Error(t, ValidateNativeRules([]NativeRule{valid, valid}, registry, testActivated, testApps), "duplicate rule ID")
	tooMany := make([]NativeRule, NativeRuleLimit+1)
	for i := range tooMany {
		tooMany[i] = singleDisplayRule()
		tooMany[i].RuleID = fmt.Sprintf("r%d", i)
	}
	require.Error(t, ValidateNativeRules(tooMany, registry, testActivated, testApps), "more than the rule limit")
}

func TestNativeEnvelopeNamesAreNotRuleFields(t *testing.T) {
	t.Parallel()

	for _, name := range []string{"display.certainty", "process.coverage", "camera.health", "camera.permission"} {
		_, err := NewNativeCapabilityRegistry([]NativeCapabilityDefinition{{
			ID: "camera", Reasons: []string{"camera.not_ready"},
			Fields: []NativeFieldDefinition{{Name: name, Type: NativeFieldCount}},
		}})
		require.Error(t, err, "%s is the envelope, not a field a rule compares", name)
	}
}
