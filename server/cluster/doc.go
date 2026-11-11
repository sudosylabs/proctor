// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

// Package cluster defines how Proctor nodes send each other messages: the
// Message envelope, the Transport interface with its handler registration, and
// the DiscoveryStore through which nodes find each other. It contains no
// network code and imports only the standard library. The two implementations
// are cluster/local (one node, no peers) and cluster/memberlist (several nodes).
//
// Delivery is best effort and nothing is stored: a message may be lost or
// arrive twice. Handlers must therefore be idempotent, and anything that
// matters is read again from PostgreSQL. GUARANTEES.md states exactly what a
// caller may rely on. Start with transport.go.
package cluster
