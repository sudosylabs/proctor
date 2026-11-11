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
	"strings"

	"github.com/sudosylabs/proctor/server/app/apperror"
	"github.com/sudosylabs/proctor/server/app/invocation"
	"github.com/sudosylabs/proctor/server/model"
	"github.com/sudosylabs/proctor/server/store"
)

// WorkspaceMutationAccess is CandidateAccess fenced to one Participation
// generation, so a superseded Participation cannot change the Workspace.
type WorkspaceMutationAccess struct {
	CandidateAccess
	ParticipationID model.AttemptParticipationID
	Generation      int64
}

type CreateWorkspaceDirectoryCommand struct {
	Access         WorkspaceMutationAccess
	Path           string
	IdempotencyKey string
}

type CreateWorkspaceFileCommand struct {
	Access         WorkspaceMutationAccess
	Path           string
	MediaType      string
	ExpectedSHA256 string
	Body           io.Reader
	Size           int64
	IdempotencyKey string
}

type ReplaceWorkspaceFileCommand struct {
	Access                 WorkspaceMutationAccess
	EntryID                model.AttemptWorkspaceEntryID
	ExpectedPath           string
	ExpectedContentVersion model.WorkspaceContentVersion
	MediaType              string
	ExpectedSHA256         string
	Body                   io.Reader
	Size                   int64
	IdempotencyKey         string
}

type MoveWorkspaceEntryCommand struct {
	Access          WorkspaceMutationAccess
	EntryID         model.AttemptWorkspaceEntryID
	ExpectedPath    string
	DestinationPath string
	IdempotencyKey  string
}

type DeleteWorkspaceEntryCommand struct {
	Access                  WorkspaceMutationAccess
	EntryID                 model.AttemptWorkspaceEntryID
	ExpectedPath            string
	ExpectedContentVersion  model.WorkspaceContentVersion
	Recursive               bool
	ExpectedWorkspaceCursor *int64
	IdempotencyKey          string
}

// WorkspaceMutationResult is the entry a Workspace mutation produced and the
// journal change it recorded. Replayed is true when an exact retry returned
// the stored result.
type WorkspaceMutationResult struct {
	AttemptID       model.ExamAttemptID
	SittingID       model.ExamSittingID
	CandidateUserID model.UserID
	WorkspaceID     model.ExamAttemptWorkspaceID
	Entry           *store.CandidateAttemptWorkspaceItem
	Change          model.AttemptWorkspaceJournalEntry
	Replayed        bool
}

// CreateCandidateExamWorkspaceDirectory idempotently creates a directory at a
// normalized path in the candidate's Attempt Workspace, fenced by
// participation generation.
func (s *Service) CreateCandidateExamWorkspaceDirectory(
	ctx context.Context,
	inv invocation.Invocation,
	command CreateWorkspaceDirectoryCommand,
) (_ WorkspaceMutationResult, resultErr error) {
	defer func() { resultErr = publicFailures.Conceal(resultErr) }()
	workspaceAccess, err := workspaceMutationSelector(inv, command.Access)
	if err != nil {
		return WorkspaceMutationResult{}, err
	}

	path, err := model.NormalizeAttemptWorkspacePath(command.Path)
	if err != nil || path != command.Path {
		return WorkspaceMutationResult{}, apperror.Invalid("exam.attempt.invalid", "workspace_command").Wrap(err)
	}

	commandIdempotency, err := prepareWorkspaceMutationIdempotency(
		inv,
		command.IdempotencyKey,
		workspaceAccess.AttemptID,
		model.AttemptWorkspaceMutationCreateDirectory,
		struct {
			Path string `json:"path"`
		}{path},
	)
	if err != nil {
		return WorkspaceMutationResult{}, err
	}

	entryID := s.newWorkspaceEntryID()
	if !entryID.IsValid() {
		return WorkspaceMutationResult{}, apperror.Invalid("exam.attempt.invalid", "workspace_entry_id")
	}

	target, err := s.resolveWorkspaceMutationTarget(ctx, workspaceAccess)
	if err != nil {
		return WorkspaceMutationResult{}, err
	}

	return s.applyWorkspaceMutation(ctx, inv, target, &store.ExamAttemptWorkspaceMutation{
		Access:          workspaceAccess,
		Operation:       model.AttemptWorkspaceMutationCreateDirectory,
		EntryID:         entryID,
		DestinationPath: path,
	}, commandIdempotency)
}

