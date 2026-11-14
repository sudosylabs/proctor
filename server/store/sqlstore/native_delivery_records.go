// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package sqlstore

import (
	"context"
	"encoding/json"
	"time"

	"github.com/sudosylabs/proctor/server/internal/canonicaljson"
	"github.com/sudosylabs/proctor/server/model"
	"github.com/sudosylabs/proctor/server/store"
)

// Row metadata is canonical counted application data, including the
// registry-read fact. SQL/index overhead is not substituted for it.
type nativeRecordMetadata struct {
	BatchSequence int64 `json:"batch_sequence"`
	RecordIndex   int   `json:"record_index"`
	// Fact is a fact record read through the release registry at append, so
	// interpretation never needs the registry.
	Fact *model.NativeObservedFact `json:"fact,omitempty"`
}
type nativeStoredRecord struct {
	Sequence int64  `db:"batch_sequence"`
	Index    int    `db:"record_index"`
	Kind     string `db:"kind"`
	Raw      []byte `db:"record_canonical"`
	Metadata []byte `db:"metadata_canonical"`
}

// encodeNativeRecord canonicalizes one record, reading a fact through the
// release registry.
func encodeNativeRecord(
	record model.NativeRecord,
	sequence int64,
	index int,
	registry *model.NativeCapabilityRegistry,
) (nativeStoredRecord, error) {
	row := nativeStoredRecord{Sequence: sequence, Index: index}
	meta := nativeRecordMetadata{BatchSequence: sequence, RecordIndex: index}
	switch {
	case record.Fact != nil:
		row.Kind = "capability_fact"
		fact, ok := registry.ObserveFact(*record.Fact)
		if !ok {
			return row, model.ErrNativeDeliveryInvalid
		}
		meta.Fact = &fact
	case record.Confirmed != nil:
		row.Kind = "participation_confirmed"
	case record.RuleStatus != nil:
		row.Kind = "rule_status"
	case record.Reset != nil:
		row.Kind = "source_reset"
	case record.Gap != nil:
		row.Kind = "source_gap"
	default:
		return row, model.ErrNativeDeliveryInvalid
	}

	var err error
	row.Raw, err = canonicalPreflightValue(record)
	if err != nil {
		return row, err
	}

	row.Metadata, err = canonicalPreflightValue(meta)
	if err != nil {
		return row, err
	}
	if len(row.Raw) > 256<<10 || len(row.Metadata) > 64<<10 {
		return row, model.ErrNativeDeliveryInvalid
	}

	return row, nil
}

// validateNativeBatchRecords checks each record of a batch against the
// admitted policy: facts, gaps and resets name an activated capability, and a
// rule status names a participation rule.
func validateNativeBatchRecords(resolved model.ResolvedNativePolicy, records []model.NativeRecord) error {
	for _, record := range records {
		if err := model.ValidateNativeRecordBinding(record, resolved); err != nil {
			return err
		}
	}
	return nil
}

