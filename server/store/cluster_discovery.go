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
	"fmt"
	"strings"
	"time"
	"unicode/utf8"
)

const (
	// ClusterDiscoveryNodeIDMaxBytes bounds the operator-configured node ID.
	ClusterDiscoveryNodeIDMaxBytes = 128
	// ClusterDiscoveryAdvertiseAddressMaxBytes bounds the join address.
	ClusterDiscoveryAdvertiseAddressMaxBytes = 512
	// ClusterDiscoveryServerVersionMaxBytes bounds the advertised version.
	ClusterDiscoveryServerVersionMaxBytes = 128
)

// ClusterDiscoveryNode is a disposable bootstrap record for finding peer join
// addresses. It is not a message queue: never store event payloads in it.
type ClusterDiscoveryNode struct {
	NodeID           string
	AdvertiseAddress string
	ServerVersion    string
	ProtocolMin      int
	ProtocolMax      int
	// ExpiresAt is exclusive: the record is dead at that instant.
	ExpiresAt time.Time
	UpdatedAt time.Time
}

// Validate checks the record's bounds and lifetime without I/O.
func (n *ClusterDiscoveryNode) Validate() error {
	if n == nil {
		return errors.New("cluster discovery node is nil")
	}
	nodeID := strings.TrimSpace(n.NodeID)
	if nodeID == "" || len(nodeID) > ClusterDiscoveryNodeIDMaxBytes || !validClusterDiscoveryName(nodeID) {
		return fmt.Errorf("cluster discovery node_id is invalid")
	}
	address := strings.TrimSpace(n.AdvertiseAddress)
	if address == "" ||
		len(address) > ClusterDiscoveryAdvertiseAddressMaxBytes ||
		!utf8.ValidString(address) {
		return fmt.Errorf("cluster discovery advertise_address is invalid")
	}
	version := strings.TrimSpace(n.ServerVersion)
	if version == "" ||
		len(version) > ClusterDiscoveryServerVersionMaxBytes ||
		!utf8.ValidString(version) {
		return fmt.Errorf("cluster discovery server_version is invalid")
	}
	if n.ProtocolMin <= 0 || n.ProtocolMax < n.ProtocolMin {
		return fmt.Errorf("cluster discovery protocol range is invalid")
	}
	if n.UpdatedAt.IsZero() || !n.ExpiresAt.After(n.UpdatedAt) {
		return fmt.Errorf("cluster discovery lifetime is invalid")
	}
	return nil
}

// IsLive reports whether now is before ExpiresAt; a zero now is never live.
func (n *ClusterDiscoveryNode) IsLive(now time.Time) bool {
	return n != nil && !now.IsZero() && n.ExpiresAt.After(now)
}

// ClusterDiscoveryStore persists cluster bootstrap records. The store enforces
// expiry and uniqueness; callers treat rows as disposable.
type ClusterDiscoveryStore interface {
	// Upsert inserts or replaces the record; its ExpiresAt is taken as given.
	Upsert(context.Context, *ClusterDiscoveryNode) error
	// ListLive returns records with ExpiresAt after liveAt, by node ID.
	ListLive(ctx context.Context, liveAt time.Time) ([]*ClusterDiscoveryNode, error)
	Delete(context.Context, string) error
	// DeleteExpired removes records with ExpiresAt at or before expiredAt.
	DeleteExpired(ctx context.Context, expiredAt time.Time) (int64, error)
}

func validClusterDiscoveryName(value string) bool {
	for _, character := range value {
		if (character < 'a' || character > 'z') &&
			(character < 'A' || character > 'Z') &&
			(character < '0' || character > '9') &&
			character != '.' && character != '_' && character != '-' {
			return false
		}
	}
	return true
}
