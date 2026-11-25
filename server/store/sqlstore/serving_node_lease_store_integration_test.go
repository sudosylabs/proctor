//go:build integration

// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package sqlstore

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/sudosylabs/proctor/server/model"
	"github.com/sudosylabs/proctor/server/store"
)

// TestServingNodeLeaseUpsertDecidesOnTheDatabaseClock pins the renewal rules
// the single-statement upsert carries: a fresh lease and a renewal succeed, a
// lease that expired under its own ID is never revived, another live lease is
// never replaced, and an expired one is.
func TestServingNodeLeaseUpsertDecidesOnTheDatabaseClock(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	sqlStore := openTestStore(t)
	resetPristineTestStore(t, sqlStore)
	leases := sqlStore.ServingNodeLease()

	first := model.NewID()
	created, err := leases.Upsert(ctx, &store.ServingNodeLeaseClaim{NodeID: "node-a", LeaseID: first, Lifetime: 30 * time.Second})
	require.NoError(t, err)
	require.Equal(t, first, created.LeaseID)
	require.InDelta(t, (30 * time.Second).Seconds(), created.ExpiresAt.Sub(created.UpdatedAt).Seconds(), 0.001)

	renewed, err := leases.Upsert(ctx, &store.ServingNodeLeaseClaim{NodeID: "node-a", LeaseID: first, Lifetime: 30 * time.Second})
	require.NoError(t, err)
	require.False(t, renewed.UpdatedAt.Before(created.UpdatedAt))

	_, err = leases.Upsert(ctx, &store.ServingNodeLeaseClaim{NodeID: "node-a", LeaseID: model.NewID(), Lifetime: 30 * time.Second})
	require.True(t, store.IsConflict(err), "another live lease for the node is never replaced: %v", err)

	_, err = sqlStore.GetMaster().Exec(ctx, `UPDATE serving_node_leases SET updated_at = clock_timestamp() - interval '2 minutes', expires_at = clock_timestamp() - interval '1 minute' WHERE node_id = 'node-a'`)
	require.NoError(t, err)
	_, err = leases.Upsert(ctx, &store.ServingNodeLeaseClaim{NodeID: "node-a", LeaseID: first, Lifetime: 30 * time.Second})
	require.True(t, store.IsConflict(err), "a lease that expired under its own ID is never revived: %v", err)

	second := model.NewID()
	replaced, err := leases.Upsert(ctx, &store.ServingNodeLeaseClaim{NodeID: "node-a", LeaseID: second, Lifetime: 30 * time.Second})
	require.NoError(t, err)
	require.Equal(t, second, replaced.LeaseID, "an expired lease gives way to a new one")
}

// TestServingNodeLeaseRenewsWhileTheMainPoolIsExhausted is the reason the
// control pool exists: with every main-pool connection held, the lease still
// renews.
func TestServingNodeLeaseRenewsWhileTheMainPoolIsExhausted(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	settings := testSettings(t)
	settings.MaxOpenConnections, settings.MaxIdleConnections = 2, 2
	sqlStore, err := New(ctx, settings)
	require.NoError(t, err)
	t.Cleanup(func() { _ = sqlStore.Close() })
	resetPristineTestStore(t, sqlStore)

	held := make([]*sql.Conn, 0, settings.MaxOpenConnections)
	for range settings.MaxOpenConnections {
		conn, connErr := sqlStore.GetMaster().DB().Conn(ctx)
		require.NoError(t, connErr)
		held = append(held, conn)
	}
	t.Cleanup(func() {
		for _, conn := range held {
			_ = conn.Close()
		}
	})

	renewCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	_, err = sqlStore.ServingNodeLease().Upsert(renewCtx, &store.ServingNodeLeaseClaim{NodeID: "node-a", LeaseID: model.NewID(), Lifetime: 30 * time.Second})
	require.NoError(t, err, "the lease renews through the control pool")

	stats := sqlStore.PoolStats()
	require.Len(t, stats, 2)
	require.Equal(t, PoolControl, stats[1].Pool)
	require.Equal(t, 2, stats[1].Stats.MaxOpenConnections)
}
