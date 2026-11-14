// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package server

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/sudosylabs/proctor/server/app/apperror"
	"github.com/sudosylabs/proctor/server/app/exam/attempt"
	"github.com/sudosylabs/proctor/server/app/invocation"
	"github.com/sudosylabs/proctor/server/model"
	"github.com/sudosylabs/proctor/server/plugin"
	"github.com/sudosylabs/proctor/server/pluginhost"
)

// environmentUseCases stands in for the Attempt service: it records what
// the plugin's calls became and stages the batch content as the use case
// does.
type environmentUseCases struct {
	systems []invocation.System
	bound   attempt.BindExecutionEnvironmentCommand
	batch   attempt.EnvironmentWorkspaceBatchCommand
	content []byte
	binding model.AttemptEnvironmentBinding
	refuse  error
}

func (u *environmentUseCases) BindExecutionEnvironment(
	_ context.Context, system invocation.System, command attempt.BindExecutionEnvironmentCommand,
) (attempt.ExecutionEnvironmentBinding, error) {
	u.systems = append(u.systems, system)
	u.bound = command
	u.binding = model.AttemptEnvironmentBinding{
		ID: model.NewAttemptEnvironmentBindingID(), AttemptID: command.AttemptID, ParticipationID: model.NewAttemptParticipationID(),
		Generation: command.Generation, Identity: command.Identity, CreatedAt: time.Now().UTC(),
	}
	return attempt.ExecutionEnvironmentBinding{Binding: u.binding}, nil
}

func (u *environmentUseCases) EndExecutionEnvironmentBinding(
	_ context.Context, system invocation.System, command attempt.EndExecutionEnvironmentBindingCommand,
) (model.AttemptEnvironmentBinding, error) {
	u.systems = append(u.systems, system)
	ended := u.binding
	ended.EndedAt = model.OptionalTimeFrom(time.Now())
	return ended, nil
}

func (u *environmentUseCases) ApplyEnvironmentWorkspaceBatch(
	ctx context.Context, system invocation.System, command attempt.EnvironmentWorkspaceBatchCommand,
) (attempt.WorkspaceBatchResult, error) {
	u.systems = append(u.systems, system)
	if u.refuse != nil {
		return attempt.WorkspaceBatchResult{}, u.refuse
	}
	u.batch = command
	reader, err := command.Content.Next(ctx)
	if err != nil {
		return attempt.WorkspaceBatchResult{}, err
	}
	if u.content, err = io.ReadAll(io.LimitReader(reader, command.Mutations[0].Size)); err != nil {
		return attempt.WorkspaceBatchResult{}, err
	}
	return attempt.WorkspaceBatchResult{
		AttemptID: command.Binding.AttemptID, WorkspaceID: model.NewExamAttemptWorkspaceID(),
		Changes: []model.AttemptWorkspaceJournalEntry{{
			Cursor: 7, EntryID: model.NewAttemptWorkspaceEntryID(), EntryKind: model.StarterWorkspaceEntryFile,
			Operation: model.AttemptWorkspaceMutationCreateFile, NewPath: command.Mutations[0].DestinationPath,
			Origin: model.AttemptWorkspaceOriginEnvironment,
		}},
	}, nil
}

func (u *environmentUseCases) ListEnvironmentWorkspace(
	context.Context, invocation.System, attempt.EnvironmentWorkspaceQuery,
) (attempt.WorkspacePage, error) {
	return attempt.WorkspacePage{}, nil
}

func (u *environmentUseCases) ListEnvironmentWorkspaceJournal(
	context.Context, invocation.System, attempt.EnvironmentWorkspaceJournalQuery,
) (attempt.WorkspaceJournalPage, error) {
	return attempt.WorkspaceJournalPage{}, nil
}

func (u *environmentUseCases) OpenEnvironmentWorkspaceFile(
	context.Context, invocation.System, attempt.EnvironmentWorkspaceFileQuery,
) (attempt.OpenedContent, error) {
	return attempt.OpenedContent{Body: io.NopCloser(strings.NewReader(""))}, nil
}

// harvestingPlugin is a fake execution plugin: it keeps the capability it
// was given, as the real one does for its workers.
type harvestingPlugin struct {
	id         string
	workspaces plugin.AttemptWorkspaces
}

func (p *harvestingPlugin) Manifest() plugin.Manifest {
	return plugin.Manifest{ID: p.id, Name: "Harvesting " + p.id, EnabledByDefault: true}
}

func (p *harvestingPlugin) Activate(_ context.Context, host plugin.Host) error {
	p.workspaces = host.AttemptWorkspaces()
	return nil
}

func (*harvestingPlugin) Deactivate(context.Context) error { return nil }

type contentOf []byte

func (c contentOf) Next(context.Context) (io.Reader, error) { return bytes.NewReader(c), nil }

