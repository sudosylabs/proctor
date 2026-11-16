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
	"strings"
	"time"

	"github.com/sudosylabs/proctor/server/model"
)

const (
	ServingNodeLeaseMinimumLifetime = 10 * time.Millisecond
	ServingNodeLeaseMaximumLifetime = 5 * time.Minute
)

// ServingNodeLease is proof, on the database clock, that a node may still
// serve this installation. It is disposable runtime state, not a membership,
// discovery or event record.
type ServingNodeLease struct {
	NodeID    string
	LeaseID   string
	UpdatedAt time.Time
	ExpiresAt time.Time
}

func (l *ServingNodeLease) Validate() error {
	if l == nil || strings.TrimSpace(l.NodeID) == "" || len(l.NodeID) > ClusterDiscoveryNodeIDMaxBytes ||
		!model.IsValidOpaqueID(l.LeaseID) || l.UpdatedAt.IsZero() || !l.ExpiresAt.After(l.UpdatedAt) {
		return errors.New("serving node lease is invalid")
	}
	return nil
}

// ServingNodeLeaseClaim is one process's renewable claim. LeaseID stops an old
// process from renewing or deleting the lease of a successor that reuses the
// same NodeID after expiry.
type ServingNodeLeaseClaim struct {
	NodeID   string
	LeaseID  string
	Lifetime time.Duration
}

// ServingNodeLeaseStore holds the serving fence every backend checks. Upsert
// and Delete serialize with offline administrator recovery.
type ServingNodeLeaseStore interface {
	Upsert(context.Context, *ServingNodeLeaseClaim) (*ServingNodeLease, error)
	Delete(context.Context, *ServingNodeLeaseDeletion) error
}

// ServingNodeLeaseDeletion is the input to ServingNodeLeaseStore.Delete.
type ServingNodeLeaseDeletion struct {
	NodeID  string
	LeaseID string
}
