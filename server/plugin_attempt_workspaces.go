// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package server

import (
	"context"
	"errors"
	"fmt"
	"sync/atomic"
	"time"

	"github.com/sudosylabs/proctor/server/app/apperror"
	"github.com/sudosylabs/proctor/server/app/exam/attempt"
	"github.com/sudosylabs/proctor/server/app/invocation"
	"github.com/sudosylabs/proctor/server/model"
	"github.com/sudosylabs/proctor/server/plugin"
	"github.com/sudosylabs/proctor/server/pluginhost"
)

// attemptEnvironmentUseCases are the Attempt service's use cases for an
// execution environment.
type attemptEnvironmentUseCases interface {
	BindExecutionEnvironment(context.Context, invocation.System, attempt.BindExecutionEnvironmentCommand) (attempt.ExecutionEnvironmentBinding, error)
	EndExecutionEnvironmentBinding(context.Context, invocation.System, attempt.EndExecutionEnvironmentBindingCommand) (model.AttemptEnvironmentBinding, error)
	ApplyEnvironmentWorkspaceBatch(context.Context, invocation.System, attempt.EnvironmentWorkspaceBatchCommand) (attempt.WorkspaceBatchResult, error)
	ListEnvironmentWorkspace(context.Context, invocation.System, attempt.EnvironmentWorkspaceQuery) (attempt.WorkspacePage, error)
	ListEnvironmentWorkspaceJournal(context.Context, invocation.System, attempt.EnvironmentWorkspaceJournalQuery) (attempt.WorkspaceJournalPage, error)
	OpenEnvironmentWorkspaceFile(context.Context, invocation.System, attempt.EnvironmentWorkspaceFileQuery) (attempt.OpenedContent, error)
}

// pluginAttemptWorkspaces adapts the Attempt service to the plugin host's
// Attempt Workspace port. Plugins activate before the application exists,
// so the service is attached once it is built; until then, and in a graph
// without one, every call answers plugin.ErrUnavailable. Each call runs as
// the system task "plugin.<id>", which the use cases record in audit.
type pluginAttemptWorkspaces struct {
	service atomic.Pointer[attemptEnvironmentUseCases]
}

var _ pluginhost.AttemptWorkspaceService = (*pluginAttemptWorkspaces)(nil)

// attach makes the use cases reachable.
func (w *pluginAttemptWorkspaces) attach(service attemptEnvironmentUseCases) {
	if w != nil && service != nil {
		w.service.Store(&service)
	}
}

func (w *pluginAttemptWorkspaces) use() (attemptEnvironmentUseCases, error) {
	if w == nil {
		return nil, plugin.ErrUnavailable
	}
	service := w.service.Load()
	if service == nil {
		return nil, plugin.ErrUnavailable
	}
	return *service, nil
}

func pluginSystem(pluginID string) invocation.System { return invocation.Task("plugin." + pluginID) }

func (w *pluginAttemptWorkspaces) BindEnvironment(
	ctx context.Context,
	pluginID string,
	request plugin.BindEnvironmentRequest,
) (plugin.EnvironmentBindResult, error) {
	service, err := w.use()
	if err != nil {
		return plugin.EnvironmentBindResult{}, err
	}
	attemptID, err := model.ParseExamAttemptID(request.AttemptID)
	if err != nil {
		return plugin.EnvironmentBindResult{}, plugin.ErrInvalidArgument
	}
	bound, err := service.BindExecutionEnvironment(ctx, pluginSystem(pluginID), attempt.BindExecutionEnvironmentCommand{
		AttemptID:  attemptID,
		Generation: request.Generation,
		Identity:   environmentIdentity(request.Identity),
	})
	if err != nil {
		return plugin.EnvironmentBindResult{}, capabilityError(err)
	}
	result := plugin.EnvironmentBindResult{Binding: pluginEnvironmentBinding(bound.Binding)}
	if bound.Replaced != nil {
		replaced := pluginEnvironmentBinding(*bound.Replaced)
		result.Replaced = &replaced
	}
	return result, nil
}

func (w *pluginAttemptWorkspaces) EndEnvironmentBinding(
	ctx context.Context,
	pluginID, attemptID, bindingID string,
) (plugin.EnvironmentBinding, error) {
	service, err := w.use()
	if err != nil {
		return plugin.EnvironmentBinding{}, err
	}
	parsedAttempt, attemptErr := model.ParseExamAttemptID(attemptID)
	parsedBinding, bindingErr := model.ParseAttemptEnvironmentBindingID(bindingID)
	if attemptErr != nil || bindingErr != nil {
		return plugin.EnvironmentBinding{}, plugin.ErrInvalidArgument
	}
	ended, err := service.EndExecutionEnvironmentBinding(ctx, pluginSystem(pluginID), attempt.EndExecutionEnvironmentBindingCommand{
		AttemptID: parsedAttempt,
		BindingID: parsedBinding,
	})
	if err != nil {
		return plugin.EnvironmentBinding{}, capabilityError(err)
	}
	return pluginEnvironmentBinding(ended), nil
}