// CreateCandidateExamWorkspaceFile idempotently stages and creates a new file
// in the candidate's Attempt Workspace, verifying the uploaded body against
// its expected SHA-256.
func (s *Service) CreateCandidateExamWorkspaceFile(
	ctx context.Context,
	inv invocation.Invocation,
	command CreateWorkspaceFileCommand,
) (_ WorkspaceMutationResult, resultErr error) {
	defer func() { resultErr = publicFailures.Conceal(resultErr) }()
	entryID := s.newWorkspaceEntryID()
	return s.stageWorkspaceFile(
		ctx,
		inv,
		command.Access,
		entryID,
		"",
		command.Path,
		"",
		command.MediaType,
		command.ExpectedSHA256,
		command.Body,
		command.Size,
		command.IdempotencyKey,
		model.AttemptWorkspaceMutationCreateFile,
	)
}

// ReplaceCandidateExamWorkspaceFile idempotently replaces a file's content in
// the candidate's Attempt Workspace, fenced by its expected path and
// Workspace Content Version.
func (s *Service) ReplaceCandidateExamWorkspaceFile(
	ctx context.Context,
	inv invocation.Invocation,
	command ReplaceWorkspaceFileCommand,
) (_ WorkspaceMutationResult, resultErr error) {
	defer func() { resultErr = publicFailures.Conceal(resultErr) }()
	return s.stageWorkspaceFile(
		ctx,
		inv,
		command.Access,
		command.EntryID,
		command.ExpectedPath,
		"",
		command.ExpectedContentVersion,
		command.MediaType,
		command.ExpectedSHA256,
		command.Body,
		command.Size,
		command.IdempotencyKey,
		model.AttemptWorkspaceMutationReplaceFile,
	)
}

// MoveCandidateExamWorkspaceEntry idempotently moves a Workspace Entry from
// its expected path to a destination path, preserving its identity.
func (s *Service) MoveCandidateExamWorkspaceEntry(
	ctx context.Context,
	inv invocation.Invocation,
	command MoveWorkspaceEntryCommand,
) (_ WorkspaceMutationResult, resultErr error) {
	defer func() { resultErr = publicFailures.Conceal(resultErr) }()
	workspaceAccess, err := workspaceMutationSelector(inv, command.Access)
	if err != nil {
		return WorkspaceMutationResult{}, err
	}

	expected, destination, err := validateWorkspaceMove(command.EntryID, command.ExpectedPath, command.DestinationPath)
	if err != nil {
		return WorkspaceMutationResult{}, err
	}

	commandIdempotency, err := prepareWorkspaceMutationIdempotency(
		inv,
		command.IdempotencyKey,
		workspaceAccess.AttemptID,
		model.AttemptWorkspaceMutationMoveEntry,
		struct{ EntryID, ExpectedPath, DestinationPath string }{
			command.EntryID.String(),
			expected,
			destination,
		},
	)
	if err != nil {
		return WorkspaceMutationResult{}, err
	}

	target, err := s.resolveWorkspaceMutationTarget(ctx, workspaceAccess)
	if err != nil {
		return WorkspaceMutationResult{}, err
	}

	return s.applyWorkspaceMutation(ctx, inv, target, &store.ExamAttemptWorkspaceMutation{
		Access:          workspaceAccess,
		Operation:       model.AttemptWorkspaceMutationMoveEntry,
		EntryID:         command.EntryID,
		ExpectedPath:    expected,
		DestinationPath: destination,
	}, commandIdempotency)
}

