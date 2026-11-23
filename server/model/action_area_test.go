// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package model

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// TestEveryGrantableActionHasAnArea keeps the role editor's grid complete: a
// new grantable action must say where it belongs, and an override must widen
// a grantable action.
func TestEveryGrantableActionHasAnArea(t *testing.T) {
	t.Parallel()
	for _, action := range GrantableActions() {
		_, ok := AreaForAction(action)
		require.True(t, ok, "%s has no area", action)
		if base, isOverride := OverriddenAction(action); isOverride {
			require.True(t, IsGrantableAction(string(base)), "%s overrides %s, which is not grantable", action, base)
		}
	}
	for action := range actionAreas {
		require.True(t, IsGrantableAction(string(action)), "%s has an area but cannot be granted", action)
	}
}
