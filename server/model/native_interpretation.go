// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package model

import (
	"encoding/json"
	"slices"
	"strings"
	"time"
)

// NativeInterpretationMaxBytes bounds the stored interpretation state of one
// Participation: every activated capability's latest facts and every rule's
// tracker.
const NativeInterpretationMaxBytes = 256 * 1024

// NativeObservedFact is a fact record read through the release registry:
// its registered fields, ready to evaluate without the registry.
type NativeObservedFact struct {
	Capability       NativeCapabilityID    `json:"capability"`
	SourceInstanceID string                `json:"source_instance_id"`
	Sequence         int64                 `json:"sequence"`
	ObservedAt       time.Time             `json:"observed_at"`
	Facts            NativeCapabilityFacts `json:"facts"`
}

// ObserveFact checks a reported fact against the capability's published
// schemas and reads its registered fields.
func (r *NativeCapabilityRegistry) ObserveFact(fact NativeCapabilityFact) (NativeObservedFact, bool) {
	facts, ok := r.facts(fact.NativeCapabilityReadiness)
	if !ok || fact.Validate() != nil {
		return NativeObservedFact{}, false
	}
	return NativeObservedFact{
		Capability: fact.Capability, SourceInstanceID: fact.SourceInstanceID, Sequence: fact.Sequence,
		ObservedAt: fact.ObservedAt, Facts: *facts,
	}, true
}

// NativeOccurrenceEvent says what a rule transition did to the rule's
// occurrence: opened one, closed the open one, or neither.
type NativeOccurrenceEvent string

const (
	NativeOccurrenceOpened NativeOccurrenceEvent = "opened"
	NativeOccurrenceClosed NativeOccurrenceEvent = "closed"
)

// NativeRuleTransition is one change of a rule's server status. An
// occurrence opens when the rule becomes not met while none is open, and
// closes when the rule is met again; an unknown stretch neither opens nor
// closes one.
type NativeRuleTransition struct {
	RuleID     string                `json:"rule_id"`
	Capability NativeCapabilityID    `json:"capability"`
	Mode       NativeRuleMode        `json:"mode"`
	Reason     string                `json:"reason"`
	From       *NativeRuleReport     `json:"from,omitempty"`
	To         NativeRuleReport      `json:"to"`
	At         time.Time             `json:"at"`
	Occurrence int64                 `json:"occurrence,omitempty"`
	Event      NativeOccurrenceEvent `json:"event,omitempty"`
}

func (t NativeRuleTransition) Validate() error {
	if !isNativeRuleID(t.RuleID) || !isNativeIdentifier(string(t.Capability)) ||
		!t.Mode.IsValid() || !isNativeReasonID(t.Reason) ||
		t.From != nil && (!t.From.valid() || t.From.RuleID != t.RuleID) ||
		!t.To.valid() || t.To.RuleID != t.RuleID || !securityInstant(t.At) ||
		t.Occurrence < 0 || !securitySafeInt(t.Occurrence) ||
		(t.Event != "") != (t.Occurrence > 0) ||
		t.Event != "" && t.Event != NativeOccurrenceOpened && t.Event != NativeOccurrenceClosed {
		return ErrNativeDeliveryInvalid
	}
	return nil
}

// NativeInterpretation is the server's evaluation of one Participation's
// native facts: the latest facts of each activated capability and the status
// of each rule of the current phase. It starts from the facts Desktop
// reported at Start, in the readiness phase, and advances one delivery record
// at a time, in stream order. Participation is confirmed on the stream.
type NativeInterpretation struct {
	capabilities map[NativeCapabilityID]*nativeCapabilityTrack
	rules        map[string]*nativeRuleTrack
	phase        NativeRulePhase
	// at is the latest fact time seen. Client clocks may step back, so time
	// never moves backwards here.
	at time.Time
}

type nativeCapabilityTrack struct {
	SourceInstanceID string                 `json:"source_instance_id"`
	Sequence         int64                  `json:"sequence"`
	StabilityMS      int64                  `json:"stability_ms"`
	Facts            *NativeCapabilityFacts `json:"facts,omitempty"`
}

type nativeRuleTrack struct {
	Capability    NativeCapabilityID `json:"capability"`
	Mode          NativeRuleMode     `json:"mode"`
	Reason        string             `json:"reason"`
	Tracker       NativeRuleTracker  `json:"tracker"`
	Occurrences   int64              `json:"occurrences"`
	Open          bool               `json:"open"`
	Desktop       *NativeRuleReport  `json:"desktop,omitempty"`
	Contained     bool               `json:"contained"`
	Discrepancies int64              `json:"discrepancies"`
}

