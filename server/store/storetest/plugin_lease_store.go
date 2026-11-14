// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package storetest

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/sudosylabs/proctor/server/model"
	"github.com/sudosylabs/proctor/server/store"
)

// TestPluginLeaseStore checks plugin leases: claiming a free key, refusing a
// live one, renewal and release only by the exact holder, expiry decided by
// PostgreSQL, fences that grow across release and reclaim, claiming the
// available keys of a set, racing claimers, the bounds, and that no plugin
// reaches another's leases.
func TestPluginLeaseStore(t *testing.T, stores store.Store) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	leases := stores.PluginLease()
	const alpha, beta = model.PluginID("alpha"), model.PluginID("beta")
	const lifetime = 30 * time.Second

	t.Run("claim renew release", func(t *testing.T) {
		held, err := leases.Claim(ctx, &store.PluginLeaseClaim{PluginID: alpha, Key: "grant/one", NodeID: "node-a", Lifetime: lifetime})
		require.NoError(t, err)
		require.NoError(t, held.Validate())
		require.Equal(t, "node-a", held.NodeID)
		require.Equal(t, held.AcquiredAt, held.RenewedAt)
		require.Equal(t, lifetime, held.ExpiresAt.Sub(held.AcquiredAt), "the lease lasts its lifetime from the database's now")

		for _, node := range []string{"node-b", "node-a"} {
			_, err = leases.Claim(ctx, &store.PluginLeaseClaim{PluginID: alpha, Key: "grant/one", NodeID: node, Lifetime: lifetime})
			require.ErrorIs(t, err, store.ErrPluginLeaseHeld, "a live lease is held, even against its own node %s", node)
		}

		renewed, err := leases.Renew(ctx, &store.PluginLeaseRenewal{
			PluginID: alpha, Key: "grant/one", NodeID: "node-a", Fence: held.Fence, Lifetime: 2 * lifetime,
		})
		require.NoError(t, err)
		require.Equal(t, held.Fence, renewed.Fence, "renewal keeps the fence")
		require.Equal(t, held.AcquiredAt, renewed.AcquiredAt)
		require.False(t, renewed.RenewedAt.Before(held.RenewedAt))
		require.Equal(t, 2*lifetime, renewed.ExpiresAt.Sub(renewed.RenewedAt))

		for name, renewal := range map[string]store.PluginLeaseRenewal{
			"another node":  {PluginID: alpha, Key: "grant/one", NodeID: "node-b", Fence: held.Fence, Lifetime: lifetime},
			"another fence": {PluginID: alpha, Key: "grant/one", NodeID: "node-a", Fence: held.Fence + 1, Lifetime: lifetime},
			"another key":   {PluginID: alpha, Key: "grant/two", NodeID: "node-a", Fence: held.Fence, Lifetime: lifetime},
		} {
			_, err := leases.Renew(ctx, &renewal)
			require.ErrorIs(t, err, store.ErrPluginLeaseLost, "renewal by %s is refused", name)
		}

		require.NoError(t, leases.Release(ctx, &store.PluginLeaseRelease{
			PluginID: alpha, Key: "grant/one", NodeID: "node-b", Fence: held.Fence,
		}), "releasing a lease the caller does not hold does nothing")
		_, err = leases.Claim(ctx, &store.PluginLeaseClaim{PluginID: alpha, Key: "grant/one", NodeID: "node-b", Lifetime: lifetime})
		require.ErrorIs(t, err, store.ErrPluginLeaseHeld, "a non-holder's release leaves the lease")

		release := &store.PluginLeaseRelease{PluginID: alpha, Key: "grant/one", NodeID: "node-a", Fence: held.Fence}
		require.NoError(t, leases.Release(ctx, release))
		require.NoError(t, leases.Release(ctx, release), "a repeated release is harmless")
		_, err = leases.Renew(ctx, &store.PluginLeaseRenewal{
			PluginID: alpha, Key: "grant/one", NodeID: "node-a", Fence: held.Fence, Lifetime: lifetime,
		})
		require.ErrorIs(t, err, store.ErrPluginLeaseLost, "a released lease cannot be renewed")

		next, err := leases.Claim(ctx, &store.PluginLeaseClaim{PluginID: alpha, Key: "grant/one", NodeID: "node-b", Lifetime: lifetime})
		require.NoError(t, err, "a released key is free")
		require.Greater(t, next.Fence, held.Fence, "a claim after release takes a larger fence")
	})

	t.Run("expiry", func(t *testing.T) {
		short := 50 * time.Millisecond
		held, err := leases.Claim(ctx, &store.PluginLeaseClaim{PluginID: alpha, Key: "grant/expiring", NodeID: "node-a", Lifetime: short})
		require.NoError(t, err)
		waitForPluginLeaseExpiry(t, held)

		_, err = leases.Renew(ctx, &store.PluginLeaseRenewal{
			PluginID: alpha, Key: held.Key, NodeID: "node-a", Fence: held.Fence, Lifetime: lifetime,
		})
		require.ErrorIs(t, err, store.ErrPluginLeaseLost, "an expired lease is lost even before anyone claims it")

		taken, err := leases.Claim(ctx, &store.PluginLeaseClaim{PluginID: alpha, Key: held.Key, NodeID: "node-b", Lifetime: lifetime})
		require.NoError(t, err, "an expired lease is free to claim")
		require.Greater(t, taken.Fence, held.Fence)
		require.Equal(t, "node-b", taken.NodeID)

		require.NoError(t, leases.Release(ctx, &store.PluginLeaseRelease{
			PluginID: alpha, Key: held.Key, NodeID: "node-a", Fence: held.Fence,
		}), "the dead holder's late release does nothing")
		_, err = leases.Renew(ctx, &store.PluginLeaseRenewal{
			PluginID: alpha, Key: held.Key, NodeID: "node-b", Fence: taken.Fence, Lifetime: lifetime,
		})
		require.NoError(t, err, "the new holder keeps its lease")
	})

	t.Run("claim available", func(t *testing.T) {
		_, err := leases.Claim(ctx, &store.PluginLeaseClaim{PluginID: alpha, Key: "set/b", NodeID: "node-b", Lifetime: lifetime})
		require.NoError(t, err)
		expired, err := leases.Claim(ctx, &store.PluginLeaseClaim{
			PluginID: alpha, Key: "set/d", NodeID: "node-b", Lifetime: store.PluginLeaseMinimumLifetime,
		})
		require.NoError(t, err)
		waitForPluginLeaseExpiry(t, expired)

		keys := []string{"set/d", "set/c", "set/b", "set/a", "set/c"}
		first, err := leases.ClaimAvailable(ctx, &store.PluginLeaseAvailableClaim{
			PluginID: alpha, Keys: keys, NodeID: "node-a", Lifetime: lifetime, Limit: 1,
		})
		require.NoError(t, err)
		require.Equal(t, []string{"set/a"}, leaseKeys(first), "the limit takes the first available keys in order")

		rest, err := leases.ClaimAvailable(ctx, &store.PluginLeaseAvailableClaim{
			PluginID: alpha, Keys: keys, NodeID: "node-a", Lifetime: lifetime, Limit: store.PluginLeaseClaimMaxKeys,
		})
		require.NoError(t, err)
		require.Equal(t, []string{"set/c", "set/d"}, leaseKeys(rest),
			"held keys are skipped, an expired one is taken, and a repeated key is claimed once")
		for _, lease := range rest {
			require.Equal(t, "node-a", lease.NodeID)
		}
		require.Greater(t, rest[1].Fence, expired.Fence, "taking an expired lease replaces its fence")

		none, err := leases.ClaimAvailable(ctx, &store.PluginLeaseAvailableClaim{
			PluginID: alpha, Keys: keys, NodeID: "node-c", Lifetime: lifetime, Limit: 10,
		})
		require.NoError(t, err)
		require.Empty(t, none, "nothing is available once every key is held")
	})

	t.Run("plugins are isolated", func(t *testing.T) {
		mine, err := leases.Claim(ctx, &store.PluginLeaseClaim{PluginID: alpha, Key: "same/key", NodeID: "node-a", Lifetime: lifetime})
		require.NoError(t, err)
		theirs, err := leases.Claim(ctx, &store.PluginLeaseClaim{PluginID: beta, Key: "same/key", NodeID: "node-a", Lifetime: lifetime})
		require.NoError(t, err, "the same key is free in another plugin")

		_, err = leases.Renew(ctx, &store.PluginLeaseRenewal{
			PluginID: beta, Key: "same/key", NodeID: "node-a", Fence: mine.Fence, Lifetime: lifetime,
		})
		require.ErrorIs(t, err, store.ErrPluginLeaseLost, "one plugin never renews another's lease")
		require.NoError(t, leases.Release(ctx, &store.PluginLeaseRelease{
			PluginID: beta, Key: "same/key", NodeID: "node-a", Fence: mine.Fence,
		}))
		_, err = leases.Renew(ctx, &store.PluginLeaseRenewal{
			PluginID: alpha, Key: "same/key", NodeID: "node-a", Fence: mine.Fence, Lifetime: lifetime,
		})
		require.NoError(t, err, "one plugin never releases another's lease")
		claimed, err := leases.ClaimAvailable(ctx, &store.PluginLeaseAvailableClaim{
			PluginID: beta, Keys: []string{"same/key"}, NodeID: "node-b", Lifetime: lifetime, Limit: 1,
		})
		require.NoError(t, err)
		require.Empty(t, claimed, "the other plugin's own lease still holds its key")
		require.NotEqual(t, mine.Fence, theirs.Fence)
	})

	t.Run("racing claimers", func(t *testing.T) {
		const racers = 8
		var wg sync.WaitGroup
		results := make(chan error, racers)
		for i := range racers {
			wg.Go(func() {
				_, err := leases.Claim(ctx, &store.PluginLeaseClaim{
					PluginID: alpha, Key: "race/one", NodeID: "node-" + string(rune('a'+i)), Lifetime: lifetime,
				})
				results <- err
			})
		}
		wg.Wait()
		close(results)
		won := 0
		for err := range results {
			if err == nil {
				won++
				continue
			}
			require.ErrorIs(t, err, store.ErrPluginLeaseHeld, "every losing claimer finds the key held")
		}
		require.Equal(t, 1, won, "exactly one claimer wins")
	})

	t.Run("bounds", func(t *testing.T) {
		valid := store.PluginLeaseClaim{PluginID: alpha, Key: "bounds/k", NodeID: "node-a", Lifetime: lifetime}
		for name, mutate := range map[string]func(*store.PluginLeaseClaim){
			"short lifetime":      func(c *store.PluginLeaseClaim) { c.Lifetime = store.PluginLeaseMinimumLifetime - time.Millisecond },
			"long lifetime":       func(c *store.PluginLeaseClaim) { c.Lifetime = store.PluginLeaseMaximumLifetime + time.Millisecond },
			"fractional lifetime": func(c *store.PluginLeaseClaim) { c.Lifetime = 20*time.Millisecond + time.Microsecond },
			"invalid key":         func(c *store.PluginLeaseClaim) { c.Key = "a//b" },
			"long key":            func(c *store.PluginLeaseClaim) { c.Key = strings.Repeat("k", store.PluginRecordKeyMaxBytes+1) },
			"empty node":          func(c *store.PluginLeaseClaim) { c.NodeID = "" },
			"long node": func(c *store.PluginLeaseClaim) {
				c.NodeID = strings.Repeat("n", store.ClusterDiscoveryNodeIDMaxBytes+1)
			},
			"invalid plugin": func(c *store.PluginLeaseClaim) { c.PluginID = "Alpha" },
		} {
			claim := valid
			mutate(&claim)
			_, err := leases.Claim(ctx, &claim)
			var target *store.ErrInvalidInput
			require.True(t, errors.As(err, &target), "%s is invalid: %v", name, err)
		}
		tooMany := make([]string, store.PluginLeaseClaimMaxKeys+1)
		for i := range tooMany {
			tooMany[i] = "bounds/" + strings.Repeat("k", i+1)
		}
		for name, claim := range map[string]store.PluginLeaseAvailableClaim{
			"no keys":       {PluginID: alpha, NodeID: "node-a", Lifetime: lifetime, Limit: 1},
			"too many keys": {PluginID: alpha, Keys: tooMany, NodeID: "node-a", Lifetime: lifetime, Limit: 1},
			"zero limit":    {PluginID: alpha, Keys: []string{"bounds/k"}, NodeID: "node-a", Lifetime: lifetime},
			"invalid key":   {PluginID: alpha, Keys: []string{"bounds/k", "/k"}, NodeID: "node-a", Lifetime: lifetime, Limit: 1},
		} {
			_, err := leases.ClaimAvailable(ctx, &claim)
			var target *store.ErrInvalidInput
			require.True(t, errors.As(err, &target), "%s is invalid: %v", name, err)
		}
		var target *store.ErrInvalidInput
		_, err := leases.Renew(ctx, &store.PluginLeaseRenewal{PluginID: alpha, Key: "bounds/k", NodeID: "node-a", Lifetime: lifetime})
		require.ErrorAs(t, err, &target, "renewal needs a fence")
		require.ErrorAs(t, leases.Release(ctx, &store.PluginLeaseRelease{PluginID: alpha, Key: "bounds/k", NodeID: "node-a"}), &target,
			"release needs a fence")
	})
}

// waitForPluginLeaseExpiry waits out the lease's lifetime, counted from
// after the claim returned, so PostgreSQL's clock has passed its expiry.
func waitForPluginLeaseExpiry(t *testing.T, lease *store.PluginLease) {
	t.Helper()
	time.Sleep(lease.ExpiresAt.Sub(lease.RenewedAt) + 50*time.Millisecond)
}

func leaseKeys(leases []*store.PluginLease) []string {
	keys := make([]string, 0, len(leases))
	for _, lease := range leases {
		keys = append(keys, lease.Key)
	}
	return keys
}
