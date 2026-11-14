// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package plugin

import (
	"context"
	"errors"
	"time"
)

// MaxLeaseClaimKeys bounds the keys one Leases.ClaimAvailable considers.
const MaxLeaseClaimKeys = 100

var (
	// ErrLeaseHeld reports that Claim found the key held by a lease that
	// has not expired, possibly one this node holds.
	ErrLeaseHeld = errors.New("plugin lease is held")
	// ErrLeaseLost reports that Renew found the lease expired, released or
	// claimed by someone else. Its holder must stop acting as the owner.
	ErrLeaseLost = errors.New("plugin lease is lost")
)

// Worker is long-running work a plugin registers with Host.AddWorker. The
// server runs one of each registered worker on every node, from after the
// node has started until the plugin is turned off, restarted with new
// settings, or the node stops; ctx ends then, and the worker must return
// promptly.
//
// A worker that returns an error or panics is started again after a delay
// that grows with each failure. A panic counts toward the plugin's panic
// limit like any other, and a plugin marked failed has all its workers
// stopped. A worker that returns nil before ctx ends has finished and is
// not started again. A worker never affects the node's readiness except
// through the plugin being marked failed.
//
// Work that only one node may do at a time, such as owning one external
// resource, is claimed under leases. leases belongs to this run of the
// worker: every lease it holds is released when the run returns.
type Worker func(ctx context.Context, leases Leases) error

// Leases claims the plugin's keys for this node. A lease is a fenced,
// expiring claim on one key that the core keeps in PostgreSQL, whose clock
// decides expiry, so a node that dies loses its leases and another node can
// claim them. Keys follow the Records key grammar; leases and records are
// separate namespaces. Like Records, Leases adds the plugin's ID and this
// node's ID to every call, and a plugin can never reach another plugin's
// leases.
//
// Every lease the core grants lasts the same lifetime, tens of seconds, and
// must be renewed well before it ends; Hold does that. Each claim takes a
// new fence, larger than any earlier fence of the same key, which the
// holder carries on every action it takes as the owner, so whoever acts on
// the key can refuse an older holder that has not yet noticed its loss.
//
// A plugin that owns many keys, such as one per live session, keeps the set
// it wants owned in its record store and offers it to ClaimAvailable from
// every node, so keys whose holder died are picked up.
type Leases interface {
	// Claim takes key if it is free or its lease has expired, or returns
	// ErrLeaseHeld.
	Claim(ctx context.Context, key string) (Lease, error)
	// ClaimAvailable takes, in key order, up to limit of keys (1 to
	// MaxLeaseClaimKeys of them) that are free or expired, and skips the
	// rest. It returns what it took, possibly nothing.
	ClaimAvailable(ctx context.Context, keys []string, limit int) ([]Lease, error)
	// Renew extends a lease this node still holds to a full lifetime from
	// now, or returns ErrLeaseLost.
	Renew(ctx context.Context, lease Lease) (Lease, error)
	// Release gives a lease up. Releasing a lease this node no longer holds
	// does nothing.
	Release(ctx context.Context, lease Lease) error
	// Hold claims key and keeps it renewed until ctx ends, then releases it.
	// It returns a context derived from ctx that ends as soon as the lease
	// is lost, or may have been: when renewal is refused, or fails for long
	// enough that the lease could expire before the next attempt. Work done
	// as the key's owner runs under that context. Hold returns ErrLeaseHeld
	// when the key is held.
	Hold(ctx context.Context, key string) (context.Context, Lease, error)
}

// Lease is one claim on a key.
type Lease struct {
	Key string
	// Fence identifies this claim; actions taken as the owner carry it.
	Fence int64
	// ExpiresAt is when the lease ends unless renewed, by PostgreSQL's
	// clock, which may differ from this node's.
	ExpiresAt time.Time
}
