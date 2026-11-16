// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package environment

import (
	"context"
	"errors"
	"fmt"

	"github.com/sudosylabs/proctor/server/app/exam/attempt"
	"github.com/sudosylabs/proctor/server/cluster"
	"github.com/sudosylabs/proctor/server/logging"
	"github.com/sudosylabs/proctor/server/model"
	"github.com/sudosylabs/proctor/server/store"
)

// Node is this node's execution environment runtime: the cluster wake and
// settle signals, the owner runtime and the execution host clients. It is
// inert until Start. Close stops the owners, then the cluster senders, then
// the host clients; machines keep running on their hosts for the next owner.
//
// Composition builds it in two steps because the application needs the
// waker and settle signals before it exists, and the owners need the
// Attempt service the application builds.
type Node struct {
	waker   *clusterWaker
	settles *ClusterSettles
	runtime *Runtime
	hosts   NodeHosts
	deps    NodeDependencies
}

// NodeHosts are the execution host clients the Node's owners and terminals
// reach machines through. The Node closes them last.
type NodeHosts interface {
	Hosts
	Close() error
}

// NodeDependencies is what NewNode takes from composition.
type NodeDependencies struct {
	Cluster      cluster.Transport
	Environments store.AttemptEnvironmentStore
	Evidence     store.ExamAttemptWorkspaceStore
	Leases       store.AttemptRuntimeLeaseStore
	Hosts        NodeHosts
	NodeID       string
	// Owners bounds how many environments this node owns at once.
	Owners int
	Logger *logging.Logger
}

// NewNode registers the wake and settle signals on the cluster. Nothing is
// dialed until a host is used.
func NewNode(deps NodeDependencies) (*Node, error) {
	logger := deps.Logger
	waker, err := newClusterWaker(deps.Cluster, func(err error) {
		logger.Warn("could not broadcast an attempt environment wake", logging.Err(err))
	})
	if err != nil {
		return nil, fmt.Errorf("construct attempt environment waker: %w", err)
	}
	settles, err := newClusterSettles(deps.Cluster, func(err error) {
		logger.Warn("could not broadcast an attempt environment settle", logging.Err(err))
	})
	if err != nil {
		return nil, err
	}
	return &Node{waker: waker, settles: settles, hosts: deps.Hosts, deps: deps}, nil
}

// Waker wakes an Attempt's owner here and on every peer.
func (n *Node) Waker() Waker { return n.waker }

// Settles delivers the settles owners complete, here or on a peer, to the
// seals waiting on them.
func (n *Node) Settles() *ClusterSettles { return n.settles }

// Hosts are the execution host clients, for the terminals.
func (n *Node) Hosts() Hosts { return n.hosts }

// Report logs an execution environment failure nobody else receives.
func (n *Node) Report(attemptID model.ExamAttemptID, err error) {
	n.deps.Logger.Warn("attempt environment failure",
		logging.String("exam_attempt_id", attemptID.String()), logging.Err(err))
}

// Complete builds the owners and their runtime over the Attempt service.
func (n *Node) Complete(attempts *attempt.Service) error {
	workspace, err := newAttemptWorkspace(attempts)
	if err != nil {
		return fmt.Errorf("construct attempt environment workspace: %w", err)
	}
	owners, err := NewOwners(OwnerDependencies{
		Environments: n.deps.Environments,
		Evidence:     n.deps.Evidence,
		Workspace:    workspace,
		Hosts:        n.hosts,
		Settled:      n.settles,
		Report:       n.Report,
	})
	if err != nil {
		return fmt.Errorf("construct attempt environment owners: %w", err)
	}
	runtime, err := NewRuntime(RuntimeDependencies{
		Leases:   n.deps.Leases,
		NodeID:   n.deps.NodeID,
		NewOwner: owners,
		Owners:   n.deps.Owners,
		Report:   n.Report,
	})
	if err != nil {
		return fmt.Errorf("construct attempt environment runtime: %w", err)
	}
	n.waker.attach(runtime)
	n.runtime = runtime
	return nil
}

// Start begins broadcasting wakes and settles and owning environments.
func (n *Node) Start(ctx context.Context) error {
	if err := n.waker.Start(ctx); err != nil {
		return err
	}
	if err := n.settles.Start(ctx); err != nil {
		return err
	}
	return n.runtime.Start(ctx)
}

// Close stops the owners, the cluster senders and the host clients.
func (n *Node) Close(ctx context.Context) error {
	return errors.Join(n.runtime.Close(ctx), n.waker.Close(ctx), n.settles.Close(ctx), n.hosts.Close())
}
