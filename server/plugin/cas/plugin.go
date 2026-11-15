// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package cas

import "github.com/sudosylabs/proctor/server/platform/externalauth"

// Type is the provider type a configured CAS provider selects.
const Type = "cas"

// NewPlugin returns the built-in Central Authentication Service sign-in
// plugin. It offers the "cas" identity provider type, which builds each
// configured CAS provider from its own settings.
func NewPlugin() externalauth.ProtocolPlugin {
	return externalauth.ProtocolPlugin{Protocol: Type, Name: "CAS", Build: New}
}
