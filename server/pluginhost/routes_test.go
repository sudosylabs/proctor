// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package pluginhost_test

import (
	"bytes"
	"context"
	"errors"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/sudosylabs/proctor/server/plugin"
	"github.com/sudosylabs/proctor/server/pluginhost"
)

func okRoute(path string) plugin.Route {
	return plugin.Route{
		Method: "GET", Path: path, Guard: plugin.GuardCandidateAttemptAccess,
		Handler: func(context.Context, plugin.Access, plugin.Request) (plugin.Response, error) {
			return plugin.Response{Status: 204}, nil
		},
	}
}

func routePlugin(log *callLog, id string, routes ...plugin.Route) *hookPlugin {
	return &hookPlugin{fixturePlugin: newFixture(log, id, true), activate: func(host plugin.Host) error {
		for _, route := range routes {
			if err := host.AddRoute(route); err != nil {
				return err
			}
		}
		return nil
	}}
}

func TestAddRouteChecksTheRouteThePluginAndWhen(t *testing.T) {
	t.Parallel()

	log := &callLog{}
	var registered plugin.Host
	var results []error
	checker := &hookPlugin{fixturePlugin: newFixture(log, "execution", true), activate: func(host plugin.Host) error {
		registered = host
		results = append(results,
			host.AddRoute(okRoute("terminal")),
			host.AddRoute(okRoute("terminal")),
			host.AddRoute(plugin.Route{Method: "GET", Path: "/terminal", Guard: plugin.GuardCandidateAttemptAccess}),
		)
		return nil
	}}
	dotted := routePlugin(log, "acme.terminal", okRoute("terminal"))
	host, err := pluginhost.Activate(context.Background(), pluginhost.Dependencies{
		Plugins: []plugin.Plugin{checker, dotted},
		Logger:  discardLogger(),
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = host.Close(context.Background()) })

	require.NoError(t, results[0])
	require.Error(t, results[1], "a plugin serves one method and path once")
	require.ErrorIs(t, results[2], plugin.ErrInvalidRoute)
	require.Equal(t, pluginhost.StateFailed, host.Statuses()[1].State, "a plugin whose ID is not one path segment cannot serve routes")
	require.Error(t, registered.AddRoute(okRoute("late")), "a route added after Activate is refused")

	routes := host.Routes()
	require.Len(t, routes, 1)
	require.Equal(t, "execution", routes[0].Plugin)
	require.Equal(t, "terminal", routes[0].Route.Path)
	require.True(t, routes[0].Available())
}

func TestRoutesFollowThePluginOnAndOff(t *testing.T) {
	t.Parallel()

	log := &callLog{}
	execution := routePlugin(log, "execution", okRoute("terminal"))
	host, err := pluginhost.Activate(context.Background(), pluginhost.Dependencies{
		Plugins: []plugin.Plugin{execution},
		Logger:  discardLogger(),
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = host.Close(context.Background()) })
	ctx := context.Background()

	mounted := host.Routes()
	require.Len(t, mounted, 1)
	host.Reconcile(ctx, map[string]pluginhost.Settings{"execution": {Enable: boolPointer(false)}})
	require.Empty(t, host.Routes(), "a turned-off plugin serves no routes")
	require.False(t, mounted[0].Available(), "a route kept from before reports its plugin gone")
	_, err = mounted[0].Route.Handler(ctx, plugin.CandidateAttemptAccess{}, plugin.Request{})
	require.ErrorIs(t, err, plugin.ErrUnavailable, "a stale route never calls into a deactivated plugin")

	host.Reconcile(ctx, map[string]pluginhost.Settings{"execution": {Enable: boolPointer(true)}})
	require.Len(t, host.Routes(), 1, "turning the plugin on again mounts what its new activation registers")
}