// DeleteCandidateExamWorkspaceEntry idempotently deletes a Workspace Entry at
// its expected path. A recursive directory delete requires
// ExpectedWorkspaceCursor; a non-recursive delete must not set it.
func (s *Service) DeleteCandidateExamWorkspaceEntry(
	ctx context.Context,
	inv invocation.Invocation,
	command DeleteWorkspaceEntryCommand,
) (_ WorkspaceMutationResult, resultErr error) {
	defer func() { resultErr = publicFailures.Conceal(resultErr) }()
	workspaceAccess, err := workspaceMutationSelector(inv, command.Access)
	if err != nil {
		return WorkspaceMutationResult{}, err
	}

	if (command.Recursive &&
		(command.ExpectedWorkspaceCursor == nil ||
			*command.ExpectedWorkspaceCursor < 0 ||
			!command.ExpectedContentVersion.IsZero())) ||
		(!command.Recursive && command.ExpectedWorkspaceCursor != nil) {
		return WorkspaceMutationResult{}, apperror.Invalid("exam.attempt.invalid", "workspace_recursive_delete")
	}

	expected, err := validateWorkspaceEntryFence(command.EntryID, command.ExpectedPath)
	if err != nil {
		return WorkspaceMutationResult{}, err
	}

	commandIdempotency, err := prepareWorkspaceMutationIdempotency(
		inv,
		command.IdempotencyKey,
		workspaceAccess.AttemptID,
		model.AttemptWorkspaceMutationDeleteEntry,
		struct {
			EntryID, ExpectedPath, ExpectedContentVersion string
			Recursive                                     bool   `json:"recursive,omitempty"`
			ExpectedWorkspaceCursor                       *int64 `json:"expected_workspace_cursor,omitempty"`
		}{command.EntryID.String(), expected, command.ExpectedContentVersion.String(), command.Recursive, command.ExpectedWorkspaceCursor},
	)
	if err != nil {
		return WorkspaceMutationResult{}, err
	}

	target, err := s.resolveWorkspaceMutationTarget(ctx, workspaceAccess)
	if err != nil {
		return WorkspaceMutationResult{}, err
	}

	return s.applyWorkspaceMutation(ctx, inv, target, &store.ExamAttemptWorkspaceMutation{
		Access:                  workspaceAccess,
		Operation:               model.AttemptWorkspaceMutationDeleteEntry,
		EntryID:                 command.EntryID,
		ExpectedPath:            expected,
		ExpectedContentVersion:  command.ExpectedContentVersion,
		Recursive:               command.Recursive,
		ExpectedWorkspaceCursor: command.ExpectedWorkspaceCursor,
	}, commandIdempotency)
}

