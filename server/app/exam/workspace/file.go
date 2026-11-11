// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package workspace

import (
	"context"
	"encoding/hex"
	"errors"
	"io"

	"github.com/sudosylabs/proctor/server/app/apperror"
	"github.com/sudosylabs/proctor/server/app/invocation"
	"github.com/sudosylabs/proctor/server/model"
	"github.com/sudosylabs/proctor/server/store"
)

type CreateFileCommand struct {
	ExamID                model.ExamID
	ExpectedDraftRevision int64
	Path                  string
	MediaType             string
	ExpectedSHA256        string
	Body                  io.Reader
	Size                  int64
	IdempotencyKey        string
}

type ReplaceFileCommand struct {
	ExamID                 model.ExamID
	EntryID                model.StarterWorkspaceEntryID
	ExpectedDraftRevision  int64
	ExpectedContentVersion model.WorkspaceContentVersion
	MediaType              string
	ExpectedSHA256         string
	Body                   io.Reader
	Size                   int64
	IdempotencyKey         string
}

// CreateExamStarterWorkspaceFile idempotently uploads and creates a file in
// the Exam Draft's Starter Workspace, fenced by ExpectedDraftRevision. Size
// -1 means the length is not known in advance.
func (s *Service) CreateExamStarterWorkspaceFile(
	ctx context.Context,
	inv invocation.Invocation,
	command CreateFileCommand,
) (_ Result, resultErr error) {
	defer func() { resultErr = publicFailures.Conceal(resultErr) }()
	path, err := validateMutation(command.ExamID, "", command.ExpectedDraftRevision, command.Path)
	if err != nil ||
		command.Body == nil ||
		command.Size < -1 ||
		command.Size > model.StarterWorkspaceMaximumFileBytes ||
		command.MediaType == "" {
		if err != nil {
			return Result{}, err
		}
		return Result{}, apperror.Invalid("exam.starter_workspace.invalid", "content")
	}

	commandIdempotency, err := prepareWorkspaceIdempotency(
		inv,
		idempotencyOperationCreateFile,
		command.IdempotencyKey,
		command.ExamID,
		command.ExpectedDraftRevision,
		"",
		"",
		path,
		command.MediaType,
		command.Size,
		command.ExpectedSHA256,
	)
	if err != nil {
		return Result{}, err
	}

	authorization, err := s.authorizeExam(ctx, inv, command.ExamID, true)
	if err != nil {
		return Result{}, err
	}

	return s.stageAndFinalize(
		ctx,
		inv,
		authorization,
		command.ExamID,
		"",
		command.ExpectedDraftRevision,
		path,
		"",
		command.MediaType,
		command.ExpectedSHA256,
		command.Body,
		command.Size,
		commandIdempotency,
		"file_create",
		ChangeFileCreated,
		s.workspaces.CreateFile,
	)
}

// ReplaceExamStarterWorkspaceFile idempotently replaces a Starter Workspace
// file's content, fenced by ExpectedDraftRevision and its expected Workspace
// Content Version.
func (s *Service) ReplaceExamStarterWorkspaceFile(
	ctx context.Context,
	inv invocation.Invocation,
	command ReplaceFileCommand,
) (_ Result, resultErr error) {
	defer func() { resultErr = publicFailures.Conceal(resultErr) }()
	if err := validateMutationBase(command.ExamID, command.EntryID, command.ExpectedDraftRevision); err != nil {
		return Result{}, err
	}

	if !command.ExpectedContentVersion.IsValid() ||
		command.Body == nil ||
		command.Size < -1 ||
		command.Size > model.StarterWorkspaceMaximumFileBytes ||
		command.MediaType == "" {
		return Result{}, apperror.Invalid("exam.starter_workspace.invalid", "content")
	}

	commandIdempotency, err := prepareWorkspaceIdempotency(
		inv,
		idempotencyOperationReplaceFile,
		command.IdempotencyKey,
		command.ExamID,
		command.ExpectedDraftRevision,
		command.ExpectedContentVersion,
		command.EntryID.String(),
		"",
		command.MediaType,
		command.Size,
		command.ExpectedSHA256,
	)
	if err != nil {
		return Result{}, err
	}

	authorization, err := s.authorizeExam(ctx, inv, command.ExamID, true)
	if err != nil {
		return Result{}, err
	}

	return s.stageAndFinalize(
		ctx,
		inv,
		authorization,
		command.ExamID,
		command.EntryID,
		command.ExpectedDraftRevision,
		"",
		command.ExpectedContentVersion,
		command.MediaType,
		command.ExpectedSHA256,
		command.Body,
		command.Size,
		commandIdempotency,
		"file_replace",
		ChangeFileReplaced,
		s.workspaces.ReplaceFile,
	)
}

