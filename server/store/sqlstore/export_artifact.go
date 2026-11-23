// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package sqlstore

import (
	"context"
	"time"

	"github.com/sudosylabs/proctor/server/model"
	"github.com/sudosylabs/proctor/server/store"
)

// An export is a finite file one Job builds and stores under the identity of
// the Job attempt that wrote it. Each kind of export keeps its own table and
// an artifacts table of (export_id, attempt_id, created_at, writer_finished,
// observed_absent_at, reconcile_after). This file holds what every kind shares:
// the build claim, the writer registration, and the purge of artifacts no
// published export points at.

// exportArtifactKind names one kind of export. Its names are fixed SQL
// identifiers chosen in code, never input.
type exportArtifactKind struct {
	entity         string
	exportsTable   string
	artifactsTable string
	buildJobType   model.JobType
	// purgePredicate is true when an artifact (alias a) of an export (alias e)
	// may be removed: not the published archive, or past the export's expiry,
	// and not the writer of a live build.
	purgePredicate string
}

// exportArtifactRef is one artifact: an export and the attempt that wrote it.
type exportArtifactRef struct {
	ExportID  string `db:"export_id"`
	AttemptID string `db:"attempt_id"`
}

// exportBuildRow is what the build claim checks of the locked export.
type exportBuildRow struct {
	JobID     string
	ExpiresAt time.Time
	// SourceExpiresAt, when set, also bounds the build.
	SourceExpiresAt time.Time
}

// lockExportBuild fences a build on its live Job claim. lockRow locks and
// reads the export between the Job and the second clock read, so every kind
// takes its locks in the same order: attempt, Job, export.
func lockExportBuild(
	ctx context.Context,
	tx *sqlxTxWrapper,
	kind exportArtifactKind,
	jobID model.JobID,
	attemptID model.JobAttemptID,
	token model.JobClaimToken,
	lockRow func() (exportBuildRow, error),
) (time.Time, error) {
	var at time.Time
	if err := tx.Get(ctx, &at, `SELECT clock_timestamp()`); err != nil {
		return at, err
	}

	attempt, err := getFencedAttempt(ctx, tx, attemptID, token, at)
	if err != nil {
		return at, err
	}

	job, err := getJob(ctx, tx, jobID, true)
	if err != nil {
		return at, err
	}

	row, err := lockRow()
	if err != nil {
		return at, err
	}
	if err = tx.Get(ctx, &at, `SELECT clock_timestamp()`); err != nil {
		return at, err
	}

	if attempt.JobID != jobID ||
		job.Type != kind.buildJobType ||
		job.Status != model.JobStatusRunning ||
		row.JobID != jobID.String() ||
		!at.Before(attempt.LeaseExpiresAt) {
		return at, store.NewErrConflict(kind.entity, "claim_lost", nil)
	}

	if !at.Before(row.ExpiresAt) || (!row.SourceExpiresAt.IsZero() && !at.Before(row.SourceExpiresAt)) {
		return at, store.NewErrConflict(kind.entity, "expired", nil)
	}

	return model.TimeUTC(at), nil
}

// registerExportArtifact reserves the attempt's storage identity before any
// byte is written, so cleanup always knows the key. A second registration by
// the same attempt is a conflict: a build begins once per attempt.
func registerExportArtifact(ctx context.Context, tx *sqlxTxWrapper, kind exportArtifactKind, ref exportArtifactRef, at time.Time) error {
	inserted, err := tx.Exec(ctx, `INSERT INTO `+kind.artifactsTable+`(
			export_id, attempt_id, created_at, reconcile_after
		)
		VALUES (?, ?, ?, ?)
		ON CONFLICT DO NOTHING`, ref.ExportID, ref.AttemptID, at, at)
	if err != nil {
		return err
	}

	affected, err := inserted.RowsAffected()
	if err != nil {
		return err
	}
	if affected != 1 {
		return store.NewErrConflict(kind.entity, "build_already_started", nil)
	}
	return nil
}

// publishExportArtifact requires the attempt's registration and marks its
// writer finished, in the transaction that publishes the export.
func publishExportArtifact(ctx context.Context, tx *sqlxTxWrapper, kind exportArtifactKind, ref exportArtifactRef) error {
	var registered bool
	if err := tx.Get(ctx, &registered, `SELECT EXISTS(
			SELECT 1
			FROM `+kind.artifactsTable+`
			WHERE export_id = ?
				AND attempt_id = ?
		)`, ref.ExportID, ref.AttemptID); err != nil {
		return err
	}

	if !registered {
		return store.NewErrConflict(kind.entity, "artifact_missing", nil)
	}
	return nil
}

