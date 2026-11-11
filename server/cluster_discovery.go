// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package server

import (
	"context"
	"time"

	"github.com/sudosylabs/proctor/server/cluster"
	"github.com/sudosylabs/proctor/server/store"
)

// storeClusterDiscovery adapts store.ClusterDiscoveryStore to cluster.DiscoveryStore.
type storeClusterDiscovery struct {
	store store.ClusterDiscoveryStore
}

func (s storeClusterDiscovery) Upsert(ctx context.Context, node cluster.DiscoveryNode) error {
	return s.store.Upsert(ctx, &store.ClusterDiscoveryNode{
		NodeID:           node.NodeID,
		AdvertiseAddress: node.AdvertiseAddress,
		ServerVersion:    node.ServerVersion,
		ProtocolMin:      node.ProtocolMin,
		ProtocolMax:      node.ProtocolMax,
		ExpiresAt:        node.ExpiresAt.UTC(),
		UpdatedAt:        node.UpdatedAt.UTC(),
	})
}

func (s storeClusterDiscovery) ListLive(ctx context.Context, now time.Time) ([]cluster.DiscoveryNode, error) {
	rows, err := s.store.ListLive(ctx, now.UTC())
	if err != nil {
		return nil, err
	}
	result := make([]cluster.DiscoveryNode, 0, len(rows))
	for _, row := range rows {
		if row == nil {
			continue
		}
		result = append(result, cluster.DiscoveryNode{
			NodeID:           row.NodeID,
			AdvertiseAddress: row.AdvertiseAddress,
			ServerVersion:    row.ServerVersion,
			ProtocolMin:      row.ProtocolMin,
			ProtocolMax:      row.ProtocolMax,
			ExpiresAt:        row.ExpiresAt.UTC(),
			UpdatedAt:        row.UpdatedAt.UTC(),
		})
	}
	return result, nil
}

func (s storeClusterDiscovery) Delete(ctx context.Context, nodeID string) error {
	return s.store.Delete(ctx, nodeID)
}

func (s storeClusterDiscovery) DeleteExpired(ctx context.Context, now time.Time) (int64, error) {
	return s.store.DeleteExpired(ctx, now.UTC())
}
