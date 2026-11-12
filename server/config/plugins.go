// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package config

import (
	"bytes"
	"encoding/json"
	"fmt"
	"maps"
	"slices"
	"sort"

	"github.com/sudosylabs/proctor/server/plugin"
)

// maxPluginSettingsBytes bounds one plugin's Settings document.
const maxPluginSettingsBytes = 64 << 10

// Plugin configures one plugin compiled into the server. The Plugins section
// is keyed by plugin ID; a plugin the section does not name keeps its
// built-in default.
type Plugin struct {
	// Enable turns the plugin on or off. When absent, the plugin's own
	// default applies.
	Enable *bool `json:"Enable"`
	// Settings is the plugin's own document. The plugin decodes and
	// validates it when it activates; the server only bounds its size and
	// redacts it when showing the configuration, because it may hold
	// secrets.
	Settings json.RawMessage `json:"Settings"`
}

// SettingsDocument returns Settings, or nil when it is absent or JSON null.
func (p Plugin) SettingsDocument() json.RawMessage {
	trimmed := bytes.TrimSpace(p.Settings)
	if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null")) {
		return nil
	}
	return p.Settings
}

// validatePlugins checks each plugin key and Settings document. Whether a key
// names a compiled-in plugin is checked when the plugins activate.
func validatePlugins(plugins map[string]Plugin, add func(string, string)) {
	ids := slices.Collect(maps.Keys(plugins))
	sort.Strings(ids)
	for _, id := range ids {
		prefix := fmt.Sprintf("plugins[%q]", id)
		if !plugin.IsValidID(id) {
			add(prefix, "must be 1-64 lowercase letters, digits, dots or hyphens, starting with a letter")
		}
		settings := plugins[id].SettingsDocument()
		if settings == nil {
			continue
		}
		if len(settings) > maxPluginSettingsBytes {
			add(prefix+".settings", fmt.Sprintf("must be at most %d bytes", maxPluginSettingsBytes))
		} else if !json.Valid(settings) || !bytes.HasPrefix(bytes.TrimSpace(settings), []byte("{")) {
			add(prefix+".settings", "must be a JSON object")
		}
	}
}

func clonePlugins(plugins map[string]Plugin) map[string]Plugin {
	if plugins == nil {
		return nil
	}
	cloned := make(map[string]Plugin, len(plugins))
	for id, settings := range plugins {
		if settings.Enable != nil {
			enable := *settings.Enable
			settings.Enable = &enable
		}
		settings.Settings = slices.Clone(settings.Settings)
		cloned[id] = settings
	}
	return cloned
}

func redactPlugins(plugins map[string]Plugin) {
	for id, settings := range plugins {
		if settings.SettingsDocument() != nil {
			settings.Settings = json.RawMessage(`"[redacted]"`)
			plugins[id] = settings
		}
	}
}