// NativeRuleStanding is a rule's current standing for the board.
type NativeRuleStanding struct {
	Rule           NativeRuleReport
	Capability     NativeCapabilityID
	Mode           NativeRuleMode
	Reason         string
	OpenOccurrence int64 // 0 when none is open
	Occurrences    int64
	Contained      bool
	Discrepancies  int64
}

// StartNativeInterpretation evaluates the readiness rules of a policy on the
// facts Desktop reported at Start; they hold until participation is
// confirmed. The registry supplies each
// capability's stability time and reads the reported facts.
func StartNativeInterpretation(
	registry *NativeCapabilityRegistry,
	policy EffectiveExamSecurityPolicy,
	reported []NativeCapabilityReadiness,
	at time.Time,
) (*NativeInterpretation, []NativeRuleTransition, error) {
	if registry == nil || !securityInstant(at) {
		return nil, nil, ErrNativeDeliveryInvalid
	}
	state := &NativeInterpretation{
		capabilities: map[NativeCapabilityID]*nativeCapabilityTrack{},
		rules:        map[string]*nativeRuleTrack{},
		phase:        NativeRuleReadiness,
		at:           at,
	}
	for _, activation := range policy.Activations {
		definition, ok := registry.Capability(activation.Capability)
		if !ok {
			return nil, nil, ErrNativeDeliveryInvalid
		}
		state.capabilities[activation.Capability] = &nativeCapabilityTrack{StabilityMS: definition.Stability.Milliseconds()}
	}
	for _, readiness := range reported {
		track, ok := state.capabilities[readiness.Capability]
		if !ok {
			return nil, nil, ErrNativeDeliveryInvalid
		}
		facts, ok := registry.facts(readiness)
		if !ok {
			return nil, nil, ErrNativeDeliveryInvalid
		}
		track.SourceInstanceID, track.Sequence, track.Facts = readiness.SourceInstanceID, readiness.Sequence, facts
	}
	return state, state.evaluate(policy.Rules), nil
}

// ApplyFact replaces a capability's facts with a newer full state. A fact no
// newer than the latest one of the same source instance changes nothing.
func (s *NativeInterpretation) ApplyFact(rules []NativeRule, fact NativeObservedFact) []NativeRuleTransition {
	track, ok := s.capabilities[fact.Capability]
	if !ok || fact.SourceInstanceID == track.SourceInstanceID && fact.Sequence <= track.Sequence {
		return []NativeRuleTransition{}
	}
	transitions := s.settle(rules, fact.ObservedAt)
	facts := fact.Facts
	track.SourceInstanceID, track.Sequence, track.Facts = fact.SourceInstanceID, fact.Sequence, &facts
	return append(transitions, s.evaluate(rules)...)
}

// ConfirmParticipation starts the participation phase: rules that apply only
// to readiness stop being tracked, and participation rules are evaluated.
// Confirming twice changes nothing.
func (s *NativeInterpretation) ConfirmParticipation(rules []NativeRule, at time.Time) []NativeRuleTransition {
	if s.phase == NativeRuleParticipation {
		return []NativeRuleTransition{}
	}
	transitions := s.settle(rules, at)
	s.phase = NativeRuleParticipation
	for _, rule := range rules {
		if !rule.Phase.AppliesTo(NativeRuleParticipation) {
			delete(s.rules, rule.RuleID)
		}
	}
	return append(transitions, s.evaluate(rules)...)
}

// ApplyGap records that a capability's events were lost: its rules are
// unknown until its next full state.
func (s *NativeInterpretation) ApplyGap(rules []NativeRule, capability NativeCapabilityID, at time.Time) []NativeRuleTransition {
	track, ok := s.capabilities[capability]
	if !ok {
		return []NativeRuleTransition{}
	}
	transitions := s.settle(rules, at)
	if track.Facts != nil {
		track.Facts.InGap = true
	}
	return append(transitions, s.evaluate(rules)...)
}

// Interrupt records that delivery itself lost records, so every capability
// is in a gap until its next full state.
func (s *NativeInterpretation) Interrupt(rules []NativeRule, at time.Time) []NativeRuleTransition {
	transitions := s.settle(rules, at)
	for _, track := range s.capabilities {
		if track.Facts != nil {
			track.Facts.InGap = true
		}
	}
	return append(transitions, s.evaluate(rules)...)
}

// ApplyDesktopStatus records Desktop's own status for a rule and whether it
// contains the candidate window, and counts a disagreement with the server.
func (s *NativeInterpretation) ApplyDesktopStatus(rules []NativeRule, report NativeRuleStatusReport) []NativeRuleTransition {
	transitions := s.settle(rules, report.ObservedAt)
	track, ok := s.rules[report.RuleID]
	if !ok {
		return transitions
	}
	desktop := NativeRuleReport{RuleID: report.RuleID, Status: report.Status, UnknownCause: report.UnknownCause}
	server := track.Tracker.Status()
	if server.Status != desktop.Status || server.Cause != desktop.UnknownCause {
		track.Discrepancies++
	}
	track.Desktop, track.Contained = &desktop, report.Contained
	return transitions
}

