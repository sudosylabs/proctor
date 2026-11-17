// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package sqlstore

import (
	"context"
	"crypto/subtle"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/lib/pq"

	"github.com/sudosylabs/proctor/server/model"
	"github.com/sudosylabs/proctor/server/store"
)

// ReserveObjects checks write access once and reserves every object one batch
// will stage.
func (s *SQLExamAttemptWorkspaceStore) ReserveObjects(
	ctx context.Context,
	input *store.ExamAttemptWorkspaceObjectReservation,
) (*store.ExamAttemptWorkspaceMutationTarget, error) {
	if input == nil ||
		!validAttemptWorkspaceWriteAccess(input.Access) ||
		len(input.ObjectIDs) > model.AttemptWorkspaceBatchMaximumMutations ||
		!distinctAttemptWorkspaceObjectIDs(input.ObjectIDs) {
		return nil, store.NewErrInvalidInput("attempt_workspace_object", "reservation", nil)
	}

	return runSQLTransaction(ctx, s.GetMaster().Begin, "reserve Attempt Workspace objects", func(
		ctx context.Context,
		tx *sqlxTxWrapper,
	) (*store.ExamAttemptWorkspaceMutationTarget, error) {
		target, databaseNow, err := lockAttemptWorkspaceMutationTarget(ctx, tx, input.Access)
		if err != nil {
			return nil, err
		}

		for _, objectID := range input.ObjectIDs {
			if err = reserveAttemptWorkspaceObject(ctx, tx, target.WorkspaceID, objectID, databaseNow); err != nil {
				return nil, err
			}
		}

		return target, nil
	})
}

func distinctAttemptWorkspaceObjectIDs(ids []model.AttemptWorkspaceObjectID) bool {
	seen := make(map[model.AttemptWorkspaceObjectID]struct{}, len(ids))
	for _, id := range ids {
		if !id.IsValid() {
			return false
		}
		if _, repeated := seen[id]; repeated {
			return false
		}
		seen[id] = struct{}{}
	}
	return true
}

func reserveAttemptWorkspaceObject(
	ctx context.Context,
	tx *sqlxTxWrapper,
	workspaceID model.ExamAttemptWorkspaceID,
	objectID model.AttemptWorkspaceObjectID,
	databaseNow time.Time,
) error {
	var row attemptWorkspaceObjectRow
	err := tx.Get(ctx, &row, attemptWorkspaceObjectSelect+` WHERE id = ? FOR UPDATE`, objectID.String())
	if err == nil {
		object, decodeErr := row.model()
		if decodeErr != nil {
			return decodeErr
		}

		if object.WorkspaceID != workspaceID ||
			object.StorageOrigin != model.AttemptWorkspaceStorageAttempt ||
			object.State != model.AttemptWorkspaceObjectStaged ||
			!databaseNow.Before(object.ExpiresAt) {
			return store.NewErrConflict("attempt_workspace_object", "attempt_workspace_object_state", nil)
		}

		return nil
	}

	if !errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("find Attempt Workspace object reservation: %w", err)
	}

	object, err := model.NewStagedAttemptWorkspaceObject(
		objectID,
		workspaceID,
		databaseNow,
		databaseNow.Add(model.AttemptWorkspaceStageLifetime),
	)
	if err != nil {
		return store.NewErrInvalidInput("attempt_workspace_object", "reservation", nil).Wrap(err)
	}

	if _, err = tx.Exec(
		ctx,
		`INSERT INTO exam_attempt_workspace_objects (
			id, workspace_id, admission_revision_id, source_starter_entry_id, storage_origin,
				starter_object_id, state, content_version, media_type, size_bytes, sha256,
				created_at, updated_at, expires_at, reclaim_after, claim_token, claimed_at
		)
		SELECT ?, w.id, w.admission_revision_id, NULL, 'attempt', NULL, 'staged', NULL, NULL,
			NULL, NULL, ?, ?, ?, NULL, NULL, NULL
		FROM exam_attempt_workspaces w
		WHERE w.id = ?`,
		object.ID.String(),
		object.CreatedAt,
		object.UpdatedAt,
		object.ExpiresAt,
		workspaceID.String(),
	); err != nil {
		return fmt.Errorf("reserve Attempt Workspace object: %w", translateError("attempt_workspace_object", object.ID.String(), err))
	}

	return nil
}

