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
	"time"

	"github.com/sudosylabs/proctor/server/model"
	"github.com/sudosylabs/proctor/server/store"
)

// SQLAuditExportStore keeps audit exports on the shared export layer.
type SQLAuditExportStore struct{ *SQLStore }

func newSQLAuditExportStore(s *SQLStore) store.AuditExportStore { return &SQLAuditExportStore{s} }

var auditExportKind = exportArtifactKind{
	entity:         "audit_export",
	exportsTable:   "audit_exports",
	artifactsTable: "audit_export_artifacts",
	buildJobType:   model.JobTypeAuditExportBuild,
	purgePredicate: auditExportPurgePredicate,
}

// An audit export's artifact may go once it is not the published file or the
// export has expired, unless it is the writer of a build still running.
const auditExportPurgePredicate = `(
		e.state <> 'ready'
		OR e.archive_attempt_id <> a.attempt_id
		OR e.expires_at <= clock_timestamp()
	)
	AND NOT (
		e.state = 'queued'
		AND e.expires_at > clock_timestamp()
		AND EXISTS(
			SELECT 1
			FROM job_attempts ja
			WHERE ja.id = a.attempt_id
				AND ja.status = 'running'
				AND ja.lease_expires_at > clock_timestamp()
		)
	)`

const auditExportColumns = `id, requester_user_id, format, redacted, state, created_at, expires_at,
		event_count, job_id, archive_attempt_id, archive_size_bytes, archive_sha256, ready_at`

type auditExportRow struct {
	ID               string         `db:"id"`
	RequesterID      string         `db:"requester_user_id"`
	Format           string         `db:"format"`
	Redacted         bool           `db:"redacted"`
	State            string         `db:"state"`
	CreatedAt        time.Time      `db:"created_at"`
	ExpiresAt        time.Time      `db:"expires_at"`
	EventCount       int            `db:"event_count"`
	JobID            string         `db:"job_id"`
	ArchiveAttemptID sql.NullString `db:"archive_attempt_id"`
	ArchiveSizeBytes sql.NullInt64  `db:"archive_size_bytes"`
	ArchiveSHA256    sql.NullString `db:"archive_sha256"`
	ReadyAt          sql.NullTime   `db:"ready_at"`
}

// value reads the row as of at: past its expiry an export is expired whatever
// state the sweep has yet to record.
func (r auditExportRow) value(at time.Time) (*model.AuditExport, error) {
	e := &model.AuditExport{
		ID:              model.AuditExportID(r.ID),
		RequesterUserID: model.UserID(r.RequesterID),
		Format:          model.AuditExportFormat(r.Format),
		Redacted:        r.Redacted,
		State:           model.AuditExportState(r.State),
		JobID:           model.JobID(r.JobID),
		EventCount:      r.EventCount,
		CreatedAt:       model.TimeUTC(r.CreatedAt),
		ExpiresAt:       model.TimeUTC(r.ExpiresAt),
		SizeBytes:       r.ArchiveSizeBytes.Int64,
		SHA256:          r.ArchiveSHA256.String,
		ReadyAt:         optionalTime(r.ReadyAt),
	}
	if e.Validate() != nil || (e.State == model.ExamExportReady && !model.JobAttemptID(r.ArchiveAttemptID.String).IsValid()) {
		return nil, invalidPersistedState("audit_export", "value", errors.New("invalid audit export metadata"))
	}

	if !at.Before(e.ExpiresAt) {
		e.State = model.ExamExportExpired
		e.SizeBytes = 0
		e.SHA256 = ""
		e.ReadyAt = model.OptionalTime{}
	}
	return e, nil
}

func getAuditExport(ctx context.Context, executor sqlxExecutor, id model.AuditExportID, lock bool) (auditExportRow, error) {
	var row auditExportRow
	q := `SELECT ` + auditExportColumns + ` FROM audit_exports WHERE id = ?`
	if lock {
		q += ` FOR UPDATE`
	}
	err := executor.Get(ctx, &row, q, id.String())
	return row, translateError("audit_export", id.String(), err)
}

// auditExportQuery is the frozen listing as stored. It is private to the
// store and read back only by a build.
type auditExportQuery struct {
	ActorID                 model.UserID           `json:"actor_id,omitempty"`
	SubjectUserID           model.UserID           `json:"subject_user_id,omitempty"`
	Actions                 []string               `json:"actions,omitempty"`
	Kind                    model.AuditKind        `json:"kind,omitempty"`
	Status                  model.AuditStatus      `json:"status,omitempty"`
	ResourceType            model.ResourceType     `json:"resource_type,omitempty"`
	ResourceID              string                 `json:"resource_id,omitempty"`
	Since                   *time.Time             `json:"since,omitempty"`
	Until                   *time.Time             `json:"until,omitempty"`
	Ascending               bool                   `json:"ascending,omitempty"`
	InstitutionWide         bool                   `json:"institution_wide,omitempty"`
	AcademicInstitutionWide bool                   `json:"academic_institution_wide,omitempty"`
	AcademicUnitRootIDs     []model.AcademicUnitID `json:"academic_unit_root_ids,omitempty"`
	AllowedActions          []string               `json:"allowed_actions,omitempty"`
}

