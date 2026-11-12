// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package model

import (
	"bytes"
	"encoding/json"
	"slices"
	"strings"
)

// Bounds on what a preflight reports about native capabilities.
const (
	NativeCapabilityStateMaxBytes = 16 * 1024
	nativeReadinessCapabilityMax  = 32
)

// NativeActivation is one capability a policy runs, with the sub-categories
// it selects; no categories means every registered one.
type NativeActivation struct {
	Capability NativeCapabilityID `json:"capability"`
	Categories []string           `json:"categories"`
}

// NativeCapabilityReadiness is one activated capability as Desktop found it
// at Start: its full state, the envelope around it, and where its event
// stream begins. The exam stream opens with this same state.
type NativeCapabilityReadiness struct {
	Capability       NativeCapabilityID   `json:"capability"`
	SourceInstanceID string               `json:"source_instance_id"`
	Sequence         int64                `json:"sequence"`
	Permission       NativeFactPermission `json:"permission"`
	Health           NativeFactHealth     `json:"health"`
	Certainty        NativeFactCertainty  `json:"certainty"`
	// State is the capability's minimized full state, as its published fact
	// schema describes it. It never holds what must not leave the device.
	State json.RawMessage `json:"state"`
}

// NativeRuleReport is one rule's status: Desktop's in a report, the
// server's in a result.
type NativeRuleReport struct {
	RuleID       string             `json:"rule_id"`
	Status       NativeRuleStatus   `json:"status"`
	UnknownCause NativeUnknownCause `json:"unknown_cause,omitempty"`
}

func (r NativeCapabilityReadiness) validate() bool {
	state := bytes.TrimSpace(r.State)
	return isNativeIdentifier(string(r.Capability)) && IsValidAgreementID(r.SourceInstanceID) &&
		securitySafeInt(r.Sequence) &&
		slices.Contains([]NativeFactPermission{NativePermissionNotDetermined, NativePermissionGranted, NativePermissionDenied,
			NativePermissionRestricted, NativePermissionUnavailable}, r.Permission) &&
		slices.Contains([]NativeFactHealth{NativeHealthUnsupported, NativeHealthHealthy, NativeHealthDegraded,
			NativeHealthFailed, NativeHealthStopped}, r.Health) &&
		slices.Contains([]NativeFactCertainty{NativeCertaintyComplete, NativeCertaintyPartial, NativeCertaintyUnknown}, r.Certainty) &&
		len(r.State) <= NativeCapabilityStateMaxBytes && len(state) > 1 && state[0] == '{' && json.Valid(state)
}

func (r NativeRuleReport) validate() bool {
	statusOK := slices.Contains([]NativeRuleStatus{NativeRuleMet, NativeRuleNotMet, NativeRuleUnknown}, r.Status)
	causeOK := (r.Status == NativeRuleUnknown) == (r.UnknownCause != "") &&
		(r.UnknownCause == "" || slices.Contains([]NativeUnknownCause{NativeUnknownUnavailable, NativeUnknownGap,
			NativeUnknownUnhealthy, NativeUnknownPermission, NativeUnknownPartial, NativeUnknownChecking}, r.UnknownCause))
	return isNativeRuleID(r.RuleID) && statusOK && causeOK
}

// compileNativePolicy turns the release registry into what a policy sends at
// activation: the baseline rules, unchanged, and the capabilities they need.
// Managers' family selections compile into further rules once the registry
// carries those families' capabilities.
func compileNativePolicy(registry *NativeCapabilityRegistry) ([]NativeActivation, []NativeRule) {
	activations, rules := []NativeActivation{}, []NativeRule{}
	if registry == nil {
		return activations, rules
	}
	for _, rule := range registry.baseline {
		rules = append(rules, rule)
		if !slices.ContainsFunc(activations, func(a NativeActivation) bool { return a.Capability == rule.Capability }) {
			activations = append(activations, NativeActivation{Capability: rule.Capability, Categories: []string{}})
		}
	}
	slices.SortFunc(activations, func(a, b NativeActivation) int { return strings.Compare(string(a.Capability), string(b.Capability)) })
	return activations, rules
}

