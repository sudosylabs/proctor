// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package model

import (
	"encoding/json"
	"errors"
	"slices"
	"time"
)

// The types in this file are documents of the desktop native delivery
// agreement. Batches are decoded closed and canonically re-encoded; receipts,
// progress and stream status are persisted as replay outcomes and returned
// verbatim. Their json tags are that one canonical spelling (see the package
// documentation on serialization).

var ErrNativeDeliveryInvalid = errors.New("native delivery is invalid")

// NativeCapabilityFact is one capability's full minimized state, as Desktop
// observed it at ObservedAt, with the envelope around it. Facts are
// change-driven: Desktop sends one whenever the state or its envelope
// changes, and the server evaluates the policy's rules on it again.
type NativeCapabilityFact struct {
	observedAtSpelling string

	Kind       string    `json:"kind"`
	ObservedAt time.Time `json:"observed_at"`
	NativeCapabilityReadiness
}

// NativeCapabilityFactMaxBytes bounds one fact record.
const NativeCapabilityFactMaxBytes = 16 * 1024

func (v NativeCapabilityFact) Validate() error {
	if v.Kind != "capability_fact" || !securityInstant(v.ObservedAt) || !v.valid() ||
		len(v.State)+len(v.Summary) > NativeCapabilityFactMaxBytes {
		return ErrNativeDeliveryInvalid
	}
	return nil
}

// NativeParticipationConfirmed marks where the exam begins on the stream:
// Desktop sends it when the candidate window mounts after admission. Before
// it, readiness rules apply; from it, participation rules do.
type NativeParticipationConfirmed struct {
	observedAtSpelling string

	Kind       string    `json:"kind"`
	ObservedAt time.Time `json:"observed_at"`
}

func (v NativeParticipationConfirmed) Validate() error {
	if v.Kind != "participation_confirmed" || !securityInstant(v.ObservedAt) {
		return ErrNativeDeliveryInvalid
	}
	return nil
}

// NativeRuleStatusReport is Desktop's own status for one rule, and whether
// it is containing the candidate window because of it. The server's status
// is authoritative; a disagreement is counted as a discrepancy.
type NativeRuleStatusReport struct {
	observedAtSpelling string

	Kind         string             `json:"kind"`
	RuleID       string             `json:"rule_id"`
	Status       NativeRuleStatus   `json:"status"`
	UnknownCause NativeUnknownCause `json:"unknown_cause,omitempty"`
	Contained    bool               `json:"contained"`
	ObservedAt   time.Time          `json:"observed_at"`
}

func (v NativeRuleStatusReport) Validate() error {
	if v.Kind != "rule_status" || !securityInstant(v.ObservedAt) ||
		!(NativeRuleReport{RuleID: v.RuleID, Status: v.Status, UnknownCause: v.UnknownCause}).valid() {
		return ErrNativeDeliveryInvalid
	}
	return nil
}

// NativeSourceGap says a capability's source lost events: the positions
// from FirstMissingSequence to LastMissingSequence of one source instance
// will never arrive. Rules on the capability are unknown until its next full
// state.
type NativeSourceGap struct {
	occurredAtSpelling string

	Kind                 string             `json:"kind"`
	Capability           NativeCapabilityID `json:"capability"`
	SourceInstanceID     string             `json:"source_instance_id"`
	FirstMissingSequence int64              `json:"first_missing_sequence"`
	LastMissingSequence  int64              `json:"last_missing_sequence"`
	Reason               string             `json:"reason"`
	OccurredAt           time.Time          `json:"occurred_at"`
}

func (v NativeSourceGap) Validate() error {
	if v.Kind != "source_gap" ||
		!isNativeIdentifier(string(v.Capability)) ||
		!IsValidAgreementID(v.SourceInstanceID) ||
		!securitySafeInt(v.FirstMissingSequence) ||
		!securitySafeInt(v.LastMissingSequence) ||
		v.LastMissingSequence < v.FirstMissingSequence ||
		!slices.Contains([]string{"overflow", "source_loss", "corruption"}, v.Reason) ||
		!securityInstant(v.OccurredAt) {
		return ErrNativeDeliveryInvalid
	}

	return nil
}

// NativeSourceReset says a capability's source restarted under a new
// instance: the previous instance ended at PreviousFinalSequence, and facts
// continue under NewSourceInstanceID. Rules on the capability are unknown
// until the new instance's first full state.
type NativeSourceReset struct {
	occurredAtSpelling string

	Kind                     string             `json:"kind"`
	ResetID                  string             `json:"reset_id"`
	Capability               NativeCapabilityID `json:"capability"`
	PreviousSourceInstanceID string             `json:"previous_source_instance_id"`
	PreviousFinalSequence    int64              `json:"previous_final_sequence"`
	NewSourceInstanceID      string             `json:"new_source_instance_id"`
	Reason                   string             `json:"reason"`
	OccurredAt               time.Time          `json:"occurred_at"`
}