// Standings returns each tracked rule's standing, by rule ID.
func (s *NativeInterpretation) Standings() []NativeRuleStanding {
	standings := make([]NativeRuleStanding, 0, len(s.rules))
	for id, track := range s.rules {
		status := track.Tracker.Status()
		standing := NativeRuleStanding{
			Rule:       NativeRuleReport{RuleID: id, Status: status.Status, UnknownCause: status.Cause},
			Capability: track.Capability, Mode: track.Mode, Reason: track.Reason,
			Occurrences: track.Occurrences, Contained: track.Contained, Discrepancies: track.Discrepancies,
		}
		if track.Open {
			standing.OpenOccurrence = track.Occurrences
		}
		standings = append(standings, standing)
	}
	slices.SortFunc(standings, func(a, b NativeRuleStanding) int { return strings.Compare(a.Rule.RuleID, b.Rule.RuleID) })
	return standings
}

func (s *NativeInterpretation) advance(at time.Time) time.Time {
	if at.After(s.at) {
		s.at = at
	}
	return s.at
}

// settle recovers every rule whose facts have stayed met for the stability
// time by at.
func (s *NativeInterpretation) settle(rules []NativeRule, at time.Time) []NativeRuleTransition {
	at = s.advance(at)
	transitions := []NativeRuleTransition{}
	for _, rule := range rules {
		track, ok := s.rules[rule.RuleID]
		if !ok {
			continue
		}
		before := track.Tracker.Status()
		if track.Tracker.Settle(at, s.stability(rule)) {
			transitions = append(transitions, track.transition(rule, &before, at))
		}
	}
	return transitions
}

func (s *NativeInterpretation) evaluate(rules []NativeRule) []NativeRuleTransition {
	transitions := []NativeRuleTransition{}
	for _, rule := range rules {
		if !rule.Phase.AppliesTo(s.phase) {
			continue
		}
		track, ok := s.rules[rule.RuleID]
		var before *NativeRuleResult
		if ok {
			status := track.Tracker.Status()
			before = &status
		} else {
			track = &nativeRuleTrack{Capability: rule.Capability, Mode: rule.Mode, Reason: rule.Reason}
			s.rules[rule.RuleID] = track
		}
		var facts *NativeCapabilityFacts
		if capability, ok := s.capabilities[rule.Capability]; ok {
			facts = capability.Facts
		}
		if track.Tracker.Observe(EvaluateNativeRule(rule, facts), s.at, s.stability(rule)) {
			transitions = append(transitions, track.transition(rule, before, s.at))
		}
	}
	return transitions
}

func (s *NativeInterpretation) stability(rule NativeRule) time.Duration {
	if capability, ok := s.capabilities[rule.Capability]; ok {
		return time.Duration(capability.StabilityMS) * time.Millisecond
	}
	return 0
}

// transition records the tracker's new status and opens or closes the
// rule's occurrence.
func (t *nativeRuleTrack) transition(rule NativeRule, before *NativeRuleResult, at time.Time) NativeRuleTransition {
	after := t.Tracker.Status()
	transition := NativeRuleTransition{
		RuleID: rule.RuleID, Capability: rule.Capability, Mode: rule.Mode, Reason: rule.Reason,
		To: NativeRuleReport{RuleID: rule.RuleID, Status: after.Status, UnknownCause: after.Cause},
		At: at.UTC().Truncate(time.Millisecond),
	}
	if before != nil {
		transition.From = &NativeRuleReport{RuleID: rule.RuleID, Status: before.Status, UnknownCause: before.Cause}
	}
	switch {
	case after.Status == NativeRuleNotMet && !t.Open:
		t.Occurrences++
		t.Open = true
		transition.Occurrence, transition.Event = t.Occurrences, NativeOccurrenceOpened
	case after.Status == NativeRuleMet && t.Open:
		t.Open = false
		transition.Occurrence, transition.Event = t.Occurrences, NativeOccurrenceClosed
	}
	return transition
}

type nativeInterpretationWire struct {
	Capabilities map[NativeCapabilityID]*nativeCapabilityTrack `json:"capabilities"`
	Rules        map[string]*nativeRuleTrack                   `json:"rules"`
	Phase        NativeRulePhase                               `json:"phase"`
	At           time.Time                                     `json:"at"`
}

