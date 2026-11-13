// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package cas

import (
	"context"

	"github.com/sudosylabs/proctor/server/plugin"
)

// Type is the provider type a configured CAS provider selects.
const Type = "cas"

// Plugin is the built-in Central Authentication Service sign-in plugin. It
// offers the "cas" identity provider type, which builds each configured CAS
// provider from its own settings.
type Plugin struct{}

// NewPlugin returns the CAS plugin, on by default.
func NewPlugin() Plugin { return Plugin{} }

var (
	_ plugin.Plugin               = Plugin{}
	_ plugin.IdentityProviderType = providerType{}
)

// Manifest names the plugin. It is on unless the configuration turns it off.
func (Plugin) Manifest() plugin.Manifest {
	return plugin.Manifest{ID: Type, Name: "CAS", EnabledByDefault: true}
}

// Activate offers the CAS identity provider type.
func (Plugin) Activate(_ context.Context, host plugin.Host) error {
	return host.AddIdentityProviderType(providerType{})
}

// Deactivate has nothing to release.
func (Plugin) Deactivate(context.Context) error { return nil }

type providerType struct{}

func (providerType) Type() string { return Type }

func (providerType) New(definition plugin.IdentityProviderDefinition) (plugin.IdentityProvider, error) {
	return New(definition)
}
