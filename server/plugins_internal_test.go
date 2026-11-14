// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package server

import (
	"context"
	"encoding/json"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/sudosylabs/proctor/server/config"
	"github.com/sudosylabs/proctor/server/logging"
	"github.com/sudosylabs/proctor/server/model"
	"github.com/sudosylabs/proctor/server/pluginhost"
	"github.com/sudosylabs/proctor/server/store"
)

// changingPluginSettings is saved plugin settings a test changes, counting
// reads so a test can see whether a check listed them.
type changingPluginSettings struct {
	store.PluginSettingsStore
	mu    sync.Mutex
	rows  []*model.PluginSettings
	count int64
	lists int
}

func (s *changingPluginSettings) save(rows ...*model.PluginSettings) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.rows = rows
	s.count++
}

func (s *changingPluginSettings) List(context.Context) ([]*model.PluginSettings, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.lists++
	return s.rows, nil
}

func (s *changingPluginSettings) ChangeCount(context.Context) (int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.count, nil
}

func newTestLogger(t *testing.T) *logging.Logger {
	t.Helper()
	logger, err := logging.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = logger.Shutdown() })
	return logger
}

func activePluginIDs(host *pluginhost.Host) []string {
	var ids []string
	for _, status := range host.Statuses() {
		if status.State == pluginhost.StateActive {
			ids = append(ids, status.ID)
		}
	}
	return ids
}

func TestPluginReconcilerAppliesSavedChangesOnlyWhenTheyChange(t *testing.T) {
	t.Parallel()

	logger := newTestLogger(t)
	saved := &changingPluginSettings{}
	plugins, count, err := activatePlugins(context.Background(), config.Default(), saved, logger, nil, nil)
	require.NoError(t, err)
	t.Cleanup(func() { _ = plugins.Close(context.Background()) })
	require.Equal(t, []string{"cas", "oidc", "export.zip"}, activePluginIDs(plugins))

	reconciler := newPluginReconciler(saved, plugins, config.Default(), logger, count)
	reconciler.check(context.Background())
	require.Equal(t, 1, saved.lists, "an unchanged count reads nothing beyond startup")

	saved.save(pluginSettingsRow(t, "cas", false))
	reconciler.check(context.Background())
	require.Equal(t, []string{"oidc", "export.zip"}, activePluginIDs(plugins), "the saved change applies")
	require.Equal(t, 2, saved.lists)

	reconciler.check(context.Background())
	require.Equal(t, 2, saved.lists, "the same count is not read again")
}

func TestPluginReconcilerRunsUntilClosedAndChecksWhenTriggered(t *testing.T) {
	t.Parallel()

	logger := newTestLogger(t)
	saved := &changingPluginSettings{}
	plugins, count, err := activatePlugins(context.Background(), config.Default(), saved, logger, nil, nil)
	require.NoError(t, err)
	t.Cleanup(func() { _ = plugins.Close(context.Background()) })
	reconciler := newPluginReconciler(saved, plugins, config.Default(), logger, count)
	reconciler.interval = time.Hour
	require.NoError(t, reconciler.Start(context.Background()))

	saved.save(pluginSettingsRow(t, "oidc", false))
	reconciler.Trigger()
	require.Eventually(t, func() bool {
		return len(activePluginIDs(plugins)) == 2
	}, 5*time.Second, 10*time.Millisecond, "a trigger checks at once")

	stop, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	require.NoError(t, reconciler.Close(stop))
	require.NoError(t, reconciler.Close(stop), "closing twice is harmless")
}

func TestSignInProvidersFollowTheirPluginBeingTurnedOffAndOn(t *testing.T) {
	t.Parallel()

	logger := newTestLogger(t)
	saved := &changingPluginSettings{}
	plugins, _, err := activatePlugins(context.Background(), config.Default(), saved, logger, nil, nil)
	require.NoError(t, err)
	t.Cleanup(func() { _ = plugins.Close(context.Background()) })

	settings := config.Default()
	settings.Authentication.External.Providers = []config.ExternalAuthenticationProvider{{
		ID: "campus-cas", Type: "cas", DisplayName: "Campus", Enabled: true,
		Settings: json.RawMessage(`{"BaseURL":"https://cas.example.test/cas","ValidationPath":"/p3/serviceValidate","Timeout":"5s","MaxResponseBytes":65536,"Claims":{"Subject":"user"}}`),
	}}
	encoded, err := json.Marshal(settings)
	require.NoError(t, err)
	configuration, err := config.NewStore(context.Background(), config.NewMemoryStore(encoded),
		config.StoreOptions{LookupEnv: func(string) (string, bool) { return "", false }})
	require.NoError(t, err)
	registry, err := newExternalAuthenticationRegistry(plugins)
	require.NoError(t, err)
	require.NoError(t, registry.Configure(settings.Authentication.External))
	followSignInPlugins(plugins, registry, configuration, logger)

	_, exists := registry.Provider("campus-cas")
	require.True(t, exists)

	plugins.Reconcile(context.Background(), map[string]pluginhost.Settings{"cas": {Enable: boolValue(false)}})
	_, exists = registry.Provider("campus-cas")
	require.False(t, exists, "turning the CAS plugin off removes its provider")
	require.Equal(t, []string{"campus-cas"}, registry.Unavailable())

	plugins.Reconcile(context.Background(), map[string]pluginhost.Settings{"cas": {Enable: boolValue(true)}})
	_, exists = registry.Provider("campus-cas")
	require.True(t, exists, "turning it back on restores the provider")
}

func boolValue(value bool) *bool { return &value }