// markExportWriterPublished records the publishing writer as finished.
func markExportWriterPublished(ctx context.Context, tx *sqlxTxWrapper, kind exportArtifactKind, ref exportArtifactRef) error {
	_, err := tx.Exec(ctx, `UPDATE `+kind.artifactsTable+`
		SET writer_finished = true
		WHERE export_id = ?
			AND attempt_id = ?`, ref.ExportID, ref.AttemptID)
	return err
}

// finishExportWriter records that the exact attempt's synchronous writer has
// returned; it never publishes. It is independent of the attempt's possibly
// expired claim.
func finishExportWriter(ctx context.Context, s *SQLStore, kind exportArtifactKind, ref exportArtifactRef) error {
	_, err := s.GetMaster().Exec(ctx, `UPDATE `+kind.artifactsTable+`
		SET writer_finished = true, reconcile_after = LEAST(reconcile_after, clock_timestamp())
		WHERE export_id = ?
			AND attempt_id = ?`, ref.ExportID, ref.AttemptID)
	return err
}

// beginExportPurgeBatch selects the oldest due artifacts and defers each for
// one hour before any I/O, so concurrent callers skip them. A row valid
// refuses rolls the whole batch back.
func beginExportPurgeBatch(
	ctx context.Context,
	s *SQLStore,
	kind exportArtifactKind,
	limit int,
	valid func(exportArtifactRef) bool,
) ([]exportArtifactRef, error) {
	return runSQLTransaction(ctx, s.GetMaster().Begin, "begin "+kind.entity+" purge batch", func(
		ctx context.Context,
		tx *sqlxTxWrapper,
	) ([]exportArtifactRef, error) {
		var rows []exportArtifactRef
		if err := tx.Select(ctx, &rows, `WITH candidates AS MATERIALIZED (
				SELECT a.export_id, a.attempt_id, a.reconcile_after
				FROM `+kind.artifactsTable+` a
				JOIN `+kind.exportsTable+` e ON e.id = a.export_id
				WHERE a.reconcile_after <= clock_timestamp()
					AND `+kind.purgePredicate+`
			ORDER BY a.reconcile_after, a.export_id, a.attempt_id
			LIMIT ?
			FOR UPDATE OF a SKIP LOCKED),
				deferred AS (
					UPDATE `+kind.artifactsTable+` a
					SET reconcile_after = clock_timestamp()+interval '1 hour'
					FROM candidates c
					WHERE a.export_id = c.export_id
						AND a.attempt_id = c.attempt_id
					RETURNING a.export_id, a.attempt_id
				)
			SELECT d.export_id, d.attempt_id
			FROM deferred d
			JOIN candidates c USING(export_id, attempt_id)
			ORDER BY c.reconcile_after, d.export_id, d.attempt_id`, limit); err != nil {
			return nil, err
		}
		for _, row := range rows {
			if !valid(row) {
				return nil, store.NewErrConflict(kind.entity, "artifact", nil)
			}
		}
		return rows, nil
	})
}

// completeExportPurge records an artifact's observed absence. A finished
// writer's artifact is forgotten; an uncertain writer's exact key is kept and
// checked again, because a remote write may still complete late.
func completeExportPurge(ctx context.Context, s *SQLStore, kind exportArtifactKind, ref exportArtifactRef) error {
	_, err := runSQLTransaction(ctx, s.GetMaster().Begin, "record "+kind.entity+" absence", func(
		ctx context.Context,
		tx *sqlxTxWrapper,
	) (bool, error) {
		if _, err := tx.Exec(ctx, `DELETE FROM `+kind.artifactsTable+` a USING `+kind.exportsTable+` e
			WHERE e.id = a.export_id
				AND a.export_id = ?
				AND a.attempt_id = ?
				AND a.writer_finished
				AND `+kind.purgePredicate, ref.ExportID, ref.AttemptID); err != nil {
			return false, err
		}

		_, err := tx.Exec(ctx, `UPDATE `+kind.artifactsTable+` a
			SET observed_absent_at = clock_timestamp(),
				reconcile_after = clock_timestamp()+interval '1 hour'
			FROM `+kind.exportsTable+` e
			WHERE e.id = a.export_id
				AND a.export_id = ?
				AND a.attempt_id = ?
				AND NOT a.writer_finished
				AND `+kind.purgePredicate, ref.ExportID, ref.AttemptID)
		return true, err
	})
	return err
}
