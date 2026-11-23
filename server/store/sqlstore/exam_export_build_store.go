// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package sqlstore

import (
	"context"
	"slices"
	"time"

	"github.com/sudosylabs/proctor/server/model"
	"github.com/sudosylabs/proctor/server/store"
)

func validExamExportBuild(input *store.ExamExportBuild) bool {
	return input != nil && input.ExportID.IsValid() && input.AttemptID.IsValid() && input.JobID.IsValid() && input.ClaimToken.IsValid()
}

// examExportKind is the exam export's place in the shared export layer.
var examExportKind = exportArtifactKind{
	entity:         "exam_export",
	exportsTable:   "exam_exports",
	artifactsTable: "exam_export_artifacts",
	buildJobType:   model.JobTypeExamExportBuild,
	purgePredicate: examExportPurgePredicate,
}

func lockExamExportBuild(ctx context.Context, tx *sqlxTxWrapper, input *store.ExamExportBuild) (examExportRow, time.Time, error) {
	var row examExportRow
	at, err := lockExportBuild(ctx, tx, examExportKind, input.JobID, input.AttemptID, input.ClaimToken, func() (exportBuildRow, error) {
		var lockErr error
		row, lockErr = getExamExport(ctx, tx, input.ExportID, true)
		return exportBuildRow{JobID: row.JobID, ExpiresAt: row.ExpiresAt, SourceExpiresAt: row.SourceExpiresAt}, lockErr
	})
	return row, at, err
}

func examExportArtifactRef(a store.ExamExportArtifact) exportArtifactRef {
	return exportArtifactRef{ExportID: a.ExportID.String(), AttemptID: a.AttemptID.String()}
}

func (s *SQLExamExportStore) BeginBuild(ctx context.Context, input *store.ExamExportBuild) (*store.ExamExportSnapshot, error) {
	if !validExamExportBuild(input) {
		return nil, store.NewErrInvalidInput("exam_export", "build", nil)
	}
	return runSQLTransaction(ctx, s.GetMaster().Begin, "begin Exam export build", func(
		ctx context.Context,
		tx *sqlxTxWrapper,
	) (*store.ExamExportSnapshot, error) {
		immutable, err := getExamExport(ctx, tx, input.ExportID, false)
		if err != nil {
			return nil, err
		}

		scope := model.RetentionHoldScope{
			ExamID:       model.ExamID(immutable.ExamID),
			SittingID:    model.ExamSittingID(immutable.SittingID),
			SubmissionID: model.SubmissionID(immutable.SubmissionID.String),
		}
		var locked string
		if err = tx.Get(ctx, &locked, `SELECT id
			FROM exams
			WHERE id = ?
			FOR UPDATE`, scope.ExamID.String()); err != nil {
			return nil, err
		}

		if err = tx.Get(ctx, &locked, `SELECT id
			FROM exam_sittings
			WHERE id = ?
			FOR UPDATE`, scope.SittingID.String()); err != nil {
			return nil, err
		}

		selected, err := listExamExportScope(ctx, tx, scope, input.ExportID, true)
		if err != nil {
			return nil, err
		}

		row, at, err := lockExamExportBuild(ctx, tx, input)
		if err != nil {
			return nil, err
		}

		e, err := row.value(at)
		if err != nil {
			return nil, err
		}

		result := &store.ExamExportSnapshot{Export: e}
		if e.State == model.ExamExportReady {
			return result, nil
		}
		if e.State != model.ExamExportQueued {
			return nil, store.NewErrConflict("exam_export", "unavailable", nil)
		}
		if len(selected) != e.SubmissionCount {
			return nil, store.NewErrConflict("exam_export", "source_changed", nil)
		}

		if err = tx.Get(ctx, &result.Records, `SELECT snapshot
			FROM exam_exports
			WHERE id = ?`, e.ID.String()); err != nil {
			return nil, err
		}

		if len(result.Records) < 1 || len(result.Records) > model.ExamExportMaximumSnapshotBytes {
			return nil, store.NewErrConflict("exam_export", "source_changed", nil)
		}

		count := 0
		var size int64
		for _, a := range selected {
			for _, c := range e.Categories {
				var protected bool
				if err = tx.Get(ctx, &protected, `SELECT EXISTS(
						SELECT 1
						FROM retention_source_protections
						WHERE export_id = ?
							AND submission_id = ?
							AND category = ?
							AND expires_at > ?
					)`, e.ID.String(), a.SubmissionID.String(), string(c), at); err != nil {
					return nil, err
				}

				if !protected {
					return nil, store.NewErrConflict("exam_export", "source_expired", nil)
				}
			}

			if !slices.Contains(e.Categories, model.RetentionCategoryWork) {
				continue
			}

			entries, loadErr := loadExamExportEntries(ctx, tx, a.SubmissionID, model.ExamExportMaximumEntries-count)
			if loadErr != nil {
				return nil, loadErr
			}

			count += len(entries)
			for _, entry := range entries {
				if entry.Kind == model.StarterWorkspaceEntryFile {
					result.Files = append(result.Files, store.ExamExportFile{SubmissionID: a.SubmissionID, Entry: entry})
					size += entry.SizeBytes
				}
			}
		}

		if len(result.Files) != e.FileCount || size != e.SourceBytes {
			return nil, store.NewErrConflict("exam_export", "source_changed", nil)
		}

		if err = registerExportArtifact(ctx, tx, examExportKind, examExportArtifactRef(input.ExamExportArtifact), at); err != nil {
			return nil, err
		}

		return result, nil
	})
}

