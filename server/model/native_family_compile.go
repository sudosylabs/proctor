// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package model

import (
	"slices"
	"strings"
)

// nativeFamilyCapability is the registry capability each authored family
// runs on. interactive_session is fully covered by the baseline rule and
// external_capture has no capability yet, so neither compiles a rule.
var nativeFamilyCapability = map[string]NativeCapabilityID{
	"process_application":   "process_application",
	"clipboard":             "clipboard",
	"printing":              "printing",
	"removable_storage":     "removable_storage",
	"virtualization":        "virtualization",
	"network_configuration": "network",
	"camera":                "camera",
	"microphone":            "microphone",
}

// nativeNetworkCategories are the network sub-categories each authored
// network option activates.
var nativeNetworkCategories = map[string]string{
	"system_proxy_state":     "system_proxy",
	"os_managed_vpn_state":   "managed_vpn",
	"tunnel_interface_state": "tunnel_interfaces",
}

// screenRecordingFreeSpaceMiB is the free disk the recorder needs before
// Start: one 3-hour session recording is about 1.4 GB, plus room to cut
// clips from it.
const screenRecordingFreeSpaceMiB = 3072

// compileNativePolicy turns the release registry and an Exam's family
// selections into what the policy sends at activation: the baseline rules,
// unchanged, then one or more rules for each family that is not disabled,
// the recorder's readiness rule when the Exam records the screen, and the
// capabilities they need. Every rule is checked against the
// registry and its application catalog, so a selection the release cannot
// evaluate refuses the policy.
func compileNativePolicy(
	registry *NativeCapabilityRegistry,
	families []NativeFamilyPolicy,
	screenRecording NativeScreenRecordingPolicy,
) ([]NativeActivation, []NativeRule, error) {
	activations, rules := []NativeActivation{}, []NativeRule{}
	if registry == nil {
		// Without a registry nothing can be evaluated, so a family that would
		// compile a rule refuses the policy instead of silently doing nothing.
		if screenRecording.Enabled {
			return nil, nil, ErrNativeCoverageUnsupported
		}
		for _, family := range families {
			if _, compiles := nativeFamilyCapability[family.ID()]; compiles && family.Mode() != NativeModeDisabled {
				return nil, nil, ErrNativeCoverageUnsupported
			}
		}
		return activations, rules, nil
	}
	activate := func(capability NativeCapabilityID, categories ...string) {
		i := slices.IndexFunc(activations, func(a NativeActivation) bool { return a.Capability == capability })
		if i < 0 {
			activations = append(activations, NativeActivation{Capability: capability, Categories: []string{}})
			i = len(activations) - 1
		}
		for _, category := range categories {
			if !slices.Contains(activations[i].Categories, category) {
				activations[i].Categories = append(activations[i].Categories, category)
			}
		}
	}
	for _, rule := range registry.baseline {
		rules = append(rules, rule)
		activate(rule.Capability)
	}

	for _, family := range families {
		if family.Mode() == NativeModeDisabled {
			continue
		}
		capability, compiles := nativeFamilyCapability[family.ID()]
		if !compiles {
			continue
		}
		if _, registered := registry.Capability(capability); !registered {
			return nil, nil, ErrNativeCoverageUnsupported
		}
		compiled := family.rules(registry, capability)
		if len(compiled) == 0 {
			continue
		}
		var categories []string
		for _, sub := range family.fields.EnabledSubCapabilities {
			if category, ok := nativeNetworkCategories[sub]; ok && capability == "network" {
				categories = append(categories, category)
			}
		}
		activate(capability, categories...)
		rules = append(rules, compiled...)
	}

	if screenRecording.Enabled {
		// The recorder must be permitted and have room before Start: a denied
		// permission or unreadable free space is unknown, which blocks too.
		// It is checked only then; a recording that fails later is a clip
		// gap, never a Flag.
		if _, registered := registry.Capability("screen_recording"); !registered {
			return nil, nil, ErrNativeCoverageUnsupported
		}
		activate("screen_recording")
		rules = append(rules, NativeRule{
			RuleID: "screen_recording.free_space", Capability: "screen_recording",
			Mode: NativeRuleEnforce, Phase: NativeRuleReadiness, Reason: "screen_recording.low_disk_space",
			Requirement: NativeCondition{Compare: &NativeComparison{
				Field: "screen_recording.summary.free_space_mib", Op: NativeCompareGe,
				Value: NativeCount(screenRecordingFreeSpaceMiB),
			}},
		})
	}

	slices.SortFunc(activations, func(a, b NativeActivation) int { return strings.Compare(string(a.Capability), string(b.Capability)) })
	for i := range activations {
		slices.Sort(activations[i].Categories)
	}
	activated := make(map[NativeCapabilityID]bool, len(activations))
	for _, activation := range activations {
		activated[activation.Capability] = true
	}
	if ValidateNativeRules(rules, registry, activated, registry) != nil {
		return nil, nil, ErrNativeCatalogUnavailable
	}
	return activations, rules, nil
}

