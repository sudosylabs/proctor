// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package server

import (
	"context"
	"fmt"
	"slices"

	"github.com/sudosylabs/proctor/server/config"
	"github.com/sudosylabs/proctor/server/logging"
	"github.com/sudosylabs/proctor/server/model"
	"github.com/sudosylabs/proctor/server/plugin"
	"github.com/sudosylabs/proctor/server/plugin/cas"
	"github.com/sudosylabs/proctor/server/plugin/oidc"
	"github.com/sudosylabs/proctor/server/plugin/zipexport"
	"github.com/sudosylabs/proctor/server/pluginhost"
	"github.com/sudosylabs/proctor/server/store"
)

// builtinPlugins are the plugins compiled into the server, in activation
// order. This is the one place a plugin is named.
func builtinPlugins() []plugin.Plugin {
	return []plugin.Plugin{
		cas.NewPlugin(),
		oidc.NewPlugin(),
		zipexport.NewPlugin(),
	}
}

// activatePlugins activates the built-in plugins, then any a test adds,
// with the settings an administrator saved and the secrets in the Plugins
// configuration section. It returns the host and the settings change count
// it read, from which the reconciler continues. A plugin that fails to
// activate is logged and left out; a configuration mistake, such as secrets
// for a plugin that is not compiled in, or unreadable saved settings, fails
// construction.
func activatePlugins(
	ctx context.Context,
	cfg config.Config,
	saved store.PluginSettingsStore,
	logger *logging.Logger,
	recorder pluginhost.Recorder,
	extra []plugin.Plugin,
) (*pluginhost.Host, int64, error) {
	plugins := append(builtinPlugins(), extra...)
	known := pluginIDSet(plugins, func(p plugin.Plugin) string { return p.Manifest().ID })
	for id := range cfg.Plugins {
		if _, ok := known[id]; !ok {
			return nil, 0, fmt.Errorf("activate plugins: plugin secrets name %q, which is not compiled in", id)
		}
	}
	count, rows, err := readSavedPluginSettings(ctx, saved)
	if err != nil {
		return nil, 0, fmt.Errorf("activate plugins: %w", err)
	}
	host, err := pluginhost.Activate(ctx, pluginhost.Dependencies{
		Plugins:  plugins,
		Settings: desiredPlugins(known, rows, cfg, logger),
		Logger:   logger.Slog(),
		Recorder: recorder,
	})
	if err != nil {
		return nil, 0, fmt.Errorf("activate plugins: %w", err)
	}
	return host, count, nil
}

// readSavedPluginSettings reads the change count, then the saved settings; a
// change between the two is picked up by the next reconcile. A graph without
// persistence has none.
func readSavedPluginSettings(ctx context.Context, saved store.PluginSettingsStore) (int64, []*model.PluginSettings, error) {
	if saved == nil {
		return 0, nil, nil
	}
	count, err := saved.ChangeCount(ctx)
	if err != nil {
		return 0, nil, fmt.Errorf("read plugin settings change count: %w", err)
	}
	rows, err := saved.List(ctx)
	if err != nil {
		return 0, nil, fmt.Errorf("read plugin settings: %w", err)
	}
	return count, rows, nil
}

// desiredPlugins combines the saved settings with the configured secrets for
// every compiled-in plugin. Saved settings for a plugin that is no longer
// compiled in are ignored.
func desiredPlugins(
	known map[string]struct{},
	rows []*model.PluginSettings,
	cfg config.Config,
	logger *logging.Logger,
) map[string]pluginhost.Settings {
	desired := make(map[string]pluginhost.Settings, len(known))
	for id, configured := range cfg.Plugins {
		desired[id] = pluginhost.Settings{Secrets: configured.SecretsDocument()}
	}
	for _, row := range rows {
		id := row.PluginID.String()
		if _, ok := known[id]; !ok {
			logger.Warn("saved settings name a plugin that is not compiled in", logging.String("plugin", id))
			continue
		}
		enable := row.Enabled
		settings := desired[id]
		settings.Enable = &enable
		settings.Settings = slices.Clone(row.Settings)
		desired[id] = settings
	}
	return desired
}

// pluginIDSet is the set of plugin IDs among items.
func pluginIDSet[T any](items []T, id func(T) string) map[string]struct{} {
	set := make(map[string]struct{}, len(items))
	for _, item := range items {
		set[id(item)] = struct{}{}
	}
	return set
}
