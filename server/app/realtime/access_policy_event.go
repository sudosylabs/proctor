// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package realtime

import (
	"encoding/json"
	"errors"
	"fmt"

	"github.com/sudosylabs/proctor/server/model"
)

type accessPolicyChangedData struct {
	Revision int64 `json:"revision"`
}

// NewAccessPolicyChangedEvent creates the content-free refetch signal emitted
// after a durable policy commit. No policy fields or provider facts fan out.
func NewAccessPolicyChangedEvent(institutionID model.InstitutionID, revision int64) (Event, error) {
	if !institutionID.IsValid() || revision < 1 {
		return Event{}, errors.New("Access Policy change event requires valid bounded metadata")
	}

	data, err := json.Marshal(accessPolicyChangedData{Revision: revision})
	if err != nil {
		return Event{}, fmt.Errorf("encode Access Policy change event: %w", err)
	}

	return Event{
		Name:     accessPolicyChangedMessage.name,
		Action:   model.ActionAccessPolicyView,
		Resource: model.Resource{Type: model.ResourceInstitution, ID: institutionID.String()},
		Data:     data,
	}, nil
}

type pluginSettingsChangedData struct {
	PluginID string `json:"plugin_id"`
	Revision int64  `json:"revision"`
}

// NewPluginSettingsChangedEvent creates the content-free refetch signal
// emitted after an administrator's change to a plugin's settings commits.
// No setting values fan out.
func NewPluginSettingsChangedEvent(institutionID model.InstitutionID, pluginID model.PluginID, revision int64) (Event, error) {
	if !institutionID.IsValid() || !pluginID.IsValid() || revision < 1 {
		return Event{}, errors.New("plugin settings change event requires valid bounded metadata")
	}
	data, err := json.Marshal(pluginSettingsChangedData{PluginID: pluginID.String(), Revision: revision})
	if err != nil {
		return Event{}, fmt.Errorf("encode plugin settings change event: %w", err)
	}
	return Event{
		Name:     pluginSettingsChangedMessage.name,
		Action:   model.ActionPluginManage,
		Resource: model.Resource{Type: model.ResourceInstitution, ID: institutionID.String()},
		Data:     data,
	}, nil
}
