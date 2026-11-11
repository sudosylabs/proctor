// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package metrics

import (
	"context"
	"sync"
	"time"

	"github.com/sudosylabs/proctor/server/cluster"
)

// WrapCluster returns next with every transport operation and received
// message timed. An event becomes a label only once a handler is registered
// for it, so a peer cannot mint label values. It adds measurement only.
func WrapCluster(next cluster.Transport, module *Module) cluster.Transport {
	return &measuredCluster{next: next, metrics: module, events: make(map[cluster.Event]struct{})}
}

type measuredCluster struct {
	next    cluster.Transport
	metrics *Module
	eventMu sync.RWMutex
	events  map[cluster.Event]struct{}
}

func (m *measuredCluster) NodeID() string { return m.next.NodeID() }
func (m *measuredCluster) Start(ctx context.Context) error {
	started := time.Now()
	err := m.next.Start(ctx)
	m.observe("start", "", err, started)
	return err
}
func (m *measuredCluster) Stop(ctx context.Context) error {
	started := time.Now()
	err := m.next.Stop(ctx)
	m.observe("stop", "", err, started)
	return err
}
func (m *measuredCluster) Ping(ctx context.Context) error {
	started := time.Now()
	err := m.next.Ping(ctx)
	m.observe("ping", "", err, started)
	return err
}
func (m *measuredCluster) RegisterHandler(event cluster.Event, handler cluster.Handler) error {
	started := time.Now()
	if handler == nil {
		err := m.next.RegisterHandler(event, nil)
		m.observe("register", event, err, started)
		return err
	}
	wrapped := func(ctx context.Context, message *cluster.Message) error {
		received := time.Now()
		err := handler(ctx, message)
		m.observe("receive", event, err, received)
		return err
	}
	err := m.next.RegisterHandler(event, wrapped)
	if err == nil {
		m.eventMu.Lock()
		if m.events == nil {
			m.events = make(map[cluster.Event]struct{})
		}
		m.events[event] = struct{}{}
		m.eventMu.Unlock()
	}
	m.observe("register", event, err, started)
	return err
}
func (m *measuredCluster) Broadcast(ctx context.Context, message *cluster.Message) error {
	started := time.Now()
	var event cluster.Event
	if message != nil {
		event = message.Event
	}
	err := m.next.Broadcast(ctx, message)
	m.observe("broadcast", event, err, started)
	return err
}
func (m *measuredCluster) SendToNode(ctx context.Context, nodeID string, message *cluster.Message) error {
	started := time.Now()
	var event cluster.Event
	if message != nil {
		event = message.Event
	}
	err := m.next.SendToNode(ctx, nodeID, message)
	m.observe("send", event, err, started)
	return err
}
func (m *measuredCluster) observe(operation string, event cluster.Event, err error, started time.Time) {
	m.metrics.ObserveCluster(operation, m.metricEvent(event), err, time.Since(started))
	if peers, ok := m.next.(interface{ PeerCount() int }); ok {
		m.metrics.SetClusterPeers(peers.PeerCount())
	}
}

func (m *measuredCluster) metricEvent(event cluster.Event) string {
	if event == "" {
		return "none"
	}
	m.eventMu.RLock()
	_, registered := m.events[event]
	m.eventMu.RUnlock()
	if !registered {
		return "unregistered"
	}
	return string(event)
}

var _ cluster.Transport = (*measuredCluster)(nil)
