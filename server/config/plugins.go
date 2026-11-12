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

// maxPluginSecretsBytes bounds one plugin's Secrets document.
const maxPluginSecretsBytes = 64 << 10

// Plugin holds the secrets of one plugin compiled into the server, keyed by
// plugin ID in the Plugins section. Whether a plugin runs and its other
// settings are not configuration: an administrator changes them through the
// plugin administration API.
type Plugin struct {
	// Secrets is the plugin's own document of secret values, such as a
	// service token. The plugin decodes and validates it when it activates;
	// the server only bounds its size and always redacts it when showing
	// the configuration.
	Secrets json.RawMessage `json:"Secrets"`
}

// SecretsDocument returns Secrets, or nil when it is absent or JSON null.
func (p Plugin) SecretsDocument() json.RawMessage {
	trimmed := bytes.TrimSpace(p.Secrets)
	if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null")) {
		return nil
	}
	return p.Secrets
}

// validatePlugins checks each plugin key and Secrets document. Whether a key
// names a compiled-in plugin is checked when the plugins activate.
func validatePlugins(plugins map[string]Plugin, add func(string, string)) {
	ids := slices.Collect(maps.Keys(plugins))
	sort.Strings(ids)
	for _, id := range ids {
		prefix := fmt.Sprintf("plugins[%q]", id)
		if !plugin.IsValidID(id) {
			add(prefix, "must be 1-64 lowercase letters, digits, dots or hyphens, starting with a letter")
		}
		secrets := plugins[id].SecretsDocument()
		if secrets == nil {
			continue
		}
		if len(secrets) > maxPluginSecretsBytes {
			add(prefix+".secrets", fmt.Sprintf("must be at most %d bytes", maxPluginSecretsBytes))
		} else if !json.Valid(secrets) || !bytes.HasPrefix(bytes.TrimSpace(secrets), []byte("{")) {
			add(prefix+".secrets", "must be a JSON object")
		}
	}
}

func clonePlugins(plugins map[string]Plugin) map[string]Plugin {
	if plugins == nil {
		return nil
	}
	cloned := make(map[string]Plugin, len(plugins))
	for id, settings := range plugins {
		settings.Secrets = slices.Clone(settings.Secrets)
		cloned[id] = settings
	}
	return cloned
}

func redactPlugins(plugins map[string]Plugin) {
	for id, settings := range plugins {
		if settings.SecretsDocument() != nil {
			settings.Secrets = json.RawMessage(`"[redacted]"`)
			plugins[id] = settings
		}
	}
}