func TestARouteHandlerPanicIsContainedAndCountsTowardFailure(t *testing.T) {
	t.Parallel()

	log := &callLog{}
	panicking := okRoute("terminal")
	panicking.Handler = func(context.Context, plugin.Access, plugin.Request) (plugin.Response, error) {
		panic("route exploded")
	}
	execution := routePlugin(log, "execution", panicking)
	host, err := pluginhost.Activate(context.Background(), pluginhost.Dependencies{
		Plugins: []plugin.Plugin{execution},
		Logger:  discardLogger(),
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = host.Close(context.Background()) })

	route := host.Routes()[0]
	for range pluginhost.PanicLimit {
		_, err = route.Route.Handler(context.Background(), plugin.CandidateAttemptAccess{}, plugin.Request{})
		require.ErrorIs(t, err, plugin.ErrUnavailable)
	}
	require.Equal(t, pluginhost.StateFailed, host.Statuses()[0].State)
	require.False(t, route.Available(), "a failed plugin's routes answer as if they did not exist")
	require.Empty(t, host.Routes())
}

func TestASocketEndsWhenItsPluginIsTurnedOff(t *testing.T) {
	t.Parallel()

	log := &callLog{}
	started := make(chan struct{})
	socketRoute := plugin.Route{Method: "GET", Path: "terminal", Guard: plugin.GuardCandidateAttemptAccess,
		Socket: func(ctx context.Context, _ plugin.Access, _ plugin.Socket) error {
			close(started)
			<-ctx.Done()
			return ctx.Err()
		}}
	execution := routePlugin(log, "execution", socketRoute)
	host, err := pluginhost.Activate(context.Background(), pluginhost.Dependencies{
		Plugins: []plugin.Plugin{execution},
		Logger:  discardLogger(),
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = host.Close(context.Background()) })

	served := make(chan error, 1)
	go func() {
		served <- host.Routes()[0].Route.Socket(context.Background(), plugin.CandidateAttemptAccess{}, nil)
	}()
	<-started
	host.Reconcile(context.Background(), map[string]pluginhost.Settings{"execution": {Enable: boolPointer(false)}})
	select {
	case err := <-served:
		require.ErrorIs(t, err, context.Canceled)
	case <-time.After(5 * time.Second):
		require.FailNow(t, "the socket outlived its plugin")
	}
}

// recordingWorkspaces records which plugin each Attempt Workspace call named.
type recordingWorkspaces struct {
	mu      sync.Mutex
	plugins []string
	content []byte
}

func (w *recordingWorkspaces) record(pluginID string) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.plugins = append(w.plugins, pluginID)
}

func (w *recordingWorkspaces) BindEnvironment(_ context.Context, pluginID string, request plugin.BindEnvironmentRequest) (plugin.EnvironmentBindResult, error) {
	w.record(pluginID)
	return plugin.EnvironmentBindResult{Binding: plugin.EnvironmentBinding{AttemptID: request.AttemptID, Generation: request.Generation}}, nil
}

func (w *recordingWorkspaces) EndEnvironmentBinding(_ context.Context, pluginID, attemptID, bindingID string) (plugin.EnvironmentBinding, error) {
	w.record(pluginID)
	return plugin.EnvironmentBinding{ID: bindingID, AttemptID: attemptID}, nil
}

func (w *recordingWorkspaces) ApplyBatch(ctx context.Context, pluginID string, batch plugin.WorkspaceBatch) (plugin.WorkspaceBatchResult, error) {
	w.record(pluginID)
	reader, err := batch.Content.Next(ctx)
	if err != nil {
		return plugin.WorkspaceBatchResult{}, err
	}
	body, err := io.ReadAll(reader)
	if err != nil {
		return plugin.WorkspaceBatchResult{}, err
	}
	w.mu.Lock()
	w.content = body
	w.mu.Unlock()
	return plugin.WorkspaceBatchResult{WorkspaceID: "workspace"}, nil
}

func (w *recordingWorkspaces) List(_ context.Context, pluginID string, _ plugin.WorkspaceListRequest) (plugin.WorkspacePage, error) {
	w.record(pluginID)
	return plugin.WorkspacePage{}, nil
}

func (w *recordingWorkspaces) ListJournal(_ context.Context, pluginID string, _ plugin.WorkspaceJournalRequest) (plugin.WorkspaceJournalPage, error) {
	w.record(pluginID)
	return plugin.WorkspaceJournalPage{}, nil
}

