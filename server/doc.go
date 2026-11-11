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
// Read these files first:
//   - server.go: Server, New, Run and the startup order
//   - infrastructure.go: acquiring concrete adapters and handing them to the platform
//   - composition.go: the order in which the application and transports are built
//   - application_dependencies.go: turning configuration into the application's policies and ports
//   - shutdown.go: the one shutdown order every stop path takes
//   - testhooks.go: NewForTesting, which builds the same graph with test substitutes
//
// Most of the remaining files each hold one adapter and are named for the two
// sides it joins: authentication_cache.go (platform cache to the identity
// service), mail_sender.go (platform mailer to application mail),
// external_identity_providers.go, realtime_cluster.go, store_cache_cluster.go,
// cluster_discovery.go, cluster_logger.go, store_slow_operation_log.go,
// metrics_recorders.go and tracing_adapters.go. An adapter lives here only
// when the two packages it joins may not import each other. A wrapper that
// only measures a port lives in package metrics instead (metrics.WrapCache
// and its siblings).
//
// There is one way to build a node. Tests go through NewForTesting, and new
// infrastructure or runtime modules join the existing construction and
// startup order rather than adding a second path. The two host commands that
// need the application, administrator recovery and administrator MFA reset,
// build an inert node through New and close it. Commands that only read or
// migrate an installation do not build a node; they live in package operator.
package server
