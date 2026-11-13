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

// NativeRuleTracker holds one rule's reported status over time. Getting
// worse is immediate. Recovering to met waits until the facts have stayed met
// for the capability's stability time.
type NativeRuleTracker struct {
	result   NativeRuleResult
	observed bool
	// metSince is when evaluations started meeting the rule while its status
	// is still not met or unknown.
	metSince time.Time
}

// Status returns the rule's current status.
func (t *NativeRuleTracker) Status() NativeRuleResult { return t.result }

// Observe records an evaluation made at a fact's time and reports whether the
// rule's status changed. The first observation, at activation, sets the
// status directly.
func (t *NativeRuleTracker) Observe(evaluation NativeRuleResult, at time.Time, stability time.Duration) bool {
	if !t.observed {
		t.observed, t.result = true, evaluation
		return true
	}
	if evaluation.Status != NativeRuleMet {
		t.metSince = time.Time{}
		if evaluation == t.result {
			return false
		}
		t.result = evaluation
		return true
	}
	if t.result.Status == NativeRuleMet {
		return false
	}
	if t.metSince.IsZero() {
		t.metSince = at
	}
	if at.Sub(t.metSince) < stability {
		return false
	}
	t.result, t.metSince = nativeMet, time.Time{}
	return true
}

// Settle applies the stability time when no new fact arrives: a rule whose
// facts have stayed met long enough recovers at now.
func (t *NativeRuleTracker) Settle(now time.Time, stability time.Duration) bool {
	if !t.observed || t.result.Status == NativeRuleMet || t.metSince.IsZero() || now.Sub(t.metSince) < stability {
		return false
	}
	t.result, t.metSince = nativeMet, time.Time{}
	return true
}

type nativeRuleTrackerWire struct {
	Status       NativeRuleStatus   `json:"status"`
	UnknownCause NativeUnknownCause `json:"unknown_cause,omitempty"`
	Observed     bool               `json:"observed"`
	MetSince     *time.Time         `json:"met_since,omitempty"`
}

func (t NativeRuleTracker) MarshalJSON() ([]byte, error) {
	wire := nativeRuleTrackerWire{Status: t.result.Status, UnknownCause: t.result.Cause, Observed: t.observed}
	if !t.metSince.IsZero() {
		since := t.metSince
		wire.MetSince = &since
	}
	return json.Marshal(wire)
}

func (t *NativeRuleTracker) UnmarshalJSON(raw []byte) error {
	var wire nativeRuleTrackerWire
	if t == nil || json.Unmarshal(raw, &wire) != nil {
		return ErrNativeDeliveryInvalid
	}
	value := NativeRuleTracker{result: NativeRuleResult{Status: wire.Status, Cause: wire.UnknownCause}, observed: wire.Observed}
	if wire.MetSince != nil {
		value.metSince = *wire.MetSince
	}
	if wire.Observed && !(NativeRuleReport{RuleID: "rule", Status: wire.Status, UnknownCause: wire.UnknownCause}).valid() ||
		!wire.Observed && (wire.Status != "" || wire.MetSince != nil) {
		return ErrNativeDeliveryInvalid
	}
	*t = value
	return nil
}
