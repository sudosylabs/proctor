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

// Package websocket is the realtime transport: the one authenticated socket a
// signed-in client keeps open for events and, during an exam, its attempt
// connection. Hub is the realtime.Sink for this node.
//
// Nothing here is durable, and nothing here decides who may see what; the
// application authorizes subscriptions. Replay after a reconnect covers only
// the same node and the connection's bounded history; otherwise the client
// gets a resync event.
package websocket