// MarkObjectsReady checks write access and the per-file capacity once and
// records the verified content of every object staged for one batch.
func (s *SQLExamAttemptWorkspaceStore) MarkObjectsReady(
	ctx context.Context,
	input *store.ExamAttemptWorkspaceObjectsReady,
) error {
	if input == nil ||
		!validAttemptWorkspaceWriteAccess(input.Access) ||
		len(input.Objects) == 0 ||
		len(input.Objects) > model.AttemptWorkspaceBatchMaximumMutations {
		return store.NewErrInvalidInput("attempt_workspace_object", "ready", nil)
	}

	ids := make([]model.AttemptWorkspaceObjectID, len(input.Objects))
	for index, ready := range input.Objects {
		if !ready.ContentVersion.IsValid() || ready.Content.Validate() != nil {
			return store.NewErrInvalidInput("attempt_workspace_object", "ready", nil)
		}
		ids[index] = ready.ObjectID
	}
	if !distinctAttemptWorkspaceObjectIDs(ids) {
		return store.NewErrInvalidInput("attempt_workspace_object", "ready", nil)
	}

	_, err := runSQLTransaction(ctx, s.GetMaster().Begin, "mark Attempt Workspace objects ready", func(
		ctx context.Context,
		tx *sqlxTxWrapper,
	) (struct{}, error) {
		target, databaseNow, err := lockAttemptWorkspaceMutationTarget(ctx, tx, input.Access)
		if err != nil {
			return struct{}{}, err
		}

		capacity, err := attemptWorkspaceCapacityPolicy(ctx, tx, target.WorkspaceID)
		if err != nil {
			return struct{}{}, err
		}

		for _, ready := range input.Objects {
			if ready.Content.SizeBytes > capacity.WorkspaceMaximumFileBytes {
				return struct{}{}, store.NewErrConflict("attempt_workspace", "attempt_workspace_size_limit", nil)
			}
			if err = markAttemptWorkspaceObjectReady(ctx, tx, target.WorkspaceID, ready, databaseNow); err != nil {
				return struct{}{}, err
			}
		}

		return struct{}{}, nil
	})
	return err
}

func markAttemptWorkspaceObjectReady(
	ctx context.Context,
	tx *sqlxTxWrapper,
	workspaceID model.ExamAttemptWorkspaceID,
	ready store.ExamAttemptWorkspaceReadyObject,
	databaseNow time.Time,
) error {
	var row attemptWorkspaceObjectRow
	if err := tx.Get(
		ctx,
		&row,
		attemptWorkspaceObjectSelect+` WHERE id = ? AND workspace_id = ? FOR UPDATE`,
		ready.ObjectID.String(),
		workspaceID.String(),
	); err != nil {
		return translateError("attempt_workspace_object", ready.ObjectID.String(), err)
	}

	object, err := row.model()
	if err != nil {
		return err
	}
	if object.State != model.AttemptWorkspaceObjectStaged || !databaseNow.Before(object.ExpiresAt) {
		return store.NewErrConflict("attempt_workspace_object", "attempt_workspace_object_state", nil)
	}

	if object.HasContent() {
		if object.ContentVersion != ready.ContentVersion ||
			object.MediaType != ready.Content.MediaType ||
			object.SizeBytes != ready.Content.SizeBytes ||
			subtle.ConstantTimeCompare([]byte(object.SHA256), []byte(strings.ToLower(ready.Content.SHA256))) != 1 {
			return store.NewErrConflict("attempt_workspace_object", "attempt_workspace_object_state", nil)
		}

		return nil
	}

	if err = object.MarkContentReady(
		ready.ContentVersion,
		ready.Content.MediaType,
		ready.Content.SizeBytes,
		ready.Content.SHA256,
		databaseNow,
	); err != nil {
		return store.NewErrInvalidInput("attempt_workspace_object", "content", nil).Wrap(err)
	}

	if _, err = tx.Exec(
		ctx,
		`UPDATE exam_attempt_workspace_objects
		SET content_version = ?, media_type = ?, size_bytes = ?, sha256 = ?, updated_at = ?
		WHERE id = ?
			AND state = 'staged'
			AND content_version IS NULL`,
		object.ContentVersion.String(),
		object.MediaType,
		object.SizeBytes,
		object.SHA256,
		object.UpdatedAt,
		object.ID.String(),
	); err != nil {
		return fmt.Errorf("mark Attempt Workspace object ready: %w", err)
	}

	return nil
}

