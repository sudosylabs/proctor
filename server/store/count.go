// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package store

// MaximumListCount is the largest bound a collection Count accepts. A Count
// reads at most one row past its bound and reports the bound when more rows
// match, so a caller presents a total at its bound as a lower bound. Every
// Count takes its bound explicitly, between 1 and MaximumListCount.
const MaximumListCount = 10000

// ValidCountMaximum reports whether maximum is a bound a Count accepts.
func ValidCountMaximum(maximum int) bool {
	return maximum >= 1 && maximum <= MaximumListCount
}