func (r NativeSourceReset) Validate() error {
	if r.Kind != "source_reset" ||
		!IsValidAgreementID(r.ResetID) ||
		!isNativeIdentifier(string(r.Capability)) ||
		!IsValidAgreementID(r.PreviousSourceInstanceID) ||
		!IsValidAgreementID(r.NewSourceInstanceID) ||
		r.PreviousSourceInstanceID == r.NewSourceInstanceID ||
		!securitySafeInt(r.PreviousFinalSequence) ||
		!slices.Contains([]string{"restart", "resume", "permission_changed"}, r.Reason) ||
		!securityInstant(r.OccurredAt) {
		return ErrNativeDeliveryInvalid
	}
	return nil
}

// NativeRecord is a closed union. It never retains an arbitrary raw detail bag.
type NativeRecord struct {
	Fact       *NativeCapabilityFact
	Confirmed  *NativeParticipationConfirmed
	RuleStatus *NativeRuleStatusReport
	Reset      *NativeSourceReset
	Gap        *NativeSourceGap
}

func (r NativeRecord) value() (any, error) {
	var value any
	count := 0
	for _, candidate := range []interface{ Validate() error }{r.Fact, r.Confirmed, r.RuleStatus, r.Reset, r.Gap} {
		// Typed nil pointers cannot be tested through the interface itself.
		switch v := candidate.(type) {
		case *NativeCapabilityFact:
			if v == nil {
				continue
			}
		case *NativeParticipationConfirmed:
			if v == nil {
				continue
			}
		case *NativeRuleStatusReport:
			if v == nil {
				continue
			}
		case *NativeSourceReset:
			if v == nil {
				continue
			}
		case *NativeSourceGap:
			if v == nil {
				continue
			}
		}

		if candidate.Validate() != nil {
			return nil, ErrNativeDeliveryInvalid
		}

		value, count = candidate, count+1
	}

	if count != 1 {
		return nil, ErrNativeDeliveryInvalid
	}

	return value, nil
}
func (r NativeRecord) Validate() error { _, err := r.value(); return err }
func (r NativeRecord) MarshalJSON() ([]byte, error) {
	v, err := r.value()
	if err != nil {
		return nil, err
	}
	return json.Marshal(v)
}
func (r *NativeRecord) UnmarshalJSON(raw []byte) error {
	if r == nil || len(raw) > 256*1024 || validateExamDocumentJSON(raw) != nil {
		return ErrNativeDeliveryInvalid
	}

	var kind struct {
		Kind string `json:"kind"`
	}
	if json.Unmarshal(raw, &kind) != nil {
		return ErrNativeDeliveryInvalid
	}

	var value NativeRecord
	var target any
	switch kind.Kind {
	case "capability_fact":
		value.Fact = new(NativeCapabilityFact)
		target = value.Fact
	case "participation_confirmed":
		value.Confirmed = new(NativeParticipationConfirmed)
		target = value.Confirmed
	case "rule_status":
		value.RuleStatus = new(NativeRuleStatusReport)
		target = value.RuleStatus
	case "source_reset":
		value.Reset = new(NativeSourceReset)
		target = value.Reset
	case "source_gap":
		value.Gap = new(NativeSourceGap)
		target = value.Gap
	default:
		return ErrNativeDeliveryInvalid
	}

	if decodeClosedDeliveryDeclaration(raw, target, 256*1024) != nil || value.Validate() != nil {
		return ErrNativeDeliveryInvalid
	}

	*r = value
	return nil
}

type NativeSecurityBatch struct {
	StreamID             string                 `json:"stream_id"`
	BatchSequence        int64                  `json:"batch_sequence"`
	PriorAcknowledgement int64                  `json:"prior_acknowledgement"`
	ParticipationID      AttemptParticipationID `json:"participation_id"`
	Generation           int64                  `json:"generation"`
	SecuritySessionID    string                 `json:"security_session_id"`
	PolicyDigest         string                 `json:"policy_digest"`
	ApplicationReleaseID string                 `json:"application_release_id"`
	MatrixID             string                 `json:"matrix_id"`
	Records              []NativeRecord         `json:"records"`
}

