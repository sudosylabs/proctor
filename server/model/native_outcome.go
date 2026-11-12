// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package model

import (
	"encoding/json"
	"time"
)

// NativeFlagEvidenceLimit bounds the evidence one native Flag retains, one
// piece per occurrence, as Focus Loss bounds its episodes.
const NativeFlagEvidenceLimit = 100

// NativeIntegrityPolicyKind is the policy kind of a native capability's
// Flags, such as native.display.
func NativeIntegrityPolicyKind(capability NativeCapabilityID) IntegrityPolicyKind {
	return IntegrityPolicyKind(NativeIntegrityPolicyNamespace + "." + string(capability))
}

// RaisesFlag reports whether a transition raises a Flag: an enforce rule's
// occurrence opened. Observe rules only record, and an unknown status never
// opens an occurrence.
func (t NativeRuleTransition) RaisesFlag() bool {
	return t.Mode == NativeRuleEnforce && t.Event == NativeOccurrenceOpened
}

// nativeFlagDetail is what a native Flag's evidence says about the
// occurrence: the rule, its reason and when it stopped being met. It never
// carries the facts themselves.
type nativeFlagDetail struct {
	RuleID     string `json:"rule_id"`
	Reason     string `json:"reason"`
	Occurrence int64  `json:"occurrence"`
	OpenedAt   string `json:"opened_at"`
}

// NewNativeRuleEvidence is the evidence an opened enforce occurrence adds to
// its capability's Flag. The device time is kept in the detail; the evidence
// is observed no later than the server recorded it.
func NewNativeRuleEvidence(
	id IntegrityEvidenceID,
	attemptID ExamAttemptID,
	participationID AttemptParticipationID,
	flagID IntegrityFlagID,
	generation int64,
	transition NativeRuleTransition,
	recordedAt time.Time,
) (*IntegrityEvidence, error) {
	if !transition.RaisesFlag() || transition.Validate() != nil {
		return nil, invalidModelError("NewNativeRuleEvidence", "integrity_evidence", "", "the transition raises no Flag", "")
	}
	detail, err := json.Marshal(nativeFlagDetail{
		RuleID: transition.RuleID, Reason: transition.Reason, Occurrence: transition.Occurrence,
		OpenedAt: transition.At.UTC().Format("2006-01-02T15:04:05.000Z"),
	})
	if err != nil {
		return nil, err
	}
	recordedAt = TimeUTC(recordedAt)
	observedAt := TimeUTC(transition.At)
	if observedAt.After(recordedAt) {
		observedAt = recordedAt
	}
	evidence := &IntegrityEvidence{
		ID: id, AttemptID: attemptID, ParticipationID: participationID, FlagID: flagID, Generation: generation,
		Kind: NativeIntegrityPolicyKind(transition.Capability), Detail: detail,
		ObservedAt: observedAt, RecordedAt: recordedAt,
	}
	if err = evidence.Validate(); err != nil {
		return nil, err
	}
	return evidence, nil
}