// rules compiles one enabled family. Each rule's ID is "<family>.<check>",
// so the server can find the family, and with it the outcome, a rule's
// occurrence leads to.
func (p NativeFamilyPolicy) rules(registry *NativeCapabilityRegistry, capability NativeCapabilityID) []NativeRule {
	f := p.fields
	mode := NativeRuleObserve
	if f.Mode == NativeModeEnforce {
		mode = NativeRuleEnforce
	}
	rule := func(check string, phase NativeRulePhase, reason string, requirement NativeCondition) NativeRule {
		return NativeRule{
			RuleID: f.ID + "." + check, Capability: capability, Mode: mode, Phase: phase,
			Reason: string(capability) + "." + reason, Requirement: requirement,
		}
	}
	equals := func(field string, value NativeScalar) NativeCondition {
		return NativeCondition{Compare: &NativeComparison{Field: string(capability) + "." + field, Op: NativeCompareEq, Value: value}}
	}
	none := func(field string) NativeCondition { return equals(field, NativeCount(0)) }

	switch f.ID {
	case "process_application":
		// The release's never-allowable applications are always blocked; the
		// manager's list, when it names any, is the only applications allowed.
		rules := []NativeRule{}
		if blocked := registry.neverAllowable(); len(blocked) > 0 {
			rules = append(rules, rule("blocked", NativeRuleBoth, "blocked_application", NativeCondition{AppsAbsent: blocked}))
		}
		if len(f.AllowedApplicationIDs) > 0 {
			rules = append(rules, rule("allowlist", NativeRuleBoth, "unlisted_application",
				NativeCondition{AppsOnly: slices.Clone(f.AllowedApplicationIDs)}))
		}
		return rules
	case "clipboard":
		return []NativeRule{rule("changes", NativeRuleParticipation, "changed", none("changes"))}
	case "printing":
		return []NativeRule{rule("candidate_jobs", NativeRuleParticipation, "candidate_job", none("candidate_jobs_since_start"))}
	case "removable_storage":
		var attached []NativeCondition
		if slices.Contains(f.EnabledSubCapabilities, "removable_block_storage") {
			attached = append(attached, none("summary.block_storage"))
		}
		if slices.Contains(f.EnabledSubCapabilities, "portable_mtp") {
			attached = append(attached, none("summary.portable_devices"))
		}
		return []NativeRule{rule("attached", NativeRuleBoth, "attached", NativeCondition{All: attached})}
	case "virtualization":
		if f.Rule == "require_certified_physical_environment" {
			return []NativeRule{rule("physical", NativeRuleBoth, "physical_required",
				equals("classification", NativeEnum("recognized_physical_or_root")))}
		}
		return []NativeRule{rule("guest", NativeRuleBoth, "guest_detected", NativeCondition{
			Compare: &NativeComparison{Field: "virtualization.classification", Op: NativeCompareNe, Value: NativeEnum("recognized_guest")},
		})}
	case "network_configuration":
		rules := []NativeRule{}
		for _, sub := range f.EnabledSubCapabilities {
			switch sub {
			case "system_proxy_state":
				rules = append(rules, rule("proxy", NativeRuleBoth, "proxy_detected", equals("system_proxy", NativeEnum("none"))))
			case "os_managed_vpn_state":
				rules = append(rules, rule("vpn", NativeRuleBoth, "vpn_detected", equals("managed_vpn", NativeEnum("inactive"))))
			case "tunnel_interface_state":
				rules = append(rules, rule("tunnels", NativeRuleBoth, "tunnel_detected", none("summary.tunnels_total")))
			}
		}
		return rules
	default: // camera and microphone
		rules := []NativeRule{}
		if f.RequireReady {
			rules = append(rules, rule("ready", NativeRuleBoth, "not_ready", equals("ready", NativeEnum("ready"))))
		}
		if f.ConcurrentUseCheck == "supported_required" {
			rules = append(rules, rule("exclusive", NativeRuleBoth, "in_use_by_other_app", equals("in_use_by_other_app", NativeEnum("no"))))
		}
		return rules
	}
}

// neverAllowable lists the catalog applications no policy may allow, sorted.
func (r *NativeCapabilityRegistry) neverAllowable() []string {
	blocked := []string{}
	for id, allowable := range r.applications {
		if !allowable {
			blocked = append(blocked, id)
		}
	}
	slices.Sort(blocked)
	return blocked
}

// NativeRuleOutcome is what an enforce rule's occurrence leads to: the
// outcome of the family that compiled it, or a Flag for a baseline rule.
func (p EffectiveExamSecurityPolicy) NativeRuleOutcome(ruleID string) IntegrityOutcome {
	family, _, compiled := strings.Cut(ruleID, ".")
	if !compiled {
		return IntegrityOutcomeFlag
	}
	for _, selected := range p.Capabilities {
		if selected.ID() == family && selected.Mode() == NativeModeEnforce {
			return selected.Outcome()
		}
	}
	return IntegrityOutcomeFlag
}
