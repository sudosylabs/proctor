// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package server

import (
	"github.com/sudosylabs/proctor/server/httpapi"
	"github.com/sudosylabs/proctor/server/pluginhost"
)

// pluginDirectory presents the plugin host to the HTTP API.
type pluginDirectory struct {
	host *pluginhost.Host
}

// Active lists the plugins active at the moment of the call, so a plugin
// that fails while serving drops out of the list.
func (d pluginDirectory) Active() []httpapi.PluginSummary {
	var summaries []httpapi.PluginSummary
	for _, status := range d.host.Statuses() {
		if status.State == pluginhost.StateActive {
			summaries = append(summaries, httpapi.PluginSummary{ID: status.ID, Name: status.Name})
		}
	}
	return summaries
}

// Degraded reports whether an enabled plugin has failed.
func (d pluginDirectory) Degraded() bool {
	for _, status := range d.host.Statuses() {
		if status.State == pluginhost.StateFailed {
			return true
		}
	}
	return false
}
