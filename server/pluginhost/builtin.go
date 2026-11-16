// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package pluginhost

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

// withTestPlugins returns the built-in plugins with each extra plugin in
// place of the built-in of the same ID, and the rest of extra after them.
func withTestPlugins(builtin, extra []plugin.Plugin) []plugin.Plugin {
	plugins := append([]plugin.Plugin(nil), builtin...)
	for _, added := range extra {
		replaced := false
		for index, existing := range plugins[:len(builtin)] {
			if existing.Manifest().ID == added.Manifest().ID {
				plugins[index], replaced = added, true
			}
		}
		if !replaced {
			plugins = append(plugins, added)
		}
	}
	return plugins
}

// ActivateBuiltin activates the built-in plugins, then any a test adds, and
// returns the host with the settings change count it read, which the
// reconciler continues from. A plugin that fails to activate is left out;
// secrets for a plugin that is not compiled in, or unreadable saved settings,
// fail construction.
func ActivateBuiltin(
	ctx context.Context,
	cfg config.Config,
	saved store.PluginSettingsStore,
	logger *logging.Logger,
	recorder Recorder,
	extra []plugin.Plugin,
) (*Host, int64, error) {
	plugins := withTestPlugins(builtinPlugins(), extra)
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
	host, err := Activate(ctx, Dependencies{
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
// change between the two is picked up by the next reconcile. saved is nil
// without persistence.
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
) map[string]Settings {
	desired := make(map[string]Settings, len(known))
	for id, configured := range cfg.Plugins {
		desired[id] = Settings{Secrets: configured.SecretsDocument()}
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

func pluginIDSet[T any](items []T, id func(T) string) map[string]struct{} {
	set := make(map[string]struct{}, len(items))
	for _, item := range items {
		set[id(item)] = struct{}{}
	}
	return set
}
