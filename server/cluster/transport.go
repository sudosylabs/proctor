// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package cluster

import (
	"context"
	"errors"
)

var (
	// ErrStopped reports that the transport was stopped; it cannot restart.
	ErrStopped = errors.New("cluster transport is stopped")
	// ErrNotStarted reports a call before Start succeeded.
	ErrNotStarted = errors.New("cluster transport is not started")
	// ErrHandlerExists reports a second handler for the same event.
	ErrHandlerExists = errors.New("cluster message handler is already registered")
	// ErrNodeUnavailable reports a send to a node that is not a member.
	ErrNodeUnavailable = errors.New("cluster node is unavailable")
	// ErrNodeIDInUse reports that another member already joined with this node ID.
	ErrNodeIDInUse = errors.New("cluster node ID is already in use")
)

// Handler receives its own clone of a message. The transport recovers its
// panics. It must be idempotent, since a message may arrive twice.
type Handler func(context.Context, *Message) error

// Logger receives transport failures; it keeps cluster free of the logging package.
type Logger interface {
	ErrorContext(ctx context.Context, message string, err error)
}

// Metrics receives bounded labels only: never node IDs, addresses, payloads or
// error text.
type Metrics interface {
	ObserveClusterMessage(direction, event, outcome string, bytes int)
	ObserveClusterMembership(event string)
	ObserveClusterDiscovery(operation, outcome string)
	ObserveClusterAdmission(reason string)
	ObserveClusterFanout(recipients int)
}

// Transport sends messages between nodes, best effort and not stored.
//
// Broadcast reaches peers only, never this node's handlers. SendToNode may
// target this node, which is how the local transport runs its own handlers.
type Transport interface {
	NodeID() string
	Start(context.Context) error
	Stop(context.Context) error
	Ping(context.Context) error
	RegisterHandler(Event, Handler) error
	Broadcast(context.Context, *Message) error
	SendToNode(context.Context, string, *Message) error
}
