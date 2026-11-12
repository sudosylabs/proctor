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

	"github.com/sudosylabs/proctor/server/config"
	"github.com/sudosylabs/proctor/server/filecontent"
	"github.com/sudosylabs/proctor/server/httpapi"
	"github.com/sudosylabs/proctor/server/logging"
	"github.com/sudosylabs/proctor/server/platform/externalauth/cas"
	"github.com/sudosylabs/proctor/server/platform/externalauth/oidc"
	"github.com/sudosylabs/proctor/server/plugin"
	"github.com/sudosylabs/proctor/server/pluginhost"
)

// builtinPlugins are the plugins compiled into the server, in activation
// order. This is the one place a plugin is named.
func builtinPlugins() []plugin.Plugin {
	return []plugin.Plugin{
		cas.NewPlugin(),
		oidc.NewPlugin(),
		filecontent.NewZipExportPlugin(),
	}
}

// activatePlugins activates the built-in plugins, then any a test adds, with
// the Plugins configuration section. A plugin that fails to activate is
// logged and left out; only a configuration mistake, such as settings for a
// plugin that is not compiled in, fails construction.
func activatePlugins(
	ctx context.Context,
	cfg config.Config,
	logger *logging.Logger,
	extra []plugin.Plugin,
) (*pluginhost.Host, error) {
	settings := make(map[string]pluginhost.Settings, len(cfg.Plugins))
	for id, configured := range cfg.Plugins {
		settings[id] = pluginhost.Settings{Enable: configured.Enable, Settings: configured.SettingsDocument()}
	}
	host, err := pluginhost.Activate(ctx, pluginhost.Dependencies{
		Plugins:  append(builtinPlugins(), extra...),
		Settings: settings,
		Logger:   logger.Slog(),
	})
	if err != nil {
		return nil, fmt.Errorf("activate plugins: %w", err)
	}
	return host, nil
}

// activePluginSummaries lists the plugins active at the moment of each call,
// so a plugin that fails while serving drops out of the list.
func activePluginSummaries(host *pluginhost.Host) httpapi.ActivePlugins {
	return func() []httpapi.PluginSummary {
		var summaries []httpapi.PluginSummary
		for _, status := range host.Statuses() {
			if status.State == pluginhost.StateActive {
				summaries = append(summaries, httpapi.PluginSummary{ID: status.ID, Name: status.Name})
			}
		}
		return summaries
	}
}
