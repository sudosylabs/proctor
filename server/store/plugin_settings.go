// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package store

import (
	"context"
	"time"

	"github.com/sudosylabs/proctor/server/model"
)

// PluginSettingsReplaceOperation is the idempotent command operation that
// saves one plugin's settings.
const PluginSettingsReplaceOperation = "plugin_settings.replace"

// PluginSettingsReplacement is the named atomic mutation that saves one
// plugin's settings and completes its audit. ExpectedRevision is 0 when the
// plugin has no saved settings yet.
type PluginSettingsReplacement struct {
	ActorID          model.UserID
	PluginID         model.PluginID
	ExpectedRevision int64
	Change           model.PluginSettingsChange
	AuditEventID     model.AuditEventID
	AuditAt          time.Time
}

// PluginSettingsReplacementResult reports the saved settings and whether
// this invocation changed them or replayed an earlier one.
type PluginSettingsReplacementResult struct {
	Settings *model.PluginSettings
	Changed  bool
	Replayed bool
}

// PluginSettingsStore owns the administrator's saved plugin settings.
type PluginSettingsStore interface {
	// List returns every saved plugin's settings, ordered by plugin ID.
	List(context.Context) ([]*model.PluginSettings, error)
	// ChangeCount grows whenever any plugin's settings change, so a node can
	// notice a change cheaply and reload the list.
	ChangeCount(context.Context) (int64, error)
	Replace(context.Context, *PluginSettingsReplacement, *CommandIdempotency) (*PluginSettingsReplacementResult, error)
}

// ErrPluginSettingsRevisionConflict reports the current revision, 0 when no
// settings are saved.
type ErrPluginSettingsRevisionConflict struct {
	CurrentRevision int64
}

func (e *ErrPluginSettingsRevisionConflict) Error() string {
	return "plugin settings revision conflict"
}
