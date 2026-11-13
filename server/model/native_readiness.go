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
	// State is the capability's minimized full state, and Summary the counts
	// Desktop derives from it, each as its published fact schema describes
	// it. Neither holds what must not leave the device.
	State   json.RawMessage `json:"state"`
	Summary json.RawMessage `json:"summary"`
}

// NativeRuleReport is one rule's status: Desktop's in a report, the
// server's in a result.
type NativeRuleReport struct {
	RuleID       string             `json:"rule_id"`
	Status       NativeRuleStatus   `json:"status"`
	UnknownCause NativeUnknownCause `json:"unknown_cause,omitempty"`
}

func (r NativeCapabilityReadiness) validate() bool {
	return isNativeIdentifier(string(r.Capability)) && IsValidAgreementID(r.SourceInstanceID) &&
		securitySafeInt(r.Sequence) &&
		slices.Contains([]NativeFactPermission{NativePermissionNotDetermined, NativePermissionGranted, NativePermissionDenied,
			NativePermissionRestricted, NativePermissionUnavailable}, r.Permission) &&
		slices.Contains([]NativeFactHealth{NativeHealthUnsupported, NativeHealthHealthy, NativeHealthDegraded,
			NativeHealthFailed, NativeHealthStopped}, r.Health) &&
		slices.Contains([]NativeFactCertainty{NativeCertaintyComplete, NativeCertaintyPartial, NativeCertaintyUnknown}, r.Certainty) &&
		nativeFactObject(r.State) && nativeFactObject(r.Summary)
}

// nativeFactObject reports whether document is one bounded JSON object.
func nativeFactObject(document json.RawMessage) bool {
	trimmed := bytes.TrimSpace(document)
	return len(document) <= NativeCapabilityStateMaxBytes && len(trimmed) > 1 && trimmed[0] == '{' && json.Valid(trimmed)
}

func (r NativeRuleReport) validate() bool {
	statusOK := slices.Contains([]NativeRuleStatus{NativeRuleMet, NativeRuleNotMet, NativeRuleUnknown}, r.Status)
	causeOK := (r.Status == NativeRuleUnknown) == (r.UnknownCause != "") &&
		(r.UnknownCause == "" || slices.Contains([]NativeUnknownCause{NativeUnknownUnavailable, NativeUnknownGap,
			NativeUnknownUnhealthy, NativeUnknownPermission, NativeUnknownPartial, NativeUnknownChecking}, r.UnknownCause))
	return isNativeRuleID(r.RuleID) && statusOK && causeOK
}

// facts reads a capability's registered fields out of its reported fact.
// "<capability>.summary.<name>" is read from the summary and every other
// "<capability>.<name>" from the state, so display.summary.active_physical
// is summary.active_physical and interactive_session.console_owner is
// state.console_owner. Both documents must match their published schemas. A
// value of the wrong type fails the report; a missing one is unavailable.
func (r *NativeCapabilityRegistry) facts(readiness NativeCapabilityReadiness) (*NativeCapabilityFacts, bool) {
	definition, ok := r.Capability(readiness.Capability)
	if !ok || !r.validFact(readiness.Capability, readiness.State, readiness.Summary) {
		return nil, false
	}
	state, ok := decodeNativeFactObject(readiness.State)
	if !ok {
		return nil, false
	}
	summary, ok := decodeNativeFactObject(readiness.Summary)
	if !ok {
		return nil, false
	}
	facts := &NativeCapabilityFacts{
		Permission: readiness.Permission, Health: readiness.Health, Certainty: readiness.Certainty,
		Fields: map[string]NativeScalar{},
	}
	for _, field := range definition.Fields {
		value, present := nativeFactValue(state, summary, field.Name)
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

func decodeNativeFactObject(document json.RawMessage) (map[string]any, bool) {
	var object map[string]any
	decoder := json.NewDecoder(bytes.NewReader(document))
	decoder.UseNumber()
	return object, decoder.Decode(&object) == nil && object != nil
}

func nativeFactValue(state, summary map[string]any, name string) (any, bool) {
	path := strings.Split(name, ".")[1:]
	var current any = state
	if len(path) > 1 && path[0] == "summary" {
		current, path = summary, path[1:]
	}
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
	kind, known := nativeFieldKinds[field.Type]
	if !known {
		return NativeScalar{}, false
	}
	return kind.read(field, value)
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
