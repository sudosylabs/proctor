// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package server

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/sudosylabs/proctor/server/model"
	"github.com/sudosylabs/proctor/server/plugin"
	"github.com/sudosylabs/proctor/server/pluginhost"
	"github.com/sudosylabs/proctor/server/store"
)

// storePluginLeases adapts the plugin lease Store to the plugin host, which
// may not import store. The host names the plugin and this node on every
// call; this adapter only translates, and answers with the plugin package's
// errors.
type storePluginLeases struct {
	store store.PluginLeaseStore
}

var _ pluginhost.LeaseStore = storePluginLeases{}

// newStorePluginLeases returns the adapter, or nil without persistence so
// the host reports leases unavailable.
func newStorePluginLeases(leases store.PluginLeaseStore) pluginhost.LeaseStore {
	if leases == nil {
		return nil
	}
	return storePluginLeases{store: leases}
}

func (l storePluginLeases) Claim(ctx context.Context, pluginID, nodeID, key string, lifetime time.Duration) (plugin.Lease, error) {
	lease, err := l.store.Claim(ctx, &store.PluginLeaseClaim{
		PluginID: model.PluginID(pluginID), Key: key, NodeID: nodeID, Lifetime: lifetime,
	})
	if err != nil {
		return plugin.Lease{}, pluginLeaseError(err)
	}
	return pluginLease(lease), nil
}

func (l storePluginLeases) ClaimAvailable(
	ctx context.Context,
	pluginID, nodeID string,
	keys []string,
	lifetime time.Duration,
	limit int,
) ([]plugin.Lease, error) {
	leases, err := l.store.ClaimAvailable(ctx, &store.PluginLeaseAvailableClaim{
		PluginID: model.PluginID(pluginID), Keys: keys, NodeID: nodeID, Lifetime: lifetime, Limit: limit,
	})
	if err != nil {
		return nil, pluginLeaseError(err)
	}
	taken := make([]plugin.Lease, 0, len(leases))
	for _, lease := range leases {
		taken = append(taken, pluginLease(lease))
	}
	return taken, nil
}

func (l storePluginLeases) Renew(
	ctx context.Context,
	pluginID, nodeID string,
	lease plugin.Lease,
	lifetime time.Duration,
) (plugin.Lease, error) {
	renewed, err := l.store.Renew(ctx, &store.PluginLeaseRenewal{
		PluginID: model.PluginID(pluginID), Key: lease.Key, NodeID: nodeID, Fence: lease.Fence, Lifetime: lifetime,
	})
	if err != nil {
		return plugin.Lease{}, pluginLeaseError(err)
	}
	return pluginLease(renewed), nil
}

func (l storePluginLeases) Release(ctx context.Context, pluginID, nodeID string, lease plugin.Lease) error {
	return pluginLeaseError(l.store.Release(ctx, &store.PluginLeaseRelease{
		PluginID: model.PluginID(pluginID), Key: lease.Key, NodeID: nodeID, Fence: lease.Fence,
	}))
}

func pluginLease(lease *store.PluginLease) plugin.Lease {
	return plugin.Lease{Key: lease.Key, Fence: lease.Fence, ExpiresAt: lease.ExpiresAt}
}

// pluginLeaseError turns a Store error into the plugin package's.
func pluginLeaseError(err error) error {
	var invalid *store.ErrInvalidInput
	switch {
	case err == nil:
		return nil
	case errors.Is(err, store.ErrPluginLeaseHeld):
		return plugin.ErrLeaseHeld
	case errors.Is(err, store.ErrPluginLeaseLost):
		return plugin.ErrLeaseLost
	case errors.As(err, &invalid):
		return plugin.ErrInvalidArgument
	default:
		return fmt.Errorf("plugin leases: %w", err)
	}
}