func (s *Service) stageWorkspaceFile(
	ctx context.Context,
	inv invocation.Invocation,
	mutationAccess WorkspaceMutationAccess,
	entryID model.AttemptWorkspaceEntryID,
	expectedPath, destinationPath string,
	expectedVersion model.WorkspaceContentVersion,
	mediaType, expectedSHA string,
	body io.Reader,
	size int64,
	idempotencyKey string,
	operation model.AttemptWorkspaceMutationKind,
) (WorkspaceMutationResult, error) {
	workspaceAccess, err := workspaceMutationSelector(inv, mutationAccess)
	if err != nil {
		return WorkspaceMutationResult{}, err
	}

	if !entryID.IsValid() ||
		body == nil ||
		size < 0 ||
		size > model.AttemptWorkspaceMaximumFileBytes ||
		mediaType == "" ||
		strings.TrimSpace(mediaType) != mediaType ||
		len(mediaType) > 255 ||
		!store.IsValidWorkspaceSHA256(expectedSHA) {
		return WorkspaceMutationResult{}, apperror.Invalid("exam.attempt.invalid", "workspace_file")
	}

	if operation == model.AttemptWorkspaceMutationCreateFile {
		destinationPath, err = validateWorkspacePath(destinationPath)
	} else {
		expectedPath, err = validateWorkspacePath(expectedPath)
		if err == nil && !expectedVersion.IsValid() {
			err = errors.New("content version is required")
		}
	}

	if err != nil {
		return WorkspaceMutationResult{}, apperror.Invalid("exam.attempt.invalid", "workspace_file").Wrap(err)
	}

	var semantic any
	if operation == model.AttemptWorkspaceMutationCreateFile {
		semantic = struct {
			Path, MediaType, SHA256 string
			Size                    int64
		}{destinationPath, mediaType, expectedSHA, size}
	} else {
		semantic = struct {
			EntryID, Path, Version, MediaType, SHA256 string
			Size                                      int64
		}{entryID.String(), expectedPath, expectedVersion.String(), mediaType, expectedSHA, size}
	}

	commandIdempotency, err := prepareWorkspaceMutationIdempotency(inv, idempotencyKey, workspaceAccess.AttemptID, operation, semantic)
	if err != nil {
		return WorkspaceMutationResult{}, err
	}

	target, err := s.resolveWorkspaceMutationTarget(ctx, workspaceAccess)
	if err != nil {
		return WorkspaceMutationResult{}, err
	}

	objectID, version := s.newWorkspaceObjectID(), s.newWorkspaceVersion()
	if !objectID.IsValid() || !version.IsValid() {
		return WorkspaceMutationResult{}, apperror.Invalid("exam.attempt.invalid", "workspace_identity")
	}

	if _, err = s.workspaces.ReserveObject(
		ctx,
		&store.ExamAttemptWorkspaceObjectReservation{Access: workspaceAccess, ObjectID: objectID},
	); err != nil {
		return WorkspaceMutationResult{}, storeError(storeErrors, err)
	}

	staged, err := s.content.StageAttemptWorkspaceObject(ctx, objectID, body, size, mediaType)
	if err != nil {
		_ = s.workspaces.MarkObjectReclaimable(ctx, objectID)
		if isInvalidAttemptWorkspaceContent(err) {
			return WorkspaceMutationResult{}, apperror.Invalid("exam.attempt.invalid", "workspace_content").Wrap(err)
		}

		return WorkspaceMutationResult{}, apperror.New("exam.attempt.unavailable").Wrap(err)
	}

	if staged == nil || staged.Validate() != nil || staged.SHA256 != expectedSHA {
		_ = s.workspaces.MarkObjectReclaimable(ctx, objectID)
		return WorkspaceMutationResult{}, apperror.Invalid("exam.attempt.invalid", "workspace_sha256")
	}

	if _, err = s.workspaces.MarkObjectReady(ctx, &store.ExamAttemptWorkspaceObjectReady{
		Access:         workspaceAccess,
		ObjectID:       objectID,
		ContentVersion: version,
		Content:        *staged,
	}); err != nil {
		mapped := storeError(storeErrors, err)
		if !isAttemptUnavailable(mapped) {
			_ = s.workspaces.MarkObjectReclaimable(ctx, objectID)
		}

		return WorkspaceMutationResult{}, mapped
	}

	result, err := s.applyWorkspaceMutation(ctx, inv, target, &store.ExamAttemptWorkspaceMutation{
		Access:                 workspaceAccess,
		Operation:              operation,
		EntryID:                entryID,
		ExpectedPath:           expectedPath,
		DestinationPath:        destinationPath,
		ExpectedContentVersion: expectedVersion,
		ObjectID:               objectID,
	}, commandIdempotency)
	if err != nil {
		if !isAttemptUnavailable(err) {
			_ = s.workspaces.MarkObjectReclaimable(ctx, objectID)
		}
		return WorkspaceMutationResult{}, err
	}

	if result.Replayed {
		_ = s.workspaces.MarkObjectReclaimable(ctx, objectID)
	}

	return result, nil
}

func isInvalidAttemptWorkspaceContent(err error) bool {
	var invalid interface{ InvalidAttemptWorkspaceContent() bool }
	return errors.As(err, &invalid) && invalid.InvalidAttemptWorkspaceContent()
}

func isAttemptUnavailable(err error) bool {
	var fault *apperror.Error
	return errors.As(err, &fault) && fault.Code() == "exam.attempt.unavailable"
}
