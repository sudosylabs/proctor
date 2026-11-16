// ---------------------------------------------------------------------------------------------
// Copyright (c) 2015-present Mattermost, Inc. All Rights Reserved.
// Modifications Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------
//
// Realtime publication is substantially adapted from Mattermost revision
// 10b780cb097b2ec94ab0f9df7ebcbd5b7850f13f, principally
// server/channels/app/platform/cluster_handlers.go and
// server/channels/app/platform/web_hub.go, governed by Mattermost's GNU Affero
// General Public License v3. Proctor preserves local-first, peer-local-only
// delivery while using transport-neutral event intents and composition-owned
// wire adapters. The server NOTICE records the complete provenance.

// Package realtime tells connected clients that something changed: this node's
// sockets first, then the other nodes. It knows no WebSocket frames.
//
// Areas hold a Publisher and publish only after their store change commits.
// Delivery is best effort: nothing queues or retries, and a failed
// publication never undoes the change. Every event name must be declared in
// message.go, which mirrors the AsyncAPI messages.
package realtime
