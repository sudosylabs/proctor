// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

// Package cluster defines the contract nodes use to message each other and
// find each other; cluster/local and cluster/memberlist implement it.
//
// Delivery is best effort and nothing is stored: a message may be lost or
// arrive twice. Handlers must be idempotent, and anything that matters is read
// again from PostgreSQL. GUARANTEES.md states what a caller may rely on.
package cluster
