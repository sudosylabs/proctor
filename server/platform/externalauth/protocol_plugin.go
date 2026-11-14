// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package externalauth

import (
	"context"

	"github.com/sudosylabs/proctor/server/plugin"
)

// ProtocolPlugin is a built-in sign-in protocol plugin, such as CAS or OIDC:
// on by default, it offers one identity provider type, Protocol, which
// builds each configured provider of that type with Build from the
// provider's own settings and secrets.
type ProtocolPlugin struct {
	Protocol string
	Name     string
	Build    func(plugin.IdentityProviderDefinition) (plugin.IdentityProvider, error)
}

var (
	_ plugin.Plugin               = ProtocolPlugin{}
	_ plugin.IdentityProviderType = protocolType{}
)

// Manifest names the plugin. It is on unless an administrator turns it off.
func (p ProtocolPlugin) Manifest() plugin.Manifest {
	return plugin.Manifest{ID: p.Protocol, Name: p.Name, EnabledByDefault: true}
}

// Activate offers the protocol's identity provider type.
func (p ProtocolPlugin) Activate(_ context.Context, host plugin.Host) error {
	return host.AddIdentityProviderType(protocolType(p))
}

// Deactivate has nothing to release.
func (ProtocolPlugin) Deactivate(context.Context) error { return nil }

// protocolType builds the configured providers of one protocol.
type protocolType ProtocolPlugin

func (t protocolType) Type() string { return t.Protocol }

func (t protocolType) New(definition plugin.IdentityProviderDefinition) (plugin.IdentityProvider, error) {
	return t.Build(definition)
}
