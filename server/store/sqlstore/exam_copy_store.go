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
	"slices"
	"strings"
	"time"

	"github.com/sudosylabs/proctor/server/model"
	"github.com/sudosylabs/proctor/server/store"
)

func (s SQLExamAuthoringStore) ReserveCopy(ctx context.Context, input *store.ExamCopyReservation) error {
	if input == nil || input.Exam == nil || input.Draft == nil || input.Manager == nil ||
		input.Exam.Validate() != nil || input.Draft.Validate() != nil || input.Manager.Validate() != nil ||
		input.Draft.ExamID != input.Exam.ID || input.Manager.ExamID != input.Exam.ID ||
		input.Manager.UserID != input.Exam.OwnerUserID ||
		!input.CopyingUntil.After(input.Exam.CreatedAt) ||
		len(input.StarterObjects) > model.StarterWorkspaceMaximumEntries {
		return store.NewErrInvalidInput("exam", "copy_reservation", nil)
	}
	for _, object := range input.StarterObjects {
		if object == nil || object.Validate() != nil || object.ExamID != input.Exam.ID ||
			object.State != model.StarterWorkspaceObjectStaged {
			return store.NewErrInvalidInput("exam", "copy_reservation_object", nil)
		}
	}

	_, err := runSQLTransaction(ctx, s.GetMaster().Begin, "reserve exam copy", func(ctx context.Context, tx *sqlxTxWrapper) (struct{}, error) {
		if _, err := insertExamAggregate(ctx, tx, input.Exam, input.Draft, input.Manager,
			sql.NullTime{Time: model.TimeUTC(input.CopyingUntil), Valid: true}); err != nil {
			return struct{}{}, err
		}
		for _, object := range input.StarterObjects {
			if _, err := tx.Exec(ctx, `INSERT INTO exam_starter_workspace_objects (
					id, exam_id, created_by_user_id, created_at, updated_at, expires_at, state
				)
				VALUES (?, ?, ?, ?, ?, ?, 'staged')`,
				object.ID.String(), object.ExamID.String(), object.CreatedByUserID.String(),
				object.CreatedAt, object.UpdatedAt, object.ExpiresAt); err != nil {
				return struct{}{}, fmt.Errorf("reserve exam copy object: %w",
					translateError("exam_starter_workspace_object", object.ID.String(), err))
			}
		}
		return struct{}{}, nil
	})
	return err
}

func (s SQLExamAuthoringStore) FindCopy(ctx context.Context, command *store.CommandIdempotency) (*store.ExamAuthoringSnapshot, error) {
	result, err := replayIdempotentMutation(ctx, s.SQLStore, "find exam copy", idempotentMutation[*store.ExamAuthoringSnapshot]{
		command:           command,
		decode:            decodeExamCopyOutcome,
		freshAuditEventID: func(*store.ExamAuthoringSnapshot) (model.AuditEventID, error) { return "", nil },
	})
	if err != nil {
		return nil, err
	}
	return result.Value, nil
}

func decodeExamCopyOutcome(data []byte) (*store.ExamAuthoringSnapshot, error) {
	var outcome examAuthoringOutcome
	if err := decodeOutcome(data, &outcome); err != nil {
		return nil, err
	}
	return outcome.model()
}

func (s SQLExamAuthoringStore) CommitCopy(
	ctx context.Context,
	input *store.ExamCopyCommit,
	command *store.CommandIdempotency,
) (*store.ExamAuthoringCommandResult, error) {
	if err := validateExamCopyCommit(input); err != nil || command == nil {
		if err != nil {
			return nil, err
		}
		return nil, store.NewErrInvalidInput("exam", "idempotency", nil)
	}

	result, err := runIdempotentMutation(ctx, s.SQLStore, "exam copy", idempotentMutation[*store.ExamAuthoringSnapshot]{
		command:      command,
		auditEventID: input.CreateAuditEventID,
		execute: func(ctx context.Context, tx *sqlxTxWrapper) (*store.ExamAuthoringSnapshot, error) {
			return commitExamCopy(ctx, tx, input)
		},
		encode: encodeExamAuthoringOutcome,
		decode: decodeExamCopyOutcome,
		completeReplay: func(ctx context.Context, tx *sqlxTxWrapper, snapshot *store.ExamAuthoringSnapshot, originalAuditID string) error {
			for _, auditID := range []model.AuditEventID{input.CreateAuditEventID, input.SourceAuditEventID} {
				encoded, encodeErr := model.EncodeAuditData(map[string]any{
					"exam_id":                 snapshot.Exam.ID.String(),
					"source_exam_id":          input.SourceExamID.String(),
					"idempotency_replayed":    true,
					"original_audit_event_id": originalAuditID,
				})
				if encodeErr != nil {
					return encodeErr
				}
				if _, completeErr := completeAuditEvent(ctx, tx, auditID, model.AuditStatusSuccess, "", encoded, input.AuditAt); completeErr != nil {
					return completeErr
				}
			}
			return nil
		},
	})
	if err != nil {
		return nil, err
	}

	return &store.ExamAuthoringCommandResult{Value: result.Value, Replayed: result.Replayed}, nil
}

