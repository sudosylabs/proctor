// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package store

// MaximumListCount is the largest bound a Count accepts. A Count that reaches
// its bound returns the bound, so callers must present it as "at least".
const MaximumListCount = 10000

// ValidCountMaximum reports whether maximum is a bound a Count accepts.
func ValidCountMaximum(maximum int) bool {
	return maximum >= 1 && maximum <= MaximumListCount
}
