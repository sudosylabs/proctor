// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package model

import "slices"

// NativeFamilyID names an authored native family: a group of options an Exam
// author turns on together, compiled into rules on one capability.
type NativeFamilyID string

const (
	NativeFamilyProcessApplication   NativeFamilyID = "process_application"
	NativeFamilyInteractiveSession   NativeFamilyID = "interactive_session"
	NativeFamilyClipboard            NativeFamilyID = "clipboard"
	NativeFamilyPrinting             NativeFamilyID = "printing"
	NativeFamilyRemovableStorage     NativeFamilyID = "removable_storage"
	NativeFamilyVirtualization       NativeFamilyID = "virtualization"
	NativeFamilyNetworkConfiguration NativeFamilyID = "network_configuration"
	NativeFamilyCamera               NativeFamilyID = "camera"
	NativeFamilyMicrophone           NativeFamilyID = "microphone"
	NativeFamilyExternalCapture      NativeFamilyID = "external_capture"
)

// NativeClipboardStrategy is what the clipboard family does with the
// candidate's clipboard.
type NativeClipboardStrategy string

const (
	NativeClipboardDisabled       NativeClipboardStrategy = "disabled"
	NativeClipboardPrivateAttempt NativeClipboardStrategy = "private_attempt"
)

// NativePrintCommands is whether the candidate's print commands are refused.
type NativePrintCommands string

const (
	NativePrintCommandsNotApplied NativePrintCommands = "not_applied"
	NativePrintCommandsDeny       NativePrintCommands = "deny"
)

// NativeSpoolerRequirement is whether printing needs the monitored spooler.
type NativeSpoolerRequirement string

const (
	NativeSpoolerRequired    NativeSpoolerRequirement = "required"
	NativeSpoolerNotRequired NativeSpoolerRequirement = "not_required"
)

// NativeVirtualizationRule is what the virtualization family requires of the
// candidate's machine.
type NativeVirtualizationRule string

const (
	NativeVirtualizationNotApplied      NativeVirtualizationRule = "not_applied"
	NativeVirtualizationBlockGuest      NativeVirtualizationRule = "block_recognized_guest"
	NativeVirtualizationRequirePhysical NativeVirtualizationRule = "require_certified_physical_environment"
)

// NativeConcurrentUseCheck is whether a camera or microphone must not be in
// use by another application.
type NativeConcurrentUseCheck string

const (
	NativeConcurrentUseNotRequired       NativeConcurrentUseCheck = "not_required"
	NativeConcurrentUseSupportedRequired NativeConcurrentUseCheck = "supported_required"
)

// NativeSubCapability is one part of a family an author turns on on its own.
type NativeSubCapability string

const (
	NativeSubRemovableBlockStorage NativeSubCapability = "removable_block_storage"
	NativeSubPortableMTP           NativeSubCapability = "portable_mtp"
	NativeSubSystemProxyState      NativeSubCapability = "system_proxy_state"
	NativeSubManagedVPNState       NativeSubCapability = "os_managed_vpn_state"
	NativeSubTunnelInterfaceState  NativeSubCapability = "tunnel_interface_state"
)

// nativeFamilyDefinition is everything one family is: the capability it
// runs on (none when it compiles no rule of its own), its default options,
// how its options are checked and encoded, and the rules it compiles.
type nativeFamilyDefinition struct {
	id         NativeFamilyID
	capability NativeCapabilityID
	defaults   func(*nativeFamilyFields)
	validate   func(f nativeFamilyFields, disabled bool) bool
	members    func(f nativeFamilyFields, into map[string]any)
	compile    func(b nativeRuleBuilder, registry *NativeCapabilityRegistry) []NativeRule
}