// attemptWorkspaceBatchOutcome is the retained result of one batch. It keeps
// what the store decided for each mutation; the paths are the batch's own,
// which an exact replay repeats, so they are not stored again. Short keys
// keep a full batch well inside the outcome bound.
type attemptWorkspaceBatchOutcome struct {
	AuthorKind         string                             `json:"r"`
	AuthorRef          string                             `json:"a"`
	SittingID          string                             `json:"s"`
	ClassID            string                             `json:"c"`
	CandidateID        string                             `json:"u"`
	WorkspaceID        string                             `json:"w"`
	ChangedAt          time.Time                          `json:"t"`
	Items              []attemptWorkspaceBatchOutcomeItem `json:"i"`
	ProtectedObjectIDs []string                           `json:"o,omitempty"`
}

type attemptWorkspaceBatchOutcomeItem struct {
	EntryID string `json:"e"`
	Kind    string `json:"k"`
	Cursor  int64  `json:"n"`
	Version string `json:"v,omitempty"`
}

func (attemptWorkspaceBatchOutcome) outcomeDocument() {}

// ApplyBatch applies every mutation of one batch in order, all or nothing.
func (s *SQLExamAttemptWorkspaceStore) ApplyBatch(
	ctx context.Context,
	input *store.ExamAttemptWorkspaceBatch,
	command *store.CommandIdempotency,
) (*store.ExamAttemptWorkspaceBatchResult, error) {
	prepared, err := prepareAttemptWorkspaceBatch(input, command)
	if err != nil {
		return nil, err
	}

	result, err := runIdempotentMutation(
		ctx,
		s.SQLStore,
		"apply Attempt Workspace batch",
		idempotentMutation[attemptWorkspaceBatchOutcome]{
			command:      command,
			auditEventID: prepared.AuditEventID,
			execute: func(ctx context.Context, tx *sqlxTxWrapper) (attemptWorkspaceBatchOutcome, error) {
				return applyAttemptWorkspaceBatch(ctx, tx, prepared, command)
			},
			encode: func(outcome attemptWorkspaceBatchOutcome) ([]byte, error) {
				return encodeOutcome(outcome)
			},
			decode: func(encoded []byte) (attemptWorkspaceBatchOutcome, error) {
				var outcome attemptWorkspaceBatchOutcome
				if decodeErr := decodeOutcome(encoded, &outcome); decodeErr != nil {
					return outcome, decodeErr
				}
				if _, resultErr := attemptWorkspaceBatchResult(outcome, prepared.Mutations); resultErr != nil {
					return outcome, fmt.Errorf("invalid Attempt Workspace batch outcome: %w", resultErr)
				}
				for _, value := range outcome.ProtectedObjectIDs {
					if _, parseErr := model.ParseAttemptWorkspaceObjectID(value); parseErr != nil {
						return outcome, fmt.Errorf("invalid Attempt Workspace batch outcome object: %w", parseErr)
					}
				}

				return outcome, nil
			},
			completeReplay: func(ctx context.Context, tx *sqlxTxWrapper, outcome attemptWorkspaceBatchOutcome, originalAuditID string) error {
				target, _, lockErr := lockAttemptWorkspaceMutationTarget(ctx, tx, prepared.Access)
				if lockErr != nil {
					return lockErr
				}

				if outcome.AuthorKind != string(prepared.Access.WorkspaceAuthor().Kind) ||
					target.SittingID.String() != outcome.SittingID ||
					target.ClassID.String() != outcome.ClassID ||
					target.CandidateUserID.String() != outcome.CandidateID ||
					target.WorkspaceID.String() != outcome.WorkspaceID {
					return store.NewErrNotFound("attempt_workspace_access", prepared.Access.WorkspaceAttemptID().String())
				}

				return completeAttemptWorkspaceBatchAudit(ctx, tx, outcome, prepared, true, originalAuditID)
			},
		},
	)
	if err != nil {
		return nil, err
	}

	response, err := attemptWorkspaceBatchResult(result.Value, prepared.Mutations)
	if err != nil {
		return nil, err
	}

	response.Replayed = result.Replayed
	return response, nil
}

