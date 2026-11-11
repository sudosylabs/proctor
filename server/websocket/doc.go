// ---------------------------------------------------------------------------------------------
// Copyright (c) 2015-present Mattermost, Inc. All Rights Reserved.
// Modifications Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------
//
// This package implementation is adapted from Mattermost's WebSocket runtime.
// See server/NOTICE for exact provenance.
//
// Package websocket is the transport for Proctor's realtime connection: the one
// authenticated WebSocket a signed-in client keeps open to receive events and,
// during an Exam, to keep its Attempt connected.
//
// Hub accepts upgrades, enforces connection limits and delivers the events the
// application publishes to the connections that should receive them; it is the
// realtime.Sink. Each connection numbers the events it sends and is closed when
// it cannot keep up. A client that reconnects to the same node may have missed
// events replayed; otherwise it is told to fetch current state again. The
// candidate actions that arrive over the socket (connect, renew, Focus Loss,
// browser activity) are decoded in exam_attempt.go and handed to app/exam/attempt.
//
// Nothing here is durable, and nothing here decides who may see what:
// subscriptions are authorized by the application. The HTTP route that upgrades
// a request is declared in httpapi. Start with hub.go, then protocol.go for the
// wire types and connection.go for the life of one connection.
package websocket