// nativeFamilyDefinitions lists the families in the order every policy
// carries them. interactive_session is fully covered by the baseline rule
// and external_capture has no capability yet, so neither compiles a rule.
var nativeFamilyDefinitions = []nativeFamilyDefinition{
	{
		id: NativeFamilyProcessApplication, capability: "process_application",
		defaults: func(f *nativeFamilyFields) { f.AllowedApplicationIDs = []string{} },
		validate: func(f nativeFamilyFields, disabled bool) bool {
			return validateNativePolicyIDs(f.AllowedApplicationIDs) == nil && !(disabled && len(f.AllowedApplicationIDs) != 0)
		},
		members: func(f nativeFamilyFields, into map[string]any) {
			into["allowed_application_ids"] = f.AllowedApplicationIDs
		},
		compile: compileNativeProcessFamily,
	},
	{
		id:       NativeFamilyInteractiveSession,
		defaults: func(*nativeFamilyFields) {},
		validate: func(nativeFamilyFields, bool) bool { return true },
		members:  func(nativeFamilyFields, map[string]any) {},
	},
	{
		id: NativeFamilyClipboard, capability: "clipboard",
		defaults: func(f *nativeFamilyFields) { f.Strategy = NativeClipboardDisabled },
		validate: func(f nativeFamilyFields, _ bool) bool {
			switch f.Mode {
			case NativeModeDisabled:
				return f.Strategy == NativeClipboardDisabled && !f.ObserveOSClipboardChanges
			case NativeModeObserve:
				return f.Strategy == NativeClipboardDisabled && f.ObserveOSClipboardChanges
			default:
				return f.Strategy == NativeClipboardPrivateAttempt
			}
		},
		members: func(f nativeFamilyFields, into map[string]any) {
			into["strategy"] = f.Strategy
			into["observe_os_clipboard_changes"] = f.ObserveOSClipboardChanges
		},
		compile: func(b nativeRuleBuilder, _ *NativeCapabilityRegistry) []NativeRule {
			return []NativeRule{b.rule("changes", NativeRuleParticipation, "changed", b.none("changes"))}
		},
	},
	{
		id: NativeFamilyPrinting, capability: "printing",
		defaults: func(f *nativeFamilyFields) {
			f.CandidatePrintCommands, f.MonitoredSpooler = NativePrintCommandsNotApplied, NativeSpoolerNotRequired
		},
		validate: func(f nativeFamilyFields, _ bool) bool {
			if f.MonitoredSpooler != NativeSpoolerRequired && f.MonitoredSpooler != NativeSpoolerNotRequired {
				return false
			}
			switch f.Mode {
			case NativeModeDisabled:
				return f.CandidatePrintCommands == NativePrintCommandsNotApplied && f.MonitoredSpooler == NativeSpoolerNotRequired
			case NativeModeObserve:
				return f.CandidatePrintCommands == NativePrintCommandsNotApplied && f.MonitoredSpooler == NativeSpoolerRequired
			default:
				return f.CandidatePrintCommands == NativePrintCommandsDeny
			}
		},
		members: func(f nativeFamilyFields, into map[string]any) {
			into["candidate_print_commands"] = f.CandidatePrintCommands
			into["monitored_spooler"] = f.MonitoredSpooler
		},
		compile: func(b nativeRuleBuilder, _ *NativeCapabilityRegistry) []NativeRule {
			return []NativeRule{b.rule("candidate_jobs", NativeRuleParticipation, "candidate_job", b.none("candidate_jobs_since_start"))}
		},
	},
	{
		id: NativeFamilyRemovableStorage, capability: "removable_storage",
		defaults: func(f *nativeFamilyFields) { f.EnabledSubCapabilities = []NativeSubCapability{} },
		validate: func(f nativeFamilyFields, disabled bool) bool {
			return validateNativePolicySelection(f.EnabledSubCapabilities,
				[]NativeSubCapability{NativeSubRemovableBlockStorage, NativeSubPortableMTP}, disabled)
		},
		members: func(f nativeFamilyFields, into map[string]any) {
			into["enabled_sub_capabilities"] = f.EnabledSubCapabilities
		},
		compile: func(b nativeRuleBuilder, _ *NativeCapabilityRegistry) []NativeRule {
			var attached []NativeCondition
			if slices.Contains(b.family.EnabledSubCapabilities, NativeSubRemovableBlockStorage) {
				attached = append(attached, b.none("summary.block_storage"))
			}
			if slices.Contains(b.family.EnabledSubCapabilities, NativeSubPortableMTP) {
				attached = append(attached, b.none("summary.portable_devices"))
			}
			return []NativeRule{b.rule("attached", NativeRuleBoth, "attached", NativeCondition{All: attached})}
		},
	},
	{
		id: NativeFamilyVirtualization, capability: "virtualization",
		defaults: func(f *nativeFamilyFields) { f.Rule = NativeVirtualizationNotApplied },
		validate: func(f nativeFamilyFields, disabled bool) bool {
			if disabled {
				return f.Rule == NativeVirtualizationNotApplied
			}
			return f.Rule == NativeVirtualizationBlockGuest || f.Rule == NativeVirtualizationRequirePhysical
		},
		members: func(f nativeFamilyFields, into map[string]any) { into["rule"] = f.Rule },
		compile: func(b nativeRuleBuilder, _ *NativeCapabilityRegistry) []NativeRule {
			if b.family.Rule == NativeVirtualizationRequirePhysical {
				return []NativeRule{b.rule("physical", NativeRuleBoth, "physical_required",
					b.equals("classification", NativeEnum("recognized_physical_or_root")))}
			}
			return []NativeRule{b.rule("guest", NativeRuleBoth, "guest_detected", NativeCondition{
				Compare: &NativeComparison{Field: "virtualization.classification", Op: NativeCompareNe, Value: NativeEnum("recognized_guest")},
			})}
		},
	},
	{
		id: NativeFamilyNetworkConfiguration, capability: "network",
		defaults: func(f *nativeFamilyFields) { f.EnabledSubCapabilities = []NativeSubCapability{} },
		validate: func(f nativeFamilyFields, disabled bool) bool {
			return validateNativePolicySelection(f.EnabledSubCapabilities,
				[]NativeSubCapability{NativeSubSystemProxyState, NativeSubManagedVPNState, NativeSubTunnelInterfaceState}, disabled)
		},
		members: func(f nativeFamilyFields, into map[string]any) {
			into["enabled_sub_capabilities"] = f.EnabledSubCapabilities
		},
		compile: func(b nativeRuleBuilder, _ *NativeCapabilityRegistry) []NativeRule {
			rules := []NativeRule{}
			for _, sub := range b.family.EnabledSubCapabilities {
				switch sub {
				case NativeSubSystemProxyState:
					rules = append(rules, b.rule("proxy", NativeRuleBoth, "proxy_detected", b.equals("system_proxy", NativeEnum("none"))))
				case NativeSubManagedVPNState:
					rules = append(rules, b.rule("vpn", NativeRuleBoth, "vpn_detected", b.equals("managed_vpn", NativeEnum("inactive"))))
				case NativeSubTunnelInterfaceState:
					rules = append(rules, b.rule("tunnels", NativeRuleBoth, "tunnel_detected", b.none("summary.tunnels_total")))
				}
			}
			return rules
		},
	},
	nativeDeviceFamily(NativeFamilyCamera, "camera"),
	nativeDeviceFamily(NativeFamilyMicrophone, "microphone"),
	{
		id:       NativeFamilyExternalCapture,
		defaults: func(*nativeFamilyFields) {},
		validate: func(_ nativeFamilyFields, disabled bool) bool { return disabled },
		members:  func(nativeFamilyFields, map[string]any) {},
	},
}

