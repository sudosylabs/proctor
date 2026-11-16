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
)

func (m *Module) newJobCollectors() {
	m.jobExecutions = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Namespace: "proctor",
			Subsystem: "jobs",
			Name:      "executions_total",
			Help:      "Durable Job executions completed on this node.",
		},
		[]string{"type", "outcome"},
	)
	m.jobDuration = prometheus.NewHistogramVec(
		prometheus.HistogramOpts{
			Namespace: "proctor",
			Subsystem: "jobs",
			Name:      "execution_duration_seconds",
			Help:      "Durable Job handler execution duration.",
			Buckets:   prometheus.DefBuckets,
		},
		[]string{"type", "outcome"},
	)
	m.jobsActive = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{Namespace: "proctor", Subsystem: "jobs", Name: "active", Help: "Durable Job handlers currently executing."},
		[]string{"type"},
	)
	m.jobActivities = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Namespace: "proctor",
			Subsystem: "jobs",
			Name:      "activities_total",
			Help:      "Durable Job runtime, recurrence, and periodic-task activity.",
		},
		[]string{"kind", "name", "operation", "outcome"},
	)
	m.jobActivityDuration = prometheus.NewHistogramVec(
		prometheus.HistogramOpts{
			Namespace: "proctor",
			Subsystem: "jobs",
			Name:      "activity_duration_seconds",
			Help:      "Durable Job runtime, recurrence, and periodic-task duration.",
			Buckets:   prometheus.DefBuckets,
		},
		[]string{"kind", "name", "operation"},
	)
	m.jobQueueLatency = prometheus.NewHistogramVec(
		prometheus.HistogramOpts{
			Namespace: "proctor",
			Subsystem: "jobs",
			Name:      "queue_latency_seconds",
			Help:      "Time a durable Job was available before being claimed.",
			Buckets:   prometheus.ExponentialBuckets(0.25, 2, 16),
		},
		[]string{"type"},
	)
}
func (m *Module) JobStarted(jobType string) { m.jobsActive.WithLabelValues(jobType).Inc() }
func (m *Module) JobFinished(jobType, result string, duration time.Duration) {
	m.jobsActive.WithLabelValues(jobType).Dec()
	m.jobExecutions.WithLabelValues(jobType, result).Inc()
	m.jobDuration.WithLabelValues(jobType, result).Observe(duration.Seconds())
}
func (m *Module) ObserveJobActivity(kind, name, operation, result string, duration, queueLatency time.Duration) {
	if m == nil {
		return
	}
	m.jobActivities.WithLabelValues(kind, name, operation, result).Inc()
	if duration >= 0 {
		m.jobActivityDuration.WithLabelValues(kind, name, operation).Observe(duration.Seconds())
	}
	if kind == "job" && operation == "claim" && result == "success" && queueLatency >= 0 {
		m.jobQueueLatency.WithLabelValues(name).Observe(queueLatency.Seconds())
	}
}
