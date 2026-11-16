// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package attempt

import (
	"context"
	"errors"
	"io"
	"strconv"
	"strings"

	"github.com/sudosylabs/proctor/server/app/access"
	"github.com/sudosylabs/proctor/server/app/apperror"
	"github.com/sudosylabs/proctor/server/app/invocation"
	"github.com/sudosylabs/proctor/server/model"
	"github.com/sudosylabs/proctor/server/store"
)

// WorkspaceMutationAccess is CandidateAccess fenced to one participation
// generation, so a superseded participation cannot change the workspace.
type WorkspaceMutationAccess struct {
	CandidateAccess
	ParticipationID model.AttemptParticipationID
	Generation      int64
}

// WorkspaceBatchMutation is one change in a workspace batch.
//
//   - create_file: DestinationPath, MediaType, SHA256 and Size; the batch's
//     content supplies the body.
//   - create_directory: DestinationPath.
//   - replace_file: EntryID, ExpectedPath, ExpectedContentVersion, MediaType,
//     SHA256 and Size; the batch's content supplies the body.
//   - move_entry: EntryID, ExpectedPath and DestinationPath.
//   - delete_entry: EntryID and ExpectedPath, plus ExpectedContentVersion for
//     a file. Recursive deletes a directory and everything under it, and
//     needs the batch's ExpectedWorkspaceCursor.
//
// A create names its entry by path; the server chooses its EntryID, so a
// later mutation in the same batch reaches it by path, not by ID.
type WorkspaceBatchMutation struct {
	Operation              model.AttemptWorkspaceMutationKind
	EntryID                model.AttemptWorkspaceEntryID
	ExpectedPath           string
	DestinationPath        string
	ExpectedContentVersion model.WorkspaceContentVersion
	Recursive              bool
	MediaType              string
	SHA256                 string
	Size                   int64
}

func (m WorkspaceBatchMutation) carriesContent() bool {
	return m.Operation == model.AttemptWorkspaceMutationCreateFile || m.Operation == model.AttemptWorkspaceMutationReplaceFile
}

// WorkspaceBatchContent supplies the bodies of a batch's file mutations, one
// per create_file or replace_file, in batch order. Next is called at most once
// per file mutation; the returned reader is read to its declared size before
// Next is called again.
type WorkspaceBatchContent interface {
	Next(context.Context) (io.Reader, error)
}

// ApplyWorkspaceBatchCommand carries 1..AttemptWorkspaceBatchMaximumMutations
// mutations, applied all or nothing.
type ApplyWorkspaceBatchCommand struct {
	Access                  WorkspaceMutationAccess
	Mutations               []WorkspaceBatchMutation
	ExpectedWorkspaceCursor *int64
	Content                 WorkspaceBatchContent
	IdempotencyKey          string
}

// WorkspaceBatchResult holds one journal change per mutation, at consecutive
// cursors. Replayed is true when an exact retry returned the stored result.
type WorkspaceBatchResult struct {
	AttemptID       model.ExamAttemptID
	SittingID       model.ExamSittingID
	CandidateUserID model.UserID
	WorkspaceID     model.ExamAttemptWorkspaceID
	Changes         []model.AttemptWorkspaceJournalEntry
	Replayed        bool
}

// ApplyCandidateExamWorkspaceBatch is the candidate's own write from the
// editor. A failure names the refusing mutation in the "item" field, and
// nothing changes.
func (s *Service) ApplyCandidateExamWorkspaceBatch(
	ctx context.Context,
	inv invocation.Invocation,
	command ApplyWorkspaceBatchCommand,
) (_ WorkspaceBatchResult, resultErr error) {
	defer func() { resultErr = publicFailures.Conceal(resultErr) }()
	workspaceAccess, err := workspaceMutationSelector(inv, command.Access)
	if err != nil {
		return WorkspaceBatchResult{}, err
	}

	return s.applyWorkspaceWrite(ctx, workspaceWriter{
		access:    workspaceAccess,
		candidate: workspaceAccess.CandidateUserID,
		beginAudit: func(ctx context.Context, target *store.ExamAttemptWorkspaceMutationTarget, value map[string]any) (model.AuditEventID, error) {
			return s.auditor.Begin(ctx, access.Mutation{
				Invocation: inv,
				Action:     model.ActionExamSittingParticipate,
				Resource:   model.Resource{Type: model.ResourceExamSitting, ID: target.SittingID.String()},
				ScopeType:  model.RoleScopeClass,
				ScopeID:    target.ClassID.String(),
				Operation:  store.ExamAttemptWorkspaceBatchOperation,
				Value:      value,
			})
		},
	}, workspaceWrite{
		Mutations:               command.Mutations,
		ExpectedWorkspaceCursor: command.ExpectedWorkspaceCursor,
		Content:                 command.Content,
		IdempotencyKey:          command.IdempotencyKey,
	})
}

