// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

// Package model defines Proctor's domain types and the checks a value can make
// on its own. Checks that need stored state, such as uniqueness or cycles in
// the academic unit tree, belong to app and the store. Each entity has its own
// ID type; they share one 26-character form but are not interchangeable. A
// type carries json tags only when model owns its one canonical form (signed or
// digest-bound documents, delivery agreements, job payloads).
package model