func encodeAuditExportQuery(o store.AuditListOptions) ([]byte, error) {
	q := auditExportQuery{
		ActorID:                 o.ActorID,
		SubjectUserID:           o.SubjectUserID,
		Actions:                 o.Actions,
		Kind:                    o.Kind,
		Status:                  o.Status,
		Ascending:               o.Ascending,
		InstitutionWide:         o.Visibility.InstitutionWide,
		AcademicInstitutionWide: o.Visibility.AcademicInstitutionWide,
		AcademicUnitRootIDs:     o.Visibility.AcademicUnitRootIDs,
		AllowedActions:          o.Visibility.AllowedActions,
	}
	if o.Resource != nil {
		q.ResourceType, q.ResourceID = o.Resource.Type, o.Resource.ID
	}
	if !o.Since.IsZero() {
		since := model.TimeUTC(o.Since)
		q.Since = &since
	}
	if !o.Until.IsZero() {
		until := model.TimeUTC(o.Until)
		q.Until = &until
	}
	return json.Marshal(q)
}

func decodeAuditExportQuery(data []byte) (store.AuditListOptions, error) {
	var q auditExportQuery
	if err := json.Unmarshal(data, &q); err != nil {
		return store.AuditListOptions{}, invalidPersistedState("audit_export", "query", err)
	}
	o := store.AuditListOptions{
		ActorID:       q.ActorID,
		SubjectUserID: q.SubjectUserID,
		Actions:       q.Actions,
		Kind:          q.Kind,
		Status:        q.Status,
		Ascending:     q.Ascending,
		Visibility: store.AuditVisibilityScope{
			InstitutionWide:         q.InstitutionWide,
			AcademicInstitutionWide: q.AcademicInstitutionWide,
			AcademicUnitRootIDs:     q.AcademicUnitRootIDs,
			AllowedActions:          q.AllowedActions,
		},
	}
	if q.ResourceType != "" {
		o.Resource = &model.Resource{Type: q.ResourceType, ID: q.ResourceID}
	}
	if q.Since != nil {
		o.Since = model.TimeUTC(*q.Since)
	}
	if q.Until != nil {
		o.Until = model.TimeUTC(*q.Until)
	}
	return o, nil
}

// auditExportOutcome is the persisted outcome of a creation: the export a
// replay rehydrates.
type auditExportOutcome struct {
	ExportID model.AuditExportID `json:"export_id"`
}

func (auditExportOutcome) outcomeDocument() {}

func (s *SQLAuditExportStore) Create(
	ctx context.Context,
	input *store.AuditExportCreation,
	command *store.CommandIdempotency,
) (*model.AuditExport, error) {
	if input == nil ||
		input.Export == nil ||
		!input.Export.ID.IsValid() ||
		!input.Export.RequesterUserID.IsValid() ||
		!input.Export.Format.IsValid() ||
		!input.Export.JobID.IsValid() ||
		input.Export.EventCount < 0 ||
		input.Export.EventCount > model.AuditExportMaximumEvents ||
		input.Export.Redacted == input.Query.Visibility.InstitutionWide ||
		!input.AuditEventID.IsValid() ||
		input.AuditAt.IsZero() ||
		command == nil ||
		command.Operation != store.AuditExportCreateOperation ||
		command.UserID != input.Export.RequesterUserID {
		return nil, store.NewErrInvalidInput("audit_export", "creation", nil)
	}
	query, err := encodeAuditExportQuery(input.Query)
	if err != nil {
		return nil, store.NewErrInvalidInput("audit_export", "query", err)
	}
	visibility, err := encodeAuditExportQuery(store.AuditListOptions{Visibility: input.Query.Visibility})
	if err != nil {
		return nil, store.NewErrInvalidInput("audit_export", "visibility", err)
	}

	result, err := runIdempotentMutation(ctx, s.SQLStore, "create audit export", idempotentMutation[*model.AuditExport]{
		command:      command,
		auditEventID: input.AuditEventID,
		execute: func(ctx context.Context, tx *sqlxTxWrapper) (*model.AuditExport, error) {
			return createAuditExport(ctx, tx, input, query, visibility)
		},
		encode: func(e *model.AuditExport) ([]byte, error) { return encodeOutcome(auditExportOutcome{ExportID: e.ID}) },
		decode: func(b []byte) (*model.AuditExport, error) {
			var outcome auditExportOutcome
			if err := decodeOutcome(b, &outcome); err != nil {
				return nil, err
			}
			if !outcome.ExportID.IsValid() {
				return nil, errors.New("invalid audit export outcome")
			}
			return &model.AuditExport{ID: outcome.ExportID}, nil
		},
		hydrateReplay: func(ctx context.Context, tx *sqlxTxWrapper, e *model.AuditExport) (*model.AuditExport, error) {
			row, err := getAuditExport(ctx, tx, e.ID, false)
			if err != nil {
				return nil, err
			}
			if row.RequesterID != input.Export.RequesterUserID.String() {
				return nil, store.NewErrNotFound("audit_export", e.ID.String())
			}
			var at time.Time
			if err = tx.Get(ctx, &at, `SELECT clock_timestamp()`); err != nil {
				return nil, err
			}
			return row.value(at)
		},
		completeReplay: func(ctx context.Context, tx *sqlxTxWrapper, e *model.AuditExport, original string) error {
			return completeAuditExportAudit(ctx, tx, input, e, original)
		},
	})
	if err != nil {
		return nil, err
	}
	return result.Value, nil
}