// workspaceWriter is who writes a batch. Both kinds write as the candidate;
// they differ in how the store proves them and how the audit records the
// candidate. The environment names no candidate until the store resolves one.
type workspaceWriter struct {
	access    store.ExamAttemptWorkspaceWriteAccess
	candidate model.UserID
	// resources size the budget; zero for the editor.
	resources  model.EnvironmentResources
	beginAudit func(context.Context, *store.ExamAttemptWorkspaceMutationTarget, map[string]any) (model.AuditEventID, error)
}

type workspaceWrite struct {
	Mutations               []WorkspaceBatchMutation
	ExpectedWorkspaceCursor *int64
	Content                 WorkspaceBatchContent
	IdempotencyKey          string
}

// applyWorkspaceWrite is the one batch write path for every writer.
func (s *Service) applyWorkspaceWrite(ctx context.Context, writer workspaceWriter, write workspaceWrite) (WorkspaceBatchResult, error) {
	attemptID := writer.access.WorkspaceAttemptID()
	if len(write.Mutations) == 0 || len(write.Mutations) > model.AttemptWorkspaceBatchMaximumMutations {
		return WorkspaceBatchResult{}, apperror.Invalid("exam.attempt.invalid", "workspace_batch")
	}

	recursive, files, bytes := false, 0, int64(0)
	for index, mutation := range write.Mutations {
		if err := validateWorkspaceBatchMutation(mutation); err != nil {
			return WorkspaceBatchResult{}, workspaceBatchItemError(err, index)
		}
		recursive = recursive || mutation.Recursive
		if mutation.carriesContent() {
			files++
			bytes += mutation.Size
		}
	}

	if recursive != (write.ExpectedWorkspaceCursor != nil) ||
		(write.ExpectedWorkspaceCursor != nil && *write.ExpectedWorkspaceCursor < 0) {
		return WorkspaceBatchResult{}, apperror.Invalid("exam.attempt.invalid", "workspace_recursive_delete")
	}
	if files > 0 && write.Content == nil {
		return WorkspaceBatchResult{}, apperror.Invalid("exam.attempt.invalid", "workspace_content")
	}
	if write.IdempotencyKey == "" {
		return WorkspaceBatchResult{}, apperror.New("idempotency.key_required")
	}

	// Spend the budget before reserving or staging, so a writer that outruns
	// it costs storage nothing. An exact retry spends again.
	if !s.workspaceBudget.spend(attemptID, writer.access.WorkspaceAuthor().Kind, writer.resources, len(write.Mutations),
		bytes, s.now()) {
		return WorkspaceBatchResult{}, apperror.New("exam.attempt.workspace.rate_limited")
	}

	mutations, objects, err := s.identifyWorkspaceBatch(write.Mutations)
	if err != nil {
		return WorkspaceBatchResult{}, err
	}

	objectIDs := make([]model.AttemptWorkspaceObjectID, len(objects))
	for index, object := range objects {
		objectIDs[index] = object.ObjectID
	}

	target, err := s.workspaces.ReserveObjects(ctx, &store.ExamAttemptWorkspaceObjectReservation{
		Access:    writer.access,
		ObjectIDs: objectIDs,
	})
	if err != nil {
		return WorkspaceBatchResult{}, storeError(storeErrors, err)
	}
	if !validWorkspaceTarget(target, writer.candidate) {
		return WorkspaceBatchResult{}, apperror.New("exam.attempt.unavailable").Wrap(errors.New("inconsistent Attempt Workspace mutation target"))
	}

	// The outcome is the candidate's, whoever writes on their behalf.
	commandIdempotency, err := prepareWorkspaceBatchIdempotency(
		target.CandidateUserID,
		writer.access.WorkspaceAuthor().Kind,
		write.IdempotencyKey,
		attemptID,
		write.Mutations,
		write.ExpectedWorkspaceCursor,
	)
	if err != nil {
		s.reclaimWorkspaceObjects(ctx, objectIDs, err)
		return WorkspaceBatchResult{}, err
	}

	if err = s.stageWorkspaceBatch(ctx, writer.access, write, objects); err != nil {
		s.reclaimWorkspaceObjects(ctx, objectIDs, err)
		return WorkspaceBatchResult{}, err
	}

	result, err := s.applyWorkspaceBatch(ctx, writer, target, &store.ExamAttemptWorkspaceBatch{
		Access:                  writer.access,
		Mutations:               mutations,
		ExpectedWorkspaceCursor: write.ExpectedWorkspaceCursor,
	}, commandIdempotency)
	if err != nil {
		s.reclaimWorkspaceObjects(ctx, objectIDs, err)
		return WorkspaceBatchResult{}, err
	}

	if result.Replayed {
		s.reclaimWorkspaceObjects(ctx, objectIDs, nil)
	}

	return result, nil
}

