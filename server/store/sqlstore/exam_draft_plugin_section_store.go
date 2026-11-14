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
	"slices"

	"github.com/sudosylabs/proctor/server/model"
	"github.com/sudosylabs/proctor/server/store"
)

// UpdateDraftPluginSection sets or removes one plugin's section on the Draft
// under the Manager and Draft revision fences, and completes the audit and
// the idempotent outcome in the same transaction.
func (s SQLExamAuthoringStore) UpdateDraftPluginSection(
	ctx context.Context,
	input *store.ExamDraftPluginSectionUpdate,
	command *store.CommandIdempotency,
) (*store.ExamAuthoringCommandResult, error) {
	prepared, err := prepareExamDraftPluginSectionUpdate(input)
	if err != nil {
		return nil, err
	}
	if command == nil {
		return nil, store.NewErrInvalidInput("exam_draft", "idempotency", nil)
	}

	result, err := runIdempotentMutation(ctx, s.SQLStore, "exam Draft plugin section update", idempotentMutation[*store.ExamAuthoringSnapshot]{
		command:      command,
		auditEventID: prepared.AuditEventID,
		execute: func(ctx context.Context, tx *sqlxTxWrapper) (*store.ExamAuthoringSnapshot, error) {
			return updateExamDraftPluginSection(ctx, tx, prepared)
		},
		encode: encodeExamAuthoringOutcome,
		decode: func(data []byte) (*store.ExamAuthoringSnapshot, error) {
			var outcome examAuthoringOutcome
			if decodeErr := decodeOutcome(data, &outcome); decodeErr != nil {
				return nil, decodeErr
			}
			return outcome.model()
		},
		completeReplay: func(ctx context.Context, tx *sqlxTxWrapper, snapshot *store.ExamAuthoringSnapshot, originalAuditID string) error {
			encoded, encodeErr := model.EncodeAuditData(map[string]any{
				"exam_id":                 snapshot.Exam.ID.String(),
				"plugin_id":               prepared.PluginID.String(),
				"draft_revision":          snapshot.Draft.Revision,
				"idempotency_replayed":    true,
				"original_audit_event_id": originalAuditID,
			})
			if encodeErr != nil {
				return encodeErr
			}
			_, completeErr := completeAuditEvent(ctx, tx, prepared.AuditEventID, model.AuditStatusSuccess, "", encoded, prepared.AuditAt)
			return completeErr
		},
	})
	if err != nil {
		return nil, err
	}
	return &store.ExamAuthoringCommandResult{Value: result.Value, Replayed: result.Replayed}, nil
}

func prepareExamDraftPluginSectionUpdate(input *store.ExamDraftPluginSectionUpdate) (*store.ExamDraftPluginSectionUpdate, error) {
	if input == nil ||
		!input.ExamID.IsValid() ||
		!input.ActorUserID.IsValid() ||
		!input.PluginID.IsValid() ||
		input.ExpectedRevision < 1 ||
		input.UpdatedAt.IsZero() ||
		!input.AuditEventID.IsValid() ||
		input.AuditAt.IsZero() {
		return nil, store.NewErrInvalidInput("exam_draft", "plugin_section_update", nil)
	}
	prepared := *input
	if input.Section != nil {
		canonical, err := model.CanonicalizeExamPluginSection(input.Section)
		if err != nil {
			return nil, store.NewErrInvalidInput("exam_draft", "plugin_section", nil).Wrap(err)
		}
		prepared.Section = canonical
	}
	return &prepared, nil
}

func updateExamDraftPluginSection(
	ctx context.Context,
	tx *sqlxTxWrapper,
	input *store.ExamDraftPluginSectionUpdate,
) (*store.ExamAuthoringSnapshot, error) {
	var row examAuthoringRow
	query := examAuthoringSelect + ` WHERE e.id = ? FOR UPDATE OF e, d`
	if err := tx.Get(ctx, &row, query, input.ActorUserID.String(), input.ExamID.String()); err != nil {
		return nil, translateError("exam", input.ExamID.String(), err)
	}

	snapshot, err := row.model()
	if err != nil {
		return nil, err
	}
	if !snapshot.ActorIsManager && !input.ManagerOverride {
		return nil, store.NewErrNotFound("exam_manager", input.ActorUserID.String())
	}
	if snapshot.Exam.IsArchived() {
		return nil, store.NewErrConflict("exam", "exam_archived", nil)
	}
	if snapshot.Draft.Revision != input.ExpectedRevision {
		return nil, store.NewErrConflict("exam_draft", "exam_draft_revision", nil)
	}

	candidate := *snapshot.Draft
	changed, err := candidate.ApplyPluginSection(input.PluginID, slices.Clone(input.Section), model.TimeUTC(input.UpdatedAt))
	if err != nil {
		return nil, store.NewErrInvalidInput("exam_draft", "plugin_section", nil).Wrap(err)
	}
	if !changed {
		return nil, store.NewErrConflict("exam_draft", "exam_draft_no_changes", nil)
	}

	sections, err := model.EncodeExamPluginSections(candidate.PluginSections)
	if err != nil {
		return nil, err
	}

	result, err := tx.Exec(
		ctx,
		`UPDATE exam_drafts
		SET plugin_sections = ?::jsonb, updated_at = ?, revision = ?
		WHERE exam_id = ?
			AND revision = ?`,
		string(sections),
		candidate.UpdatedAt,
		candidate.Revision,
		input.ExamID.String(),
		input.ExpectedRevision,
	)
	if err != nil {
		return nil, fmt.Errorf("update exam Draft plugin section: %w", translateError("exam_draft", input.ExamID.String(), err))
	}
	if affected, rowsErr := result.RowsAffected(); rowsErr != nil {
		return nil, fmt.Errorf("inspect exam Draft plugin section update: %w", rowsErr)
	} else if affected != 1 {
		return nil, store.NewErrConflict("exam_draft", "exam_draft_revision", nil)
	}

	row.PluginSections = jsonValue(sections)
	row.DraftUpdatedAt, row.DraftRevision = candidate.UpdatedAt, candidate.Revision
	_, present := candidate.PluginSections[input.PluginID]
	auditData, err := model.EncodeAuditData(map[string]any{
		"exam_id":                input.ExamID.String(),
		"plugin_id":              input.PluginID.String(),
		"draft_revision":         candidate.Revision,
		"plugin_section_present": present,
	})
	if err != nil {
		return nil, err
	}
	if _, err = completeAuditEvent(ctx, tx, input.AuditEventID, model.AuditStatusSuccess, "", auditData, input.AuditAt); err != nil {
		return nil, fmt.Errorf("complete exam Draft plugin section audit: %w", err)
	}

	return row.model()
}
