// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package server_test

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/sudosylabs/proctor/server"
	"github.com/sudosylabs/proctor/server/config"
	"github.com/sudosylabs/proctor/server/logging"
	"github.com/sudosylabs/proctor/server/model"
	"github.com/sudosylabs/proctor/server/plugin"
	"github.com/sudosylabs/proctor/server/testlib"
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
		config.NewMemoryStore([]byte(`{"Plugins":{"missing":{"Secrets":{"token":"x"}}}}`)),
		config.StoreOptions{LookupEnv: func(string) (string, bool) { return "", false }},
	)
	require.NoError(t, err)
	overrides.Configuration = configuration

	runtime, err := server.NewForTesting(context.Background(), overrides)
	require.Nil(t, runtime)
	require.ErrorContains(t, err, `plugin secrets name "missing", which is not compiled in`)
	require.True(t, persistence.closed.Load(), "the refused construction released its infrastructure")
}

// panickingProtocol offers the cas sign-in protocol, but panics while
// activating or while building a provider, as a defective plugin would.
type panickingProtocol struct {
	panicOnActivate bool

	mu    sync.Mutex
	calls []string
}

func (p *panickingProtocol) Manifest() plugin.Manifest {
	return plugin.Manifest{ID: "faulty-cas", Name: "Faulty CAS", EnabledByDefault: true}
}

func (p *panickingProtocol) Activate(_ context.Context, host plugin.Host) error {
	p.record("activate")
	if p.panicOnActivate {
		panic("activation exploded")
	}
	return host.AddIdentityProviderType(panickingType{})
}

func (p *panickingProtocol) Deactivate(context.Context) error {
	p.record("deactivate")
	return nil
}

func (p *panickingProtocol) record(call string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.calls = append(p.calls, call)
}

func (p *panickingProtocol) recorded() []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]string(nil), p.calls...)
}

type panickingType struct{}

func (panickingType) Type() string { return "cas" }

func (panickingType) New(plugin.IdentityProviderDefinition) (plugin.IdentityProvider, error) {
	panic("cannot build a provider")
}

// casProviderConfiguration configures one CAS provider. Tests turn the
// built-in cas plugin off with saved settings, so the fixture plugin alone
// offers the protocol.
func casProviderConfiguration(t *testing.T) *config.Store {
	t.Helper()
	configuration, err := config.NewStore(
		context.Background(),
		config.NewMemoryStore([]byte(`{
			"Authentication": {"External": {"LoginStateTTL": "10m", "Providers": [{
				"ID": "campus-cas", "Type": "cas", "DisplayName": "Campus", "Enabled": true, "AutoProvision": false,
				"Settings": {"BaseURL": "https://cas.example.test/cas", "ValidationPath": "/p3/serviceValidate", "Timeout": "5s", "MaxResponseBytes": 65536,
					"Claims": {"Subject": "user"}}
			}]}}
		}`)),
		config.StoreOptions{LookupEnv: func(string) (string, bool) { return "", false }},
	)
	require.NoError(t, err)
	return configuration
}

func TestServerStartsWhenAPluginPanicsWhileActivating(t *testing.T) {
	t.Parallel()

	overrides, persistence, _, _ := newHookOverrides(t)
	overrides.Configuration = casProviderConfiguration(t)
	persistence.pluginSettings = testlib.NewSavedPluginSettings(savedPluginRow(t, "cas", false))
	faulty := &panickingProtocol{panicOnActivate: true}
	overrides.Plugins = []plugin.Plugin{faulty}
	logs := captureLogs(t, overrides)

	runtime, err := server.NewForTesting(context.Background(), overrides)
	require.NoError(t, err, "a panicking plugin, and the CAS provider it would serve, do not stop the server")
	require.Equal(t, []string{"activate", "deactivate"}, faulty.recorded(), "the panicked activation is cleaned up at once")
	require.NoError(t, overrides.Logger.Flush())
	require.NoError(t, runtime.Server.Close())
	require.Equal(t, []string{"activate", "deactivate"}, faulty.recorded(), "it is not deactivated a second time")
	require.Contains(t, logs.String(), `"msg":"plugin failed to activate"`)
	require.Contains(t, logs.String(), `"provider_id":"campus-cas"`, "the left-out provider is named in a warning")
}

func TestServerStartsWhenASignInProtocolPanicsBuildingAProvider(t *testing.T) {
	t.Parallel()

	overrides, persistence, _, _ := newHookOverrides(t)
	overrides.Configuration = casProviderConfiguration(t)
	persistence.pluginSettings = testlib.NewSavedPluginSettings(savedPluginRow(t, "cas", false))
	faulty := &panickingProtocol{}
	overrides.Plugins = []plugin.Plugin{faulty}
	logs := captureLogs(t, overrides)

	runtime, err := server.NewForTesting(context.Background(), overrides)
	require.NoError(t, err, "a provider whose protocol panics is left out; the server starts")
	require.NoError(t, overrides.Logger.Flush())
	require.NoError(t, runtime.Server.Close())
	require.Equal(t, []string{"activate", "deactivate"}, faulty.recorded())
	require.Contains(t, logs.String(), `"msg":"plugin panicked"`, "the panic is contained and logged")
	require.Contains(t, logs.String(), `"provider_id":"campus-cas"`, "the left-out provider is named in a warning")
}

// captureLogs sends the overrides' logger to a JSON buffer.
func captureLogs(t *testing.T, overrides server.TestingOverrides) *logging.Buffer {
	t.Helper()
	logs := &logging.Buffer{}
	require.NoError(t, overrides.Logger.Configure(logging.Config{
		MaxFieldBytes: 1 << 16,
		Targets:       []logging.Target{{Name: "test", Type: "console", Level: "info", Format: "json", Writer: logs}},
	}))
	// Keep the platform from replacing these targets with the configured ones.
	overrides.Logger.LockConfiguration()
	return logs
}

// savedPluginRow is a saved row turning plugin id on or off.
func savedPluginRow(t *testing.T, id model.PluginID, enabled bool) *model.PluginSettings {
	t.Helper()
	row, err := model.NewPluginSettings(id, model.PluginSettingsChange{Enabled: enabled, Settings: json.RawMessage(`{}`)}, time.Now())
	require.NoError(t, err)
	return row
}