type workspaceBatchObject struct {
	Index    int
	ObjectID model.AttemptWorkspaceObjectID
	Version  model.WorkspaceContentVersion
}

func (s *Service) identifyWorkspaceBatch(
	commands []WorkspaceBatchMutation,
) ([]store.ExamAttemptWorkspaceMutation, []workspaceBatchObject, error) {
	mutations := make([]store.ExamAttemptWorkspaceMutation, len(commands))
	objects := make([]workspaceBatchObject, 0, len(commands))
	for index, command := range commands {
		mutation := store.ExamAttemptWorkspaceMutation{
			Operation:              command.Operation,
			EntryID:                command.EntryID,
			ExpectedPath:           command.ExpectedPath,
			DestinationPath:        command.DestinationPath,
			ExpectedContentVersion: command.ExpectedContentVersion,
			Recursive:              command.Recursive,
		}
		if command.Operation == model.AttemptWorkspaceMutationCreateFile ||
			command.Operation == model.AttemptWorkspaceMutationCreateDirectory {
			mutation.EntryID = s.newWorkspaceEntryID()
		}
		if !mutation.EntryID.IsValid() {
			return nil, nil, apperror.Invalid("exam.attempt.invalid", "workspace_identity")
		}

		if command.carriesContent() {
			object := workspaceBatchObject{Index: index, ObjectID: s.newWorkspaceObjectID(), Version: s.newWorkspaceVersion()}
			if !object.ObjectID.IsValid() || !object.Version.IsValid() {
				return nil, nil, apperror.Invalid("exam.attempt.invalid", "workspace_identity")
			}
			mutation.ObjectID = object.ObjectID
			objects = append(objects, object)
		}

		mutations[index] = mutation
	}

	return mutations, objects, nil
}

func (s *Service) stageWorkspaceBatch(
	ctx context.Context,
	workspaceAccess store.ExamAttemptWorkspaceWriteAccess,
	command workspaceWrite,
	objects []workspaceBatchObject,
) error {
	if len(objects) == 0 {
		return nil
	}

	ready := make([]store.ExamAttemptWorkspaceReadyObject, len(objects))
	for position, object := range objects {
		mutation := command.Mutations[object.Index]
		body, err := command.Content.Next(ctx)
		if err != nil {
			return workspaceBatchItemError(apperror.Invalid("exam.attempt.invalid", "workspace_content").Wrap(err), object.Index)
		}
		if body == nil {
			return workspaceBatchItemError(apperror.Invalid("exam.attempt.invalid", "workspace_content"), object.Index)
		}

		staged, err := s.content.StageAttemptWorkspaceObject(ctx, object.ObjectID, body, mutation.Size, mutation.MediaType)
		if err != nil {
			if isInvalidAttemptWorkspaceContent(err) {
				return workspaceBatchItemError(apperror.Invalid("exam.attempt.invalid", "workspace_content").Wrap(err), object.Index)
			}

			return apperror.New("exam.attempt.unavailable").Wrap(err)
		}
		if staged == nil || staged.Validate() != nil || staged.SHA256 != mutation.SHA256 {
			return workspaceBatchItemError(apperror.Invalid("exam.attempt.invalid", "workspace_sha256"), object.Index)
		}

		ready[position] = store.ExamAttemptWorkspaceReadyObject{
			ObjectID:       object.ObjectID,
			ContentVersion: object.Version,
			Content:        *staged,
		}
	}

	if err := s.workspaces.MarkObjectsReady(ctx, &store.ExamAttemptWorkspaceObjectsReady{
		Access:  workspaceAccess,
		Objects: ready,
	}); err != nil {
		return workspaceBatchStoreError(err)
	}

	return nil
}

