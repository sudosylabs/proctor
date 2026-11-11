// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

// Package memberlist is the cluster transport for an installation with more
// than one node, built on HashiCorp Memberlist. It keeps an encrypted gossip
// membership, sends messages directly to peers, and refuses a peer whose
// identity or protocol range does not match this build.
//
// To find its first peers a node advertises itself in a cluster.DiscoveryStore
// under a short lease and keeps reading the live entries, retrying a few seeds
// at a time. The module root supplies the store; this package never imports
// SQL. New starts nothing: Start opens the network and the discovery loop, and
// Stop ends both.
//
// Delivery is best effort (see ../GUARANTEES.md), and what a message means is
// its sender's business, not this package's. Start with transport.go;
// admission.go holds the peer checks and discovery_maintenance.go the
// discovery loop.
package memberlist