func (s *Service) stageAndFinalize(
	ctx context.Context,
	inv invocation.Invocation,
	authorization authorizationDecision,
	examID model.ExamID,
	entryID model.StarterWorkspaceEntryID,
	expectedRevision int64,
	path string,
	expectedContentVersion model.WorkspaceContentVersion,
	mediaType, expectedSHA256 string,
	body io.Reader,
	size int64,
	commandIdempotency *store.CommandIdempotency,
	auditOperation string,
	effectOperation ChangeOperation,
	run mutationRunner,
) (Result, error) {
	at := model.TimeUTC(s.now())
	if entryID.IsZero() {
		entryID = s.newEntryID()
	}

	objectID, version := s.newObjectID(), s.newVersion()
	if !entryID.IsValid() || !objectID.IsValid() || !version.IsValid() {
		return Result{}, apperror.Invalid("exam.starter_workspace.invalid", "identity")
	}

	object, err := model.NewStagedStarterWorkspaceObject(
		objectID,
		examID,
		inv.Principal().UserID,
		at,
		at.Add(model.StarterWorkspaceUploadLease),
	)
	if err != nil {
		return Result{}, apperror.Invalid("exam.starter_workspace.invalid", "object").Wrap(err)
	}

	_, err = s.workspaces.ReserveObject(ctx, &store.ExamStarterWorkspaceReservation{Object: object})
	if err != nil {
		return Result{}, storeErrors.Translate(err)
	}

	staged, err := s.content.StageStarterWorkspaceObject(ctx, objectID, body, size, mediaType)
	if err != nil {
		_ = s.workspaces.MarkObjectReclaimable(ctx, &store.StarterWorkspaceObjectReclaim{ObjectID: objectID, At: at})
		if isInvalidContentError(err) {
			return Result{}, apperror.Invalid("exam.starter_workspace.invalid", "content").Wrap(err)
		}

		return Result{}, apperror.New("exam.starter_workspace.unavailable").Wrap(err)
	}

	if !validSHA256(expectedSHA256) || staged.SHA256 != expectedSHA256 {
		_ = s.workspaces.MarkObjectReclaimable(ctx, &store.StarterWorkspaceObjectReclaim{ObjectID: objectID, At: at})
		return Result{}, apperror.Invalid("exam.starter_workspace.invalid", "sha256")
	}

	mutation := &store.ExamStarterWorkspaceMutation{
		ExamID:                 examID,
		ActorUserID:            inv.Principal().UserID,
		ManagerOverride:        authorization.override,
		ExpectedDraftRevision:  expectedRevision,
		ChangedAt:              at,
		EntryID:                entryID,
		Path:                   path,
		ObjectID:               objectID,
		ExpectedContentVersion: expectedContentVersion,
		ContentVersion:         version,
		MediaType:              staged.MediaType,
		SizeBytes:              staged.SizeBytes,
		SHA256:                 staged.SHA256,
	}
	result, err := s.runMutation(ctx, inv, authorization, auditOperation, effectOperation, mutation, commandIdempotency, run)
	if err != nil {
		if !apperror.Is(err, "exam.starter_workspace.unavailable") {
			_ = s.workspaces.MarkObjectReclaimable(ctx, &store.StarterWorkspaceObjectReclaim{ObjectID: objectID, At: at})
		}
		return Result{}, err
	}

	if result.Replayed {
		_ = s.workspaces.MarkObjectReclaimable(ctx, &store.StarterWorkspaceObjectReclaim{ObjectID: objectID, At: at})
	}

	return result, nil
}

func validSHA256(value string) bool {
	if len(value) != 64 {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil
}

func isInvalidContentError(err error) bool {
	var invalid interface{ InvalidStarterWorkspaceContent() bool }
	return errors.As(err, &invalid) && invalid.InvalidStarterWorkspaceContent()
}
