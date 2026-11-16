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

// newWorkCollectors pre-creates each pool's series so they export zero before first use.
func (m *Module) newWorkCollectors() {
	m.workActive = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Namespace: "proctor",
			Subsystem: "work",
			Name:      "active",
			Help:      "Expensive operations currently admitted on this node.",
		},
		[]string{"pool"},
	)
	m.workRejected = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Namespace: "proctor",
			Subsystem: "work",
			Name:      "rejected_total",
			Help:      "Expensive operations refused because their node-local pool was full.",
		},
		[]string{"pool"},
	)
	m.workDuration = prometheus.NewHistogramVec(
		prometheus.HistogramOpts{
			Namespace: "proctor",
			Subsystem: "work",
			Name:      "duration_seconds",
			Help:      "Duration of admitted expensive operations, including failed operations.",
			Buckets:   prometheus.DefBuckets,
		},
		[]string{"pool"},
	)
	for _, pool := range []string{"password", "file_content"} {
		m.workActive.WithLabelValues(pool)
		m.workRejected.WithLabelValues(pool)
		m.workDuration.WithLabelValues(pool)
	}
}

// WorkStarted records admission to a pool; an unknown pool is ignored.
func (m *Module) WorkStarted(pool string) {
	if validWorkPool(pool) {
		m.workActive.WithLabelValues(pool).Inc()
	}
}

// WorkFinished must follow a WorkStarted for the same pool.
func (m *Module) WorkFinished(pool string, duration time.Duration) {
	if validWorkPool(pool) {
		m.workActive.WithLabelValues(pool).Dec()
		m.workDuration.WithLabelValues(pool).Observe(duration.Seconds())
	}
}

// WorkRejected counts a refusal by a full pool. A full pool is load, not a
// fault: refusals are counted rather than logged, and never change readiness.
func (m *Module) WorkRejected(pool string) {
	if validWorkPool(pool) {
		m.workRejected.WithLabelValues(pool).Inc()
	}
}

func validWorkPool(pool string) bool { return pool == "password" || pool == "file_content" }

// WorkRecorder reports one pool. The pool is chosen at composition, never
// derived from a request or an error.
type WorkRecorder struct {
	module *Module
	pool   string
}

// WorkRecorder returns the recorder for pool.
func (m *Module) WorkRecorder(pool string) WorkRecorder {
	return WorkRecorder{module: m, pool: pool}
}

// Started records admission to the pool.
func (r WorkRecorder) Started() { r.module.WorkStarted(r.pool) }

// Finished records that admitted work returned after duration.
func (r WorkRecorder) Finished(duration time.Duration) {
	r.module.WorkFinished(r.pool, duration)
}

// Rejected records that the pool refused work.
func (r WorkRecorder) Rejected() { r.module.WorkRejected(r.pool) }