func (w *recordingWorkspaces) OpenFile(_ context.Context, pluginID string, _ plugin.BoundEnvironment, _ string) (plugin.WorkspaceFile, error) {
	w.record(pluginID)
	return plugin.WorkspaceFile{Body: io.NopCloser(strings.NewReader(""))}, nil
}

type oneFile struct {
	body  []byte
	panic bool
}

func (c oneFile) Next(context.Context) (io.Reader, error) {
	if c.panic {
		panic("content exploded")
	}
	return bytes.NewReader(c.body), nil
}

func TestOnlyListedPluginsGetAttemptWorkspaces(t *testing.T) {
	t.Parallel()

	require.True(t, pluginhost.AttemptWorkspaceGranted("execution"))
	require.False(t, pluginhost.AttemptWorkspaceGranted("display"))

	log := &callLog{}
	service := &recordingWorkspaces{}
	var granted, refused plugin.Host
	execution := &hookPlugin{fixturePlugin: newFixture(log, "execution", true), activate: func(host plugin.Host) error {
		granted = host
		return nil
	}}
	display := &hookPlugin{fixturePlugin: newFixture(log, "display", true), activate: func(host plugin.Host) error {
		refused = host
		return nil
	}}
	host, err := pluginhost.Activate(context.Background(), pluginhost.Dependencies{
		Plugins:           []plugin.Plugin{execution, display},
		Logger:            discardLogger(),
		AttemptWorkspaces: service,
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = host.Close(context.Background()) })
	ctx := context.Background()

	workspaces := granted.AttemptWorkspaces()
	_, err = workspaces.BindEnvironment(ctx, plugin.BindEnvironmentRequest{AttemptID: "attempt", Generation: 1})
	require.NoError(t, err)
	_, err = workspaces.ApplyBatch(ctx, plugin.WorkspaceBatch{Content: oneFile{body: []byte("package main\n")}})
	require.NoError(t, err)
	_, err = workspaces.List(ctx, plugin.WorkspaceListRequest{})
	require.NoError(t, err)
	_, err = workspaces.ListJournal(ctx, plugin.WorkspaceJournalRequest{})
	require.NoError(t, err)
	_, err = workspaces.OpenFile(ctx, plugin.BoundEnvironment{}, "entry")
	require.NoError(t, err)
	_, err = workspaces.EndEnvironmentBinding(ctx, "attempt", "binding")
	require.NoError(t, err)
	require.Equal(t, []string{"execution", "execution", "execution", "execution", "execution", "execution"}, service.plugins,
		"the host names the calling plugin on every call")
	require.Equal(t, "package main\n", string(service.content))

	_, err = refused.AttemptWorkspaces().BindEnvironment(ctx, plugin.BindEnvironmentRequest{})
	require.ErrorIs(t, err, plugin.ErrUnavailable, "a plugin the core does not list never reaches the use cases")
	_, err = refused.AttemptWorkspaces().ApplyBatch(ctx, plugin.WorkspaceBatch{})
	require.ErrorIs(t, err, plugin.ErrUnavailable)

	_, err = workspaces.ApplyBatch(ctx, plugin.WorkspaceBatch{Content: oneFile{panic: true}})
	require.ErrorIs(t, err, plugin.ErrUnavailable, "a panic in the plugin's content is contained")

	host.Reconcile(ctx, map[string]pluginhost.Settings{"execution": {Enable: boolPointer(false)}})
	_, err = workspaces.List(ctx, plugin.WorkspaceListRequest{})
	require.ErrorIs(t, err, plugin.ErrUnavailable, "a deactivated plugin's capability is unavailable")
	require.Len(t, service.plugins, 7)

	unbacked := &hookPlugin{fixturePlugin: newFixture(log, "execution", true), activate: func(host plugin.Host) error {
		granted = host
		return nil
	}}
	other, err := pluginhost.Activate(ctx, pluginhost.Dependencies{Plugins: []plugin.Plugin{unbacked}, Logger: discardLogger()})
	require.NoError(t, err)
	t.Cleanup(func() { _ = other.Close(context.Background()) })
	_, err = granted.AttemptWorkspaces().List(ctx, plugin.WorkspaceListRequest{})
	require.True(t, errors.Is(err, plugin.ErrUnavailable), "without the use cases, the capability is unavailable")
}