func (b NativeSecurityBatch) Validate() error {
	if !IsValidAgreementID(b.StreamID) ||
		b.BatchSequence <= 0 ||
		!securitySafeInt(b.BatchSequence) ||
		!securitySafeInt(b.PriorAcknowledgement) ||
		!b.ParticipationID.IsValid() ||
		b.Generation <= 0 ||
		!securitySafeInt(b.Generation) ||
		!IsValidAgreementID(b.SecuritySessionID) ||
		!IsValidSHA256Fingerprint(b.PolicyDigest) ||
		!IsValidAgreementID(b.ApplicationReleaseID) ||
		!IsValidAgreementID(b.MatrixID) ||
		len(b.Records) == 0 ||
		len(b.Records) > 64 {
		return ErrNativeDeliveryInvalid
	}

	for _, r := range b.Records {
		if r.Validate() != nil {
			return ErrNativeDeliveryInvalid
		}
	}

	raw, err := encodeCanonicalExamDocument(b)
	if err != nil || len(raw) > 256*1024 {
		return ErrNativeDeliveryInvalid
	}

	return nil
}
func (b NativeSecurityBatch) Canonical() ([]byte, error) {
	if b.Validate() != nil {
		return nil, ErrNativeDeliveryInvalid
	}
	return encodeCanonicalExamDocument(b)
}
func (b *NativeSecurityBatch) UnmarshalJSON(raw []byte) error {
	if b == nil {
		return ErrNativeDeliveryInvalid
	}
	type wire NativeSecurityBatch
	var value wire
	if decodeClosedDeliveryDeclaration(raw, &value, 256*1024) != nil {
		return ErrNativeDeliveryInvalid
	}
	candidate := NativeSecurityBatch(value)
	if candidate.Validate() != nil {
		return ErrNativeDeliveryInvalid
	}
	*b = candidate
	return nil
}

type NativeBatchReceipt struct {
	StreamID      string    `json:"stream_id"`
	BatchSequence int64     `json:"batch_sequence"`
	RequestDigest string    `json:"request_digest"`
	ReceivedAt    time.Time `json:"received_at"`
}

func (r NativeBatchReceipt) Validate() error {
	if !IsValidAgreementID(r.StreamID) ||
		r.BatchSequence < 1 ||
		!securitySafeInt(r.BatchSequence) ||
		!IsValidSHA256Fingerprint(r.RequestDigest) ||
		!securityInstant(r.ReceivedAt) {
		return ErrNativeDeliveryInvalid
	}

	return nil
}

type NativeDeliveryProgress struct {
	HighestContiguousBatchSequence int64           `json:"highest_contiguous_batch_sequence"`
	SettledThroughBatchSequence    int64           `json:"settled_through_batch_sequence"`
	HighestSeenBatchSequence       int64           `json:"highest_seen_batch_sequence"`
	MissingBatchRanges             []SequenceRange `json:"missing_batch_ranges"`
	MissingRangesTruncated         bool            `json:"missing_ranges_truncated"`
	ServerTime                     time.Time       `json:"server_time"`
}
type NativeSecurityAcknowledgement struct {
	Receipt NativeBatchReceipt
	NativeDeliveryProgress
}

type NativeSecurityStreamStatus struct {
	NativeDeliveryProgress
	StreamID                       string                     `json:"stream_id"`
	AllocatedThroughSequence       int64                      `json:"allocated_through_sequence"`
	TerminalMissingThroughSequence int64                      `json:"terminal_missing_through_sequence"`
	Closure                        DeliveryClosure            `json:"closure"`
	DeclarationRevision            int64                      `json:"declaration_revision"`
	DetailMode                     string                     `json:"detail_mode"`
	BudgetScope                    *string                    `json:"budget_scope"`
	SummaryOnlyReason              *DeliveryStopReason        `json:"summary_only_reason"`
	Summary                        *UnretainedDeliverySummary `json:"summary"`
}

