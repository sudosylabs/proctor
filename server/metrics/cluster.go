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

	"github.com/prometheus/client_golang/prometheus"
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

func (m *Module) newClusterCollectors() {
	m.clusterOperations = prometheus.NewCounterVec(
		prometheus.CounterOpts{Namespace: "proctor", Subsystem: "cluster", Name: "operations_total", Help: "Cluster transport operations."},
		[]string{"operation", "event", "outcome"},
	)
	m.clusterDuration = prometheus.NewHistogramVec(
		prometheus.HistogramOpts{
			Namespace: "proctor",
			Subsystem: "cluster",
			Name:      "operation_duration_seconds",
			Help:      "Cluster transport operation duration.",
			Buckets:   prometheus.DefBuckets,
		},
		[]string{"operation", "event"},
	)
	m.clusterMessages = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Namespace: "proctor",
			Subsystem: "cluster",
			Name:      "messages_total",
			Help:      "Cluster messages by direction, registered event, and outcome.",
		},
		[]string{"direction", "event", "outcome"},
	)
	m.clusterMessageBytes = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Namespace: "proctor",
			Subsystem: "cluster",
			Name:      "message_bytes_total",
			Help:      "Cluster wire bytes by direction and registered event.",
		},
		[]string{"direction", "event"},
	)
	m.clusterMembership = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Namespace: "proctor",
			Subsystem: "cluster",
			Name:      "membership_events_total",
			Help:      "Memberlist membership transitions.",
		},
		[]string{"event"},
	)
	m.clusterDiscovery = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Namespace: "proctor",
			Subsystem: "cluster",
			Name:      "discovery_operations_total",
			Help:      "Cluster discovery and rejoin work by operation and outcome.",
		},
		[]string{"operation", "outcome"},
	)
	m.clusterAdmission = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Namespace: "proctor",
			Subsystem: "cluster",
			Name:      "admission_rejections_total",
			Help:      "Rejected cluster peers by bounded reason.",
		},
		[]string{"reason"},
	)
	m.clusterFanout = prometheus.NewHistogram(prometheus.HistogramOpts{
		Namespace: "proctor",
		Subsystem: "cluster",
		Name:      "broadcast_recipients",
		Help:      "Peer nodes selected by cluster broadcasts.",
		Buckets:   prometheus.ExponentialBuckets(1, 2, 10),
	})
}
func (m *Module) ObserveCluster(operation, event string, err error, duration time.Duration) {
	m.clusterOperations.WithLabelValues(operation, event, outcome(err)).Inc()
	m.clusterDuration.WithLabelValues(operation, event).Observe(duration.Seconds())
}
func (m *Module) ObserveClusterMessage(direction, event, result string, bytes int) {
	if m == nil {
		return
	}
	m.clusterMessages.WithLabelValues(direction, event, result).Inc()
	if bytes > 0 {
		m.clusterMessageBytes.WithLabelValues(direction, event).Add(float64(bytes))
	}
}
func (m *Module) ObserveClusterMembership(event string) {
	if m != nil {
		m.clusterMembership.WithLabelValues(event).Inc()
	}
}
func (m *Module) ObserveClusterDiscovery(operation, result string) {
	if m != nil {
		m.clusterDiscovery.WithLabelValues(operation, result).Inc()
	}
}
func (m *Module) ObserveClusterAdmission(reason string) {
	if m != nil {
		m.clusterAdmission.WithLabelValues(reason).Inc()
	}
}
func (m *Module) ObserveClusterFanout(recipients int) {
	if m != nil {
		m.clusterFanout.Observe(float64(max(recipients, 0)))
	}
}
func (m *Module) SetClusterPeers(peers int) { m.clusterPeerCount.Store(int64(max(peers, 0))) }
func (m *Module) SetClusterPeerSource(source func() int) {
	m.clusterPeerMu.Lock()
	m.clusterPeerSource = source
	m.clusterPeerMu.Unlock()
}
func (m *Module) currentClusterPeers() float64 {
	m.clusterPeerMu.RLock()
	source := m.clusterPeerSource
	m.clusterPeerMu.RUnlock()
	if source != nil {
		return float64(max(source(), 0))
	}
	return float64(m.clusterPeerCount.Load())
}
