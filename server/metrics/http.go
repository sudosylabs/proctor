// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package metrics

import (
	"strconv"
	"time"

	"github.com/prometheus/client_golang/prometheus"
)

func (m *Module) newHTTPCollectors() {
	m.httpRequests = prometheus.NewCounterVec(
		prometheus.CounterOpts{Namespace: "proctor", Subsystem: "http", Name: "requests_total", Help: "Completed public HTTP requests."},
		[]string{"route", "method", "status_class"},
	)
	m.httpDuration = prometheus.NewHistogramVec(
		prometheus.HistogramOpts{
			Namespace: "proctor",
			Subsystem: "http",
			Name:      "request_duration_seconds",
			Help:      "Public HTTP request duration.",
			Buckets:   prometheus.DefBuckets,
		},
		[]string{"route", "method"},
	)
	m.httpInFlight = prometheus.NewGauge(prometheus.GaugeOpts{
		Namespace: "proctor",
		Subsystem: "http",
		Name:      "in_flight_requests",
		Help:      "Public HTTP requests currently executing.",
	})
	m.httpRequestBytes = prometheus.NewHistogramVec(
		prometheus.HistogramOpts{
			Namespace: "proctor",
			Subsystem: "http",
			Name:      "request_size_bytes",
			Help:      "Public HTTP request body bytes consumed by handlers.",
			Buckets:   prometheus.ExponentialBuckets(256, 4, 9),
		},
		[]string{"route", "method"},
	)
	m.httpResponseBytes = prometheus.NewHistogramVec(
		prometheus.HistogramOpts{
			Namespace: "proctor",
			Subsystem: "http",
			Name:      "response_size_bytes",
			Help:      "Public HTTP response body size.",
			Buckets:   prometheus.ExponentialBuckets(256, 4, 9),
		},
		[]string{"route", "method"},
	)
}

func (m *Module) ObserveHTTPRequest(route, method string, status int, duration time.Duration) {
	if m == nil {
		return
	}
	m.httpRequests.WithLabelValues(route, method, strconv.Itoa(status/100)+"xx").Inc()
	m.httpDuration.WithLabelValues(route, method).Observe(duration.Seconds())
}
func (m *Module) ObserveHTTPPayload(route, method string, requestBytes, responseBytes int64) {
	if m == nil {
		return
	}
	m.httpRequestBytes.WithLabelValues(route, method).Observe(float64(max(requestBytes, 0)))
	m.httpResponseBytes.WithLabelValues(route, method).Observe(float64(max(responseBytes, 0)))
}
func (m *Module) HTTPStarted() func() {
	if m == nil {
		return func() {}
	}
	m.httpInFlight.Inc()
	return m.httpInFlight.Dec
}
