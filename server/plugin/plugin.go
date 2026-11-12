// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package plugin

import (
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
	// Activate prepares the plugin and registers what it offers. An error,
	// or a panic, leaves the plugin failed: what it registered is discarded,
	// Deactivate is not called, and the server starts without it. Activate
	// releases anything it acquired before failing.
	Activate(ctx context.Context, host Host) error
	// Deactivate releases what Activate acquired. It stops when ctx is done.
	Deactivate(ctx context.Context) error
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
}

// ErrInvalidManifest is returned by Manifest.Validate.
var ErrInvalidManifest = errors.New("plugin manifest is invalid")

// Validate reports whether m has a valid ID and a name.
func (m Manifest) Validate() error {
	if !IsValidID(m.ID) || m.Name == "" || len(m.Name) > 128 {
		return ErrInvalidManifest
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
	// Settings is the plugin's Settings document from the Plugins
	// configuration section, or nil when none is configured. The plugin
	// decodes and validates it; invalid settings should fail Activate.
	Settings() json.RawMessage
}