func TestAFakeExecutionPluginBindsAnEnvironmentAndAppliesABatch(t *testing.T) {
	t.Parallel()

	execution, display := &harvestingPlugin{id: "execution"}, &harvestingPlugin{id: "display"}
	adapter := &pluginAttemptWorkspaces{}
	host, err := pluginhost.Activate(context.Background(), pluginhost.Dependencies{
		Plugins:           []plugin.Plugin{execution, display},
		Logger:            slog.New(slog.DiscardHandler),
		AttemptWorkspaces: adapter,
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = host.Close(context.Background()) })
	ctx := context.Background()
	attemptID := model.NewExamAttemptID()
	identity := plugin.EnvironmentIdentity{GrantID: "grant-1", Epoch: "epoch-1", Incarnation: 0}

	_, err = execution.workspaces.BindEnvironment(ctx, plugin.BindEnvironmentRequest{AttemptID: attemptID.String(), Generation: 1, Identity: identity})
	require.ErrorIs(t, err, plugin.ErrUnavailable, "the capability waits for the application to be built")

	useCases := &environmentUseCases{}
	adapter.attach(useCases)
	bound, err := execution.workspaces.BindEnvironment(ctx, plugin.BindEnvironmentRequest{AttemptID: attemptID.String(), Generation: 1, Identity: identity})
	require.NoError(t, err)
	require.Equal(t, attempt.BindExecutionEnvironmentCommand{
		AttemptID: attemptID, Generation: 1,
		Identity: model.AttemptEnvironmentIdentity{GrantID: "grant-1", Epoch: "epoch-1"},
	}, useCases.bound)
	require.Equal(t, attemptID.String(), bound.Binding.AttemptID)
	require.True(t, bound.Binding.EndedAt.IsZero(), "a live binding has no end")

	environment := plugin.BoundEnvironment{AttemptID: attemptID.String(), BindingID: bound.Binding.ID, Identity: identity, Generation: 1}
	cursor := int64(6)
	body := []byte("print('hello')\n")
	result, err := execution.workspaces.ApplyBatch(ctx, plugin.WorkspaceBatch{
		Environment: environment,
		Mutations: []plugin.WorkspaceMutation{{
			Operation: plugin.WorkspaceCreateFile, DestinationPath: "main.py", MediaType: "text/x-python",
			SHA256: strings.Repeat("a", 64), Size: int64(len(body)),
		}},
		ExpectedCursor: &cursor,
		Content:        contentOf(body),
		IdempotencyKey: "harvest-1",
	})
	require.NoError(t, err)
	require.Equal(t, body, useCases.content, "the batch body reaches the use case")
	require.Equal(t, attempt.EnvironmentBinding{
		AttemptID: attemptID, BindingID: useCases.binding.ID, Generation: 1,
		Identity: model.AttemptEnvironmentIdentity{GrantID: "grant-1", Epoch: "epoch-1"},
	}, useCases.batch.Binding)
	require.Equal(t, model.AttemptWorkspaceMutationCreateFile, useCases.batch.Mutations[0].Operation)
	require.Equal(t, int64(6), *useCases.batch.ExpectedWorkspaceCursor)
	require.Equal(t, "harvest-1", useCases.batch.IdempotencyKey)
	require.Equal(t, []plugin.WorkspaceChange{{
		Cursor: 7, EntryID: result.Changes[0].EntryID, EntryKind: "file", Operation: plugin.WorkspaceCreateFile,
		NewPath: "main.py", Origin: "environment",
	}}, result.Changes)

	ended, err := execution.workspaces.EndEnvironmentBinding(ctx, attemptID.String(), bound.Binding.ID)
	require.NoError(t, err)
	require.False(t, ended.EndedAt.IsZero())
	for _, system := range useCases.systems {
		require.Equal(t, invocation.Task("plugin.execution"), system, "every call runs as the system task naming the plugin")
	}

	useCases.refuse = apperror.New("exam.attempt.workspace.cursor_conflict").Wrap(errors.New("cursor moved"))
	_, err = execution.workspaces.ApplyBatch(ctx, plugin.WorkspaceBatch{Environment: environment, Content: contentOf(nil)})
	var refusal *plugin.CapabilityError
	require.ErrorAs(t, err, &refusal)
	require.Equal(t, "exam.attempt.workspace.cursor_conflict", refusal.Code, "a refusal carries its public code and nothing else")
	require.NotContains(t, err.Error(), "cursor moved")

	_, err = execution.workspaces.ApplyBatch(ctx, plugin.WorkspaceBatch{Environment: plugin.BoundEnvironment{AttemptID: "not-an-id"}})
	require.ErrorIs(t, err, plugin.ErrInvalidArgument)

	calls := len(useCases.systems)
	_, err = display.workspaces.BindEnvironment(ctx, plugin.BindEnvironmentRequest{AttemptID: attemptID.String(), Generation: 1, Identity: identity})
	require.ErrorIs(t, err, plugin.ErrUnavailable, "a plugin the core does not list is refused")
	require.Len(t, useCases.systems, calls, "a refused plugin never reaches the use cases")
}
