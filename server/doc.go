// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

// Package server builds and runs one Proctor node. It is the only package that
// picks concrete infrastructure (PostgreSQL, the file store, cache, mail and
// cluster adapters) and wires it into the application and its transports.
// Business rules live in package app, not here.
//
// New reads the configuration, acquires infrastructure, hands it to the
// platform, and builds the application, the HTTP API and the WebSocket
// transport. It never binds a listener or starts background work. Run starts
// the platform, durable Jobs, WebSocket and HTTP serving in order, marks the
// node ready, and blocks until shutdown or failure. Close, or a failure during
// Run, marks the node unready, drains HTTP, and shuts each part down once.
//
// Where to look:
//   - server.go: Server, New, Run and Close
//   - infrastructure.go: acquiring concrete adapters
//   - composition.go: the order in which the application and transports are built
//   - testhooks.go: NewForTesting, which builds the same graph with test substitutes
//
// There is one way to build a node. Tests go through NewForTesting, and new
// infrastructure or runtime modules join the existing construction and
// startup order rather than adding a second path.
package server
