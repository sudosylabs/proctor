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
	"slices"
	"time"

	"github.com/sudosylabs/proctor/server/internal/canonicaljson"
	"github.com/sudosylabs/proctor/server/model"
	"github.com/sudosylabs/proctor/server/store"
)

// Row metadata is canonical counted application data, including the
// registry-read fact and source-watermark projection. SQL/index overhead is not
// substituted for it. Projection changes use already charged bounded slots.
type nativeRecordMetadata struct {
	BatchSequence int64                     `json:"batch_sequence"`
	RecordIndex   int                       `json:"record_index"`
	SourceRanges  []model.NativeSourceRange `json:"source_ranges"`
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

// encodeNativeRecord canonicalizes one record. A fact is read through
// registry; with no registry, only the record's source ranges are needed.
func encodeNativeRecord(
	record model.NativeRecord,
	sequence int64,
	index int,
	registry *model.NativeCapabilityRegistry,
) (nativeStoredRecord, error) {
	row := nativeStoredRecord{Sequence: sequence, Index: index}
	meta := nativeRecordMetadata{BatchSequence: sequence, RecordIndex: index, SourceRanges: []model.NativeSourceRange{}}
	switch {
	case record.Fact != nil:
		row.Kind = "capability_fact"
		if registry != nil {
			fact, ok := registry.ObserveFact(*record.Fact)
			if !ok {
				return row, model.ErrNativeDeliveryInvalid
			}
			meta.Fact = &fact
		}
	case record.Confirmed != nil:
		row.Kind = "participation_confirmed"
	case record.RuleStatus != nil:
		row.Kind = "rule_status"
	case record.Coverage != nil:
		row.Kind = "coverage_transition"
		s := record.Coverage.Source
		meta.SourceRanges = []model.NativeSourceRange{{
			SourceID:         s.SourceID,
			SourceInstanceID: s.SourceInstanceID,
			FirstSequence:    s.Sequence,
			LastSequence:     s.Sequence,
		}}
	case record.Reset != nil:
		row.Kind = "source_reset"
	case record.Gap != nil:
		row.Kind = "source_gap"
		g := record.Gap
		meta.SourceRanges = []model.NativeSourceRange{{
			SourceID:         g.SourceID,
			SourceInstanceID: g.SourceInstanceID,
			FirstSequence:    g.FirstMissingSequence,
			LastSequence:     g.LastMissingSequence,
		}}
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

func validateNativeBatchSources(
	ctx context.Context,
	tx *sqlxTxWrapper,
	owner nativeDeliveryOwner,
	resolved model.ResolvedNativePolicy,
	agreement *model.DesktopNativeAgreement,
	records []model.NativeRecord,
) error {
	var raw []byte
	if err := tx.Get(
		ctx,
		&raw,
		`SELECT latest_report_canonical
		FROM exam_attempt_security_owners
		WHERE participation_id = ?`,
		owner.ParticipationID,
	); err != nil {
		return err
	}
	snapshot, err := decodeNativeCoverageSnapshot(raw)
	if err != nil {
		return err
	}
	var historyRaw [][]byte
	if err = tx.Select(
		ctx,
		&historyRaw,
		`SELECT reset_canonical FROM exam_native_source_resets WHERE participation_id = ?`,
		owner.ParticipationID,
	); err != nil {
		return err
	}
	history, lifetimes, err := nativeSourceLifetimes(owner, snapshot, historyRaw)
	if err != nil {
		return err
	}
	pendingResets := []nativeResetRecord{}
	for _, record := range records {
		if err = model.ValidateNativeRecordBinding(record, resolved, agreement, lifetimes); err != nil {
			return err
		}
		if record.Reset == nil {
			continue
		}
		edge := *record.Reset
		pending, previous, admitErr := admitNativeBatchReset(ctx, tx, owner, snapshot, records, edge, history, lifetimes)
		if admitErr != nil {
			return admitErr
		}
		if pending == nil {
			continue
		}
		pendingResets = append(pendingResets, *pending)
		final := edge.PreviousFinalSequence
		lifetimes[previous].FinalSequence = &final
		lifetimes = append(lifetimes, model.NativeSourceLifetime{SourceID: edge.SourceID, InstanceID: edge.NewSourceInstanceID})
		history = append(history, edge)
	}
	if len(pendingResets) > 0 {
		if err = storeNativeBatchResets(ctx, tx, owner, pendingResets); err != nil {
			return err
		}
	}

	return nil
}

// nativeSourceLifetimes decodes the retained reset history and derives every
// known source lifetime with the final sequence of each retired predecessor.
func nativeSourceLifetimes(
	owner nativeDeliveryOwner,
	snapshot nativeCoverageSnapshot,
	historyRaw [][]byte,
) ([]model.NativeSourceReset, []model.NativeSourceLifetime, error) {
	history := make([]model.NativeSourceReset, 0, len(historyRaw))
	lifetimes := []model.NativeSourceLifetime{}
	for _, head := range snapshot.Sources {
		lifetimes = append(lifetimes, model.NativeSourceLifetime{SourceID: head.SourceID, InstanceID: head.SourceInstanceID})
	}

	// First collect all instances, then retire predecessors. SQL ordering is not
	// an event ordering and must not resurrect an intermediate lifetime.
	for _, raw := range historyRaw {
		record, err := decodeNativeResetRecord(raw, model.AttemptParticipationID(owner.ParticipationID), "")
		if err != nil {
			return nil, nil, err
		}

		history = append(history, record.Reset)
		for _, instance := range []string{record.Reset.PreviousSourceInstanceID, record.Reset.NewSourceInstanceID} {
			if !slices.ContainsFunc(lifetimes, func(l model.NativeSourceLifetime) bool {
				return l.SourceID == record.Reset.SourceID && l.InstanceID == instance
			}) {
				lifetimes = append(lifetimes, model.NativeSourceLifetime{SourceID: record.Reset.SourceID, InstanceID: instance})
			}
		}
	}

	for _, edge := range history {
		for i := range lifetimes {
			if lifetimes[i].SourceID == edge.SourceID && lifetimes[i].InstanceID == edge.PreviousSourceInstanceID {
				final := edge.PreviousFinalSequence
				lifetimes[i].FinalSequence = &final
			}
		}
	}

	return history, lifetimes, nil
}

// admitNativeBatchReset checks one reset record in a batch, returning nil for
// a reset already retained or the pending record and its predecessor lifetime
// index for a new historical reset.
func admitNativeBatchReset(
	ctx context.Context,
	tx *sqlxTxWrapper,
	owner nativeDeliveryOwner,
	snapshot nativeCoverageSnapshot,
	records []model.NativeRecord,
	edge model.NativeSourceReset,
	history []model.NativeSourceReset,
	lifetimes []model.NativeSourceLifetime,
) (*nativeResetRecord, int, error) {
	canonical, err := edge.Canonical()
	if err != nil {
		return nil, 0, err
	}

	digest := model.SHA256Fingerprint(canonical)
	for _, prior := range history {
		if prior.ResetID == edge.ResetID {
			raw, priorErr := prior.Canonical()
			if priorErr != nil {
				return nil, 0, priorErr
			}
			if model.SHA256Fingerprint(raw) != digest {
				return nil, 0, nativeAppendConflict("batch_conflict")
			}

			return nil, 0, nil
		}
	}

	// Live edges must have been accepted by the control owner. Historical edges
	// establish evidence continuity only; they never update live heads or gates.
	if owner.Closure.ClosedAt == nil {
		return nil, 0, nativeAppendConflict("batch_conflict")
	}

	previous := -1
	restarts := 0
	for i, l := range lifetimes {
		if l.SourceID == edge.SourceID && l.InstanceID == edge.NewSourceInstanceID {
			return nil, 0, nativeAppendConflict("batch_conflict")
		}
		if l.SourceID == edge.SourceID && l.InstanceID == edge.PreviousSourceInstanceID && l.FinalSequence == nil {
			previous = i
		}
	}

	for _, prior := range history {
		if prior.SourceID == edge.SourceID && prior.Reason == "restart" {
			restarts++
		}
	}

	if previous < 0 || edge.Reason == "restart" && restarts >= 1 {
		return nil, 0, nativeAppendConflict("batch_conflict")
	}

	watermark := int64(0)
	for _, head := range snapshot.Sources {
		if head.SourceID == edge.SourceID && head.SourceInstanceID == edge.PreviousSourceInstanceID {
			watermark = head.Sequence
		}
	}

	var retained int64
	if err = tx.Get(
		ctx,
		&retained,
		`SELECT COALESCE(MAX((r->>'last_sequence')::bigint), 0)
		FROM exam_native_delivery_records d
		CROSS JOIN LATERAL jsonb_array_elements(convert_from(d.metadata_canonical, 'UTF8')::jsonb->'source_ranges') r
		WHERE d.participation_id = ?
			AND r->>'source_id' = ?
			AND r->>'source_instance_id' = ?`,
		owner.ParticipationID,
		string(edge.SourceID),
		edge.PreviousSourceInstanceID,
	); err != nil {
		return nil, 0, err
	}

	if edge.PreviousFinalSequence < max(watermark, retained) {
		return nil, 0, nativeAppendConflict("batch_conflict")
	}
	if err = checkNativeBatchResetRanges(records, edge); err != nil {
		return nil, 0, err
	}

	return &nativeResetRecord{ParticipationID: model.AttemptParticipationID(owner.ParticipationID), Reset: edge, Digest: digest}, previous, nil
}

// checkNativeBatchResetRanges refuses a batch whose other records place the
// reset predecessor past its declared final sequence.
func checkNativeBatchResetRanges(records []model.NativeRecord, edge model.NativeSourceReset) error {
	for _, other := range records {
		if other.Reset == nil {
			row, err := encodeNativeRecord(other, 1, 0, nil)
			if err != nil {
				return err
			}
			var m nativeRecordMetadata
			if json.Unmarshal(row.Metadata, &m) != nil {
				return invalidPersistedState("native_delivery", "record", model.ErrNativeDeliveryInvalid)
			}
			for _, r := range m.SourceRanges {
				if r.SourceID == edge.SourceID && r.SourceInstanceID == edge.PreviousSourceInstanceID && r.LastSequence > edge.PreviousFinalSequence {
					return nativeAppendConflict("batch_conflict")
				}
			}
		}
	}
	return nil
}

// storeNativeBatchResets reserves metadata for and inserts the batch's new
// historical reset records.
func storeNativeBatchResets(ctx context.Context, tx *sqlxTxWrapper, owner nativeDeliveryOwner, pendingResets []nativeResetRecord) error {
	var used int64
	if err := tx.Get(
		ctx,
		&used,
		`SELECT b.control_metadata_bytes
		FROM exam_attempt_delivery_budgets b
		JOIN exam_attempt_security_owners o ON o.exam_attempt_id = b.exam_attempt_id
		WHERE o.participation_id = ?`,
		owner.ParticipationID,
	); err != nil {
		return err
	}

	encoded := make([][]byte, len(pendingResets))
	required := int64(0)
	for i, record := range pendingResets {
		raw, err := encodeNativeResetRecord(record)
		if err != nil {
			return err
		}

		encoded[i] = raw
		required += int64(len(raw))
	}

	next, err := model.ReserveDeliveryMetadata(used, required)
	if err != nil {
		return err
	}

	for i, raw := range encoded {
		if _, err = tx.Exec(
			ctx,
			`INSERT INTO exam_native_source_resets(participation_id, reset_id, reset_canonical)
			VALUES(?, ?, ?)`,
			owner.ParticipationID,
			pendingResets[i].Reset.ResetID,
			raw,
		); err != nil {
			return err
		}
	}

	if _, err = tx.Exec(
		ctx,
		`UPDATE exam_attempt_delivery_budgets
		SET control_metadata_bytes = ?
		WHERE exam_attempt_id = (
			SELECT exam_attempt_id
			FROM exam_attempt_security_owners
			WHERE participation_id = ?
		)`,
		next,
		owner.ParticipationID,
	); err != nil {
		return err
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
) ([]store.NativeIntegrityFlagged, error) {
	received, gaps, err := nativeDeliveryRanges(ctx, tx, owner)
	if err != nil {
		return nil, err
	}

	progress, err := model.ResolveDeliveryProgress(owner.Allocated, received, gaps, owner.TerminalThrough)
	if err != nil {
		return nil, invalidPersistedState("delivery", "progress", err)
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
		return nil, selectErr
	}

	flagged, err := interpretNativeRecords(ctx, tx, owner, rows, gaps)
	if err != nil {
		return nil, err
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
		return nil, getErr
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
		return nil, execErr
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
		return nil, execErr
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
		return nil, err
	}
	return flagged, nil
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
) ([]store.NativeIntegrityFlagged, error) {
	var stored []byte
	if err := tx.Get(
		ctx,
		&stored,
		`SELECT native_interpretation_canonical FROM exam_attempt_security_owners WHERE participation_id = ?`,
		owner.ParticipationID,
	); err != nil {
		return nil, err
	}
	if len(stored) == 0 || len(rows) == 0 {
		return nil, nil
	}

	var binding admittedSecurityBinding
	if json.Unmarshal(owner.Binding, &binding) != nil || binding.Security.Validate() != nil {
		return nil, invalidPersistedState("native_interpretation", "binding", model.ErrNativeDeliveryInvalid)
	}
	var state model.NativeInterpretation
	if json.Unmarshal(stored, &state) != nil {
		return nil, invalidPersistedState("native_interpretation", "state", model.ErrNativeDeliveryInvalid)
	}
	rules := binding.Security.Policy.Rules

	flags := nativeFlagRaiser{owner: owner, binding: binding}
	interrupted := int64(0)
	for _, row := range rows {
		var record model.NativeRecord
		var meta nativeRecordMetadata
		if json.Unmarshal(row.Raw, &record) != nil || json.Unmarshal(row.Metadata, &meta) != nil {
			return nil, invalidPersistedState("native_delivery", "record", model.ErrNativeDeliveryInvalid)
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
				return nil, invalidPersistedState("native_delivery", "record", model.ErrNativeDeliveryInvalid)
			}
			transitions = append(transitions, state.ApplyFact(rules, *meta.Fact)...)
		case record.Confirmed != nil:
			transitions = append(transitions, state.ConfirmParticipation(rules, record.Confirmed.ObservedAt)...)
		case record.RuleStatus != nil:
			transitions = append(transitions, state.ApplyDesktopStatus(rules, *record.RuleStatus)...)
		case record.Gap != nil:
			transitions = append(transitions,
				state.ApplyGap(rules, model.NativeSourceCapability(record.Gap.SourceID), record.Gap.OccurredAt)...)
		case record.Reset != nil:
			transitions = append(transitions,
				state.ApplyGap(rules, model.NativeSourceCapability(record.Reset.SourceID), record.Reset.OccurredAt)...)
		}
		if err := retainNativeTransitions(ctx, tx, owner, binding, row.Sequence, row.Index, transitions); err != nil {
			return nil, err
		}
		if err := flags.raise(ctx, tx, transitions); err != nil {
			return nil, err
		}
	}

	raw, err := json.Marshal(&state)
	if err == nil {
		raw, err = canonicaljson.Canonicalize(raw, model.NativeInterpretationMaxBytes)
	}
	if err != nil {
		return nil, err
	}
	if _, err = tx.Exec(
		ctx,
		`UPDATE exam_attempt_security_owners SET native_interpretation_canonical = ? WHERE participation_id = ?`,
		raw,
		owner.ParticipationID,
	); err != nil {
		return nil, err
	}
	return flags.flagged, nil
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
	case record.Coverage != nil:
		return record.Coverage.OccurredAt
	}
	return now.UTC().Truncate(time.Millisecond)
}
