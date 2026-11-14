// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package oidc

import "github.com/sudosylabs/proctor/server/platform/externalauth"

// Type is the provider type a configured OpenID Connect provider selects.
const Type = "oidc"

// NewPlugin returns the built-in OpenID Connect sign-in plugin. It offers
// the "oidc" identity provider type, which builds each configured OpenID
// Connect provider from its own settings and secrets.
func NewPlugin() externalauth.ProtocolPlugin {
	return externalauth.ProtocolPlugin{Protocol: Type, Name: "OpenID Connect", Build: New}
}