func (s *NativeInterpretation) MarshalJSON() ([]byte, error) {
	return json.Marshal(nativeInterpretationWire{Capabilities: s.capabilities, Rules: s.rules, Phase: s.phase, At: s.at})
}

func (s *NativeInterpretation) UnmarshalJSON(raw []byte) error {
	var wire nativeInterpretationWire
	if s == nil || len(raw) > NativeInterpretationMaxBytes || json.Unmarshal(raw, &wire) != nil ||
		wire.Capabilities == nil || wire.Rules == nil || !securityInstant(wire.At) ||
		wire.Phase != NativeRuleReadiness && wire.Phase != NativeRuleParticipation ||
		len(wire.Capabilities) > nativeReadinessCapabilityMax || len(wire.Rules) > NativeRuleLimit {
		return ErrNativeDeliveryInvalid
	}
	for id, track := range wire.Capabilities {
		if track == nil || !isNativeIdentifier(string(id)) || track.StabilityMS < 0 ||
			track.StabilityMS > time.Minute.Milliseconds() || !securitySafeInt(track.Sequence) {
			return ErrNativeDeliveryInvalid
		}
	}
	for id, track := range wire.Rules {
		if track == nil || !isNativeRuleID(id) || track.Occurrences < 0 || track.Discrepancies < 0 ||
			!isNativeIdentifier(string(track.Capability)) || !track.Mode.IsValid() ||
			!isNativeReasonID(track.Reason) ||
			track.Open && track.Occurrences == 0 || track.Desktop != nil && !track.Desktop.valid() {
			return ErrNativeDeliveryInvalid
		}
	}
	*s = NativeInterpretation{capabilities: wire.Capabilities, rules: wire.Rules, phase: wire.Phase, at: wire.At}
	return nil
}

type nativeScalarWire struct {
	Type    NativeFieldType `json:"type"`
	Count   int64           `json:"count,omitempty"`
	Enum    string          `json:"enum,omitempty"`
	Boolean bool            `json:"boolean,omitempty"`
}

type nativeRuleMatchWire struct {
	Matched       bool     `json:"matched"`
	CatalogAppIDs []string `json:"catalog_app_ids,omitempty"`
}

type nativeCapabilityFactsWire struct {
	Permission NativeFactPermission           `json:"permission"`
	Health     NativeFactHealth               `json:"health"`
	Certainty  NativeFactCertainty            `json:"certainty"`
	InGap      bool                           `json:"in_gap"`
	Fields     map[string]nativeScalarWire    `json:"fields"`
	Checking   int64                          `json:"checking"`
	Matches    map[string]nativeRuleMatchWire `json:"matches"`
}

// MarshalJSON stores facts read through the registry, so the server can
// evaluate them later without it.
func (f NativeCapabilityFacts) MarshalJSON() ([]byte, error) {
	wire := nativeCapabilityFactsWire{
		Permission: f.Permission, Health: f.Health, Certainty: f.Certainty, InGap: f.InGap,
		Fields: map[string]nativeScalarWire{}, Checking: f.Applications.Checking, Matches: map[string]nativeRuleMatchWire{},
	}
	for name, value := range f.Fields {
		wire.Fields[name] = nativeScalarWire(value)
	}
	for rule, match := range f.Applications.Matches {
		wire.Matches[rule] = nativeRuleMatchWire(match)
	}
	return json.Marshal(wire)
}

func (f *NativeCapabilityFacts) UnmarshalJSON(raw []byte) error {
	var wire nativeCapabilityFactsWire
	if f == nil || json.Unmarshal(raw, &wire) != nil || wire.Fields == nil || wire.Matches == nil ||
		wire.Checking < 0 || len(wire.Fields) > 128 || len(wire.Matches) > NativeRuleLimit {
		return ErrNativeDeliveryInvalid
	}
	value := NativeCapabilityFacts{
		Permission: wire.Permission, Health: wire.Health, Certainty: wire.Certainty, InGap: wire.InGap,
		Fields:       make(map[string]NativeScalar, len(wire.Fields)),
		Applications: NativeApplicationFacts{Checking: wire.Checking, Matches: make(map[string]NativeRuleMatch, len(wire.Matches))},
	}
	for name, scalar := range wire.Fields {
		if !isNativeFieldName(name) ||
			!slices.Contains([]NativeFieldType{NativeFieldCount, NativeFieldEnum, NativeFieldBoolean}, scalar.Type) {
			return ErrNativeDeliveryInvalid
		}
		value.Fields[name] = NativeScalar(scalar)
	}
	for rule, match := range wire.Matches {
		if !isNativeRuleID(rule) {
			return ErrNativeDeliveryInvalid
		}
		value.Applications.Matches[rule] = NativeRuleMatch(match)
	}
	*f = value
	return nil
}
