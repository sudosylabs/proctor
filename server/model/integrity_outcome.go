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
	"regexp"
	"strings"
	"time"
)

// IntegrityOutcome is what an integrity policy does once its rule is met.
// Every policy picks from this closed list, and audited core code applies it.
type IntegrityOutcome string

const (
	IntegrityOutcomeFlag           IntegrityOutcome = "flag"
	IntegrityOutcomeFlagAndWarn    IntegrityOutcome = "flag_and_warn"
	IntegrityOutcomeFlagAndSuspend IntegrityOutcome = "flag_and_suspend"
)

func (o IntegrityOutcome) IsValid() bool {
	return o == IntegrityOutcomeFlag || o == IntegrityOutcomeFlagAndWarn || o == IntegrityOutcomeFlagAndSuspend
}

// IntegrityFailMode is what a policy does when it cannot observe what it
// watches.
type IntegrityFailMode string

const (
	// IntegrityFailContain treats losing sight of the candidate as the
	// violation itself: connection loss suspends when its lease expires.
	IntegrityFailContain IntegrityFailMode = "contain"
	// IntegrityFailRecordGap records that signals are missing and carries on:
	// focus loss counts the signals missing before each one it gets.
	IntegrityFailRecordGap IntegrityFailMode = "record_gap"
)

// IntegrityPolicyKind identifies the policy behind evidence and a flag; it is
// not a client-supplied verdict or severity. Core kinds are plain names and a
// capability's kinds are "<plugin-id>.<kind>".
type IntegrityPolicyKind string

const (
	IntegrityPolicyConnectionLoss    IntegrityPolicyKind = "connection_loss"
	IntegrityPolicyFocusLoss         IntegrityPolicyKind = "focus_loss"
	IntegrityPolicyBrowserNavigation IntegrityPolicyKind = "browser_navigation"
)

// NativeIntegrityPolicyNamespace prefixes the kinds of desktop native
// capabilities, as in native.display_topology. The core owns it, so no plugin
// may use it as its ID.
const NativeIntegrityPolicyNamespace = "native"

// IntegrityPolicyKindMaxLength is a 64-character plugin ID, a dot and a
// 32-character name; the SQL schema uses the same bound.
const IntegrityPolicyKindMaxLength = 97

// capabilityPolicyKind must match the SQL CHECK pattern character for
// character.
var capabilityPolicyKind = regexp.MustCompile(`^[a-z][a-z0-9.-]{0,63}\.[a-z][a-z0-9_]{0,31}$`)

func (k IntegrityPolicyKind) IsValid() bool {
	return k.IsCore() || k.IsCapability()
}

// IsCore reports whether the kind is one of the policies built into the core.
func (k IntegrityPolicyKind) IsCore() bool {
	_, core := coreIntegrityPolicies[k]
	return core
}

// IsCapability reports whether the kind is spelled as a capability's kind,
// which includes native capabilities.
func (k IntegrityPolicyKind) IsCapability() bool {
	return capabilityPolicyKind.MatchString(string(k))
}

func (k IntegrityPolicyKind) nativeCapability() (NativeCapabilityID, bool) {
	capability, native := strings.CutPrefix(string(k), NativeIntegrityPolicyNamespace+".")
	return NativeCapabilityID(capability), native && isNativeIdentifier(capability)
}

// IntegrityPolicy is what the core knows about one built-in policy.
type IntegrityPolicy struct {
	Kind     IntegrityPolicyKind
	FailMode IntegrityFailMode
	// SuspensionReason is what the candidate is told when the policy suspends
	// their attempt; empty means it never suspends.
	SuspensionReason AttemptSuspensionCandidateReason
}

var coreIntegrityPolicies = map[IntegrityPolicyKind]IntegrityPolicy{
	IntegrityPolicyConnectionLoss: {
		Kind:             IntegrityPolicyConnectionLoss,
		FailMode:         IntegrityFailContain,
		SuspensionReason: AttemptSuspensionCandidateReasonSecureContinuityLost,
	},
	IntegrityPolicyFocusLoss: {
		Kind:             IntegrityPolicyFocusLoss,
		FailMode:         IntegrityFailRecordGap,
		SuspensionReason: AttemptSuspensionCandidateReasonFocusLossPolicy,
	},
	IntegrityPolicyBrowserNavigation: {
		Kind:     IntegrityPolicyBrowserNavigation,
		FailMode: IntegrityFailRecordGap,
	},
}

// Every native capability's policy contains on failure, because the
// desktop's enforce rules contain the window when a capability cannot be
// observed, and suspends with the native reason.
func integrityPolicy(kind IntegrityPolicyKind) (IntegrityPolicy, bool) {
	if policy, ok := coreIntegrityPolicies[kind]; ok {
		return policy, true
	}
	if _, native := kind.nativeCapability(); native {
		return IntegrityPolicy{
			Kind:             kind,
			FailMode:         IntegrityFailContain,
			SuspensionReason: AttemptSuspensionCandidateReasonNativePolicy,
		}, true
	}
	return IntegrityPolicy{}, false
}

func integrityPolicySuspends(reason AttemptSuspensionCandidateReason) bool {
	if reason == "" {
		return false
	}
	if reason == AttemptSuspensionCandidateReasonNativePolicy {
		return true
	}
	for _, policy := range coreIntegrityPolicies {
		if policy.SuspensionReason == reason {
			return true
		}
	}
	return false
}

// IntegrityEvidenceDetailMaxBytes bounds a capability's opaque evidence detail;
// the SQL schema uses the same bound.
const IntegrityEvidenceDetailMaxBytes = 64 * 1024

func validEvidenceDetail(detail json.RawMessage) bool {
	trimmed := bytes.TrimSpace(detail)
	return len(detail) <= IntegrityEvidenceDetailMaxBytes &&
		len(trimmed) > 1 && trimmed[0] == '{' && json.Valid(trimmed)
}

// NewIntegrityPolicySuspension opens the suspension a policy's decision causes,
// with that policy's reason. A policy that never suspends has none to open.
func NewIntegrityPolicySuspension(
	id AttemptSuspensionID,
	attemptID ExamAttemptID,
	participationID AttemptParticipationID,
	flagID IntegrityFlagID,
	generation int64,
	kind IntegrityPolicyKind,
	at time.Time,
) (*AttemptSuspension, error) {
	policy, ok := integrityPolicy(kind)
	if !ok || policy.SuspensionReason == "" {
		return nil, invalidModelError("NewIntegrityPolicySuspension", "attempt_suspension", "", "the policy does not suspend", "")
	}

	return NewPolicyAttemptSuspension(id, attemptID, participationID, flagID, generation, policy.SuspensionReason, at)
}