// facts reads a capability's registered fields out of its reported state.
// A field's path is its name after the capability prefix, so
// display.summary.active_physical is summary.active_physical in the state.
// A value of the wrong type fails the report; a missing one is unavailable.
func (r *NativeCapabilityRegistry) facts(readiness NativeCapabilityReadiness) (*NativeCapabilityFacts, bool) {
	definition, ok := r.Capability(readiness.Capability)
	if !ok {
		return nil, false
	}
	var state map[string]any
	decoder := json.NewDecoder(bytes.NewReader(readiness.State))
	decoder.UseNumber()
	if decoder.Decode(&state) != nil {
		return nil, false
	}
	facts := &NativeCapabilityFacts{
		Permission: readiness.Permission, Health: readiness.Health, Certainty: readiness.Certainty,
		Fields: map[string]NativeScalar{},
	}
	for _, field := range definition.Fields {
		value, present := nativeStateValue(state, field.Name)
		if !present {
			continue
		}
		scalar, ok := nativeFieldValue(field, value)
		if !ok {
			return nil, false
		}
		facts.Fields[field.Name] = scalar
	}
	if definition.AppConditions {
		applications, ok := nativeApplicationFacts(state)
		if !ok {
			return nil, false
		}
		facts.Applications = applications
	}
	return facts, true
}

func nativeStateValue(state map[string]any, name string) (any, bool) {
	path := strings.Split(name, ".")[1:]
	var current any = state
	for _, segment := range path {
		object, ok := current.(map[string]any)
		if !ok {
			return nil, false
		}
		if current, ok = object[segment]; !ok {
			return nil, false
		}
	}
	return current, true
}

func nativeFieldValue(field NativeFieldDefinition, value any) (NativeScalar, bool) {
	switch field.Type {
	case NativeFieldCount:
		number, ok := value.(json.Number)
		if !ok {
			return NativeScalar{}, false
		}
		count, err := number.Int64()
		if err != nil || count < 0 || field.Max > 0 && count > field.Max {
			return NativeScalar{}, false
		}
		return NativeCount(count), true
	case NativeFieldEnum:
		text, ok := value.(string)
		return NativeEnum(text), ok && slices.Contains(field.Values, text)
	default:
		boolean, ok := value.(bool)
		return NativeBoolean(boolean), ok
	}
}

// nativeApplicationFacts reads the process capability's minimized state: the
// count of identities being checked and each rule's match.
func nativeApplicationFacts(state map[string]any) (NativeApplicationFacts, bool) {
	applications := NativeApplicationFacts{Matches: map[string]NativeRuleMatch{}}
	if raw, present := state["checking"]; present {
		number, ok := raw.(json.Number)
		checking, err := number.Int64()
		if !ok || err != nil || checking < 0 {
			return NativeApplicationFacts{}, false
		}
		applications.Checking = checking
	}
	matches, _ := state["rule_matches"].([]any)
	for _, raw := range matches {
		match, ok := raw.(map[string]any)
		ruleID, idOK := match["rule_id"].(string)
		matched, matchedOK := match["matched"].(bool)
		if !ok || !idOK || !matchedOK || !isNativeRuleID(ruleID) {
			return NativeApplicationFacts{}, false
		}
		applications.Matches[ruleID] = NativeRuleMatch{Matched: matched}
	}
	return applications, true
}

// evaluateNativeReadiness evaluates a policy's readiness rules on what
// Desktop reported at Start. The report must name every activated capability
// once, in activation order. It returns the server's status for each
// readiness rule, and whether an enforce rule that is not met, or unknown,
// blocks admission.
func evaluateNativeReadiness(
	registry *NativeCapabilityRegistry,
	activations []NativeActivation,
	rules []NativeRule,
	reported []NativeCapabilityReadiness,
) ([]NativeRuleReport, bool, bool) {
	if len(reported) != len(activations) {
		return nil, false, false
	}
	facts := make(map[NativeCapabilityID]*NativeCapabilityFacts, len(reported))
	for i, readiness := range reported {
		if readiness.Capability != activations[i].Capability {
			return nil, false, false
		}
		capability, ok := registry.facts(readiness)
		if !ok {
			return nil, false, false
		}
		facts[readiness.Capability] = capability
	}

	statuses, blocked := []NativeRuleReport{}, false
	for _, rule := range rules {
		if !rule.Phase.AppliesTo(NativeRuleReadiness) {
			continue
		}
		result := EvaluateNativeRule(rule, facts[rule.Capability])
		statuses = append(statuses, NativeRuleReport{RuleID: rule.RuleID, Status: result.Status, UnknownCause: result.Cause})
		if rule.Mode == NativeRuleEnforce && result.Status != NativeRuleMet {
			blocked = true
		}
	}
	return statuses, blocked, true
}
