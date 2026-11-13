// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package model

import (
	"slices"
)

// NativeRuleStatus is a rule's three-valued result.
type NativeRuleStatus string

const (
	NativeRuleMet     NativeRuleStatus = "met"
	NativeRuleNotMet  NativeRuleStatus = "not_met"
	NativeRuleUnknown NativeRuleStatus = "unknown"
)

// IsValid reports whether the status is met, not met or unknown.
func (s NativeRuleStatus) IsValid() bool {
	return s == NativeRuleMet || s == NativeRuleNotMet || s == NativeRuleUnknown
}

// NativeUnknownCause says why a rule is unknown.
type NativeUnknownCause string

const (
	NativeUnknownUnavailable NativeUnknownCause = "unavailable"
	NativeUnknownUnhealthy   NativeUnknownCause = "unhealthy"
	NativeUnknownPermission  NativeUnknownCause = "permission"
	NativeUnknownPartial     NativeUnknownCause = "partial"
	NativeUnknownGap         NativeUnknownCause = "gap"
	NativeUnknownChecking    NativeUnknownCause = "checking"
)

// IsValid reports whether the cause is a known one.
func (c NativeUnknownCause) IsValid() bool {
	return slices.Contains([]NativeUnknownCause{NativeUnknownUnavailable, NativeUnknownUnhealthy, NativeUnknownPermission,
		NativeUnknownPartial, NativeUnknownGap, NativeUnknownChecking}, c)
}

// NativeFactPermission is a capability's OS permission state.
type NativeFactPermission string

const (
	NativePermissionNotDetermined NativeFactPermission = "not_determined"
	NativePermissionGranted       NativeFactPermission = "granted"
	NativePermissionDenied        NativeFactPermission = "denied"
	NativePermissionRestricted    NativeFactPermission = "restricted"
	NativePermissionUnavailable   NativeFactPermission = "unavailable"
)

// IsValid reports whether the permission state is a known one.
func (p NativeFactPermission) IsValid() bool {
	return slices.Contains([]NativeFactPermission{NativePermissionNotDetermined, NativePermissionGranted,
		NativePermissionDenied, NativePermissionRestricted, NativePermissionUnavailable}, p)
}

// NativeFactHealth is a capability's health, separate from its permission.
type NativeFactHealth string

const (
	NativeHealthUnsupported NativeFactHealth = "unsupported"
	NativeHealthHealthy     NativeFactHealth = "healthy"
	NativeHealthDegraded    NativeFactHealth = "degraded"
	NativeHealthFailed      NativeFactHealth = "failed"
	NativeHealthStopped     NativeFactHealth = "stopped"
)

// IsValid reports whether the health is a known one.
func (h NativeFactHealth) IsValid() bool {
	return slices.Contains([]NativeFactHealth{NativeHealthUnsupported, NativeHealthHealthy, NativeHealthDegraded,
		NativeHealthFailed, NativeHealthStopped}, h)
}

// NativeFactCertainty is how sure a capability's state is, covering its
// coverage too. Only a complete state can meet or fail a rule; anything less
// is unknown.
type NativeFactCertainty string

const (
	NativeCertaintyComplete NativeFactCertainty = "complete"
	NativeCertaintyPartial  NativeFactCertainty = "partial"
	NativeCertaintyUnknown  NativeFactCertainty = "unknown"
)

// IsValid reports whether the certainty is a known one.
func (c NativeFactCertainty) IsValid() bool {
	return c == NativeCertaintyComplete || c == NativeCertaintyPartial || c == NativeCertaintyUnknown
}

// NativeRuleMatch is what Desktop's on-device application matching found for
// one rule. Matched means the rule's application condition is violated: a
// blocked application is running, or an application outside the allowed
// list is. CatalogAppIDs names the blocked catalog applications present; it
// is always empty for an allowlist.
type NativeRuleMatch struct {
	Matched       bool
	CatalogAppIDs []string
}

// NativeApplicationFacts is the process capability's minimized state: the
// device's match for each application rule, never the inventory.
type NativeApplicationFacts struct {
	Checking int64 // processes whose identity is still being verified
	Matches  map[string]NativeRuleMatch
}

// NativeCapabilityFacts is what the evaluator knows about one capability:
// its latest state and the envelope around it. The envelope (health,
// permission, certainty, gaps) decides whether a rule can be known; rules
// never compare it.
type NativeCapabilityFacts struct {
	Permission NativeFactPermission
	Health     NativeFactHealth
	Certainty  NativeFactCertainty
	// InGap is true when events were lost since the latest full state, until
	// a fresh full state arrives.
	InGap        bool
	Fields       map[string]NativeScalar
	Applications NativeApplicationFacts
}

// NativeRuleResult is one evaluation of a rule.
type NativeRuleResult struct {
	Status NativeRuleStatus
	Cause  NativeUnknownCause // set only when Status is unknown
}

var nativeMet = NativeRuleResult{Status: NativeRuleMet}
var nativeNotMet = NativeRuleResult{Status: NativeRuleNotMet}

func nativeUnknown(cause NativeUnknownCause) NativeRuleResult {
	return NativeRuleResult{Status: NativeRuleUnknown, Cause: cause}
}

