// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package store

import (
	"context"
	"errors"
	"time"

	"github.com/sudosylabs/proctor/server/model"
)

const (
	// PluginLeaseMinimumLifetime and PluginLeaseMaximumLifetime bound the
	// lifetime one claim or renewal asks for. Lifetimes are whole
	// milliseconds.
	PluginLeaseMinimumLifetime = 10 * time.Millisecond
	PluginLeaseMaximumLifetime = 10 * time.Minute
	// PluginLeaseClaimMaxKeys bounds the keys one ClaimAvailable considers.
	PluginLeaseClaimMaxKeys = 100
)

// ErrPluginLeaseHeld is returned by Claim when another holder's lease on the
// key has not expired.
var ErrPluginLeaseHeld = errors.New("plugin lease is held")

// ErrPluginLeaseLost is returned by Renew when the caller no longer holds the
// lease: it expired, it was released, or another claim replaced it.
var ErrPluginLeaseLost = errors.New("plugin lease is lost")

// PluginLease is one node's fenced claim on one of a plugin's keys. It is
// disposable runtime state, like the serving node lease: PostgreSQL time
// decides when it expires, and an expired lease is lost even if nobody has
// claimed it since.
type PluginLease struct {
	PluginID model.PluginID
	Key      string
	NodeID   string
	// Fence identifies this claim. Every claim of any key takes a fence
	// from one PostgreSQL sequence, so a key's fences only grow, even
	// after its lease is released and claimed again. An owner action
	// carries it so that a newer claim's holder can refuse a stale one.
	Fence      int64
	AcquiredAt time.Time
	RenewedAt  time.Time
	ExpiresAt  time.Time
}

// Validate checks a rehydrated lease.
func (l *PluginLease) Validate() error {
	if l == nil || !l.PluginID.IsValid() || !ValidPluginRecordKey(l.Key) || !ValidPluginLeaseNodeID(l.NodeID) ||
		l.Fence < 1 || l.AcquiredAt.IsZero() || l.RenewedAt.Before(l.AcquiredAt) || !l.ExpiresAt.After(l.RenewedAt) {
		return ErrInvalidState
	}
	return nil
}

// PluginLeaseClaim asks for one key for Lifetime.
type PluginLeaseClaim struct {
	PluginID model.PluginID
	Key      string
	NodeID   string
	Lifetime time.Duration
}

// PluginLeaseAvailableClaim asks for up to Limit of Keys (1 to
// PluginLeaseClaimMaxKeys of them, Limit no more than that) for Lifetime,
// taking only those nobody holds.
type PluginLeaseAvailableClaim struct {
	PluginID model.PluginID
	Keys     []string
	NodeID   string
	Lifetime time.Duration
	Limit    int
}

// PluginLeaseRenewal extends the holder's lease to Lifetime from now.
type PluginLeaseRenewal struct {
	PluginID model.PluginID
	Key      string
	NodeID   string
	Fence    int64
	Lifetime time.Duration
}

// PluginLeaseRelease gives up the holder's lease.
type PluginLeaseRelease struct {
	PluginID model.PluginID
	Key      string
	NodeID   string
	Fence    int64
}

// PluginLeaseStore keeps the plugin leases, one table shared by every
// plugin and keyed by plugin ID and key. A lease belongs to exactly one
// plugin and is held by one node; nothing claims or releases across plugins.
//
// Each operation is one statement, decided at PostgreSQL's clock_timestamp().
// Of two claims racing for a free or expired key exactly one wins; a renewal
// succeeds only for the holder's exact fence before expiry, so a renewal
// racing a claim of the same expired lease loses.
type PluginLeaseStore interface {
	// Claim takes a key that is free or whose lease has expired, with a new
	// fence. A live lease, even one the same node holds, is
	// ErrPluginLeaseHeld.
	Claim(ctx context.Context, claim *PluginLeaseClaim) (*PluginLease, error)
	// ClaimAvailable takes, in key order, up to Limit of the keys that are
	// free or expired, each with a new fence, and skips the rest. It returns
	// what it took, possibly nothing.
	ClaimAvailable(ctx context.Context, claim *PluginLeaseAvailableClaim) ([]*PluginLease, error)
	// Renew extends a lease the caller still holds, or returns
	// ErrPluginLeaseLost.
	Renew(ctx context.Context, renewal *PluginLeaseRenewal) (*PluginLease, error)
	// Release removes a lease the caller still holds. Releasing a lease it
	// no longer holds does nothing, so a repeated release is harmless.
	Release(ctx context.Context, release *PluginLeaseRelease) error
}

// ValidPluginLeaseLifetime reports whether lifetime is whole milliseconds
// between PluginLeaseMinimumLifetime and PluginLeaseMaximumLifetime.
func ValidPluginLeaseLifetime(lifetime time.Duration) bool {
	return lifetime >= PluginLeaseMinimumLifetime && lifetime <= PluginLeaseMaximumLifetime && lifetime%time.Millisecond == 0
}

// ValidPluginLeaseNodeID reports whether nodeID can hold a plugin lease.
func ValidPluginLeaseNodeID(nodeID string) bool {
	return nodeID != "" && len(nodeID) <= ClusterDiscoveryNodeIDMaxBytes
}