// Interpret only records in the settled prefix, in stream order: each record
// advances the server's evaluation of the policy's native rules, and every
// rule status change is retained as condition evidence. A position that will
// never arrive puts every capability in a gap until its next full state.
// It returns the native Flags the interpreted records raised.
func advanceNativeInterpretation(
	ctx context.Context,
	tx *sqlxTxWrapper,
	owner nativeDeliveryOwner,
) (nativeInterpretationEffects, error) {
	received, gaps, err := nativeDeliveryRanges(ctx, tx, owner)
	if err != nil {
		return nativeInterpretationEffects{}, err
	}

	progress, err := model.ResolveDeliveryProgress(owner.Allocated, received, gaps, owner.TerminalThrough)
	if err != nil {
		return nativeInterpretationEffects{}, invalidPersistedState("delivery", "progress", err)
	}

	var rows []nativeStoredRecord
	if selectErr := tx.Select(
		ctx,
		&rows,
		`SELECT r.batch_sequence, r.record_index, r.kind, r.record_canonical, r.metadata_canonical
		FROM exam_native_delivery_records r
		JOIN exam_native_delivery_batches b USING(participation_id, batch_sequence)
		WHERE r.participation_id = ?
			AND NOT b.processed
			AND r.batch_sequence <= ?
		ORDER BY r.batch_sequence, r.record_index`,
		owner.ParticipationID,
		progress.SettledThrough,
	); selectErr != nil {
		return nativeInterpretationEffects{}, selectErr
	}

	effects, err := interpretNativeRecords(ctx, tx, owner, rows, gaps)
	if err != nil {
		return nativeInterpretationEffects{}, err
	}

	var released int64
	if getErr := tx.Get(
		ctx,
		&released,
		`SELECT COALESCE(SUM(counted_bytes), 0)
		FROM exam_native_delivery_batches
		WHERE participation_id = ?
			AND NOT processed
			AND batch_sequence <= ?`,
		owner.ParticipationID,
		progress.SettledThrough,
	); getErr != nil {
		return nativeInterpretationEffects{}, getErr
	}

	if _, execErr := tx.Exec(
		ctx,
		`UPDATE exam_native_delivery_batches
		SET processed = true
		WHERE participation_id = ?
			AND batch_sequence <= ?
			AND NOT processed`,
		owner.ParticipationID,
		progress.SettledThrough,
	); execErr != nil {
		return nativeInterpretationEffects{}, execErr
	}

	if _, execErr := tx.Exec(
		ctx,
		`UPDATE exam_attempt_delivery_budgets
		SET native_pending_bytes = native_pending_bytes-?
		WHERE exam_attempt_id = (
			SELECT exam_attempt_id
			FROM exam_attempt_security_owners
			WHERE participation_id = ?
		)`,
		released,
		owner.ParticipationID,
	); execErr != nil {
		return nativeInterpretationEffects{}, execErr
	}

	_, err = tx.Exec(
		ctx,
		`UPDATE exam_attempt_security_owners
		SET acknowledged_through_sequence = ?, interpreted_through_sequence = ?
		WHERE participation_id = ?`,
		progress.HighestContiguous,
		progress.SettledThrough,
		owner.ParticipationID,
	)
	if err != nil {
		return nativeInterpretationEffects{}, err
	}
	return effects, nil
}