func (s *SQLExamExportStore) Publish(ctx context.Context, input *store.ExamExportPublication) error {
	if input == nil || !validExamExportBuild(&input.ExamExportBuild) || !model.ValidExamExportContent(input.SizeBytes, input.SHA256) {
		return store.NewErrInvalidInput("exam_export", "publication", nil)
	}

	_, err := runSQLTransaction(ctx, s.GetMaster().Begin, "publish Exam export", func(ctx context.Context, tx *sqlxTxWrapper) (bool, error) {
		row, at, err := lockExamExportBuild(ctx, tx, &input.ExamExportBuild)
		if err != nil {
			return false, err
		}

		if row.State == string(model.ExamExportReady) {
			if row.ArchiveAttemptID.String == input.AttemptID.String() &&
				row.ArchiveSizeBytes.Int64 == input.SizeBytes &&
				row.ArchiveSHA256.String == input.SHA256 {
				return true, nil
			}

			return false, store.NewErrConflict("exam_export", "publication", nil)
		}

		if row.State != string(model.ExamExportQueued) {
			return false, store.NewErrConflict("exam_export", "unavailable", nil)
		}

		ref := examExportArtifactRef(input.ExamExportArtifact)
		if err = publishExportArtifact(ctx, tx, examExportKind, ref); err != nil {
			return false, err
		}

		if _, err = tx.Exec(ctx, `UPDATE exam_exports
			SET state = 'ready', snapshot = NULL, archive_attempt_id = ?, archive_size_bytes = ?,
				archive_sha256 = ?, ready_at = ?
			WHERE id = ?`, input.AttemptID.String(), input.SizeBytes, input.SHA256, at, input.ExportID.String()); err != nil {
			return false, err
		}

		if err = markExportWriterPublished(ctx, tx, examExportKind, ref); err != nil {
			return false, err
		}

		if _, err = tx.Exec(ctx, `DELETE FROM retention_source_protections
			WHERE export_id = ?`, input.ExportID.String()); err != nil {
			return false, err
		}

		return true, nil
	})
	return err
}

