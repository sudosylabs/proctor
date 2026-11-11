// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package metrics

import "time"

// WorkStarted records admission to one of the two fixed node-local work pools.
func (m *Module) WorkStarted(pool string) {
	if validWorkPool(pool) {
		m.workActive.WithLabelValues(pool).Inc()
	}
}

// WorkFinished releases the active count after synchronous work has returned.
func (m *Module) WorkFinished(pool string, duration time.Duration) {
	if validWorkPool(pool) {
		m.workActive.WithLabelValues(pool).Dec()
		m.workDuration.WithLabelValues(pool).Observe(duration.Seconds())
	}
}

// WorkRejected records immediate refusal, without user, file, or error labels.
func (m *Module) WorkRejected(pool string) {
	if validWorkPool(pool) {
		m.workRejected.WithLabelValues(pool).Inc()
	}
}

func validWorkPool(pool string) bool { return pool == "password" || pool == "file_content" }

// WorkRecorder reports one of the fixed work pools to the consumer that runs
// it. The pool is chosen by composition, never derived from requests or
// errors.
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