// interpretNativeRecords advances the stored interpretation over rows. A
// Participation whose interpretation was retired, or whose integrity record
// is retired, keeps its delivery bookkeeping only.
func interpretNativeRecords(
	ctx context.Context,
	tx *sqlxTxWrapper,
	owner nativeDeliveryOwner,
	rows []nativeStoredRecord,
	gaps []model.SequenceRange,
) (nativeInterpretationEffects, error) {
	var stored []byte
	if err := tx.Get(
		ctx,
		&stored,
		`SELECT native_interpretation_canonical FROM exam_attempt_security_owners WHERE participation_id = ?`,
		owner.ParticipationID,
	); err != nil {
		return nativeInterpretationEffects{}, err
	}
	if len(stored) == 0 || len(rows) == 0 {
		return nativeInterpretationEffects{}, nil
	}

	var binding admittedSecurityBinding
	if json.Unmarshal(owner.Binding, &binding) != nil || binding.Security.Validate() != nil {
		return nativeInterpretationEffects{}, invalidPersistedState("native_interpretation", "binding", model.ErrNativeDeliveryInvalid)
	}
	var state model.NativeInterpretation
	if json.Unmarshal(stored, &state) != nil {
		return nativeInterpretationEffects{}, invalidPersistedState("native_interpretation", "state", model.ErrNativeDeliveryInvalid)
	}
	rules := binding.Security.Policy.Rules
	disagreedBefore := nativeDiscrepancies(&state)

	flags := nativeFlagRaiser{owner: owner, binding: binding}
	interrupted := int64(0)
	for _, row := range rows {
		var record model.NativeRecord
		var meta nativeRecordMetadata
		if json.Unmarshal(row.Raw, &record) != nil || json.Unmarshal(row.Metadata, &meta) != nil {
			return nativeInterpretationEffects{}, invalidPersistedState("native_delivery", "record", model.ErrNativeDeliveryInvalid)
		}

		transitions := []model.NativeRuleTransition{}
		// A batch position before this one that will never arrive lost records.
		if lost := nativeLostBefore(owner, gaps, row.Sequence); lost > interrupted {
			interrupted = lost
			transitions = append(transitions, state.Interrupt(rules, nativeRecordTime(record, owner.Now))...)
		}
		switch {
		case record.Fact != nil:
			if meta.Fact == nil {
				return nativeInterpretationEffects{}, invalidPersistedState("native_delivery", "record", model.ErrNativeDeliveryInvalid)
			}
			transitions = append(transitions, state.ApplyFact(rules, *meta.Fact)...)
		case record.Confirmed != nil:
			transitions = append(transitions, state.ConfirmParticipation(rules, record.Confirmed.ObservedAt)...)
		case record.RuleStatus != nil:
			transitions = append(transitions, state.ApplyDesktopStatus(rules, *record.RuleStatus)...)
		case record.Gap != nil:
			transitions = append(transitions, state.ApplyGap(rules, record.Gap.Capability, record.Gap.OccurredAt)...)
		case record.Reset != nil:
			// Facts continue under the new instance; until its first full
			// state the capability's rules are unknown.
			transitions = append(transitions, state.ApplyGap(rules, record.Reset.Capability, record.Reset.OccurredAt)...)
		}
		if err := retainNativeTransitions(ctx, tx, owner, binding, row.Sequence, row.Index, transitions); err != nil {
			return nativeInterpretationEffects{}, err
		}
		if err := flags.raise(ctx, tx, transitions); err != nil {
			return nativeInterpretationEffects{}, err
		}
	}

	raw, err := json.Marshal(&state)
	if err == nil {
		raw, err = canonicaljson.Canonicalize(raw, model.NativeInterpretationMaxBytes)
	}
	if err != nil {
		return nativeInterpretationEffects{}, err
	}
	if _, err = tx.Exec(
		ctx,
		`UPDATE exam_attempt_security_owners SET native_interpretation_canonical = ? WHERE participation_id = ?`,
		raw,
		owner.ParticipationID,
	); err != nil {
		return nativeInterpretationEffects{}, err
	}
	return nativeInterpretationEffects{
		Flagged:             flags.flagged,
		StatusDiscrepancies: nativeDiscrepancies(&state) - disagreedBefore,
	}, nil
}

// nativeInterpretationEffects is what one interpretation pass did that the
// application acts on: the Flags it raised, and how often Desktop's own rule
// status disagreed with the server's.
type nativeInterpretationEffects struct {
	Flagged             []store.NativeIntegrityFlagged
	StatusDiscrepancies int64
}

func nativeDiscrepancies(state *model.NativeInterpretation) int64 {
	var total int64
	for _, standing := range state.Standings() {
		total += standing.Discrepancies
	}
	return total
}

// nativeLostBefore returns the latest batch position before sequence that
// will never arrive, or 0.
func nativeLostBefore(owner nativeDeliveryOwner, gaps []model.SequenceRange, sequence int64) int64 {
	lost := int64(0)
	if owner.TerminalThrough > 0 && sequence > 1 {
		lost = min(sequence-1, owner.TerminalThrough)
	}
	for _, gap := range gaps {
		if gap.First < sequence {
			lost = max(lost, min(gap.Last, sequence-1))
		}
	}
	return lost
}

// nativeRecordTime is the device time a record was observed at, or now for a
// record that carries none.
func nativeRecordTime(record model.NativeRecord, now time.Time) time.Time {
	switch {
	case record.Fact != nil:
		return record.Fact.ObservedAt
	case record.Confirmed != nil:
		return record.Confirmed.ObservedAt
	case record.RuleStatus != nil:
		return record.RuleStatus.ObservedAt
	case record.Gap != nil:
		return record.Gap.OccurredAt
	case record.Reset != nil:
		return record.Reset.OccurredAt
	}
	return now.UTC().Truncate(time.Millisecond)
}