// FinishWriter records that the exact attempt's synchronous writer has
// returned; it never publishes. It is intentionally independent of the
// attempt's possibly expired claim.
func (s *SQLExamExportStore) FinishWriter(ctx context.Context, a store.ExamExportArtifact) error {
	if !a.ExportID.IsValid() || !a.AttemptID.IsValid() {
		return store.NewErrInvalidInput("exam_export", "artifact", nil)
	}
	return finishExportWriter(ctx, s.SQLStore, examExportKind, examExportArtifactRef(a))
}

func (s *SQLExamExportStore) Reconcile(ctx context.Context, limit int) (int, error) {
	if limit < 1 || limit > 100 {
		return 0, store.NewErrInvalidInput("exam_export", "limit", nil)
	}
	return runSQLTransaction(ctx, s.GetMaster().Begin, "expire Exam exports", func(ctx context.Context, tx *sqlxTxWrapper) (int, error) {
		var rows []examExportRow
		if err := tx.Select(ctx, &rows, `SELECT `+examExportColumns+` FROM exam_exports e
			WHERE (e.state IN ('queued', 'ready', 'failed') AND e.expires_at <= clock_timestamp())
				OR (
					e.state = 'queued'
					AND (
						e.source_expires_at <= clock_timestamp()
						OR NOT EXISTS(
							SELECT 1
							FROM jobs j
							WHERE j.id = e.job_id
								AND j.status IN ('queued', 'running', 'cancel_requested')
						)
					)
				)
			ORDER BY LEAST(expires_at, source_expires_at), id
			LIMIT ?
			FOR UPDATE OF e SKIP LOCKED`, limit); err != nil {
			return 0, err
		}

		for _, r := range rows {
			if _, err := tx.Exec(ctx, `UPDATE exam_exports
				SET state = CASE
					WHEN expires_at <= clock_timestamp() THEN 'expired'
					ELSE 'failed'
				END,
					snapshot = NULL, archive_attempt_id = NULL, archive_size_bytes = NULL,
					archive_sha256 = NULL, ready_at = NULL
				WHERE id = ?`, r.ID); err != nil {
				return 0, err
			}

			if _, err := tx.Exec(ctx, `DELETE FROM retention_source_protections
				WHERE export_id = ?`, r.ID); err != nil {
				return 0, err
			}
		}

		return len(rows), nil
	})
}

const examExportPurgePredicate = `(
		e.state <> 'ready'
		OR e.archive_attempt_id <> a.attempt_id
		OR e.expires_at <= clock_timestamp()
	)
	AND NOT (
		e.state = 'queued'
		AND e.source_expires_at > clock_timestamp()
		AND EXISTS(
			SELECT 1
			FROM job_attempts ja
			WHERE ja.id = a.attempt_id
				AND ja.status = 'running'
				AND ja.lease_expires_at > clock_timestamp()
		)
	)`

func (s *SQLExamExportStore) BeginPurgeBatch(ctx context.Context, limit int) ([]store.ExamExportArtifact, error) {
	if limit < 1 || limit > 100 {
		return nil, store.NewErrInvalidInput("exam_export", "limit", nil)
	}
	rows, err := beginExportPurgeBatch(ctx, s.SQLStore, examExportKind, limit, func(r exportArtifactRef) bool {
		return model.ExamExportID(r.ExportID).IsValid() && model.JobAttemptID(r.AttemptID).IsValid()
	})
	if err != nil {
		return nil, err
	}

	result := make([]store.ExamExportArtifact, 0, len(rows))
	for _, r := range rows {
		result = append(result, store.ExamExportArtifact{ExportID: model.ExamExportID(r.ExportID), AttemptID: model.JobAttemptID(r.AttemptID)})
	}

	return result, nil
}

func (s *SQLExamExportStore) CompletePurge(ctx context.Context, a store.ExamExportArtifact) error {
	if !a.ExportID.IsValid() || !a.AttemptID.IsValid() {
		return store.NewErrInvalidInput("exam_export", "artifact", nil)
	}
	return completeExportPurge(ctx, s.SQLStore, examExportKind, examExportArtifactRef(a))
}