func prepareAttemptWorkspaceBatch(
	input *store.ExamAttemptWorkspaceBatch,
	command *store.CommandIdempotency,
) (*store.ExamAttemptWorkspaceBatch, error) {
	if input == nil ||
		command == nil ||
		command.Operation != store.ExamAttemptWorkspaceBatchOperation ||
		!command.UserID.IsValid() ||
		!validAttemptWorkspaceWriteAccess(input.Access) ||
		len(input.Mutations) == 0 ||
		len(input.Mutations) > model.AttemptWorkspaceBatchMaximumMutations ||
		!input.AuditEventID.IsValid() ||
		input.AuditAt.IsZero() {
		return nil, store.NewErrInvalidInput("attempt_workspace", "batch", nil)
	}
	// An editor names its candidate; an environment's candidate is resolved
	// under the batch's locks and checked there.
	if editor, ok := input.Access.(store.ExamAttemptWorkspaceEditorAccess); ok && command.UserID != editor.CandidateUserID {
		return nil, store.NewErrInvalidInput("attempt_workspace", "batch", nil)
	}

	prepared := *input
	prepared.Mutations = make([]store.ExamAttemptWorkspaceMutation, len(input.Mutations))
	recursive := false
	objects := make([]model.AttemptWorkspaceObjectID, 0, len(input.Mutations))
	for index, mutation := range input.Mutations {
		normalized, err := prepareAttemptWorkspaceMutation(mutation)
		if err != nil {
			return nil, &store.ErrAttemptWorkspaceBatchItem{Index: index, Err: err}
		}
		prepared.Mutations[index] = normalized
		recursive = recursive || normalized.Recursive
		if !normalized.ObjectID.IsZero() {
			objects = append(objects, normalized.ObjectID)
		}
	}

	if !distinctAttemptWorkspaceObjectIDs(objects) {
		return nil, store.NewErrInvalidInput("attempt_workspace", "batch_objects", nil)
	}

	if recursive != (input.ExpectedWorkspaceCursor != nil) ||
		(input.ExpectedWorkspaceCursor != nil && *input.ExpectedWorkspaceCursor < 0) {
		return nil, store.NewErrInvalidInput("attempt_workspace", "recursive_delete", nil)
	}
	if input.ExpectedWorkspaceCursor != nil {
		cursor := *input.ExpectedWorkspaceCursor
		prepared.ExpectedWorkspaceCursor = &cursor
	}

	return &prepared, nil
}

func prepareAttemptWorkspaceMutation(input store.ExamAttemptWorkspaceMutation) (store.ExamAttemptWorkspaceMutation, error) {
	prepared := input
	if !prepared.EntryID.IsValid() {
		return prepared, store.NewErrInvalidInput("attempt_workspace", "mutation", nil)
	}
	if prepared.Recursive &&
		(prepared.Operation != model.AttemptWorkspaceMutationDeleteEntry || !prepared.ExpectedContentVersion.IsZero()) {
		return prepared, store.NewErrInvalidInput("attempt_workspace", "recursive_delete", nil)
	}

	normalize := func(value string) (string, bool) {
		path, normalizeErr := model.NormalizeAttemptWorkspacePath(value)
		return path, normalizeErr == nil && path == value
	}
	switch prepared.Operation {
	case model.AttemptWorkspaceMutationCreateFile:
		path, valid := normalize(prepared.DestinationPath)
		if !valid || prepared.ExpectedPath != "" || !prepared.ExpectedContentVersion.IsZero() || !prepared.ObjectID.IsValid() {
			return prepared, store.NewErrInvalidInput("attempt_workspace", "create_file", nil)
		}

		prepared.DestinationPath = path
	case model.AttemptWorkspaceMutationCreateDirectory:
		path, valid := normalize(prepared.DestinationPath)
		if !valid || prepared.ExpectedPath != "" || !prepared.ExpectedContentVersion.IsZero() || !prepared.ObjectID.IsZero() {
			return prepared, store.NewErrInvalidInput("attempt_workspace", "create_directory", nil)
		}

		prepared.DestinationPath = path
	case model.AttemptWorkspaceMutationReplaceFile:
		path, valid := normalize(prepared.ExpectedPath)
		if !valid || prepared.DestinationPath != "" || !prepared.ExpectedContentVersion.IsValid() || !prepared.ObjectID.IsValid() {
			return prepared, store.NewErrInvalidInput("attempt_workspace", "replace_file", nil)
		}

		prepared.ExpectedPath = path
	case model.AttemptWorkspaceMutationMoveEntry:
		oldPath, oldValid := normalize(prepared.ExpectedPath)
		newPath, newValid := normalize(prepared.DestinationPath)
		if !oldValid || !newValid || oldPath == newPath || !prepared.ExpectedContentVersion.IsZero() || !prepared.ObjectID.IsZero() {
			return prepared, store.NewErrInvalidInput("attempt_workspace", "move_entry", nil)
		}

		prepared.ExpectedPath, prepared.DestinationPath = oldPath, newPath
	case model.AttemptWorkspaceMutationDeleteEntry:
		path, valid := normalize(prepared.ExpectedPath)
		if !valid ||
			prepared.DestinationPath != "" ||
			!prepared.ObjectID.IsZero() ||
			(!prepared.ExpectedContentVersion.IsZero() && !prepared.ExpectedContentVersion.IsValid()) {
			return prepared, store.NewErrInvalidInput("attempt_workspace", "delete_entry", nil)
		}

		prepared.ExpectedPath = path
	default:
		return prepared, store.NewErrInvalidInput("attempt_workspace", "operation", nil)
	}

	return prepared, nil
}

