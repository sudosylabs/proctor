// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package app

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strconv"

	"github.com/sudosylabs/proctor/server/app/apperror"
	"github.com/sudosylabs/proctor/server/app/exam/attempt"
	"github.com/sudosylabs/proctor/server/app/exam/environment"
	"github.com/sudosylabs/proctor/server/app/invocation"
	"github.com/sudosylabs/proctor/server/model"
	"github.com/sudosylabs/proctor/server/store"
)

// environmentTask names the runtime task the environment owner's Workspace
// calls run as.
const environmentTask = "execution"

// environmentWorkspace is the environment owner's Workspace port over the
// Attempt service's environment use cases.
type environmentWorkspace struct {
	attempts *attempt.Service
}

// NewEnvironmentWorkspace returns the Workspace port the environment owners
// read and write the Attempt Workspace through.
func NewEnvironmentWorkspace(attempts *attempt.Service) (environment.Workspace, error) {
	if attempts == nil {
		return nil, errors.New("the environment Workspace needs the Attempt service")
	}
	return environmentWorkspace{attempts: attempts}, nil
}

func environmentOwner(access environment.WorkspaceAccess) attempt.EnvironmentOwner {
	return attempt.EnvironmentOwner{AttemptID: access.AttemptID, Incarnation: access.Incarnation, Fence: access.Fence,
		Tag: access.Tag}
}

func (w environmentWorkspace) List(
	ctx context.Context,
	access environment.WorkspaceAccess,
	expectedCursor int64,
	after model.AttemptWorkspaceEntryID,
	limit int,
) (environment.WorkspacePage, error) {
	page, err := w.attempts.ListEnvironmentWorkspace(ctx, invocation.Task(environmentTask), attempt.EnvironmentWorkspaceQuery{
		Owner: environmentOwner(access), ExpectedCursor: expectedCursor, AfterEntryID: after, Limit: limit,
	})
	if err != nil {
		return environment.WorkspacePage{}, environmentReadError(err)
	}
	result := environment.WorkspacePage{Cursor: page.Cursor, HasMore: page.HasMore, RefreshRequired: page.RefreshRequired,
		Items: make([]environment.WorkspaceItem, len(page.Items))}
	for index, item := range page.Items {
		result.Items[index] = environment.WorkspaceItem{EntryID: item.EntryID, Kind: item.Kind, Path: item.Path,
			ContentVersion: item.ContentVersion, SHA256: item.SHA256, Size: item.SizeBytes}
	}
	return result, nil
}

func (w environmentWorkspace) Journal(
	ctx context.Context,
	access environment.WorkspaceAccess,
	afterCursor int64,
	limit int,
) (environment.WorkspaceJournal, error) {
	page, err := w.attempts.ListEnvironmentWorkspaceJournal(ctx, invocation.Task(environmentTask),
		attempt.EnvironmentWorkspaceJournalQuery{Owner: environmentOwner(access), AfterCursor: afterCursor, Limit: limit})
	if err != nil {
		return environment.WorkspaceJournal{}, environmentReadError(err)
	}
	return environment.WorkspaceJournal{CurrentCursor: page.CurrentCursor, Entries: page.Entries, HasMore: page.HasMore,
		RefreshRequired: page.RefreshRequired}, nil
}

func (w environmentWorkspace) Open(
	ctx context.Context,
	access environment.WorkspaceAccess,
	entryID model.AttemptWorkspaceEntryID,
) (environment.WorkspaceFile, error) {
	opened, err := w.attempts.OpenEnvironmentWorkspaceFile(ctx, invocation.Task(environmentTask),
		attempt.EnvironmentWorkspaceFileQuery{Owner: environmentOwner(access), EntryID: entryID})
	if err != nil {
		return environment.WorkspaceFile{}, environmentReadError(err)
	}
	return environment.WorkspaceFile{Body: opened.Body, SHA256: opened.SHA256, Size: opened.SizeBytes,
		ContentVersion: opened.ContentVersion}, nil
}

func (w environmentWorkspace) Apply(
	ctx context.Context,
	access environment.WorkspaceAccess,
	batch environment.WorkspaceBatch,
) ([]model.AttemptWorkspaceJournalEntry, error) {
	mutations := make([]attempt.WorkspaceBatchMutation, len(batch.Mutations))
	for index, mutation := range batch.Mutations {
		mutations[index] = attempt.WorkspaceBatchMutation{
			Operation: mutation.Operation, EntryID: mutation.EntryID, ExpectedPath: mutation.ExpectedPath,
			DestinationPath: mutation.DestinationPath, ExpectedContentVersion: mutation.ExpectedContentVersion,
			Recursive: mutation.Recursive, MediaType: mutation.MediaType, SHA256: mutation.SHA256, Size: mutation.Size,
		}
	}
	var content attempt.WorkspaceBatchContent
	if batch.Content != nil {
		content = batchContent(batch.Content)
	}
	result, err := w.attempts.ApplyEnvironmentWorkspaceBatch(ctx, invocation.Task(environmentTask),
		attempt.EnvironmentWorkspaceBatchCommand{
			Owner: environmentOwner(access), Mutations: mutations, ExpectedWorkspaceCursor: batch.ExpectedCursor,
			Content: content, IdempotencyKey: batch.IdempotencyKey,
		})
	if err != nil {
		return nil, environmentWriteError(err)
	}
	return result.Changes, nil
}

// batchContent hands a batch the owner's bodies, one per file mutation.
type batchContent func(ctx context.Context) (io.Reader, error)

func (c batchContent) Next(ctx context.Context) (io.Reader, error) { return c(ctx) }

// environmentAccessError recognizes the refusals of the owner itself: a
// stale lease or incarnation, and a sealed Attempt.
func environmentAccessError(err error) error {
	var conflict *store.ErrConflict
	if errors.As(err, &conflict) {
		switch conflict.Constraint {
		case "attempt_environment_owner":
			return fmt.Errorf("%w: %w", environment.ErrWorkspaceOwnerStale, err)
		case "exam_attempt_state":
			return fmt.Errorf("%w: %w", environment.ErrWorkspaceSealed, err)
		}
	}
	return nil
}

func environmentReadError(err error) error {
	if refused := environmentAccessError(err); refused != nil {
		return refused
	}
	if apperror.Is(err, "resource.not_found") || apperror.Is(err, "exam.attempt.not_found") {
		return fmt.Errorf("%w: %w", environment.ErrWorkspaceEntryNotFound, err)
	}
	return err
}

// environmentWriteError presents a refused batch with its public code and
// the mutation that refused it.
func environmentWriteError(err error) error {
	if refused := environmentAccessError(err); refused != nil {
		return refused
	}
	failure, ok := apperror.As(err)
	if !ok {
		return err
	}
	item := -1
	if named, present := failure.Fields()["item"]; present {
		if index, parseErr := strconv.Atoi(named); parseErr == nil {
			item = index
		}
	}
	return &environment.WorkspaceRefusal{Code: failure.Code(), Item: item}
}