func validateExamCopyCommit(input *store.ExamCopyCommit) error {
	if input == nil || !input.SourceExamID.IsValid() || !input.ExamID.IsValid() || input.SourceExamID == input.ExamID ||
		input.ExpectedSourceDraftRevision < 1 ||
		!input.ActorUserID.IsValid() || input.ChangedAt.IsZero() || input.AuditAt.IsZero() ||
		!input.CreateAuditEventID.IsValid() || !input.SourceAuditEventID.IsValid() ||
		input.CreateAuditEventID == input.SourceAuditEventID ||
		len(input.Resources) > model.ExamResourceMaximumCount ||
		len(input.StarterEntries) > model.StarterWorkspaceMaximumEntries {
		return store.NewErrInvalidInput("exam", "copy", nil)
	}
	for index, resource := range input.Resources {
		if resource.Resource == nil || resource.Rendition == nil || resource.Resource.Validate() != nil ||
			resource.Rendition.Validate() != nil || resource.Resource.ExamID != input.ExamID ||
			resource.Resource.Position != index || !resource.LeaseID.IsValid() ||
			resource.Resource.SelectedFileRevisionID != resource.Rendition.RevisionID {
			return store.NewErrInvalidInput("exam", "copy_resource", nil)
		}
	}
	for _, entry := range input.StarterEntries {
		if entry.Entry == nil || entry.Entry.Validate() != nil || entry.Entry.ExamID != input.ExamID ||
			(entry.Entry.Kind == model.StarterWorkspaceEntryFile) != entry.Entry.CurrentObjectID.IsValid() {
			return store.NewErrInvalidInput("exam", "copy_starter_entry", nil)
		}
	}
	return nil
}

// commitExamCopy holds the source under a share lock while it rechecks the
// manager relationship, so a concurrent removal waits; the copy itself is
// locked as the row it publishes.
func commitExamCopy(ctx context.Context, tx *sqlxTxWrapper, input *store.ExamCopyCommit) (*store.ExamAuthoringSnapshot, error) {
	var source examAccessRow
	if err := tx.Get(ctx, &source, examAccessSelect+` FOR SHARE OF e`, input.ActorUserID.String(), input.SourceExamID.String()); err != nil {
		return nil, translateError("exam", input.SourceExamID.String(), err)
	}
	if !source.ActorIsManager && !input.SourceManagerOverride {
		return nil, store.NewErrNotFound("exam_manager", input.ActorUserID.String())
	}

	var sourceDraftRevision int64
	if err := tx.Get(ctx, &sourceDraftRevision, `SELECT revision FROM exam_drafts WHERE exam_id = ? FOR SHARE`,
		input.SourceExamID.String()); err != nil {
		return nil, translateError("exam_draft", input.SourceExamID.String(), err)
	}
	if sourceDraftRevision != input.ExpectedSourceDraftRevision {
		return nil, store.NewErrConflict("exam", "exam_copy_source_changed", nil)
	}

	var reservation struct {
		CopyingUntil sql.NullTime `db:"copying_until"`
		Now          time.Time    `db:"now"`
	}
	if err := tx.Get(ctx, &reservation, `SELECT copying_until, statement_timestamp() AS now
		FROM exams
		WHERE id = ?
		FOR UPDATE`, input.ExamID.String()); err != nil {
		return nil, translateError("exam", input.ExamID.String(), err)
	}
	if !reservation.CopyingUntil.Valid || !reservation.Now.Before(reservation.CopyingUntil.Time) {
		return nil, store.NewErrConflict("exam", "exam_copy_lapsed", nil)
	}

	capacity, err := currentExamCapacityPolicy(ctx, tx)
	if err != nil {
		return nil, err
	}
	if exceedsCopyCapacity(capacity, input) {
		return nil, store.NewErrConflict("exam_revision", "exam_revision_capacity", nil)
	}

	for _, copied := range input.Resources {
		finalization := &store.ExamResourceUploadFinalization{
			ExamID: input.ExamID, ActorUserID: input.ActorUserID, Resource: copied.Resource,
			LeaseID: copied.LeaseID, Rendition: copied.Rendition, ChangedAt: input.ChangedAt,
		}
		if err = lockExamResourceUploadLease(ctx, tx, finalization); err != nil {
			return nil, err
		}
		if err = makeExamResourceUploadCurrent(ctx, tx, finalization); err != nil {
			return nil, err
		}
		resource := copied.Resource
		if _, err = tx.Exec(ctx, `INSERT INTO exam_resource_identities (id, exam_id, file_entry_id) VALUES (?, ?, ?)`,
			resource.ID.String(), input.ExamID.String(), resource.FileEntryID.String()); err != nil {
			return nil, fmt.Errorf("copy exam resource identity: %w", translateError("exam_resource", resource.ID.String(), err))
		}
		if _, err = tx.Exec(ctx, `INSERT INTO exam_resources (
				id, exam_id, file_entry_id, selected_file_revision_id, display_name,
				description_markdown, position, created_at, updated_at, archived_at
			)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, NULL)`,
			resource.ID.String(), input.ExamID.String(), resource.FileEntryID.String(),
			copied.Rendition.RevisionID.String(), resource.DisplayName, resource.DescriptionMarkdown,
			resource.Position, resource.CreatedAt, input.ChangedAt); err != nil {
			return nil, fmt.Errorf("copy exam resource: %w", translateError("exam_resource", resource.ID.String(), err))
		}
	}

	// Parents sort before their children, so every entry's parent exists
	// when it is inserted.
	entries := slices.Clone(input.StarterEntries)
	slices.SortFunc(entries, func(a, b store.ExamCopyStarterEntry) int { return strings.Compare(a.Entry.Path, b.Entry.Path) })
	for _, copied := range entries {
		if copied.Entry.Kind == model.StarterWorkspaceEntryFile {
			if _, err = consumeStarterWorkspaceObject(ctx, tx, &store.ExamStarterWorkspaceMutation{
				ExamID: input.ExamID, ObjectID: copied.Entry.CurrentObjectID, ContentVersion: copied.ContentVersion,
				MediaType: copied.MediaType, SizeBytes: copied.SizeBytes, SHA256: copied.SHA256, ChangedAt: input.ChangedAt,
			}); err != nil {
				return nil, err
			}
		}
		if err = insertStarterWorkspaceEntry(ctx, tx, copied.Entry); err != nil {
			return nil, err
		}
	}

	if _, err = tx.Exec(ctx, `UPDATE exams SET copying_until = NULL WHERE id = ?`, input.ExamID.String()); err != nil {
		return nil, fmt.Errorf("publish exam copy: %w", err)
	}

	var row examAuthoringRow
	if err = tx.Get(ctx, &row, examAuthoringSelect+` AND e.id = ?`, input.ActorUserID.String(), input.ExamID.String()); err != nil {
		return nil, translateError("exam", input.ExamID.String(), err)
	}
	snapshot, err := row.model()
	if err != nil {
		return nil, err
	}

	created := snapshot.Exam.Auditable()
	created["source_exam_id"] = input.SourceExamID.String()
	created["resource_count"], created["starter_entry_count"] = len(input.Resources), len(input.StarterEntries)
	createData, err := model.EncodeAuditData(created)
	if err != nil {
		return nil, err
	}
	sourceData, err := model.EncodeAuditData(map[string]any{
		"exam_id": input.SourceExamID.String(), "copy_exam_id": input.ExamID.String(),
	})
	if err != nil {
		return nil, err
	}
	if _, err = completeAuditEvent(ctx, tx, input.CreateAuditEventID, model.AuditStatusSuccess, "", createData, input.AuditAt); err != nil {
		return nil, fmt.Errorf("complete exam copy audit: %w", err)
	}
	if _, err = completeAuditEvent(ctx, tx, input.SourceAuditEventID, model.AuditStatusSuccess, "", sourceData, input.AuditAt); err != nil {
		return nil, fmt.Errorf("complete exam copy source audit: %w", err)
	}

	return snapshot, nil
}