func applyAttemptWorkspaceBatch(
	ctx context.Context,
	tx *sqlxTxWrapper,
	input *store.ExamAttemptWorkspaceBatch,
	command *store.CommandIdempotency,
) (attemptWorkspaceBatchOutcome, error) {
	var zero attemptWorkspaceBatchOutcome
	target, databaseNow, err := lockAttemptWorkspaceMutationTarget(ctx, tx, input.Access)
	if err != nil {
		return zero, err
	}
	// The outcome is the candidate's, whoever wrote on their behalf.
	if command.UserID != target.CandidateUserID {
		return zero, store.NewErrInvalidInput("attempt_workspace", "batch", nil)
	}

	var workspace struct {
		AdmissionRevisionID string `db:"admission_revision_id"`
		Cursor              int64  `db:"cursor"`
	}
	if err = tx.Get(ctx, &workspace, `SELECT admission_revision_id, cursor
		FROM exam_attempt_workspaces
		WHERE id = ?
		FOR UPDATE`, target.WorkspaceID.String()); err != nil {
		return zero, fmt.Errorf("lock Attempt Workspace: %w", err)
	}

	if input.ExpectedWorkspaceCursor != nil && workspace.Cursor != *input.ExpectedWorkspaceCursor {
		return zero, store.NewErrConflict("attempt_workspace", "attempt_workspace_cursor", nil)
	}

	revisionID, err := model.ParseExamRevisionID(workspace.AdmissionRevisionID)
	if err != nil {
		return zero, invalidPersistedState("attempt_workspace", "admission_revision_id", err)
	}

	scope := attemptWorkspaceMutationScope{
		workspaceID:  target.WorkspaceID,
		revisionID:   revisionID,
		databaseNow:  databaseNow,
		auditEventID: input.AuditEventID,
	}
	author := input.Access.WorkspaceAuthor()
	outcome := attemptWorkspaceBatchOutcome{
		AuthorKind:  string(author.Kind),
		AuthorRef:   author.Ref,
		SittingID:   target.SittingID.String(),
		ClassID:     target.ClassID.String(),
		CandidateID: target.CandidateUserID.String(),
		WorkspaceID: target.WorkspaceID.String(),
		ChangedAt:   databaseNow,
		Items:       make([]attemptWorkspaceBatchOutcomeItem, 0, len(input.Mutations)),
	}
	for index := range input.Mutations {
		mutation := &input.Mutations[index]
		effect := attemptWorkspaceMutationEffect{protected: make([]string, 0, 2)}
		switch mutation.Operation {
		case model.AttemptWorkspaceMutationCreateFile:
			err = createAttemptWorkspaceFile(ctx, tx, mutation, scope, &effect)
		case model.AttemptWorkspaceMutationCreateDirectory:
			err = createAttemptWorkspaceDirectory(ctx, tx, mutation, scope, &effect)
		case model.AttemptWorkspaceMutationReplaceFile:
			err = replaceAttemptWorkspaceFile(ctx, tx, mutation, scope, &effect)
		case model.AttemptWorkspaceMutationMoveEntry:
			err = moveAttemptWorkspaceMutationEntry(ctx, tx, mutation, scope, &effect)
		case model.AttemptWorkspaceMutationDeleteEntry:
			err = deleteAttemptWorkspaceMutationEntry(ctx, tx, mutation, scope, &effect)
		}
		if err != nil {
			return zero, &store.ErrAttemptWorkspaceBatchItem{Index: index, Err: err}
		}

		change, appendErr := appendAttemptWorkspaceJournal(
			ctx,
			tx,
			target.WorkspaceID,
			mutation.EntryID,
			effect.entryKind,
			mutation.Operation,
			effect.oldPath,
			effect.newPath,
			effect.version,
			mutation.Recursive,
			author,
			command.KeyDigest[:],
			databaseNow,
		)
		if appendErr != nil {
			return zero, appendErr
		}

		outcome.Items = append(outcome.Items, attemptWorkspaceBatchOutcomeItem{
			EntryID: mutation.EntryID.String(),
			Kind:    string(effect.entryKind),
			Cursor:  change.Cursor,
			Version: effect.version.String(),
		})
		outcome.ProtectedObjectIDs = append(outcome.ProtectedObjectIDs, effect.protected...)
	}

	if author.Kind == model.AttemptWorkspaceAuthorCandidate {
		if err = matchAssistantProposals(ctx, tx, input.Access.WorkspaceAttemptID(), input.Mutations); err != nil {
			return zero, err
		}
	}

	if err = trimAttemptWorkspaceJournal(ctx, tx, target.WorkspaceID); err != nil {
		return zero, err
	}
	if err = ensureAttemptWorkspaceCapacity(ctx, tx, target.WorkspaceID); err != nil {
		return zero, err
	}
	if err = completeAttemptWorkspaceBatchAudit(ctx, tx, outcome, input, false, ""); err != nil {
		return zero, err
	}

	return outcome, nil
}

