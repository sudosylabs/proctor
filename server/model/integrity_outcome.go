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
)

// IntegrityOutcome is what an integrity policy does once its rule is met. It
// comes from this closed list whichever policy decides, and audited core code
// applies it.
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
	// violation itself: Connection Loss suspends when its lease expires.
	IntegrityFailContain IntegrityFailMode = "contain"
	// IntegrityFailRecordGap records that signals are missing and keeps
	// going: Focus Loss counts the signals missing before each one it gets.
	IntegrityFailRecordGap IntegrityFailMode = "record_gap"
)

// IntegrityPolicyKind identifies the policy that caused evidence and a flag.
// It is not a verdict or severity supplied by a client. The core kinds are
// plain names; a capability's kinds are "<plugin-id>.<kind>".
type IntegrityPolicyKind string

const (
	IntegrityPolicyConnectionLoss    IntegrityPolicyKind = "connection_loss"
	IntegrityPolicyFocusLoss         IntegrityPolicyKind = "focus_loss"
	IntegrityPolicyBrowserNavigation IntegrityPolicyKind = "browser_navigation"
)

// IntegrityPolicyKindMaxLength bounds a kind: a 64-character plugin ID, a
// dot, and a 32-character name. The SQL schema uses the same bound.
const IntegrityPolicyKindMaxLength = 97

// capabilityPolicyKind is the SQL CHECK's pattern, character for character.
var capabilityPolicyKind = regexp.MustCompile(`^[a-z][a-z0-9.-]{0,63}\.[a-z][a-z0-9_]{0,31}$`)

func (k IntegrityPolicyKind) IsValid() bool {
	return k.IsCore() || k.IsCapability()
}

// IsCore reports whether the kind is one of the policies built into the core.
func (k IntegrityPolicyKind) IsCore() bool {
	_, core := coreIntegrityPolicies[k]
	return core
}

// IsCapability reports whether the kind is spelled as a capability's kind.
func (k IntegrityPolicyKind) IsCapability() bool {
	return capabilityPolicyKind.MatchString(string(k))
}

// IntegrityPolicy is what the core knows about a built-in policy.
type IntegrityPolicy struct {
	Kind     IntegrityPolicyKind
	FailMode IntegrityFailMode
	// SuspensionReason is what the candidate is told when the policy
	// suspends their Attempt, or empty when it never suspends.
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

// integrityPolicySuspends reports whether some built-in policy suspends with
// the reason.
func integrityPolicySuspends(reason AttemptSuspensionCandidateReason) bool {
	if reason == "" {
		return false
	}
	for _, policy := range coreIntegrityPolicies {
		if policy.SuspensionReason == reason {
			return true
		}
	}
	return false
}

// IntegrityEvidenceDetailMaxBytes bounds the opaque detail a capability's
// evidence carries. The SQL schema uses the same bound.
const IntegrityEvidenceDetailMaxBytes = 64 * 1024

// validCapabilityEvidenceDetail reports whether detail is one bounded JSON
// object.
func validCapabilityEvidenceDetail(detail json.RawMessage) bool {
	trimmed := bytes.TrimSpace(detail)
	return len(detail) <= IntegrityEvidenceDetailMaxBytes &&
		len(trimmed) > 1 && trimmed[0] == '{' && json.Valid(trimmed)
}
