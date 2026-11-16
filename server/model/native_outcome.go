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

// NativeFlagEvidenceLimit bounds the evidence one native flag keeps, one
// piece per occurrence, as focus loss bounds its episodes.
const NativeFlagEvidenceLimit = 100

// NativeIntegrityPolicyKind is the policy kind of a native capability's
// flags, such as native.display.
func NativeIntegrityPolicyKind(capability NativeCapabilityID) IntegrityPolicyKind {
	return IntegrityPolicyKind(NativeIntegrityPolicyNamespace + "." + string(capability))
}

// RaisesFlag reports whether a transition raises a flag, which is when an
// enforce rule's occurrence opens. Observe rules only record, and an unknown
// status never opens an occurrence.
func (t NativeRuleTransition) RaisesFlag() bool {
	return t.Mode == NativeRuleEnforce && t.Event == NativeOccurrenceOpened
}

// nativeFlagDetail never carries the facts themselves.
type nativeFlagDetail struct {
	RuleID     string `json:"rule_id"`
	Reason     string `json:"reason"`
	Occurrence int64  `json:"occurrence"`
	OpenedAt   string `json:"opened_at"`
}

// NewNativeRuleEvidence is the evidence an opened enforce occurrence adds to
// its capability's flag. The device time stays in the detail; the evidence is
// observed no later than the server recorded it.
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
