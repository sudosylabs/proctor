// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package sqlstore

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/sudosylabs/proctor/server/model"
	"github.com/sudosylabs/proctor/server/store"
)

// emptyCapabilityEvidenceDigest is the digest of no records, which the
// schema's seal trigger writes too.
var emptyCapabilityEvidenceDigest = model.NewCapabilityEvidenceInventory().Digest

type capabilityEvidenceInventoryRow struct {
	Records      int64          `db:"records"`
	Bytes        int64          `db:"bytes"`
	Gaps         int64          `db:"gaps"`
	Overflow     int64          `db:"overflow"`
	Digest       string         `db:"digest"`
	SubmissionID sql.NullString `db:"submission_id"`
}

func (row capabilityEvidenceInventoryRow) value() (model.CapabilityEvidenceInventory, error) {
	value := model.CapabilityEvidenceInventory{Records: row.Records, Bytes: row.Bytes, Gaps: row.Gaps,
		Overflow: row.Overflow, Digest: row.Digest}
	return value, validatePersistedModel("capability_evidence_inventory", value)
}

// AppendEnvironmentEvidence stores one capability evidence record through a
// live environment binding. Concurrent appends for one Attempt serialize on
// its inventory row; the seal takes the Attempt row for update, so no append
// interleaves with it, and the schema refuses any record once the Submission
// exists.
func (s *SQLExamAttemptWorkspaceStore) AppendEnvironmentEvidence(
	ctx context.Context,
	input *store.ExamAttemptEnvironmentEvidenceAppend,
) (*store.ExamAttemptEnvironmentEvidenceAppendResult, error) {
	if input == nil ||
		!input.Access.Valid() ||
		!input.EvidenceID.IsValid() ||
		!model.ValidCapabilityEvidenceClaim(input.Kind, input.Sequence, input.Detail) ||
		input.ObservedAt.IsZero() {
		return nil, store.NewErrInvalidInput("capability_evidence", "append", nil)
	}

	return runSQLTransaction(ctx, s.GetMaster().Begin, "append capability evidence", func(
		ctx context.Context,
		tx *sqlxTxWrapper,
	) (*store.ExamAttemptEnvironmentEvidenceAppendResult, error) {
		if err := lockAttemptEnvironmentReadGuard(ctx, tx, input.Access); err != nil {
			return nil, err
		}

		var binding struct {
			ParticipationID string    `db:"participation_id"`
			DatabaseNow     time.Time `db:"database_now"`
		}
		if err := tx.Get(ctx, &binding, `SELECT participation_id, statement_timestamp() AS database_now
			FROM exam_attempt_environment_bindings
			WHERE id = ?`, input.Access.BindingID.String()); err != nil {
			return nil, fmt.Errorf("read capability evidence binding: %w", err)
		}
		participationID, err := model.ParseAttemptParticipationID(binding.ParticipationID)
		if err != nil {
			return nil, invalidPersistedState("attempt_environment_binding", "participation_id", err)
		}
		recordedAt := binding.DatabaseNow.UTC().Truncate(time.Millisecond)

		inventory, err := lockCapabilityEvidenceInventory(ctx, tx, input.Access.AttemptID)
		if err != nil {
			return nil, err
		}

		observedAt := input.ObservedAt.UTC().Truncate(time.Millisecond)
		var stored []byte
		err = tx.Get(ctx, &stored, `SELECT canonical
			FROM capability_evidence
			WHERE environment_binding_id = ?
				AND policy_kind = ?
				AND sequence = ?`, input.Access.BindingID.String(), string(input.Kind), input.Sequence)
		switch {
		case err == nil:
			existing, decodeErr := model.DecodeCapabilityEvidence(stored)
			if decodeErr != nil {
				return nil, invalidPersistedState("capability_evidence", "canonical", decodeErr)
			}
			claim := existing
			claim.Gap, claim.Detail, claim.ObservedAt = input.Gap, input.Detail, minTime(observedAt, existing.RecordedAt)
			if !existing.SameClaim(claim) {
				return nil, store.NewErrConflict("capability_evidence", "capability_evidence_sequence", nil)
			}
			return &store.ExamAttemptEnvironmentEvidenceAppendResult{Evidence: existing, Repeated: true}, nil
		case !errors.Is(err, sql.ErrNoRows):
			return nil, fmt.Errorf("find repeated capability evidence: %w", err)
		}

		record := model.CapabilityEvidence{ID: input.EvidenceID, AttemptID: input.Access.AttemptID,
			ParticipationID: participationID, Generation: input.Access.Generation, BindingID: input.Access.BindingID,
			Kind: input.Kind, Sequence: input.Sequence, Gap: input.Gap, Detail: input.Detail,
			ObservedAt: minTime(observedAt, recordedAt), RecordedAt: recordedAt}
		canonical, err := record.Canonical()
		if err != nil {
			return nil, store.NewErrInvalidInput("capability_evidence", "append", err)
		}
		if !inventory.Admits(len(canonical)) {
			if _, err = tx.Exec(ctx, `UPDATE exam_attempt_capability_evidence_inventories
				SET overflow = overflow + 1
				WHERE exam_attempt_id = ?`, input.Access.AttemptID.String()); err != nil {
				return nil, fmt.Errorf("count refused capability evidence: %w", err)
			}
			return &store.ExamAttemptEnvironmentEvidenceAppendResult{Refused: true}, nil
		}

		if _, err = tx.Exec(ctx, `INSERT INTO capability_evidence (
				id, exam_attempt_id, participation_id, generation, environment_binding_id, policy_kind, sequence,
				gap, canonical, recorded_at
			)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			record.ID.String(), record.AttemptID.String(), record.ParticipationID.String(), record.Generation,
			record.BindingID.String(), string(record.Kind), record.Sequence, record.Gap, canonical, record.RecordedAt,
		); err != nil {
			return nil, translateError("capability_evidence", record.ID.String(), err)
		}
		if inventory, err = inventory.Append(canonical, record.Gap); err != nil {
			return nil, err
		}
		if _, err = tx.Exec(ctx, `UPDATE exam_attempt_capability_evidence_inventories
			SET records = ?, bytes = ?, gaps = ?, digest = ?
			WHERE exam_attempt_id = ?`,
			inventory.Records, inventory.Bytes, inventory.Gaps, inventory.Digest, input.Access.AttemptID.String()); err != nil {
			return nil, fmt.Errorf("extend capability evidence inventory: %w", err)
		}
		return &store.ExamAttemptEnvironmentEvidenceAppendResult{Evidence: record}, nil
	})
}

// lockCapabilityEvidenceInventory creates the Attempt's empty inventory on
// its first record and locks it. A sealed inventory accepts nothing.
func lockCapabilityEvidenceInventory(
	ctx context.Context,
	tx *sqlxTxWrapper,
	attemptID model.ExamAttemptID,
) (model.CapabilityEvidenceInventory, error) {
	if _, err := tx.Exec(ctx, `INSERT INTO exam_attempt_capability_evidence_inventories (
			exam_attempt_id, records, bytes, gaps, overflow, digest, submission_id
		)
		VALUES (?, 0, 0, 0, 0, ?, NULL)
		ON CONFLICT (exam_attempt_id) DO NOTHING`, attemptID.String(), emptyCapabilityEvidenceDigest); err != nil {
		return model.CapabilityEvidenceInventory{}, fmt.Errorf("create capability evidence inventory: %w", err)
	}
	var row capabilityEvidenceInventoryRow
	if err := tx.Get(ctx, &row, `SELECT records, bytes, gaps, overflow, digest, submission_id
		FROM exam_attempt_capability_evidence_inventories
		WHERE exam_attempt_id = ?
		FOR UPDATE`, attemptID.String()); err != nil {
		return model.CapabilityEvidenceInventory{}, fmt.Errorf("lock capability evidence inventory: %w", err)
	}
	if row.SubmissionID.Valid {
		return model.CapabilityEvidenceInventory{}, store.NewErrNotFound("exam_attempt_access", attemptID.String())
	}
	return row.value()
}

// sealedCapabilityEvidenceInventory returns the inventory the Attempt's
// Submission seal fixed.
func sealedCapabilityEvidenceInventory(
	ctx context.Context,
	tx *sqlxTxWrapper,
	attemptID model.ExamAttemptID,
) (model.CapabilityEvidenceInventory, error) {
	var row capabilityEvidenceInventoryRow
	if err := tx.Get(ctx, &row, `SELECT records, bytes, gaps, overflow, digest, submission_id
		FROM exam_attempt_capability_evidence_inventories
		WHERE exam_attempt_id = ?
			AND submission_id IS NOT NULL`, attemptID.String()); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return model.CapabilityEvidenceInventory{}, invalidPersistedState("capability_evidence_inventory", "submission_id",
				errors.New("sealed Submission has no capability evidence inventory"))
		}
		return model.CapabilityEvidenceInventory{}, fmt.Errorf("read sealed capability evidence inventory: %w", err)
	}
	return row.value()
}

// ListCapabilityEvidence pages a sealed Submission's capability evidence for
// review, in ID order.
func (s *SQLExamIntegrityReviewStore) ListCapabilityEvidence(
	ctx context.Context,
	options store.CapabilityEvidenceListOptions,
) (*store.CapabilityEvidencePage, error) {
	if !options.SubmissionID.IsValid() ||
		options.Limit < 1 ||
		options.Limit > store.CapabilityEvidenceReviewMaximum ||
		(!options.AfterID.IsZero() && !options.AfterID.IsValid()) {
		return nil, store.NewErrInvalidInput("capability_evidence", "list_options", nil)
	}

	var rows [][]byte
	if err := s.GetMaster().Select(
		ctx,
		&rows,
		`SELECT e.canonical
		FROM exam_submissions sub
		JOIN capability_evidence e ON e.exam_attempt_id = sub.exam_attempt_id
		WHERE sub.id = ?
			AND sub.sealed = true
			AND sub.integrity_retired_at IS NULL
			AND e.id > ?
		ORDER BY e.id
		LIMIT ?`,
		options.SubmissionID.String(),
		options.AfterID.String(),
		options.Limit+1,
	); err != nil {
		return nil, fmt.Errorf("list capability evidence: %w", err)
	}

	page := &store.CapabilityEvidencePage{Items: []model.CapabilityEvidence{}, HasMore: len(rows) > options.Limit}
	if page.HasMore {
		rows = rows[:options.Limit]
	}
	for _, raw := range rows {
		value, err := model.DecodeCapabilityEvidence(raw)
		if err != nil {
			return nil, invalidPersistedState("capability_evidence", "canonical", err)
		}
		page.Items = append(page.Items, value)
	}
	return page, nil
}

func minTime(left, right time.Time) time.Time {
	if left.After(right) {
		return right
	}
	return left
}