func createAuditExport(ctx context.Context, tx *sqlxTxWrapper, input *store.AuditExportCreation, query, visibility []byte) (*model.AuditExport, error) {
	policy, err := getRetentionPolicy(ctx, tx, "FOR SHARE OF p, i")
	if err != nil {
		return nil, err
	}
	if policy.ExportRetentionDays < 1 || policy.ExportRetentionDays > model.ExamExportMaximumDays {
		return nil, store.NewErrConflict("audit_export", "policy_unconfigured", nil)
	}

	var at time.Time
	if err = tx.Get(ctx, &at, `SELECT clock_timestamp()`); err != nil {
		return nil, err
	}
	at = model.TimeUTC(at)
	e := *input.Export
	e.State = model.ExamExportQueued
	e.CreatedAt = at
	e.ExpiresAt = at.Add(time.Duration(policy.ExportRetentionDays) * 24 * time.Hour)
	e.SizeBytes, e.SHA256, e.ReadyAt = 0, "", model.OptionalTime{}
	if err = e.Validate(); err != nil {
		return nil, store.NewErrInvalidInput("audit_export", "creation", err)
	}

	if _, err = tx.Exec(ctx, `INSERT INTO audit_exports (
			id, requester_user_id, format, redacted, state, created_at, expires_at, event_count, job_id,
				visibility, query
		)
		VALUES (?, ?, ?, ?, 'queued', ?, ?, ?, ?, ?::jsonb, ?::jsonb)`,
		e.ID.String(), e.RequesterUserID.String(), string(e.Format), e.Redacted, e.CreatedAt, e.ExpiresAt,
		e.EventCount, e.JobID.String(), string(visibility), string(query)); err != nil {
		return nil, err
	}

	payload, _ := json.Marshal(struct {
		ExportID model.AuditExportID `json:"export_id"`
	}{e.ID})
	job, err := model.NewJob(e.JobID, model.JobTypeAuditExportBuild, payload, e.ID.String(), at, at, model.AuditExportMaximumAttempts)
	if err != nil {
		return nil, err
	}
	job.CreatedByUserID = e.RequesterUserID
	if _, err = insertQueuedJob(ctx, tx, job, false); err != nil {
		return nil, err
	}

	if err = completeAuditExportAudit(ctx, tx, input, &e, ""); err != nil {
		return nil, err
	}
	return &e, nil
}

func completeAuditExportAudit(ctx context.Context, tx *sqlxTxWrapper, input *store.AuditExportCreation, e *model.AuditExport, original string) error {
	data := map[string]any{"audit_export_id": e.ID.String(), "format": string(input.Export.Format), "event_count": input.Export.EventCount}
	if original != "" {
		data["replayed"] = true
		data["original_audit_event_id"] = original
	}
	encoded, err := model.EncodeAuditData(data)
	if err != nil {
		return err
	}
	_, err = completeAuditEvent(ctx, tx, input.AuditEventID, model.AuditStatusSuccess, "", encoded, input.AuditAt)
	return err
}

