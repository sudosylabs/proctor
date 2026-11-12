// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package cas

import (
	"context"

	"github.com/sudosylabs/proctor/server/config"
	"github.com/sudosylabs/proctor/server/platform/externalauth"
	"github.com/sudosylabs/proctor/server/plugin"
)

// Plugin is the built-in Central Authentication Service sign-in. It offers the "cas" identity
// provider type, which builds each configured CAS provider.
type Plugin struct{}

// NewPlugin returns the built-in CAS plugin.
func NewPlugin() Plugin { return Plugin{} }

// Manifest names the plugin. It is on unless the configuration turns it off.
func (Plugin) Manifest() plugin.Manifest {
	return plugin.Manifest{ID: config.ExternalAuthenticationTypeCAS, Name: "CAS", EnabledByDefault: true}
}

// Activate offers the CAS identity provider type.
func (Plugin) Activate(_ context.Context, host plugin.Host) error {
	return host.AddIdentityProviderType(providerType{})
}

// Deactivate has nothing to release.
func (Plugin) Deactivate(context.Context) error { return nil }

type providerType struct{}

func (providerType) Type() string { return config.ExternalAuthenticationTypeCAS }

func (providerType) New(definition plugin.IdentityProviderDefinition) (plugin.IdentityProvider, error) {
	settings, err := externalauth.DecodeDefinition(definition)
	if err != nil {
		return nil, err
	}
	return NewFactory().New(settings)
}
