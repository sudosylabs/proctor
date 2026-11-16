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

func (m *Module) newStorageCollectors() {
	m.vfsOperations = operationCounter("vfs", "VFS backend operations.", []string{"backend", "operation", "outcome"})
	m.vfsDuration = operationDuration("vfs", "VFS backend operation duration.", []string{"backend", "operation"})
	m.vfsBytes = prometheus.NewCounterVec(
		prometheus.CounterOpts{Namespace: "proctor", Subsystem: "vfs", Name: "bytes_total", Help: "VFS payload bytes transferred."},
		[]string{"backend", "direction"},
	)
	m.vfsObjectSize = prometheus.NewHistogramVec(
		prometheus.HistogramOpts{
			Namespace: "proctor",
			Subsystem: "vfs",
			Name:      "object_size_bytes",
			Help:      "VFS object sizes returned by successful operations.",
			Buckets:   prometheus.ExponentialBuckets(256, 4, 10),
		},
		[]string{"backend", "operation"},
	)
	m.vfsListEntries = prometheus.NewHistogramVec(
		prometheus.HistogramOpts{
			Namespace: "proctor",
			Subsystem: "vfs",
			Name:      "list_entries",
			Help:      "Entries returned by VFS list pages.",
			Buckets:   prometheus.ExponentialBuckets(1, 2, 14),
		},
		[]string{"backend"},
	)
	m.vfsStreams = prometheus.NewCounterVec(
		prometheus.CounterOpts{Namespace: "proctor", Subsystem: "vfs", Name: "streams_total", Help: "VFS read streams by terminal outcome."},
		[]string{"backend", "outcome"},
	)
	m.cacheOperations = operationCounter("cache", "Shared cache backend operations.", []string{"backend", "operation", "outcome"})
	m.cacheDuration = operationDuration("cache", "Shared cache backend operation duration.", []string{"backend", "operation"})
	m.cacheBytes = prometheus.NewCounterVec(
		prometheus.CounterOpts{Namespace: "proctor", Subsystem: "cache", Name: "bytes_total", Help: "Shared-cache payload bytes transferred."},
		[]string{"backend", "direction"},
	)
	m.redisOperations = operationCounter("redis", "Redis cache operations.", []string{"operation", "outcome"})
	m.redisDuration = operationDuration("redis", "Redis cache operation duration.", []string{"operation"})
}
func (m *Module) ObserveVFS(backend, operation string, err error, duration time.Duration) {
	m.ObserveVFSResult(backend, operation, outcome(err), duration)
}
func (m *Module) ObserveVFSResult(backend, operation, result string, duration time.Duration) {
	m.vfsOperations.WithLabelValues(backend, operation, result).Inc()
	m.vfsDuration.WithLabelValues(backend, operation).Observe(duration.Seconds())
}
func (m *Module) ObserveVFSObject(backend, operation string, size int64) {
	if m != nil && size >= 0 {
		m.vfsObjectSize.WithLabelValues(backend, operation).Observe(float64(size))
	}
}
func (m *Module) ObserveVFSList(backend string, entries int) {
	if m != nil {
		m.vfsListEntries.WithLabelValues(backend).Observe(float64(max(entries, 0)))
	}
}
func (m *Module) AddVFSBytes(backend, direction string, bytes int64) {
	if m != nil && bytes > 0 {
		m.vfsBytes.WithLabelValues(backend, direction).Add(float64(bytes))
	}
}
func (m *Module) ObserveVFSStream(backend, result string) {
	if m != nil {
		m.vfsStreams.WithLabelValues(backend, result).Inc()
	}
}
func (m *Module) ObserveCache(backend, operation string, err error, duration time.Duration) {
	m.ObserveCacheResult(backend, operation, outcome(err), duration)
}
func (m *Module) ObserveCacheResult(backend, operation, result string, duration time.Duration) {
	m.cacheOperations.WithLabelValues(backend, operation, result).Inc()
	m.cacheDuration.WithLabelValues(backend, operation).Observe(duration.Seconds())
	if backend == "redis" {
		m.redisOperations.WithLabelValues(operation, result).Inc()
		m.redisDuration.WithLabelValues(operation).Observe(duration.Seconds())
	}
}
func (m *Module) AddCacheBytes(backend, direction string, bytes int) {
	if m != nil && bytes > 0 {
		m.cacheBytes.WithLabelValues(backend, direction).Add(float64(bytes))
	}
}