func (s *SQLAuditExportStore) Get(ctx context.Context, id model.AuditExportID) (*store.AuditExportRead, error) {
	if !id.IsValid() {
		return nil, store.NewErrInvalidInput("audit_export", "id", nil)
	}
	return runSQLTransaction(ctx, s.GetMaster().Begin, "read audit export", func(ctx context.Context, tx *sqlxTxWrapper) (*store.AuditExportRead, error) {
		var row struct {
			auditExportRow
			Visibility []byte `db:"visibility"`
		}
		if err := tx.Get(ctx, &row, `SELECT `+auditExportColumns+`, visibility
			FROM audit_exports
			WHERE id = ?`, id.String()); err != nil {
			return nil, translateError("audit_export", id.String(), err)
		}
		var at time.Time
		if err := tx.Get(ctx, &at, `SELECT clock_timestamp()`); err != nil {
			return nil, err
		}
		e, err := row.value(at)
		if err != nil {
			return nil, err
		}
		var visibility auditExportQuery
		if err = json.Unmarshal(row.Visibility, &visibility); err != nil {
			return nil, invalidPersistedState("audit_export", "visibility", err)
		}
		result := &store.AuditExportRead{Export: e, Visibility: store.AuditVisibilityScope{
			InstitutionWide:         visibility.InstitutionWide,
			AcademicInstitutionWide: visibility.AcademicInstitutionWide,
			AcademicUnitRootIDs:     visibility.AcademicUnitRootIDs,
			AllowedActions:          visibility.AllowedActions,
		}}
		if e.State == model.ExamExportReady {
			result.Artifact = store.AuditExportArtifact{ExportID: e.ID, AttemptID: model.JobAttemptID(row.ArchiveAttemptID.String)}
		}
		return result, nil
	})
}

func validAuditExportBuild(input *store.AuditExportBuild) bool {
	return input != nil && input.ExportID.IsValid() && input.AttemptID.IsValid() && input.JobID.IsValid() && input.ClaimToken.IsValid()
}

func lockAuditExportBuild(ctx context.Context, tx *sqlxTxWrapper, input *store.AuditExportBuild) (auditExportRow, time.Time, error) {
	var row auditExportRow
	at, err := lockExportBuild(ctx, tx, auditExportKind, input.JobID, input.AttemptID, input.ClaimToken, func() (exportBuildRow, error) {
		var lockErr error
		row, lockErr = getAuditExport(ctx, tx, input.ExportID, true)
		return exportBuildRow{JobID: row.JobID, ExpiresAt: row.ExpiresAt}, lockErr
	})
	return row, at, err
}

func auditExportArtifactRef(a store.AuditExportArtifact) exportArtifactRef {
	return exportArtifactRef{ExportID: a.ExportID.String(), AttemptID: a.AttemptID.String()}
}

func (s *SQLAuditExportStore) BeginBuild(ctx context.Context, input *store.AuditExportBuild) (*store.AuditExportSnapshot, error) {
	if !validAuditExportBuild(input) {
		return nil, store.NewErrInvalidInput("audit_export", "build", nil)
	}
	return runSQLTransaction(ctx, s.GetMaster().Begin, "begin audit export build", func(
		ctx context.Context,
		tx *sqlxTxWrapper,
	) (*store.AuditExportSnapshot, error) {
		row, at, err := lockAuditExportBuild(ctx, tx, input)
		if err != nil {
			return nil, err
		}
		e, err := row.value(at)
		if err != nil {
			return nil, err
		}
		result := &store.AuditExportSnapshot{Export: e}
		if e.State == model.ExamExportReady {
			return result, nil
		}
		if e.State != model.ExamExportQueued {
			return nil, store.NewErrConflict("audit_export", "unavailable", nil)
		}

		var query []byte
		if err = tx.Get(ctx, &query, `SELECT query FROM audit_exports WHERE id = ?`, e.ID.String()); err != nil {
			return nil, err
		}
		if result.Query, err = decodeAuditExportQuery(query); err != nil {
			return nil, err
		}
		if err = registerExportArtifact(ctx, tx, auditExportKind, auditExportArtifactRef(input.AuditExportArtifact), at); err != nil {
			return nil, err
		}
		return result, nil
	})
}