// nativeDeviceFamily is the camera or microphone family: the device ready,
// and not in use by another application.
func nativeDeviceFamily(id NativeFamilyID, capability NativeCapabilityID) nativeFamilyDefinition {
	return nativeFamilyDefinition{
		id: id, capability: capability,
		defaults: func(f *nativeFamilyFields) { f.ConcurrentUseCheck = NativeConcurrentUseNotRequired },
		validate: func(f nativeFamilyFields, disabled bool) bool {
			if f.ConcurrentUseCheck != NativeConcurrentUseNotRequired && f.ConcurrentUseCheck != NativeConcurrentUseSupportedRequired {
				return false
			}
			if disabled {
				return !f.RequireReady && f.ConcurrentUseCheck == NativeConcurrentUseNotRequired
			}
			return f.RequireReady || f.ConcurrentUseCheck != NativeConcurrentUseNotRequired
		},
		members: func(f nativeFamilyFields, into map[string]any) {
			into["require_ready"] = f.RequireReady
			into["concurrent_use_check"] = f.ConcurrentUseCheck
		},
		compile: func(b nativeRuleBuilder, _ *NativeCapabilityRegistry) []NativeRule {
			rules := []NativeRule{}
			if b.family.RequireReady {
				rules = append(rules, b.rule("ready", NativeRuleBoth, "not_ready", b.equals("ready", NativeEnum("ready"))))
			}
			if b.family.ConcurrentUseCheck == NativeConcurrentUseSupportedRequired {
				rules = append(rules, b.rule("exclusive", NativeRuleBoth, "in_use_by_other_app", b.equals("in_use_by_other_app", NativeEnum("no"))))
			}
			return rules
		},
	}
}

