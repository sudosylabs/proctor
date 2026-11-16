// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

// Package memberlist is the cluster transport for more than one node, built on
// HashiCorp memberlist with encrypted gossip. It refuses a peer whose identity
// or protocol range does not match this build.
//
// Nodes find their first peers through short-lived advertisements in a
// cluster.DiscoveryStore. New starts nothing; Start opens the network and the
// discovery loop. Delivery is best effort (see ../GUARANTEES.md).
package memberlist