func exceedsCopyCapacity(capacity model.ExamCapacityPolicy, input *store.ExamCopyCommit) bool {
	if len(input.Resources) > capacity.ResourceMaximumCount || len(input.StarterEntries) > capacity.WorkspaceMaximumEntries {
		return true
	}
	for _, resource := range input.Resources {
		if resource.Rendition.Size > capacity.ResourceMaximumBytes {
			return true
		}
	}
	var total int64
	for _, entry := range input.StarterEntries {
		if entry.SizeBytes > capacity.WorkspaceMaximumFileBytes {
			return true
		}
		total += entry.SizeBytes
	}
	return total > capacity.WorkspaceMaximumTotalBytes
}

// DeleteLapsedExamCopies removes copies whose reservation lapsed. It waits for
// the purge job to remove their staged objects first, so no bytes lose their
// record; deleting the exam takes its draft and manager with it.
func (s SQLExamStarterWorkspaceStore) DeleteLapsedExamCopies(ctx context.Context, limit int) (int, error) {
	if limit < 1 || limit > 100 {
		return 0, store.NewErrInvalidInput("exam", "lapsed_copies", nil)
	}
	result, err := s.GetMaster().Exec(ctx, `WITH lapsed AS (
			SELECT e.id
			FROM exams e
			WHERE e.copying_until IS NOT NULL
				AND e.copying_until <= CURRENT_TIMESTAMP
				AND NOT EXISTS (
					SELECT 1 FROM exam_starter_workspace_objects o WHERE o.exam_id = e.id
				)
			ORDER BY e.copying_until, e.id
			LIMIT ?
			FOR UPDATE SKIP LOCKED
		)
		DELETE FROM exams WHERE id IN (SELECT id FROM lapsed)`, limit)
	if err != nil {
		return 0, fmt.Errorf("delete lapsed exam copies: %w", err)
	}
	deleted, err := result.RowsAffected()
	if err != nil {
		return 0, errors.Join(errors.New("inspect lapsed exam copies"), err)
	}
	return int(deleted), nil
}
