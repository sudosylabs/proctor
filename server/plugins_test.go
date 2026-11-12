// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package server_test

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/sudosylabs/proctor/server"
	"github.com/sudosylabs/proctor/server/config"
	"github.com/sudosylabs/proctor/server/plugin"
)

// lifecyclePlugin records its lifecycle calls through the real server graph.
type lifecyclePlugin struct {
	id          string
	activateErr error

	mu    sync.Mutex
	calls []string
}

func (p *lifecyclePlugin) Manifest() plugin.Manifest {
	return plugin.Manifest{ID: p.id, Name: "Lifecycle " + p.id, EnabledByDefault: true}
}

func (p *lifecyclePlugin) Activate(context.Context, plugin.Host) error {
	p.record("activate")
	return p.activateErr
}

func (p *lifecyclePlugin) Deactivate(context.Context) error {
	p.record("deactivate")
	return nil
}

func (p *lifecyclePlugin) record(call string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.calls = append(p.calls, call)
}

func (p *lifecyclePlugin) recorded() []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]string(nil), p.calls...)
}

func TestServerActivatesPluginsWhileBuiltAndDeactivatesThemOnClose(t *testing.T) {
	t.Parallel()

	overrides, _, _, _ := newHookOverrides(t)
	healthy := &lifecyclePlugin{id: "healthy"}
	broken := &lifecyclePlugin{id: "broken", activateErr: errors.New("cannot reach its service")}
	overrides.Plugins = []plugin.Plugin{broken, healthy}

	runtime, err := server.NewForTesting(context.Background(), overrides)
	require.NoError(t, err, "a plugin that fails to activate does not stop the server")
	require.Equal(t, []string{"activate"}, healthy.recorded())
	require.Equal(t, []string{"activate", "deactivate"}, broken.recorded(), "a failed activation is cleaned up at once")

	require.NoError(t, runtime.Server.Close())
	require.Equal(t, []string{"activate", "deactivate"}, healthy.recorded())
	require.Equal(t, []string{"activate", "deactivate"}, broken.recorded(),
		"a plugin whose activation failed is deactivated once, at once, and not again on Close")
}

func TestServerDeactivatesPluginsWhenConstructionFails(t *testing.T) {
	t.Parallel()

	overrides, persistence, _, _ := newHookOverrides(t)
	overrides.AllowMissingJobs = false
	fixture := &lifecyclePlugin{id: "fixture"}
	overrides.Plugins = []plugin.Plugin{fixture}

	runtime, err := server.NewForTesting(context.Background(), overrides)
	require.Nil(t, runtime)
	require.ErrorContains(t, err, "require durable Job runtime")
	require.Equal(t, []string{"activate", "deactivate"}, fixture.recorded())
	require.True(t, persistence.closed.Load(), "the failed construction released its infrastructure")
}

func TestServerRefusesSettingsForAPluginThatIsNotCompiledIn(t *testing.T) {
	t.Parallel()

	overrides, persistence, _, _ := newHookOverrides(t)
	configuration, err := config.NewStore(
		context.Background(),
		config.NewMemoryStore([]byte(`{"Plugins":{"missing":{"Enable":true,"Settings":null}}}`)),
		config.StoreOptions{LookupEnv: func(string) (string, bool) { return "", false }},
	)
	require.NoError(t, err)
	overrides.Configuration = configuration

	runtime, err := server.NewForTesting(context.Background(), overrides)
	require.Nil(t, runtime)
	require.ErrorContains(t, err, `plugin settings name "missing", which is not compiled in`)
	require.True(t, persistence.closed.Load(), "the refused construction released its infrastructure")
}
