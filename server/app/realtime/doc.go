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

// Package realtime tells connected clients that something changed. Event is
// one such notice (its name, who may receive it and a small payload), event.go
// and the *_event.go files beside it have one constructor per event,
// message.go declares the closed list of events the server sends (each one an
// AsyncAPI message in server/asyncapi), and Delivery delivers an event to this
// node's sockets first and then to the other nodes.
//
// An application area does not call Delivery directly. It holds a Publisher,
// builds its events in its own effects.go, and publishes only after its Store
// operation has committed. Service is the production Publisher: it turns
// Delivery's failures into application errors and adds the Attempt and
// current-user notices a Session revocation or an authorization change owes.
// The one Attempt lookup that needs comes through SessionAttemptTargets, which
// the parent application answers from the Store, so nothing here touches
// storage. Delivery is best effort: nothing here queues or retries, and a
// failed publication never undoes the committed change.
//
// The package knows nothing about WebSocket frames or the cluster wire format.
// The websocket Hub is attached as the Sink and the cluster adapter as the
// ClusterFanout, once each, while the node is built. Start with publisher.go,
// then service.go and delivery.go. The realtimetest child holds the recording
// Publisher that area tests use, and sitting_invalidation.go the two Sitting
// notices several Exam packages share.
package realtime
