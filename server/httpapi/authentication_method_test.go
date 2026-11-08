// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package httpapi

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestAuthenticationMethodOpenAPIDocument(t *testing.T) {
	document := readOpenAPIDocument(t)
	for _, schemaName := range []string{"AuthenticationMethodResponse", "AuthenticationProviderMethodResponse"} {
		schema := document.Components.Schemas[schemaName]
		for _, forbidden := range []string{"subject", "password_hash", "token", "credential"} {
			_, exists := schema.Properties[forbidden]
			require.False(t, exists, "%s exposes %q", schemaName, forbidden)
		}
	}
}
