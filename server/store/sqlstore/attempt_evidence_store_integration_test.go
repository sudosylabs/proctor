//go:build integration

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
	"testing"

	"github.com/lib/pq"
	"github.com/stretchr/testify/require"

	"github.com/sudosylabs/proctor/server/model"
	"github.com/sudosylabs/proctor/server/store"
	"github.com/sudosylabs/proctor/server/store/storetest"
)

func TestAttemptEvidence(t *testing.T) {
	t.Parallel()
	persistence := openTestStore(t)
	for _, test := range []struct {
		name     string
		decorate func(*testing.T, *SQLStore) store.Store
	}{
		{"SQL", func(_ *testing.T, s *SQLStore) store.Store { return s }},
		{"Retry", newRetryConformanceStore}, {"Timer", newTimerConformanceStore},
	} {
		t.Run(test.name, func(t *testing.T) {
			resetTestStore(t, persistence)
			storetest.TestAttemptEvidence(t, test.decorate(t, persistence), attemptEvidenceExportProbe(persistence))
		})
	}
}

// attemptEvidenceExportProbe captures a Submission's integrity records as
// an Examination Export does.
func attemptEvidenceExportProbe(persistence *SQLStore) storetest.AttemptEvidenceExportProbe {
	return func(t *testing.T, ctx context.Context, submissionID model.SubmissionID, attemptID model.ExamAttemptID) map[string][]json.RawMessage {
		t.Helper()
		captured, err := runSQLTransaction(ctx, persistence.GetMaster().Begin, "capture export integrity", func(
			ctx context.Context, tx *sqlxTxWrapper,
		) (map[string]any, error) {
			return captureExamExportIntegrity(ctx, tx, store.ExamSubmissionAuthorization{SubmissionID: submissionID, AttemptID: attemptID},
				&examExportSnapshotBudget{})
		})
		require.NoError(t, err)
		records := make(map[string][]json.RawMessage, len(captured))
		for name, value := range captured {
			records[name] = value.([]json.RawMessage)
		}
		return records
	}
}

// TestAttemptEvidenceSchemaKeepsRecordsSealed holds the schema's own
// guarantees, which no Store path should ever need: records and a sealed
// inventory are immutable, and nothing is inserted once the Submission
// exists.
func TestAttemptEvidenceSchemaKeepsRecordsSealed(t *testing.T) {
	t.Parallel()
	persistence := openTestStore(t)
	resetTestStore(t, persistence)
	ctx := context.Background()
	storetest.TestAttemptEvidence(t, persistence, nil)

	var row struct {
		ID        string `db:"id"`
		AttemptID string `db:"exam_attempt_id"`
	}
	require.NoError(t, persistence.GetMaster().Get(ctx, &row, `SELECT e.id, e.exam_attempt_id
		FROM exam_attempt_evidence e
		JOIN exam_submissions s ON s.exam_attempt_id = e.exam_attempt_id
		ORDER BY e.id LIMIT 1`))
	requireObjectState := func(t *testing.T, err error) {
		t.Helper()
		var postgresErr *pq.Error
		require.ErrorAs(t, err, &postgresErr)
		require.Equal(t, "55000", string(postgresErr.Code), "%v", err)
	}
	_, err := persistence.GetMaster().Exec(ctx, `UPDATE exam_attempt_evidence SET sequence = sequence + 1 WHERE id = ?`, row.ID)
	requireObjectState(t, err)
	_, err = persistence.GetMaster().Exec(ctx, `DELETE FROM exam_attempt_evidence WHERE id = ?`, row.ID)
	requireObjectState(t, err)
	_, err = persistence.GetMaster().Exec(ctx, `UPDATE exam_attempt_evidence_inventories SET overflow = overflow + 1
		WHERE exam_attempt_id = ?`, row.AttemptID)
	requireObjectState(t, err)
	_, err = persistence.GetMaster().Exec(ctx, `INSERT INTO exam_attempt_evidence (id, exam_attempt_id, kind, author_kind,
			author_ref, sequence, canonical, recorded_at)
		SELECT ?, exam_attempt_id, kind, author_kind, author_ref, sequence + 1000, canonical, recorded_at
		FROM exam_attempt_evidence WHERE id = ?`, model.NewIntegrityEvidenceID().String(), row.ID)
	requireObjectState(t, err)
}