// compileNativeProcessFamily always blocks the release's never-allowable
// applications; the author's list, when it names any, is then the only
// applications allowed.
func compileNativeProcessFamily(b nativeRuleBuilder, registry *NativeCapabilityRegistry) []NativeRule {
	rules := []NativeRule{}
	if blocked := registry.neverAllowable(); len(blocked) > 0 {
		rules = append(rules, b.rule("blocked", NativeRuleBoth, "blocked_application", NativeCondition{AppsAbsent: blocked}))
	}
	if len(b.family.AllowedApplicationIDs) > 0 {
		rules = append(rules, b.rule("allowlist", NativeRuleBoth, "unlisted_application",
			NativeCondition{AppsOnly: slices.Clone(b.family.AllowedApplicationIDs)}))
	}
	return rules
}

// nativeFamilyDefinitionOf returns a family's definition.
func nativeFamilyDefinitionOf(id NativeFamilyID) (nativeFamilyDefinition, bool) {
	i := slices.IndexFunc(nativeFamilyDefinitions, func(d nativeFamilyDefinition) bool { return d.id == id })
	if i < 0 {
		return nativeFamilyDefinition{}, false
	}
	return nativeFamilyDefinitions[i], true
}

// nativeFamilyCapability returns the capability a family compiles rules on,
// and whether it compiles any.
func nativeFamilyCapability(id NativeFamilyID) (NativeCapabilityID, bool) {
	definition, ok := nativeFamilyDefinitionOf(id)
	return definition.capability, ok && definition.capability != ""
}

// nativeRuleBuilder builds one enabled family's rules. Each rule's ID is
// "<family>.<check>", so the server can find the family, and with it the
// outcome, a rule's occurrence leads to.
type nativeRuleBuilder struct {
	family     nativeFamilyFields
	capability NativeCapabilityID
}

func (b nativeRuleBuilder) rule(check string, phase NativeRulePhase, reason string, requirement NativeCondition) NativeRule {
	mode := NativeRuleObserve
	if b.family.Mode == NativeModeEnforce {
		mode = NativeRuleEnforce
	}
	return NativeRule{
		RuleID: string(b.family.ID) + "." + check, Capability: b.capability, Mode: mode, Phase: phase,
		Reason: string(b.capability) + "." + reason, Requirement: requirement,
	}
}

func (b nativeRuleBuilder) equals(field string, value NativeScalar) NativeCondition {
	return NativeCondition{Compare: &NativeComparison{Field: string(b.capability) + "." + field, Op: NativeCompareEq, Value: value}}
}

func (b nativeRuleBuilder) none(field string) NativeCondition { return b.equals(field, NativeCount(0)) }
