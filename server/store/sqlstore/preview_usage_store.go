// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package sqlstore

import (
	"context"
	"fmt"
	"time"

	"github.com/sudosylabs/proctor/server/model"
	"github.com/sudosylabs/proctor/server/store"
)

// AddPreviewUsage accumulates one connection under the same lock as the seal.
func (s SQLAttemptEnvironmentStore) AddPreviewUsage(ctx context.Context, attemptID model.ExamAttemptID,
	port int, bytesIn, bytesOut int64, at time.Time,
) error {
	if !attemptID.IsValid() || port < 1024 || port > 65535 || bytesIn < 0 || bytesOut < 0 || at.IsZero() {
		return store.NewErrInvalidInput("preview_usage", "connection", nil)
	}
	at = model.TimeUTC(at)
	_, err := runSQLTransaction(ctx, s.GetMaster().Begin, "record preview usage", func(ctx context.Context, tx *sqlxTxWrapper) (bool, error) {
		var id string
		// Seal takes the same lock. Read the Submission in a separate
		// statement so a seal that committed while we waited is visible.
		if err := tx.Get(ctx, &id, `SELECT id FROM exam_attempts WHERE id = ? FOR UPDATE`, attemptID.String()); err != nil {
			return false, translateError("exam_attempt", attemptID.String(), err)
		}
		var sealed bool
		if err := tx.Get(ctx, &sealed, `SELECT EXISTS (SELECT 1 FROM exam_submissions WHERE exam_attempt_id = ?)`, id); err != nil {
			return false, fmt.Errorf("check preview usage seal: %w", err)
		}
		if sealed {
			return false, store.NewErrConflict("preview_usage", "submission_sealed", nil)
		}
		_, err := tx.Exec(ctx, `INSERT INTO exam_attempt_preview_usage
			(exam_attempt_id, port, connections, bytes_in, bytes_out, first_used_at, last_used_at)
			VALUES (?, ?, 1, ?, ?, ?, ?)
			ON CONFLICT (exam_attempt_id, port) DO UPDATE SET
				connections = exam_attempt_preview_usage.connections + 1,
				bytes_in = exam_attempt_preview_usage.bytes_in + EXCLUDED.bytes_in,
				bytes_out = exam_attempt_preview_usage.bytes_out + EXCLUDED.bytes_out,
				first_used_at = LEAST(exam_attempt_preview_usage.first_used_at, EXCLUDED.first_used_at),
				last_used_at = GREATEST(exam_attempt_preview_usage.last_used_at, EXCLUDED.last_used_at)`,
			id, port, bytesIn, bytesOut, at, at)
		return err == nil, err
	})
	return err
}

type previewUsageRow struct {
	Port        int       `db:"port"`
	Connections int64     `db:"connections"`
	BytesIn     int64     `db:"bytes_in"`
	BytesOut    int64     `db:"bytes_out"`
	FirstUsedAt time.Time `db:"first_used_at"`
	LastUsedAt  time.Time `db:"last_used_at"`
}

// ListPreviewUsage returns the sealed counts in port order.
func (s SQLAttemptEnvironmentStore) ListPreviewUsage(ctx context.Context, submissionID model.SubmissionID) ([]model.PreviewUsage, error) {
	if !submissionID.IsValid() {
		return nil, store.NewErrInvalidInput("preview_usage", "submission_id", nil)
	}
	return runSQLTransaction(ctx, s.GetMaster().Begin, "read preview usage", func(ctx context.Context, tx *sqlxTxWrapper) ([]model.PreviewUsage, error) {
		var id string
		// Retirement locks the Submission before deleting its usage rows.
		if err := tx.Get(ctx, &id, `SELECT id FROM exam_submissions
			WHERE id = ? AND sealed AND work_retired_at IS NULL AND integrity_retired_at IS NULL
			FOR SHARE`, submissionID.String()); err != nil {
			return nil, translateError("submission", submissionID.String(), err)
		}
		var rows []previewUsageRow
		if err := tx.Select(ctx, &rows, `SELECT port, connections, bytes_in, bytes_out, first_used_at, last_used_at
			FROM exam_attempt_preview_usage WHERE submission_id = ? ORDER BY port`, id); err != nil {
			return nil, fmt.Errorf("list preview usage: %w", err)
		}
		result := make([]model.PreviewUsage, len(rows))
		for i, row := range rows {
			result[i] = model.PreviewUsage{Port: row.Port, Connections: row.Connections, BytesIn: row.BytesIn, BytesOut: row.BytesOut,
				FirstUsedAt: model.TimeUTC(row.FirstUsedAt), LastUsedAt: model.TimeUTC(row.LastUsedAt)}
		}
		return result, nil
	})
}
