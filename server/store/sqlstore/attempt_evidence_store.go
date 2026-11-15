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
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/sudosylabs/proctor/server/model"
	"github.com/sudosylabs/proctor/server/store"
)

// emptyAttemptEvidenceDigest is the digest of no records, which the
// schema's seal trigger writes too.
var emptyAttemptEvidenceDigest = model.NewAttemptEvidenceInventory().Digest

type attemptEvidenceInventoryRow struct {
	Records      int64          `db:"records"`
	Bytes        int64          `db:"bytes"`
	Gaps         int64          `db:"gaps"`
	Overflow     int64          `db:"overflow"`
	Digest       string         `db:"digest"`
	SubmissionID sql.NullString `db:"submission_id"`
}

func (row attemptEvidenceInventoryRow) value() (model.AttemptEvidenceInventory, error) {
	value := model.AttemptEvidenceInventory{Records: row.Records, Bytes: row.Bytes, Gaps: row.Gaps,
		Overflow: row.Overflow, Digest: row.Digest}
	return value, validatePersistedModel("attempt_evidence_inventory", value)
}

// AppendAttemptEvidence stores one Attempt evidence record through the
// environment owner's current lease and incarnation, by that incarnation's
// author. Concurrent appends for one Attempt serialize on
// its inventory row; the seal takes the Attempt row for update, so no append
// interleaves with it, and the schema refuses any record once the Submission
// exists.
func (s *SQLExamAttemptWorkspaceStore) AppendAttemptEvidence(
	ctx context.Context,
	input *store.ExamAttemptEvidenceAppend,
) (*store.ExamAttemptEvidenceAppendResult, error) {
	if input == nil ||
		!input.Access.Valid() ||
		!input.EvidenceID.IsValid() ||
		!model.ValidAttemptEvidenceClaim(input.Kind, input.Access.WorkspaceAuthor(), input.Sequence, input.Detail) ||
		input.ObservedAt.IsZero() {
		return nil, store.NewErrInvalidInput("attempt_evidence", "append", nil)
	}

	return runSQLTransaction(ctx, s.GetMaster().Begin, "append Attempt evidence", func(
		ctx context.Context,
		tx *sqlxTxWrapper,
	) (*store.ExamAttemptEvidenceAppendResult, error) {
		if err := lockAttemptEnvironmentReadGuard(ctx, tx, input.Access); err != nil {
			return nil, err
		}

		return appendAttemptEvidence(ctx, tx, input.Access.AttemptID, input.EvidenceID, input.Kind,
			input.Access.WorkspaceAuthor(), input.Sequence, input.Detail, input.ObservedAt)
	})
}

