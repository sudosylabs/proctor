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

	"github.com/sudosylabs/proctor/server/model"
	"github.com/sudosylabs/proctor/server/store"
)

func assistantReadAttempt(ctx context.Context, tx *sqlxTxWrapper, query store.AssistantTranscriptQuery) (model.ExamAttemptID, error) {
	var id string
	if query.Owner != nil {
		owner := query.Owner
		if !query.ReviewSubmissionID.IsZero() || !owner.AttemptID.IsValid() || owner.AttemptID != query.AttemptID ||
			!owner.CandidateUserID.IsValid() || !owner.SessionID.IsValid() {
			return "", store.NewErrInvalidInput("assistant_transcript", "owner", nil)
		}
		err := tx.Get(ctx, &id, `SELECT a.id FROM exam_attempts a JOIN sessions s ON s.id=? AND s.user_id=a.candidate_user_id JOIN users u ON u.id=s.user_id
 WHERE a.id=? AND a.candidate_user_id=? AND NOT EXISTS(SELECT 1 FROM exam_submissions retired WHERE retired.exam_attempt_id=a.id AND
 (retired.work_retired_at IS NOT NULL OR retired.integrity_retired_at IS NOT NULL)) AND s.archived_at IS NULL AND s.revoked_at IS NULL
 AND s.expires_at>statement_timestamp() AND s.idle_expires_at>statement_timestamp() AND u.archived_at IS NULL AND u.disabled_at IS NULL
 AND (NOT ? OR EXISTS(SELECT 1 FROM exam_submissions sealed WHERE sealed.exam_attempt_id=a.id AND sealed.sealed=true))
 FOR SHARE OF a`, owner.SessionID.String(), owner.AttemptID.String(), owner.CandidateUserID.String(), owner.SealedOnly)
		if err != nil {
			return "", translateError("assistant_transcript", owner.AttemptID.String(), err)
		}
	} else {
		if !query.ReviewSubmissionID.IsValid() {
			return "", store.NewErrInvalidInput("assistant_transcript", "review", nil)
		}
		// The review use case has authorized this submission, just as for attempt evidence.
		err := tx.Get(ctx, &id, `SELECT a.id FROM exam_submissions sub JOIN exam_attempts a ON a.id=sub.exam_attempt_id WHERE sub.id=? AND
		sub.sealed=true AND sub.work_retired_at IS NULL AND sub.integrity_retired_at IS NULL FOR SHARE OF a`, query.ReviewSubmissionID.String())
		if err != nil {
			return "", translateError("assistant_transcript", query.ReviewSubmissionID.String(), err)
		}
		if !query.AttemptID.IsZero() && query.AttemptID.String() != id {
			return "", store.NewErrNotFound("assistant_transcript", query.AttemptID.String())
		}
	}
	return model.ParseExamAttemptID(id)
}
func readAssistantInventory(ctx context.Context, tx *sqlxTxWrapper, id model.ExamAttemptID) (model.AssistantInventory, error) {
	var value model.AssistantInventory
	err := tx.Get(ctx, &value, `SELECT records,bytes,gaps,digest FROM exam_attempt_assistant_inventories WHERE id=?`, id.String())
	if errors.Is(err, sql.ErrNoRows) {
		return model.NewAssistantInventory(), nil
	}
	if err != nil {
		return value, fmt.Errorf("read assistant inventory: %w", err)
	}
	return value, value.Validate()
}
func (s *SQLAssistantTranscriptStore) ListForAttempt(ctx context.Context, query store.AssistantTranscriptQuery) (*store.AssistantTranscriptPage, error) {
	if query.Limit < 1 || query.Limit > 200 || query.AfterSequence < 0 {
		return nil, store.NewErrInvalidInput("assistant_transcript", "paging", nil)
	}
	return runSQLTransaction(ctx, s.GetMaster().Begin, "read assistant transcript", func(ctx context.Context,
		tx *sqlxTxWrapper) (*store.AssistantTranscriptPage, error) {
		id, err := assistantReadAttempt(ctx, tx, query)
		if err != nil {
			return nil, err
		}
		inventory, err := readAssistantInventory(ctx, tx, id)
		if err != nil {
			return nil, err
		}
		var rows [][]byte
		if err = tx.Select(ctx, &rows, `SELECT canonical FROM (
 SELECT sequence,canonical FROM exam_attempt_assistant_turns WHERE exam_attempt_id=? AND sequence>? AND sequence<=?
 UNION ALL SELECT sequence,canonical FROM exam_attempt_assistant_calls WHERE exam_attempt_id=? AND sequence>? AND sequence<=?
 ) history ORDER BY sequence LIMIT ?`, id.String(), query.AfterSequence, inventory.Records, id.String(), query.AfterSequence,
			inventory.Records, query.Limit+1); err != nil {
			return nil, fmt.Errorf("list assistant transcript: %w", err)
		}
		more := len(rows) > query.Limit
		if more {
			rows = rows[:query.Limit]
		}
		page := &store.AssistantTranscriptPage{Records: make([]model.AssistantRecord, 0, len(rows)), Inventory: inventory}
		previous := query.AfterSequence
		for _, raw := range rows {
			record, decodeErr := model.DecodeAssistantRecord(raw)
			if decodeErr != nil {
				return nil, invalidPersistedState("assistant_record", "canonical", decodeErr)
			}
			if record.Sequence != previous+1 || record.Sequence > inventory.Records {
				return nil, invalidPersistedState("assistant_record", "sequence", model.ErrAssistantTranscriptInvalid)
			}
			previous = record.Sequence
			page.Records = append(page.Records, record)
		}
		if more {
			page.NextSequence = previous
		}
		return page, nil
	})
}
func (s *SQLAssistantTranscriptStore) Inventory(ctx context.Context, query store.AssistantTranscriptQuery) (model.AssistantInventory, error) {
	return runSQLTransaction(ctx, s.GetMaster().Begin, "read assistant inventory", func(ctx context.Context,
		tx *sqlxTxWrapper) (model.AssistantInventory, error) {
		id, err := assistantReadAttempt(ctx, tx, query)
		if err != nil {
			return model.AssistantInventory{}, err
		}
		return readAssistantInventory(ctx, tx, id)
	})
}
