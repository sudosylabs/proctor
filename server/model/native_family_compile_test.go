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

// familiesWith is the default selection with some families replaced.
func familiesWith(t *testing.T, documents ...string) []NativeFamilyPolicy {
	t.Helper()
	families := DefaultNativeSecurityPolicy().Families
	for _, document := range documents {
		var family NativeFamilyPolicy
		require.NoError(t, json.Unmarshal([]byte(document), &family))
		for i := range families {
			if families[i].ID() == family.ID() {
				families[i] = family
			}
		}
	}
	return families
}

func compiledRules(t *testing.T, families []NativeFamilyPolicy) (map[string]NativeRule, []NativeActivation) {
	t.Helper()
	activations, rules, err := compileNativePolicy(checkedRegistry(t), families)
	require.NoError(t, err)
	byID := map[string]NativeRule{}
	for _, rule := range rules {
		byID[rule.RuleID] = rule
	}
	return byID, activations
}

func TestEachEnabledFamilyCompilesIntoRegistryRules(t *testing.T) {
	t.Parallel()

	rules, activations := compiledRules(t, familiesWith(t,
		`{"id":"process_application","mode":"enforce","allowed_application_ids":["discord","visual_studio_code"],"outcome":"flag_and_suspend"}`,
		`{"id":"clipboard","mode":"observe","strategy":"disabled","observe_os_clipboard_changes":true,"outcome":"flag"}`,
		`{"id":"printing","mode":"enforce","candidate_print_commands":"deny","monitored_spooler":"not_required","outcome":"flag"}`,
		`{"id":"removable_storage","mode":"enforce","enabled_sub_capabilities":["portable_mtp","removable_block_storage"],"outcome":"flag"}`,
		`{"id":"virtualization","mode":"enforce","rule":"require_certified_physical_environment","outcome":"flag"}`,
		`{"id":"network_configuration","mode":"observe","enabled_sub_capabilities":["system_proxy_state","tunnel_interface_state"],"outcome":"flag"}`,
		`{"id":"camera","mode":"enforce","require_ready":true,"concurrent_use_check":"supported_required","outcome":"flag"}`,
	))

	blocked := rules["process_application.blocked"]
	require.Equal(t, NativeRuleEnforce, blocked.Mode)
	require.Equal(t, "process_application.blocked_application", blocked.Reason)
	require.Contains(t, blocked.Requirement.AppsAbsent, "teamviewer", "never-allowable applications are always blocked")
	require.NotContains(t, blocked.Requirement.AppsAbsent, "discord")
	require.Equal(t, []string{"discord", "visual_studio_code"}, rules["process_application.allowlist"].Requirement.AppsOnly)

	require.Equal(t, NativeRuleObserve, rules["clipboard.changes"].Mode, "an observed family only records")
	require.Equal(t, NativeRuleParticipation, rules["printing.candidate_jobs"].Phase)
	require.Len(t, rules["removable_storage.attached"].Requirement.All, 2)
	require.Equal(t, "virtualization.physical_required", rules["virtualization.physical"].Reason)
	require.Contains(t, rules, "network_configuration.proxy")
	require.Contains(t, rules, "network_configuration.tunnels")
	require.NotContains(t, rules, "network_configuration.vpn", "only selected network checks compile")
	require.Contains(t, rules, "camera.ready")
	require.Contains(t, rules, "camera.exclusive")
	require.NotContains(t, rules, "microphone.ready", "a disabled family compiles nothing")

	for _, activation := range activations {
		if activation.Capability == "network" {
			require.Equal(t, []string{"system_proxy", "tunnel_interfaces"}, activation.Categories)
		}
	}
}

func TestAFamilyTheReleaseCannotCheckRefusesThePolicy(t *testing.T) {
	t.Parallel()

	registry := checkedRegistry(t)
	for name, document := range map[string]string{
		"unregistered application":    `{"id":"process_application","mode":"enforce","allowed_application_ids":["anydesk"],"outcome":"flag"}`,
		"never-allowable allowlisted": `{"id":"process_application","mode":"enforce","allowed_application_ids":["teamviewer"],"outcome":"flag"}`,
	} {
		_, _, err := compileNativePolicy(registry, familiesWith(t, document))
		require.ErrorIs(t, err, ErrNativeCatalogUnavailable, name)
	}
	_, _, err := compileNativePolicy(nil, familiesWith(t,
		`{"id":"virtualization","mode":"observe","rule":"block_recognized_guest","outcome":"flag"}`))
	require.ErrorIs(t, err, ErrNativeCoverageUnsupported, "an enabled family without a registry does nothing, so it is refused")
}

func TestARuleLeadsToItsFamilysOutcome(t *testing.T) {
	t.Parallel()

	policy := EffectiveExamSecurityPolicy{Capabilities: familiesWith(t,
		`{"id":"camera","mode":"enforce","require_ready":true,"concurrent_use_check":"not_required","outcome":"flag_and_suspend"}`,
		`{"id":"printing","mode":"enforce","candidate_print_commands":"deny","monitored_spooler":"not_required","outcome":"flag"}`,
	)}
	require.Equal(t, IntegrityOutcomeFlagAndSuspend, policy.NativeRuleOutcome("camera.ready"))
	require.Equal(t, IntegrityOutcomeFlag, policy.NativeRuleOutcome("printing.candidate_jobs"))
	require.Equal(t, IntegrityOutcomeFlag, policy.NativeRuleOutcome("baseline.single_display"), "baseline rules flag")
}