// appendAttemptEvidence stores one record by author in the caller's
// transaction, after the caller checked who may append: it repeats the same
// claim, refuses a different one for the same stream sequence, counts an
// append over the bounds and extends the inventory.
func appendAttemptEvidence(
	ctx context.Context,
	tx *sqlxTxWrapper,
	attemptID model.ExamAttemptID,
	evidenceID model.IntegrityEvidenceID,
	kind model.AttemptEvidenceKind,
	author model.AttemptWorkspaceAuthor,
	sequence int64,
	detail json.RawMessage,
	observedAtInput time.Time,
) (*store.ExamAttemptEvidenceAppendResult, error) {
	var databaseNow time.Time
	if err := tx.Get(ctx, &databaseNow, `SELECT statement_timestamp()`); err != nil {
		return nil, fmt.Errorf("read Attempt evidence time: %w", err)
	}
	recordedAt := databaseNow.UTC().Truncate(time.Millisecond)

	inventory, err := lockAttemptEvidenceInventory(ctx, tx, attemptID)
	if err != nil {
		return nil, err
	}

	observedAt := observedAtInput.UTC().Truncate(time.Millisecond)
	var stored []byte
	err = tx.Get(ctx, &stored, `SELECT canonical
		FROM exam_attempt_evidence
		WHERE exam_attempt_id = ?
			AND kind = ?
			AND author_kind = ?
			AND author_ref = ?
			AND sequence = ?`, attemptID.String(), string(kind), string(author.Kind), author.Ref,
		sequence)
	switch {
	case err == nil:
		existing, decodeErr := model.DecodeAttemptEvidence(stored)
		if decodeErr != nil {
			return nil, invalidPersistedState("attempt_evidence", "canonical", decodeErr)
		}
		claim := existing
		claim.Detail, claim.ObservedAt = detail, minTime(observedAt, existing.RecordedAt)
		if !existing.SameClaim(claim) {
			return nil, store.NewErrConflict("attempt_evidence", "attempt_evidence_sequence", nil)
		}
		return &store.ExamAttemptEvidenceAppendResult{Evidence: existing, Repeated: true}, nil
	case !errors.Is(err, sql.ErrNoRows):
		return nil, fmt.Errorf("find repeated Attempt evidence: %w", err)
	}

	record := model.AttemptEvidence{ID: evidenceID, AttemptID: attemptID, Kind: kind,
		Author: author, Sequence: sequence, Detail: detail,
		ObservedAt: minTime(observedAt, recordedAt), RecordedAt: recordedAt}
	canonical, err := record.Canonical()
	if err != nil {
		return nil, store.NewErrInvalidInput("attempt_evidence", "append", err)
	}
	if !inventory.Admits(len(canonical)) {
		if _, err = tx.Exec(ctx, `UPDATE exam_attempt_evidence_inventories
			SET overflow = overflow + 1
			WHERE exam_attempt_id = ?`, attemptID.String()); err != nil {
			return nil, fmt.Errorf("count refused Attempt evidence: %w", err)
		}
		return &store.ExamAttemptEvidenceAppendResult{Refused: true}, nil
	}

	if _, err = tx.Exec(ctx, `INSERT INTO exam_attempt_evidence (
			id, exam_attempt_id, kind, author_kind, author_ref, sequence, canonical, recorded_at
		)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		record.ID.String(), record.AttemptID.String(), string(record.Kind), string(author.Kind), author.Ref,
		record.Sequence, canonical, record.RecordedAt,
	); err != nil {
		return nil, translateError("attempt_evidence", record.ID.String(), err)
	}
	if inventory, err = inventory.Append(canonical, record.Gap()); err != nil {
		return nil, err
	}
	if _, err = tx.Exec(ctx, `UPDATE exam_attempt_evidence_inventories
		SET records = ?, bytes = ?, gaps = ?, digest = ?
		WHERE exam_attempt_id = ?`,
		inventory.Records, inventory.Bytes, inventory.Gaps, inventory.Digest, attemptID.String()); err != nil {
		return nil, fmt.Errorf("extend Attempt evidence inventory: %w", err)
	}
	return &store.ExamAttemptEvidenceAppendResult{Evidence: record}, nil
}

// lockAttemptEvidenceInventory creates the Attempt's empty inventory on
// its first record and locks it. A sealed inventory accepts nothing.
func lockAttemptEvidenceInventory(
	ctx context.Context,
	tx *sqlxTxWrapper,
	attemptID model.ExamAttemptID,
) (model.AttemptEvidenceInventory, error) {
	if _, err := tx.Exec(ctx, `INSERT INTO exam_attempt_evidence_inventories (
			exam_attempt_id, records, bytes, gaps, overflow, digest, submission_id
		)
		VALUES (?, 0, 0, 0, 0, ?, NULL)
		ON CONFLICT (exam_attempt_id) DO NOTHING`, attemptID.String(), emptyAttemptEvidenceDigest); err != nil {
		return model.AttemptEvidenceInventory{}, fmt.Errorf("create Attempt evidence inventory: %w", err)
	}
	var row attemptEvidenceInventoryRow
	if err := tx.Get(ctx, &row, `SELECT records, bytes, gaps, overflow, digest, submission_id
		FROM exam_attempt_evidence_inventories
		WHERE exam_attempt_id = ?
		FOR UPDATE`, attemptID.String()); err != nil {
		return model.AttemptEvidenceInventory{}, fmt.Errorf("lock Attempt evidence inventory: %w", err)
	}
	if row.SubmissionID.Valid {
		return model.AttemptEvidenceInventory{}, store.NewErrNotFound("exam_attempt_access", attemptID.String())
	}
	return row.value()
}

// sealedAttemptEvidenceInventory returns the inventory the Attempt's
// Submission seal fixed.
func sealedAttemptEvidenceInventory(
	ctx context.Context,
	tx *sqlxTxWrapper,
	attemptID model.ExamAttemptID,
) (model.AttemptEvidenceInventory, error) {
	var row attemptEvidenceInventoryRow
	if err := tx.Get(ctx, &row, `SELECT records, bytes, gaps, overflow, digest, submission_id
		FROM exam_attempt_evidence_inventories
		WHERE exam_attempt_id = ?
			AND submission_id IS NOT NULL`, attemptID.String()); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return model.AttemptEvidenceInventory{}, invalidPersistedState("attempt_evidence_inventory", "submission_id",
				errors.New("sealed Submission has no Attempt evidence inventory"))
		}
		return model.AttemptEvidenceInventory{}, fmt.Errorf("read sealed Attempt evidence inventory: %w", err)
	}
	return row.value()
}

// ListAttemptEvidence pages a sealed Submission's Attempt evidence for
// review, in ID order.
func (s *SQLExamIntegrityReviewStore) ListAttemptEvidence(
	ctx context.Context,
	options store.AttemptEvidenceListOptions,
) (*store.AttemptEvidencePage, error) {
	if !options.SubmissionID.IsValid() ||
		options.Limit < 1 ||
		options.Limit > store.AttemptEvidenceReviewMaximum ||
		(!options.AfterID.IsZero() && !options.AfterID.IsValid()) {
		return nil, store.NewErrInvalidInput("attempt_evidence", "list_options", nil)
	}

	var rows [][]byte
	if err := s.GetMaster().Select(
		ctx,
		&rows,
		`SELECT e.canonical
		FROM exam_submissions sub
		JOIN exam_attempt_evidence e ON e.exam_attempt_id = sub.exam_attempt_id
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
		return nil, fmt.Errorf("list Attempt evidence: %w", err)
	}

	page := &store.AttemptEvidencePage{Items: []model.AttemptEvidence{}, HasMore: len(rows) > options.Limit}
	if page.HasMore {
		rows = rows[:options.Limit]
	}
	for _, raw := range rows {
		value, err := model.DecodeAttemptEvidence(raw)
		if err != nil {
			return nil, invalidPersistedState("attempt_evidence", "canonical", err)
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