func (s NativeSecurityStreamStatus) Validate() error {
	if !IsValidAgreementID(s.StreamID) ||
		!securitySafeInt(s.AllocatedThroughSequence) ||
		!securitySafeInt(s.TerminalMissingThroughSequence) ||
		s.TerminalMissingThroughSequence > s.AllocatedThroughSequence ||
		s.Closure.Validate(true) != nil ||
		!securitySafeInt(s.DeclarationRevision) ||
		!securityInstant(s.ServerTime) ||
		!securitySafeInt(s.HighestContiguousBatchSequence) ||
		!securitySafeInt(s.SettledThroughBatchSequence) ||
		!securitySafeInt(s.HighestSeenBatchSequence) ||
		s.HighestContiguousBatchSequence > s.SettledThroughBatchSequence ||
		s.HighestContiguousBatchSequence > s.HighestSeenBatchSequence ||
		s.HighestSeenBatchSequence > s.AllocatedThroughSequence ||
		s.SettledThroughBatchSequence > s.AllocatedThroughSequence ||
		ValidateSequenceRanges(s.MissingBatchRanges, 32) != nil {
		return ErrNativeDeliveryInvalid
	}

	if len(s.MissingBatchRanges) > 0 &&
		(s.MissingBatchRanges[0].First <= s.SettledThroughBatchSequence ||
			s.MissingBatchRanges[len(s.MissingBatchRanges)-1].Last > s.AllocatedThroughSequence) {
		return ErrNativeDeliveryInvalid
	}

	switch s.DetailMode {
	case "collecting":
		if s.BudgetScope != nil || s.SummaryOnlyReason != nil {
			return ErrNativeDeliveryInvalid
		}
	case "summary_only":
		if s.BudgetScope == nil ||
			!slices.Contains([]string{"participation", "attempt"}, *s.BudgetScope) ||
			s.SummaryOnlyReason == nil ||
			!slices.Contains(
				[]DeliveryStopReason{
					DeliveryStopRecords,
					DeliveryStopBytes,
					DeliveryStopPositions,
					DeliveryStopMetadata,
					DeliveryStopLocalLossInventory,
				},
				*s.SummaryOnlyReason,
			) {
			return ErrNativeDeliveryInvalid
		}
	default:
		return ErrNativeDeliveryInvalid
	}

	if s.Summary != nil && s.Summary.Validate() != nil {
		return ErrNativeDeliveryInvalid
	}

	raw, err := encodeCanonicalExamDocument(s)
	if err != nil || len(raw) > 16*1024 {
		return ErrNativeDeliveryInvalid
	}

	return nil
}

// ValidateNativeRecordBinding checks a record against the admitted policy:
// facts, gaps and resets name an activated capability, and a rule status
// names one of its participation rules.
func ValidateNativeRecordBinding(record NativeRecord, resolved ResolvedNativePolicy) error {
	if record.Validate() != nil {
		return ErrNativeDeliveryInvalid
	}
	activated := func(capability NativeCapabilityID) error {
		if !slices.ContainsFunc(resolved.Policy.Activations, func(a NativeActivation) bool { return a.Capability == capability }) {
			return ErrNativeDeliveryInvalid
		}
		return nil
	}
	switch {
	case record.Fact != nil:
		return activated(record.Fact.Capability)
	case record.Gap != nil:
		return activated(record.Gap.Capability)
	case record.Reset != nil:
		return activated(record.Reset.Capability)
	case record.RuleStatus != nil:
		status := record.RuleStatus
		if !slices.ContainsFunc(resolved.Policy.Rules, func(r NativeRule) bool {
			return r.RuleID == status.RuleID && r.Phase.AppliesTo(NativeRuleParticipation)
		}) {
			return ErrNativeDeliveryInvalid
		}
	}
	return nil
}

func (v NativeCapabilityFact) MarshalJSON() ([]byte, error) {
	type wire struct {
		Kind       string              `json:"kind"`
		ObservedAt securityJSONInstant `json:"observed_at"`
		NativeCapabilityReadiness
	}
	return json.Marshal(wire{
		Kind: v.Kind, ObservedAt: securityJSONInstant{Time: v.ObservedAt, spelling: v.observedAtSpelling},
		NativeCapabilityReadiness: v.NativeCapabilityReadiness,
	})
}
func (v *NativeCapabilityFact) UnmarshalJSON(raw []byte) error {
	if v == nil {
		return ErrNativeDeliveryInvalid
	}
	var value struct {
		Kind       string              `json:"kind"`
		ObservedAt securityJSONInstant `json:"observed_at"`
		NativeCapabilityReadiness
	}
	if decodeClosedDeliveryDeclaration(raw, &value, 256*1024) != nil {
		return ErrNativeDeliveryInvalid
	}
	candidate := NativeCapabilityFact{
		observedAtSpelling: value.ObservedAt.spelling, Kind: value.Kind, ObservedAt: value.ObservedAt.Time,
		NativeCapabilityReadiness: value.NativeCapabilityReadiness,
	}
	if candidate.Validate() != nil {
		return ErrNativeDeliveryInvalid
	}
	*v = candidate
	return nil
}

