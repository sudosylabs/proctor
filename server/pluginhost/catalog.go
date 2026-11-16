// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package pluginhost

import (
	"context"
	"encoding/json"

	"github.com/sudosylabs/proctor/server/app/administration"
	"github.com/sudosylabs/proctor/server/model"
)

// Catalog presents host to plugin administration. A save through it has
// reconciler, when there is one, apply the change on this node at once.
func Catalog(host *Host, reconciler *Reconciler) administration.PluginCatalog {
	return pluginCatalog{host: host, reconciler: reconciler}
}

type pluginCatalog struct {
	host       *Host
	reconciler *Reconciler
}

// Plugins describes every compiled-in plugin.
func (c pluginCatalog) Plugins() []administration.PluginDescriptor {
	statuses := c.host.Statuses()
	descriptors := make([]administration.PluginDescriptor, 0, len(statuses))
	for _, status := range statuses {
		descriptors = append(descriptors, administration.PluginDescriptor{
			ID:               model.PluginID(status.ID),
			Name:             status.Name,
			State:            string(status.State),
			Required:         status.Required,
			EnabledByDefault: status.EnabledByDefault,
			SettingsSchema:   status.SettingsSchema,
		})
	}
	return descriptors
}

func (c pluginCatalog) ValidatePluginSettings(ctx context.Context, id model.PluginID, settings json.RawMessage) error {
	return c.host.ValidateSettings(ctx, id.String(), settings)
}

// PluginSettingsChanged has this node apply saved settings now; other
// nodes notice at their next check.
func (c pluginCatalog) PluginSettingsChanged() {
	if c.reconciler != nil {
		c.reconciler.Trigger()
	}
}
