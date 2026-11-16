// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package metrics

import (
	"time"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/sudosylabs/proctor/server/store/localcachelayer"
	"github.com/sudosylabs/proctor/server/store/timerlayer"
)

func (m *Module) newStoreAndWebSocketCollectors() {
	m.storeDuration = prometheus.NewHistogramVec(
		prometheus.HistogramOpts{
			Namespace: "proctor",
			Subsystem: "store",
			Name:      "operation_duration_seconds",
			Help:      "Authoritative store operation duration.",
			Buckets:   prometheus.DefBuckets,
		},
		[]string{"operation", "outcome"},
	)
	m.storeCache = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Namespace: "proctor",
			Subsystem: "store_cache",
			Name:      "requests_total",
			Help:      "Process-local store cache decisions.",
		},
		[]string{"operation", "outcome"},
	)
	m.storeRetries = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Namespace: "proctor",
			Subsystem: "store",
			Name:      "retries_total",
			Help:      "Aggregate authoritative-store retry decisions.",
		},
		[]string{"outcome"},
	)
	m.websocketConnections = prometheus.NewGauge(prometheus.GaugeOpts{
		Namespace: "proctor",
		Subsystem: "websocket",
		Name:      "connections",
		Help:      "WebSocket connections attached to this node.",
	})
	m.websocketTransitions = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Namespace: "proctor",
			Subsystem: "websocket",
			Name:      "transitions_total",
			Help:      "WebSocket connection transitions.",
		},
		[]string{"transition", "outcome"},
	)
	m.websocketBackpressure = prometheus.NewCounter(prometheus.CounterOpts{
		Namespace: "proctor",
		Subsystem: "websocket",
		Name:      "backpressure_disconnects_total",
		Help:      "WebSocket connections closed by outbound backpressure.",
	})
	m.websocketMessages = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Namespace: "proctor",
			Subsystem: "websocket",
			Name:      "messages_total",
			Help:      "WebSocket messages processed by direction, kind, and outcome.",
		},
		[]string{"direction", "kind", "outcome"},
	)
	m.websocketMessageBytes = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Namespace: "proctor",
			Subsystem: "websocket",
			Name:      "message_bytes_total",
			Help:      "Approximate WebSocket payload bytes processed.",
		},
		[]string{"direction", "kind"},
	)
	m.websocketBroadcasts = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Namespace: "proctor",
			Subsystem: "websocket",
			Name:      "broadcasts_total",
			Help:      "Local WebSocket publications by bounded event class and outcome.",
		},
		[]string{"event", "outcome"},
	)
	m.websocketFanout = prometheus.NewHistogram(prometheus.HistogramOpts{
		Namespace: "proctor",
		Subsystem: "websocket",
		Name:      "broadcast_recipients",
		Help:      "Connections selected by a local WebSocket publication.",
		Buckets:   prometheus.ExponentialBuckets(1, 2, 12),
	})
	m.websocketReplays = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Namespace: "proctor",
			Subsystem: "websocket",
			Name:      "replays_total",
			Help:      "WebSocket resume attempts by outcome.",
		},
		[]string{"outcome"},
	)
	m.websocketReplayEvents = prometheus.NewHistogram(prometheus.HistogramOpts{
		Namespace: "proctor",
		Subsystem: "websocket",
		Name:      "replay_events",
		Help:      "Events restored by successful WebSocket resumes.",
		Buckets:   prometheus.ExponentialBuckets(1, 2, 8),
	})
	m.websocketSubscriptions = prometheus.NewGauge(prometheus.GaugeOpts{
		Namespace: "proctor",
		Subsystem: "websocket",
		Name:      "subscriptions",
		Help:      "Active WebSocket subscriptions on this node.",
	})
}
func (m *Module) Observe(operation timerlayer.Operation, result timerlayer.Outcome, duration time.Duration) {
	m.storeDuration.WithLabelValues(operation.String(), string(result)).Observe(duration.Seconds())
}
func (m *Module) ObserveStoreCache(operation localcachelayer.Operation, result localcachelayer.Outcome) {
	m.storeCache.WithLabelValues(operation.String(), string(result)).Inc()
}
func (m *Module) ObserveStoreRetry(result string) {
	if m != nil {
		m.storeRetries.WithLabelValues(result).Inc()
	}
}
func (m *Module) WebSocketOpened(outcome string) {
	m.websocketTransitions.WithLabelValues("open", outcome).Inc()
	if outcome == "success" {
		m.websocketConnections.Inc()
	}
}
func (m *Module) WebSocketClosed(outcome string, backpressure bool) {
	m.websocketTransitions.WithLabelValues("close", outcome).Inc()
	m.websocketConnections.Dec()
	if backpressure {
		m.websocketBackpressure.Inc()
	}
}
func (m *Module) ConnectionOpened(result string) { m.WebSocketOpened(result) }
func (m *Module) ConnectionClosed(result string) { m.WebSocketClosed(result, false) }
func (m *Module) Backpressure() {
	if m != nil {
		m.websocketBackpressure.Inc()
	}
}
func (m *Module) ObserveWebSocketMessage(direction, kind, result string, bytes int) {
	if m == nil {
		return
	}
	m.websocketMessages.WithLabelValues(direction, kind, result).Inc()
	if bytes > 0 {
		m.websocketMessageBytes.WithLabelValues(direction, kind).Add(float64(bytes))
	}
}
func (m *Module) ObserveWebSocketBroadcast(event, result string, recipients int) {
	if m == nil {
		return
	}
	m.websocketBroadcasts.WithLabelValues(event, result).Inc()
	m.websocketFanout.Observe(float64(max(recipients, 0)))
}
func (m *Module) ObserveWebSocketReplay(result string, events int) {
	if m == nil {
		return
	}
	m.websocketReplays.WithLabelValues(result).Inc()
	if result == "resumed" {
		m.websocketReplayEvents.Observe(float64(max(events, 0)))
	}
}
func (m *Module) AddWebSocketSubscriptions(delta int) {
	if m != nil && delta != 0 {
		m.websocketSubscriptions.Add(float64(delta))
	}
}

var _ timerlayer.Recorder = (*Module)(nil)

// StoreCacheRecorder adapts m to the store cache layer's recorder.
func (m *Module) StoreCacheRecorder() localcachelayer.Recorder {
	return localcachelayer.RecorderFunc(m.ObserveStoreCache)
}