// reclaimWorkspaceObjects gives back objects a batch will not consume. A batch
// whose outcome is unknown keeps them: the store may yet have consumed them.
func (s *Service) reclaimWorkspaceObjects(ctx context.Context, objectIDs []model.AttemptWorkspaceObjectID, cause error) {
	if len(objectIDs) == 0 || (cause != nil && isAttemptUnavailable(cause)) {
		return
	}
	_ = s.workspaces.MarkObjectsReclaimable(ctx, objectIDs)
}

func validateWorkspaceBatchMutation(mutation WorkspaceBatchMutation) error {
	var err error
	switch mutation.Operation {
	case model.AttemptWorkspaceMutationCreateFile, model.AttemptWorkspaceMutationCreateDirectory:
		if !mutation.EntryID.IsZero() || mutation.ExpectedPath != "" || !mutation.ExpectedContentVersion.IsZero() || mutation.Recursive {
			return apperror.Invalid("exam.attempt.invalid", "workspace_command")
		}
		if _, err = validateWorkspacePath(mutation.DestinationPath); err != nil {
			return apperror.Invalid("exam.attempt.invalid", "workspace_path").Wrap(err)
		}
	case model.AttemptWorkspaceMutationReplaceFile:
		if _, err = validateWorkspaceEntryFence(mutation.EntryID, mutation.ExpectedPath); err != nil {
			return err
		}
		if mutation.DestinationPath != "" || !mutation.ExpectedContentVersion.IsValid() || mutation.Recursive {
			return apperror.Invalid("exam.attempt.invalid", "workspace_command")
		}
	case model.AttemptWorkspaceMutationMoveEntry:
		if _, _, err = validateWorkspaceMove(mutation.EntryID, mutation.ExpectedPath, mutation.DestinationPath); err != nil {
			return err
		}
		if !mutation.ExpectedContentVersion.IsZero() || mutation.Recursive {
			return apperror.Invalid("exam.attempt.invalid", "workspace_command")
		}
	case model.AttemptWorkspaceMutationDeleteEntry:
		if _, err = validateWorkspaceEntryFence(mutation.EntryID, mutation.ExpectedPath); err != nil {
			return err
		}
		if mutation.DestinationPath != "" ||
			(mutation.Recursive && !mutation.ExpectedContentVersion.IsZero()) ||
			(!mutation.ExpectedContentVersion.IsZero() && !mutation.ExpectedContentVersion.IsValid()) {
			return apperror.Invalid("exam.attempt.invalid", "workspace_command")
		}
	default:
		return apperror.Invalid("exam.attempt.invalid", "workspace_operation")
	}

	if mutation.carriesContent() {
		if mutation.Size < 0 ||
			mutation.Size > model.AttemptWorkspaceMaximumFileBytes ||
			mutation.MediaType == "" ||
			strings.TrimSpace(mutation.MediaType) != mutation.MediaType ||
			len(mutation.MediaType) > 255 ||
			!store.IsValidWorkspaceSHA256(mutation.SHA256) {
			return apperror.Invalid("exam.attempt.invalid", "workspace_file")
		}
	} else if mutation.MediaType != "" || mutation.SHA256 != "" || mutation.Size != 0 {
		return apperror.Invalid("exam.attempt.invalid", "workspace_command")
	}

	return nil
}

func workspaceBatchItemError(err error, index int) error {
	var fault *apperror.Error
	if errors.As(err, &fault) {
		return fault.WithField("item", strconv.Itoa(index))
	}
	return err
}

func workspaceBatchStoreError(err error) error {
	var item *store.ErrAttemptWorkspaceBatchItem
	if errors.As(err, &item) {
		return workspaceBatchItemError(storeError(storeErrors, item.Err), item.Index)
	}
	return storeError(storeErrors, err)
}

func isInvalidAttemptWorkspaceContent(err error) bool {
	var invalid interface{ InvalidAttemptWorkspaceContent() bool }
	return errors.As(err, &invalid) && invalid.InvalidAttemptWorkspaceContent()
}

func isAttemptUnavailable(err error) bool {
	var fault *apperror.Error
	return errors.As(err, &fault) && fault.Code() == "exam.attempt.unavailable"
}