func (s *SQLAuditExportStore) Publish(ctx context.Context, input *store.AuditExportPublication) error {
	if input == nil ||
		!validAuditExportBuild(&input.AuditExportBuild) ||
		!model.ValidAuditExportContent(input.SizeBytes, input.SHA256) ||
		input.EventCount < 0 ||
		input.EventCount > model.AuditExportMaximumEvents {
		return store.NewErrInvalidInput("audit_export", "publication", nil)
	}

	_, err := runSQLTransaction(ctx, s.GetMaster().Begin, "publish audit export", func(ctx context.Context, tx *sqlxTxWrapper) (bool, error) {
		row, at, err := lockAuditExportBuild(ctx, tx, &input.AuditExportBuild)
		if err != nil {
			return false, err
		}

		if row.State == string(model.ExamExportReady) {
			if row.ArchiveAttemptID.String == input.AttemptID.String() &&
				row.ArchiveSizeBytes.Int64 == input.SizeBytes &&
				row.ArchiveSHA256.String == input.SHA256 {
				return true, nil
			}
			return false, store.NewErrConflict("audit_export", "publication", nil)
		}
		if row.State != string(model.ExamExportQueued) {
			return false, store.NewErrConflict("audit_export", "unavailable", nil)
		}

		ref := auditExportArtifactRef(input.AuditExportArtifact)
		if err = publishExportArtifact(ctx, tx, auditExportKind, ref); err != nil {
			return false, err
		}
		// The frozen query goes with the queued state; the visibility reads
		// recheck stays.
		if _, err = tx.Exec(ctx, `UPDATE audit_exports
			SET state = 'ready', query = NULL, event_count = ?, archive_attempt_id = ?,
				archive_size_bytes = ?, archive_sha256 = ?, ready_at = ?
			WHERE id = ?`, input.EventCount, input.AttemptID.String(), input.SizeBytes, input.SHA256, at,
			input.ExportID.String()); err != nil {
			return false, err
		}
		return true, markExportWriterPublished(ctx, tx, auditExportKind, ref)
	})
	return err
}

func (s *SQLAuditExportStore) FinishWriter(ctx context.Context, a store.AuditExportArtifact) error {
	if !a.ExportID.IsValid() || !a.AttemptID.IsValid() {
		return store.NewErrInvalidInput("audit_export", "artifact", nil)
	}
	return finishExportWriter(ctx, s.SQLStore, auditExportKind, auditExportArtifactRef(a))
}

func (s *SQLAuditExportStore) Reconcile(ctx context.Context, limit int) (int, error) {
	if limit < 1 || limit > 100 {
		return 0, store.NewErrInvalidInput("audit_export", "limit", nil)
	}
	return runSQLTransaction(ctx, s.GetMaster().Begin, "expire audit exports", func(ctx context.Context, tx *sqlxTxWrapper) (int, error) {
		var ids []string
		if err := tx.Select(ctx, &ids, `SELECT e.id FROM audit_exports e
			WHERE (e.state IN ('queued', 'ready', 'failed') AND e.expires_at <= clock_timestamp())
				OR (
					e.state = 'queued'
					AND NOT EXISTS(
						SELECT 1
						FROM jobs j
						WHERE j.id = e.job_id
							AND j.status IN ('queued', 'running', 'cancel_requested')
					)
				)
			ORDER BY e.expires_at, e.id
			LIMIT ?
			FOR UPDATE OF e SKIP LOCKED`, limit); err != nil {
			return 0, err
		}
		for _, id := range ids {
			if _, err := tx.Exec(ctx, `UPDATE audit_exports
				SET state = CASE
					WHEN expires_at <= clock_timestamp() THEN 'expired'
					ELSE 'failed'
				END,
					query = NULL, archive_attempt_id = NULL, archive_size_bytes = NULL,
					archive_sha256 = NULL, ready_at = NULL
				WHERE id = ?`, id); err != nil {
				return 0, err
			}
		}
		return len(ids), nil
	})
}

func (s *SQLAuditExportStore) BeginPurgeBatch(ctx context.Context, limit int) ([]store.AuditExportArtifact, error) {
	if limit < 1 || limit > 100 {
		return nil, store.NewErrInvalidInput("audit_export", "limit", nil)
	}
	rows, err := beginExportPurgeBatch(ctx, s.SQLStore, auditExportKind, limit, func(r exportArtifactRef) bool {
		return model.AuditExportID(r.ExportID).IsValid() && model.JobAttemptID(r.AttemptID).IsValid()
	})
	if err != nil {
		return nil, err
	}
	result := make([]store.AuditExportArtifact, 0, len(rows))
	for _, r := range rows {
		result = append(result, store.AuditExportArtifact{ExportID: model.AuditExportID(r.ExportID), AttemptID: model.JobAttemptID(r.AttemptID)})
	}
	return result, nil
}

func (s *SQLAuditExportStore) CompletePurge(ctx context.Context, a store.AuditExportArtifact) error {
	if !a.ExportID.IsValid() || !a.AttemptID.IsValid() {
		return store.NewErrInvalidInput("audit_export", "artifact", nil)
	}
	return completeExportPurge(ctx, s.SQLStore, auditExportKind, auditExportArtifactRef(a))
}