// EvaluateNativeRule evaluates a rule against the latest facts of its
// capability, with Kleene three-valued logic. It is the server's half of the
// rule semantics Desktop publishes with its conformance vectors, and must
// reach the same result on every conformance vector.
func EvaluateNativeRule(rule NativeRule, facts *NativeCapabilityFacts) NativeRuleResult {
	if facts == nil {
		return nativeUnknown(NativeUnknownUnavailable)
	}
	return rule.Requirement.evaluate(rule.RuleID, facts)
}

// envelopeUnknown is why the capability's envelope keeps any of its facts
// from being known, checked after the fact itself is found to be present.
// The order is Desktop's: a gap (events lost or an interruption, with no
// fresh state since), then unhealthy, then permission, then partial. A
// degraded capability is running; its certainty decides.
func (f *NativeCapabilityFacts) envelopeUnknown() (NativeRuleResult, bool) {
	switch {
	case f.InGap:
		return nativeUnknown(NativeUnknownGap), true
	case f.Health != NativeHealthHealthy && f.Health != NativeHealthDegraded:
		return nativeUnknown(NativeUnknownUnhealthy), true
	case f.Permission != NativePermissionGranted:
		return nativeUnknown(NativeUnknownPermission), true
	case f.Certainty != NativeCertaintyComplete:
		return nativeUnknown(NativeUnknownPartial), true
	default:
		return NativeRuleResult{}, false
	}
}

func (c NativeCondition) evaluate(ruleID string, facts *NativeCapabilityFacts) NativeRuleResult {
	switch {
	case c.Compare != nil:
		return c.Compare.evaluate(facts)
	case c.All != nil:
		return evaluateNativeAll(ruleID, c.All, facts)
	case c.Any != nil:
		return evaluateNativeAny(ruleID, c.Any, facts)
	case c.Not != nil:
		return evaluateNativeNot(c.Not.evaluate(ruleID, facts))
	default:
		return evaluateNativeApplications(ruleID, facts)
	}
}

// evaluateNativeAll is not met if any operand is not met, otherwise unknown
// if any is unknown (with the first unknown operand's cause), otherwise met.
func evaluateNativeAll(ruleID string, conditions []NativeCondition, facts *NativeCapabilityFacts) NativeRuleResult {
	result := nativeMet
	for _, condition := range conditions {
		switch operand := condition.evaluate(ruleID, facts); operand.Status {
		case NativeRuleNotMet:
			return nativeNotMet
		case NativeRuleUnknown:
			if result.Status == NativeRuleMet {
				result = operand
			}
		}
	}
	return result
}

// evaluateNativeAny is met if any operand is met, otherwise unknown if any is
// unknown, otherwise not met.
func evaluateNativeAny(ruleID string, conditions []NativeCondition, facts *NativeCapabilityFacts) NativeRuleResult {
	result := nativeNotMet
	for _, condition := range conditions {
		switch operand := condition.evaluate(ruleID, facts); operand.Status {
		case NativeRuleMet:
			return nativeMet
		case NativeRuleUnknown:
			if result.Status == NativeRuleNotMet {
				result = operand
			}
		}
	}
	return result
}

func evaluateNativeNot(operand NativeRuleResult) NativeRuleResult {
	switch operand.Status {
	case NativeRuleMet:
		return nativeNotMet
	case NativeRuleNotMet:
		return nativeMet
	default:
		return operand
	}
}

// evaluate is unknown when the field is unavailable or the envelope keeps it
// from being known, and otherwise compares its value.
func (c NativeComparison) evaluate(facts *NativeCapabilityFacts) NativeRuleResult {
	value, ok := facts.Fields[c.Field]
	if !ok || value.Type == "" {
		return nativeUnknown(NativeUnknownUnavailable)
	}
	if unknown, ok := facts.envelopeUnknown(); ok {
		return unknown
	}
	if c.compare(value) {
		return nativeMet
	}
	return nativeNotMet
}

func (c NativeComparison) compare(value NativeScalar) bool {
	switch c.Op {
	case NativeCompareEq:
		return value.equal(c.Value)
	case NativeCompareNe:
		return !value.equal(c.Value)
	case NativeCompareLt:
		return value.Count < c.Value.Count
	case NativeCompareLe:
		return value.Count <= c.Value.Count
	case NativeCompareGt:
		return value.Count > c.Value.Count
	case NativeCompareGe:
		return value.Count >= c.Value.Count
	case NativeCompareIn:
		return slices.ContainsFunc(c.Values, value.equal)
	default:
		return !slices.ContainsFunc(c.Values, value.equal)
	}
}

// evaluateNativeApplications reads the device's match for the rule. It is
// unknown when no match is reported yet, when the envelope keeps it from
// being known (incomplete process coverage reads as partial), and while a
// new process is being identified.
func evaluateNativeApplications(ruleID string, facts *NativeCapabilityFacts) NativeRuleResult {
	match, ok := facts.Applications.Matches[ruleID]
	if !ok {
		return nativeUnknown(NativeUnknownUnavailable)
	}
	if unknown, known := facts.envelopeUnknown(); known {
		return unknown
	}
	if facts.Applications.Checking > 0 {
		return nativeUnknown(NativeUnknownChecking)
	}
	if match.Matched {
		return nativeNotMet
	}
	return nativeMet
}

func (s NativeScalar) equal(o NativeScalar) bool { return s == o }