// completeAttemptWorkspaceBatchAudit records what the batch did in aggregate:
// counts by operation, the cursor range and the writer's author, never paths
// or content. An environment batch also names its binding.
func completeAttemptWorkspaceBatchAudit(
	ctx context.Context,
	tx *sqlxTxWrapper,
	outcome attemptWorkspaceBatchOutcome,
	batch *store.ExamAttemptWorkspaceBatch,
	replayed bool,
	originalAuditID string,
) error {
	counts := make(map[string]int, 5)
	for _, mutation := range batch.Mutations {
		counts[string(mutation.Operation)]++
	}

	data := map[string]any{
		"exam_sitting_id":        outcome.SittingID,
		"candidate_user_id":      outcome.CandidateID,
		"workspace_id":           outcome.WorkspaceID,
		"mutation_count":         len(outcome.Items),
		"operation_counts":       counts,
		"first_workspace_cursor": outcome.Items[0].Cursor,
		"last_workspace_cursor":  outcome.Items[len(outcome.Items)-1].Cursor,
		"author_kind":            outcome.AuthorKind,
		"author_ref":             outcome.AuthorRef,
	}
	if environment, ok := batch.Access.(store.ExamAttemptWorkspaceEnvironmentAccess); ok {
		data["environment_lease_fence"] = environment.Fence
	}
	if replayed {
		data["idempotency_replayed"] = true
		data["original_audit_event_id"] = originalAuditID
	}

	encoded, err := model.EncodeAuditData(data)
	if err != nil {
		return err
	}
	if _, err = completeAuditEvent(ctx, tx, batch.AuditEventID, model.AuditStatusSuccess, "", encoded, batch.AuditAt); err != nil {
		return fmt.Errorf("complete Attempt Workspace batch audit: %w", err)
	}

	return nil
}

