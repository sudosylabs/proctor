// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package model

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestEveryNativeFamilyIsDefinedOnceAndRoundTrips(t *testing.T) {
	t.Parallel()

	seen := map[NativeFamilyID]bool{}
	for _, definition := range nativeFamilyDefinitions {
		require.False(t, seen[definition.id], "%s is defined twice", definition.id)
		seen[definition.id] = true
		require.NotNil(t, definition.defaults, definition.id)
		require.NotNil(t, definition.validate, definition.id)
		require.NotNil(t, definition.members, definition.id)
		require.Equal(t, definition.capability != "", definition.compile != nil,
			"%s compiles rules exactly when it has a capability", definition.id)
	}
	require.Len(t, NativeFamilies(), len(seen))

	defaults := DefaultNativeSecurityPolicy()
	require.NoError(t, defaults.Validate())
	for i, family := range defaults.Families {
		require.Equal(t, NativeFamilies()[i], family.ID(), "the default policy carries the families in order")
		encoded, err := json.Marshal(family)
		require.NoError(t, err, family.ID())
		var decoded NativeFamilyPolicy
		require.NoError(t, json.Unmarshal(encoded, &decoded), "%s round-trips: %s", family.ID(), encoded)
		require.Equal(t, family, decoded)
	}

	_, known := nativeFamilyDefinitionOf("teleportation")
	require.False(t, known)
	_, compiles := nativeFamilyCapability(NativeFamilyInteractiveSession)
	require.False(t, compiles, "the baseline covers the session family")
	capability, compiles := nativeFamilyCapability(NativeFamilyNetworkConfiguration)
	require.True(t, compiles)
	require.Equal(t, NativeCapabilityID("network"), capability)
}
