// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package plugin

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
)

// Plugin is one optional feature compiled into the server.
//
// The server activates every enabled plugin once while it starts, before it
// serves anything, and deactivates it once while it stops, after the last
// call into it has returned. A plugin offers its capabilities, such as an
// IdentityProviderType, by registering them on the Host during Activate;
// nothing registered later is accepted.
type Plugin interface {
	Manifest() Manifest
	// Activate prepares the plugin and registers what it offers. It must
	// return when ctx is done. An error, a panic or running past ctx leaves
	// the plugin failed: what it registered is discarded and the server
	// starts without it. After an error or a panic, Deactivate is called at
	// once, so it must cope with a partial activation.
	Activate(ctx context.Context, host Host) error
	// Deactivate releases what Activate acquired. It must return when ctx is
	// done.
	Deactivate(ctx context.Context) error
}

// SettingsValidator is implemented by a plugin that checks a settings
// document before an administrator's change is saved. An error refuses the
// change; its text is not shown to the administrator. A plugin that is not
// a SettingsValidator accepts any JSON object.
type SettingsValidator interface {
	ValidateSettings(ctx context.Context, settings json.RawMessage) error
}

// Reconfigurer is implemented by a plugin that can take new settings while
// it runs. When an administrator changes the settings of an active plugin,
// the server calls Reconfigure with a Host that carries them and accepts no
// registrations. A plugin that is not a Reconfigurer, or whose Reconfigure
// fails, is deactivated and activated again instead.
type Reconfigurer interface {
	Reconfigure(ctx context.Context, host Host) error
}

// Manifest names a plugin.
type Manifest struct {
	// ID is the plugin's stable identifier and its key in the Plugins
	// configuration section: 1 to 64 lowercase letters, digits, dots or
	// hyphens, starting with a letter, for example "oidc".
	ID string
	// Name is a short human-readable name, for example "OpenID Connect".
	Name string
	// EnabledByDefault turns the plugin on when the configuration does not
	// say otherwise.
	EnabledByDefault bool
	// Required marks a plugin the server always runs, such as the Exam
	// export format. An administrator cannot turn it off; if it fails to
	// activate, the server still starts without it.
	Required bool
	// SettingsSchema is a JSON Schema object describing the plugin's
	// settings document, from which an administration UI draws its form.
	// The server serves it unchanged; ValidateSettings, not the schema,
	// decides what is accepted. Nil means the plugin has no settings.
	SettingsSchema json.RawMessage
}

// ErrInvalidManifest is returned by Manifest.Validate.
var ErrInvalidManifest = errors.New("plugin manifest is invalid")

// maxSettingsSchemaBytes bounds a manifest's SettingsSchema.
const maxSettingsSchemaBytes = 64 << 10

// Validate reports whether m has a valid ID, a name, and a SettingsSchema
// that is absent or a JSON object.
func (m Manifest) Validate() error {
	if !IsValidID(m.ID) || m.Name == "" || len(m.Name) > 128 {
		return ErrInvalidManifest
	}
	if m.SettingsSchema != nil {
		trimmed := bytes.TrimSpace(m.SettingsSchema)
		if len(m.SettingsSchema) > maxSettingsSchemaBytes || !json.Valid(trimmed) || !bytes.HasPrefix(trimmed, []byte("{")) {
			return ErrInvalidManifest
		}
	}
	return nil
}

// IsValidID reports whether id is a valid plugin ID.
func IsValidID(id string) bool {
	if id == "" || len(id) > 64 || id[0] < 'a' || id[0] > 'z' {
		return false
	}
	for _, r := range id {
		if (r < 'a' || r > 'z') && (r < '0' || r > '9') && r != '.' && r != '-' {
			return false
		}
	}
	return true
}

// ErrUnavailable is returned in place of a capability's own result when its
// plugin has failed. The server reports the plugin as failed and keeps
// running without it.
var ErrUnavailable = errors.New("plugin is unavailable")

// Host is what the server gives a plugin during Activate.
//
// The Host grows one typed method per capability a plugin may offer or use.
// A plugin uses only what it needs; the server records which plugin offered
// what, and contains a panic in any call into it.
type Host interface {
	// Logger writes to the server log, with every record labeled with the
	// plugin's ID.
	Logger() *slog.Logger
	// Settings is the plugin's settings document, which an administrator
	// changes through the plugin administration API, or nil when none is
	// saved. The plugin decodes and validates it; invalid settings should
	// fail Activate.
	Settings() json.RawMessage
	// Secrets is the plugin's secrets document from the Plugins section of
	// the configuration file, or nil when none is configured. It never goes
	// through the API and is never shown.
	Secrets() json.RawMessage
	// AddIdentityProviderType offers a sign-in protocol. It fails when
	// another plugin already offers the same Type, or after Activate has
	// returned.
	AddIdentityProviderType(IdentityProviderType) error
	// AddExportFormat offers an encoding for Exam exports. It fails when
	// another plugin already offers the same media type, or after Activate
	// has returned.
	AddExportFormat(ExportFormat) error
	// AddEventHandler subscribes handler to topic. A plugin subscribes at
	// most one handler to each topic, during Activate.
	AddEventHandler(topic EventTopic, handler EventHandler) error
}