// attemptWorkspaceBatchResult rebuilds the journal changes of a batch from
// its retained outcome and its own mutations.
func attemptWorkspaceBatchResult(
	outcome attemptWorkspaceBatchOutcome,
	mutations []store.ExamAttemptWorkspaceMutation,
) (*store.ExamAttemptWorkspaceBatchResult, error) {
	sittingID, err := model.ParseExamSittingID(outcome.SittingID)
	if err != nil {
		return nil, err
	}

	classID, err := model.ParseClassID(outcome.ClassID)
	if err != nil {
		return nil, err
	}

	candidateID, err := model.ParseUserID(outcome.CandidateID)
	if err != nil {
		return nil, err
	}

	workspaceID, err := model.ParseExamAttemptWorkspaceID(outcome.WorkspaceID)
	if err != nil {
		return nil, err
	}

	author := model.AttemptWorkspaceAuthor{Kind: model.AttemptWorkspaceAuthorKind(outcome.AuthorKind), Ref: outcome.AuthorRef}
	if len(outcome.Items) != len(mutations) || outcome.ChangedAt.IsZero() || !author.IsValid() {
		return nil, errors.New("invalid Attempt Workspace batch result")
	}

	changes := make([]model.AttemptWorkspaceJournalEntry, len(mutations))
	for index, item := range outcome.Items {
		mutation := mutations[index]
		entryID, parseErr := model.ParseAttemptWorkspaceEntryID(item.EntryID)
		if parseErr != nil {
			return nil, parseErr
		}

		var version model.WorkspaceContentVersion
		if item.Version != "" {
			if version, parseErr = model.ParseWorkspaceContentVersion(item.Version); parseErr != nil {
				return nil, parseErr
			}
		}

		change := model.AttemptWorkspaceJournalEntry{
			WorkspaceID:    workspaceID,
			Cursor:         item.Cursor,
			EntryID:        entryID,
			EntryKind:      model.StarterWorkspaceEntryKind(item.Kind),
			Operation:      mutation.Operation,
			ContentVersion: version,
			ChangedAt:      model.TimeUTC(outcome.ChangedAt),
			Recursive:      mutation.Recursive,
			Author:         author,
		}
		switch mutation.Operation {
		case model.AttemptWorkspaceMutationCreateFile, model.AttemptWorkspaceMutationCreateDirectory:
			change.NewPath = mutation.DestinationPath
		case model.AttemptWorkspaceMutationReplaceFile:
			change.OldPath, change.NewPath = mutation.ExpectedPath, mutation.ExpectedPath
		case model.AttemptWorkspaceMutationMoveEntry:
			change.OldPath, change.NewPath = mutation.ExpectedPath, mutation.DestinationPath
		case model.AttemptWorkspaceMutationDeleteEntry:
			change.OldPath = mutation.ExpectedPath
		}

		fileContent := change.EntryKind == model.StarterWorkspaceEntryFile &&
			mutation.Operation != model.AttemptWorkspaceMutationDeleteEntry
		if change.Validate() != nil ||
			(index > 0 && change.Cursor != changes[index-1].Cursor+1) ||
			fileContent != change.ContentVersion.IsValid() {
			return nil, errors.New("invalid Attempt Workspace batch change")
		}
		changes[index] = change
	}

	return &store.ExamAttemptWorkspaceBatchResult{
		SittingID:       sittingID,
		ClassID:         classID,
		CandidateUserID: candidateID,
		WorkspaceID:     workspaceID,
		Changes:         changes,
	}, nil
}

// MarkObjectsReclaimable gives back staged objects a failed or replayed batch
// will never consume. Objects that something still references are left
// alone.
func (s *SQLExamAttemptWorkspaceStore) MarkObjectsReclaimable(ctx context.Context, objectIDs []model.AttemptWorkspaceObjectID) error {
	if len(objectIDs) == 0 {
		return nil
	}
	if len(objectIDs) > model.AttemptWorkspaceBatchMaximumMutations || !distinctAttemptWorkspaceObjectIDs(objectIDs) {
		return store.NewErrInvalidInput("attempt_workspace_object", "reclaim", nil)
	}

	ids := make([]string, len(objectIDs))
	for index, id := range objectIDs {
		ids[index] = id.String()
	}

	if _, err := s.GetMaster().Exec(
		ctx,
		`UPDATE exam_attempt_workspace_objects objects
		SET state = 'reclaimable', updated_at = GREATEST(objects.updated_at, statement_timestamp()),
			reclaim_after = statement_timestamp()+(? * INTERVAL '1 millisecond')
		WHERE objects.id = ANY(?)
			AND objects.storage_origin = 'attempt'
			AND objects.state = 'staged'
			AND NOT EXISTS (
				SELECT 1
				FROM exam_attempt_workspace_entries entries
				WHERE entries.current_object_id = objects.id
			)
			AND NOT EXISTS (
				SELECT 1
				FROM exam_submission_manifest_entries submitted
				WHERE submitted.attempt_object_id = objects.id
			)
			AND NOT `+attemptWorkspaceOutcomeProtects+`
			AND NOT EXISTS (
				SELECT 1
				FROM command_outcomes outcomes
				WHERE outcomes.original_audit_event_id = objects.retired_by_audit_event_id
			)`,
		model.AttemptWorkspaceReclaimSafetyWindow.Milliseconds(),
		pq.Array(ids),
	); err != nil {
		return fmt.Errorf("mark Attempt Workspace objects reclaimable: %w", err)
	}

	return nil
}