func (w *pluginAttemptWorkspaces) ApplyBatch(
	ctx context.Context,
	pluginID string,
	batch plugin.WorkspaceBatch,
) (plugin.WorkspaceBatchResult, error) {
	service, err := w.use()
	if err != nil {
		return plugin.WorkspaceBatchResult{}, err
	}
	binding, err := environmentBinding(batch.Environment)
	if err != nil {
		return plugin.WorkspaceBatchResult{}, err
	}
	mutations := make([]attempt.WorkspaceBatchMutation, len(batch.Mutations))
	for index, mutation := range batch.Mutations {
		entryID, entryErr := optionalID(mutation.EntryID, model.ParseAttemptWorkspaceEntryID)
		version, versionErr := optionalID(mutation.ExpectedContentVersion, model.ParseWorkspaceContentVersion)
		if entryErr != nil || versionErr != nil {
			return plugin.WorkspaceBatchResult{}, plugin.ErrInvalidArgument
		}
		mutations[index] = attempt.WorkspaceBatchMutation{
			Operation:              model.AttemptWorkspaceMutationKind(mutation.Operation),
			EntryID:                entryID,
			ExpectedPath:           mutation.ExpectedPath,
			DestinationPath:        mutation.DestinationPath,
			ExpectedContentVersion: version,
			Recursive:              mutation.Recursive,
			MediaType:              mutation.MediaType,
			SHA256:                 mutation.SHA256,
			Size:                   mutation.Size,
		}
	}
	var content attempt.WorkspaceBatchContent
	if batch.Content != nil {
		content = batch.Content
	}
	var expectedCursor *int64
	if batch.ExpectedCursor != nil {
		cursor := *batch.ExpectedCursor
		expectedCursor = &cursor
	}
	result, err := service.ApplyEnvironmentWorkspaceBatch(ctx, pluginSystem(pluginID), attempt.EnvironmentWorkspaceBatchCommand{
		Binding:                 binding,
		Mutations:               mutations,
		ExpectedWorkspaceCursor: expectedCursor,
		Content:                 content,
		IdempotencyKey:          batch.IdempotencyKey,
	})
	if err != nil {
		return plugin.WorkspaceBatchResult{}, capabilityError(err)
	}
	return plugin.WorkspaceBatchResult{
		WorkspaceID: result.WorkspaceID.String(),
		Changes:     pluginWorkspaceChanges(result.Changes),
		Replayed:    result.Replayed,
	}, nil
}

func (w *pluginAttemptWorkspaces) List(
	ctx context.Context,
	pluginID string,
	request plugin.WorkspaceListRequest,
) (plugin.WorkspacePage, error) {
	service, err := w.use()
	if err != nil {
		return plugin.WorkspacePage{}, err
	}
	binding, err := environmentBinding(request.Environment)
	if err != nil {
		return plugin.WorkspacePage{}, err
	}
	after, err := optionalID(request.AfterEntryID, model.ParseAttemptWorkspaceEntryID)
	if err != nil {
		return plugin.WorkspacePage{}, plugin.ErrInvalidArgument
	}
	page, err := service.ListEnvironmentWorkspace(ctx, pluginSystem(pluginID), attempt.EnvironmentWorkspaceQuery{
		Binding:        binding,
		ExpectedCursor: request.ExpectedCursor,
		AfterEntryID:   after,
		Limit:          request.Limit,
	})
	if err != nil {
		return plugin.WorkspacePage{}, capabilityError(err)
	}
	items := make([]plugin.WorkspaceItem, len(page.Items))
	for index, item := range page.Items {
		items[index] = plugin.WorkspaceItem{
			EntryID:        item.EntryID.String(),
			Kind:           string(item.Kind),
			Path:           item.Path,
			ContentVersion: string(item.ContentVersion),
			MediaType:      item.MediaType,
			SizeBytes:      item.SizeBytes,
			SHA256:         item.SHA256,
		}
	}
	return plugin.WorkspacePage{
		WorkspaceID:     page.WorkspaceID.String(),
		Cursor:          page.Cursor,
		Items:           items,
		HasMore:         page.HasMore,
		RefreshRequired: page.RefreshRequired,
	}, nil
}

func (w *pluginAttemptWorkspaces) ListJournal(
	ctx context.Context,
	pluginID string,
	request plugin.WorkspaceJournalRequest,
) (plugin.WorkspaceJournalPage, error) {
	service, err := w.use()
	if err != nil {
		return plugin.WorkspaceJournalPage{}, err
	}
	binding, err := environmentBinding(request.Environment)
	if err != nil {
		return plugin.WorkspaceJournalPage{}, err
	}
	page, err := service.ListEnvironmentWorkspaceJournal(ctx, pluginSystem(pluginID), attempt.EnvironmentWorkspaceJournalQuery{
		Binding:     binding,
		AfterCursor: request.AfterCursor,
		Limit:       request.Limit,
	})
	if err != nil {
		return plugin.WorkspaceJournalPage{}, capabilityError(err)
	}
	return plugin.WorkspaceJournalPage{
		WorkspaceID:     page.WorkspaceID.String(),
		CurrentCursor:   page.CurrentCursor,
		Entries:         pluginWorkspaceChanges(page.Entries),
		HasMore:         page.HasMore,
		RefreshRequired: page.RefreshRequired,
	}, nil
}

