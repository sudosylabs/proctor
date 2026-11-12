// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package externalauth

import (
	"context"

	"github.com/sudosylabs/proctor/server/config"
	"github.com/sudosylabs/proctor/server/plugin"
)

// ProtocolPlugin is a built-in sign-in protocol plugin, such as CAS or OIDC.
// Its ID is the protocol type configured providers select; when active, it
// offers one identity provider type that builds each configured provider of
// that type with build.
type ProtocolPlugin struct {
	protocol string
	name     string
	build    func(config.ExternalAuthenticationProvider) (plugin.IdentityProvider, error)
}

// NewProtocolPlugin returns the plugin for protocol, on by default.
func NewProtocolPlugin(
	protocol, name string,
	build func(config.ExternalAuthenticationProvider) (plugin.IdentityProvider, error),
) ProtocolPlugin {
	return ProtocolPlugin{protocol: protocol, name: name, build: build}
}

var (
	_ plugin.Plugin               = ProtocolPlugin{}
	_ plugin.IdentityProviderType = protocolType{}
)

// Manifest names the plugin. It is on unless the configuration turns it off.
func (p ProtocolPlugin) Manifest() plugin.Manifest {
	return plugin.Manifest{ID: p.protocol, Name: p.name, EnabledByDefault: true}
}

// Activate offers the protocol's identity provider type.
func (p ProtocolPlugin) Activate(_ context.Context, host plugin.Host) error {
	return host.AddIdentityProviderType(protocolType(p))
}

// Deactivate has nothing to release.
func (ProtocolPlugin) Deactivate(context.Context) error { return nil }

// protocolType builds the configured providers of one protocol.
type protocolType ProtocolPlugin

func (t protocolType) Type() string { return t.protocol }

func (t protocolType) New(definition plugin.IdentityProviderDefinition) (plugin.IdentityProvider, error) {
	settings, err := DecodeDefinition(definition)
	if err != nil {
		return nil, err
	}
	return t.build(settings)
}