func (v NativeParticipationConfirmed) MarshalJSON() ([]byte, error) {
	type wire NativeParticipationConfirmed
	return json.Marshal(struct {
		*wire
		ObservedAt securityJSONInstant `json:"observed_at"`
	}{wire: (*wire)(&v), ObservedAt: securityJSONInstant{Time: v.ObservedAt, spelling: v.observedAtSpelling}})
}
func (v *NativeParticipationConfirmed) UnmarshalJSON(raw []byte) error {
	if v == nil {
		return ErrNativeDeliveryInvalid
	}
	type wire NativeParticipationConfirmed
	var decoded wire
	value := struct {
		*wire
		ObservedAt securityJSONInstant `json:"observed_at"`
	}{wire: &decoded}
	if decodeClosedDeliveryDeclaration(raw, &value, 256*1024) != nil {
		return ErrNativeDeliveryInvalid
	}
	candidate := NativeParticipationConfirmed(decoded)
	candidate.ObservedAt = value.ObservedAt.Time
	candidate.observedAtSpelling = value.ObservedAt.spelling
	if candidate.Validate() != nil {
		return ErrNativeDeliveryInvalid
	}
	*v = candidate
	return nil
}

func (v NativeRuleStatusReport) MarshalJSON() ([]byte, error) {
	type wire NativeRuleStatusReport
	return json.Marshal(struct {
		*wire
		ObservedAt securityJSONInstant `json:"observed_at"`
	}{wire: (*wire)(&v), ObservedAt: securityJSONInstant{Time: v.ObservedAt, spelling: v.observedAtSpelling}})
}
func (v *NativeRuleStatusReport) UnmarshalJSON(raw []byte) error {
	if v == nil {
		return ErrNativeDeliveryInvalid
	}
	type wire NativeRuleStatusReport
	var decoded wire
	value := struct {
		*wire
		ObservedAt securityJSONInstant `json:"observed_at"`
	}{wire: &decoded}
	if decodeClosedDeliveryDeclaration(raw, &value, 256*1024) != nil {
		return ErrNativeDeliveryInvalid
	}
	candidate := NativeRuleStatusReport(decoded)
	candidate.ObservedAt = value.ObservedAt.Time
	candidate.observedAtSpelling = value.ObservedAt.spelling
	if candidate.Validate() != nil {
		return ErrNativeDeliveryInvalid
	}
	*v = candidate
	return nil
}

func (v NativeSourceGap) MarshalJSON() ([]byte, error) {
	type wire NativeSourceGap
	return json.Marshal(struct {
		*wire
		OccurredAt securityJSONInstant `json:"occurred_at"`
	}{wire: (*wire)(&v), OccurredAt: securityJSONInstant{Time: v.OccurredAt, spelling: v.occurredAtSpelling}})
}
func (v *NativeSourceGap) UnmarshalJSON(raw []byte) error {
	if v == nil {
		return ErrNativeDeliveryInvalid
	}
	type wire NativeSourceGap
	var decoded wire
	value := struct {
		*wire
		OccurredAt securityJSONInstant `json:"occurred_at"`
	}{wire: &decoded}
	if decodeClosedDeliveryDeclaration(raw, &value, 256*1024) != nil {
		return ErrNativeDeliveryInvalid
	}
	candidate := NativeSourceGap(decoded)
	candidate.OccurredAt = value.OccurredAt.Time
	candidate.occurredAtSpelling = value.OccurredAt.spelling
	if candidate.Validate() != nil {
		return ErrNativeDeliveryInvalid
	}
	*v = candidate
	return nil
}

func (r NativeSourceReset) MarshalJSON() ([]byte, error) {
	type wire NativeSourceReset
	return json.Marshal(struct {
		*wire
		OccurredAt securityJSONInstant `json:"occurred_at"`
	}{wire: (*wire)(&r), OccurredAt: securityJSONInstant{Time: r.OccurredAt, spelling: r.occurredAtSpelling}})
}
func (r *NativeSourceReset) UnmarshalJSON(raw []byte) error {
	if r == nil {
		return ErrNativeDeliveryInvalid
	}
	type wire NativeSourceReset
	var decoded wire
	value := struct {
		*wire
		OccurredAt securityJSONInstant `json:"occurred_at"`
	}{wire: &decoded}
	if decodeClosedDeliveryDeclaration(raw, &value, 256*1024) != nil {
		return ErrNativeDeliveryInvalid
	}
	candidate := NativeSourceReset(decoded)
	candidate.OccurredAt = value.OccurredAt.Time
	candidate.occurredAtSpelling = value.OccurredAt.spelling
	if candidate.Validate() != nil {
		return ErrNativeDeliveryInvalid
	}
	*r = candidate
	return nil
}