func (w *pluginAttemptWorkspaces) OpenFile(
	ctx context.Context,
	pluginID string,
	environment plugin.BoundEnvironment,
	entryID string,
) (plugin.WorkspaceFile, error) {
	service, err := w.use()
	if err != nil {
		return plugin.WorkspaceFile{}, err
	}
	binding, err := environmentBinding(environment)
	if err != nil {
		return plugin.WorkspaceFile{}, err
	}
	entry, err := model.ParseAttemptWorkspaceEntryID(entryID)
	if err != nil {
		return plugin.WorkspaceFile{}, plugin.ErrInvalidArgument
	}
	opened, err := service.OpenEnvironmentWorkspaceFile(ctx, pluginSystem(pluginID), attempt.EnvironmentWorkspaceFileQuery{
		Binding: binding,
		EntryID: entry,
	})
	if err != nil {
		return plugin.WorkspaceFile{}, capabilityError(err)
	}
	return plugin.WorkspaceFile{
		Body:           opened.Body,
		MediaType:      opened.MediaType,
		SizeBytes:      opened.SizeBytes,
		SHA256:         opened.SHA256,
		ContentVersion: string(opened.ContentVersion),
	}, nil
}

// capabilityError hands a plugin the public code of a use case's refusal,
// and nothing else of it.
func capabilityError(err error) error {
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return err
	}
	public, ok := apperror.As(err)
	if !ok {
		return &plugin.CapabilityError{Code: "exam.attempt.unavailable"}
	}
	return &plugin.CapabilityError{Code: public.Code(), Fields: public.Fields()}
}

func environmentIdentity(identity plugin.EnvironmentIdentity) model.AttemptEnvironmentIdentity {
	return model.AttemptEnvironmentIdentity{GrantID: identity.GrantID, Epoch: identity.Epoch, Incarnation: identity.Incarnation}
}

func environmentBinding(environment plugin.BoundEnvironment) (attempt.EnvironmentBinding, error) {
	attemptID, attemptErr := model.ParseExamAttemptID(environment.AttemptID)
	bindingID, bindingErr := model.ParseAttemptEnvironmentBindingID(environment.BindingID)
	if attemptErr != nil || bindingErr != nil {
		return attempt.EnvironmentBinding{}, plugin.ErrInvalidArgument
	}
	return attempt.EnvironmentBinding{
		AttemptID:  attemptID,
		BindingID:  bindingID,
		Identity:   environmentIdentity(environment.Identity),
		Generation: environment.Generation,
	}, nil
}

// optionalID parses value, leaving the zero value for "".
func optionalID[T any](value string, parse func(string) (T, error)) (T, error) {
	var zero T
	if value == "" {
		return zero, nil
	}
	parsed, err := parse(value)
	if err != nil {
		return zero, fmt.Errorf("parse identifier: %w", err)
	}
	return parsed, nil
}

func pluginEnvironmentBinding(binding model.AttemptEnvironmentBinding) plugin.EnvironmentBinding {
	var ended time.Time
	if binding.EndedAt.IsSet() {
		ended = binding.EndedAt.Time
	}
	return plugin.EnvironmentBinding{
		ID:              binding.ID.String(),
		AttemptID:       binding.AttemptID.String(),
		ParticipationID: binding.ParticipationID.String(),
		Generation:      binding.Generation,
		Identity: plugin.EnvironmentIdentity{
			GrantID:     binding.Identity.GrantID,
			Epoch:       binding.Identity.Epoch,
			Incarnation: binding.Identity.Incarnation,
		},
		CreatedAt: binding.CreatedAt,
		EndedAt:   ended,
	}
}

func pluginWorkspaceChanges(entries []model.AttemptWorkspaceJournalEntry) []plugin.WorkspaceChange {
	changes := make([]plugin.WorkspaceChange, len(entries))
	for index, entry := range entries {
		changes[index] = plugin.WorkspaceChange{
			Cursor:         entry.Cursor,
			EntryID:        entry.EntryID.String(),
			EntryKind:      string(entry.EntryKind),
			Operation:      plugin.WorkspaceOperation(entry.Operation),
			OldPath:        entry.OldPath,
			NewPath:        entry.NewPath,
			ContentVersion: string(entry.ContentVersion),
			ChangedAt:      entry.ChangedAt,
			Recursive:      entry.Recursive,
			Origin:         string(entry.Origin),
		}
	}
	return changes
}

// The plugin's content source is the use case's, method for method.
var _ attempt.WorkspaceBatchContent = plugin.WorkspaceContent(nil)
