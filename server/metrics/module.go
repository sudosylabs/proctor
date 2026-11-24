// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

// Package metrics owns the node-local Prometheus registry and scrape listener.
// Consumers get narrow recorders, so no use case depends on Prometheus types.
package metrics

import (
	"database/sql"
	"log"
	"net"
	"net/http"
	"sync"
	"sync/atomic"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"
	"github.com/prometheus/client_golang/prometheus/promhttp"

	"github.com/sudosylabs/proctor/server/config"
	"github.com/sudosylabs/proctor/server/internal/tlscert"
)

type BuildInfo struct {
	Version   string
	Commit    string
	GoVersion string
}

type LogStats struct {
	Dropped        uint64
	InternalErrors uint64
}

type Sources struct {
	Database func() []DatabasePool
	Logging  func() LogStats
	ErrorLog *log.Logger
}

// Module owns the registry, its instruments and the authenticated scrape
// listener. Every label value it records comes from a closed set.
type Module struct {
	settings config.Metrics
	registry *prometheus.Registry
	handler  http.Handler
	errorLog *log.Logger
	// certificate serves the TLS key pair once Start loads it; nil without TLS.
	certificate *tlscert.Static

	ready atomic.Bool

	httpRequests           *prometheus.CounterVec
	httpDuration           *prometheus.HistogramVec
	httpInFlight           prometheus.Gauge
	workActive             *prometheus.GaugeVec
	workRejected           *prometheus.CounterVec
	workDuration           *prometheus.HistogramVec
	pluginCalls            *prometheus.CounterVec
	pluginCallDuration     *prometheus.HistogramVec
	pluginState            *prometheus.GaugeVec
	httpRequestBytes       *prometheus.HistogramVec
	httpResponseBytes      *prometheus.HistogramVec
	storeDuration          *prometheus.HistogramVec
	storeCache             *prometheus.CounterVec
	storeRetries           *prometheus.CounterVec
	websocketConnections   prometheus.Gauge
	websocketTransitions   *prometheus.CounterVec
	websocketBackpressure  prometheus.Counter
	websocketMessages      *prometheus.CounterVec
	websocketMessageBytes  *prometheus.CounterVec
	websocketBroadcasts    *prometheus.CounterVec
	websocketFanout        prometheus.Histogram
	websocketReplays       *prometheus.CounterVec
	websocketReplayEvents  prometheus.Histogram
	websocketSubscriptions prometheus.Gauge
	clusterOperations      *prometheus.CounterVec
	clusterDuration        *prometheus.HistogramVec
	clusterMessages        *prometheus.CounterVec
	clusterMessageBytes    *prometheus.CounterVec
	clusterMembership      *prometheus.CounterVec
	clusterDiscovery       *prometheus.CounterVec
	clusterAdmission       *prometheus.CounterVec
	clusterFanout          prometheus.Histogram
	clusterPeerCount       atomic.Int64
	clusterPeerMu          sync.RWMutex
	clusterPeerSource      func() int
	jobExecutions          *prometheus.CounterVec
	jobDuration            *prometheus.HistogramVec
	jobsActive             *prometheus.GaugeVec
	jobActivities          *prometheus.CounterVec
	jobActivityDuration    *prometheus.HistogramVec
	jobQueueLatency        *prometheus.HistogramVec
	jobsQueued             *prometheus.GaugeVec
	jobWakes               *prometheus.CounterVec
	jobLostAttempts        prometheus.Counter
	leaseRenewals          *prometheus.CounterVec
	leaseRenewalDuration   prometheus.Histogram
	vfsOperations          *prometheus.CounterVec
	vfsDuration            *prometheus.HistogramVec
	vfsBytes               *prometheus.CounterVec
	vfsObjectSize          *prometheus.HistogramVec
	vfsListEntries         *prometheus.HistogramVec
	vfsStreams             *prometheus.CounterVec
	cacheOperations        *prometheus.CounterVec
	cacheDuration          *prometheus.HistogramVec
	cacheBytes             *prometheus.CounterVec
	redisOperations        *prometheus.CounterVec
	redisDuration          *prometheus.HistogramVec
	smtpOperations         *prometheus.CounterVec
	smtpDuration           *prometheus.HistogramVec
	smtpMessages           *prometheus.CounterVec
	smtpRecipients         *prometheus.CounterVec
	smtpBytes              *prometheus.CounterVec
	mailDeliveries         *prometheus.CounterVec
	mailAttempts           *prometheus.CounterVec
	mailProcessingLatency  *prometheus.HistogramVec
	mailQueueCount         *prometheus.GaugeVec
	mailQueueOldest        *prometheus.GaugeVec
	mailQueueTruncated     prometheus.Gauge
	mailHealth             *prometheus.GaugeVec
	applicationEvents      *prometheus.CounterVec

	mu        sync.Mutex
	server    *http.Server
	listener  net.Listener
	done      chan struct{}
	started   bool
	closed    bool
	failures  chan error
	closeOnce sync.Once
	closeDone chan struct{}
	closeErr  error
}

func New(settings config.Metrics, build BuildInfo, sources Sources) (*Module, error) {
	registry := prometheus.NewRegistry()
	module := &Module{
		settings:  settings,
		registry:  registry,
		errorLog:  sources.ErrorLog,
		failures:  make(chan error, 1),
		closeDone: make(chan struct{}),
	}
	module.newWorkCollectors()
	module.newPluginCollectors()
	module.newHTTPCollectors()
	module.newStoreAndWebSocketCollectors()
	module.newClusterCollectors()
	module.newJobCollectors()
	module.newStorageCollectors()
	module.newMailCollectors()
	module.registerCollectors(registry, build)
	if sources.Database != nil {
		registerDatabaseCollectors(registry, sources.Database)
	}

	if sources.Logging != nil {
		registry.MustRegister(
			prometheus.NewCounterFunc(prometheus.CounterOpts{
				Namespace: "proctor",
				Subsystem: "logging",
				Name:      "dropped_records_total",
				Help:      "Records dropped by bounded logging queues.",
			}, func() float64 { return float64(sources.Logging().Dropped) }),
			prometheus.NewCounterFunc(prometheus.CounterOpts{
				Namespace: "proctor",
				Subsystem: "logging",
				Name:      "internal_errors_total",
				Help:      "Logging pipeline internal errors.",
			}, func() float64 { return float64(sources.Logging().InternalErrors) }),
		)
	}

	scrapeHandler := promhttp.HandlerFor(registry, promhttp.HandlerOpts{
		ErrorLog:            sources.ErrorLog,
		MaxRequestsInFlight: settings.MaximumConcurrentScrapes,
	})
	module.handler = module.authorize(module.routes(promhttp.InstrumentMetricHandler(registry, scrapeHandler)))
	return module, nil
}

func (m *Module) registerCollectors(registry *prometheus.Registry, build BuildInfo) {
	registry.MustRegister(
		collectors.NewGoCollector(),
		collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}),
		prometheus.NewGaugeFunc(prometheus.GaugeOpts{
			Namespace: "proctor",
			Name:      "ready",
			Help:      "Whether this node is ready to serve public traffic.",
		}, func() float64 {
			if m.ready.Load() {
				return 1
			}
			return 0
		}),
		prometheus.NewGaugeFunc(prometheus.GaugeOpts{
			Namespace:   "proctor",
			Name:        "build_info",
			Help:        "Build information.",
			ConstLabels: prometheus.Labels{"version": build.Version, "commit": build.Commit, "go_version": build.GoVersion},
		}, func() float64 { return 1 }),
		m.httpRequests,
		m.httpDuration,
		m.httpInFlight,
		m.httpRequestBytes,
		m.httpResponseBytes,
		m.storeDuration,
		m.storeCache,
		m.storeRetries,
		m.websocketConnections,
		m.websocketTransitions,
		m.websocketBackpressure,
		m.websocketMessages,
		m.websocketMessageBytes,
		m.websocketBroadcasts,
		m.websocketFanout,
		m.websocketReplays,
		m.websocketReplayEvents,
		m.websocketSubscriptions,
		m.clusterOperations,
		m.clusterDuration,
		m.clusterMessages,
		m.clusterMessageBytes,
		m.clusterMembership,
		m.clusterDiscovery,
		m.clusterAdmission,
		m.clusterFanout,
		prometheus.NewGaugeFunc(
			prometheus.GaugeOpts{Namespace: "proctor", Subsystem: "cluster", Name: "peers", Help: "Known peer nodes, excluding this node."},
			m.currentClusterPeers,
		),
		m.jobExecutions,
		m.jobDuration,
		m.jobsActive,
		m.jobActivities,
		m.jobActivityDuration,
		m.jobQueueLatency,
		m.jobsQueued,
		m.jobWakes,
		m.jobLostAttempts,
		m.leaseRenewals,
		m.leaseRenewalDuration,
		m.vfsOperations,
		m.vfsDuration,
		m.vfsBytes,
		m.vfsObjectSize,
		m.vfsListEntries,
		m.vfsStreams,
		m.cacheOperations,
		m.cacheDuration,
		m.cacheBytes,
		m.redisOperations,
		m.redisDuration,
		m.smtpOperations,
		m.smtpDuration,
		m.smtpMessages,
		m.smtpRecipients,
		m.smtpBytes,
		m.mailDeliveries,
		m.mailAttempts,
		m.mailProcessingLatency,
		m.mailQueueCount,
		m.mailQueueOldest,
		m.mailQueueTruncated,
		m.mailHealth,
		m.applicationEvents,
		m.workActive,
		m.workRejected,
		m.workDuration,
		m.pluginCalls,
		m.pluginCallDuration,
		m.pluginState,
	)
}

func (m *Module) Enabled() bool { return m != nil && m.settings.Enabled }

func operationCounter(subsystem, help string, labels []string) *prometheus.CounterVec {
	return prometheus.NewCounterVec(
		prometheus.CounterOpts{Namespace: "proctor", Subsystem: subsystem, Name: "operations_total", Help: help},
		labels,
	)
}

func operationDuration(subsystem, help string, labels []string) *prometheus.HistogramVec {
	return prometheus.NewHistogramVec(
		prometheus.HistogramOpts{
			Namespace: "proctor",
			Subsystem: subsystem,
			Name:      "operation_duration_seconds",
			Help:      help,
			Buckets:   prometheus.DefBuckets,
		},
		labels,
	)
}

// DatabasePool is one connection pool's statistics. Opened counts every
// connection the pool has made since start, which database/sql itself does
// not report; a rising Opened under steady load is connection churn.
type DatabasePool struct {
	Pool   string
	Stats  sql.DBStats
	Opened uint64
}

func registerDatabaseCollectors(registry *prometheus.Registry, pools func() []DatabasePool) {
	registry.MustRegister(databaseCollector{pools: pools})
}

// databaseCollector reports every pool under a pool label, read once per
// scrape so the values of one pool are consistent with each other.
type databaseCollector struct {
	pools func() []DatabasePool
}

type databaseMetric struct {
	desc  *prometheus.Desc
	kind  prometheus.ValueType
	value func(DatabasePool) float64
}

var databaseMetrics = []databaseMetric{
	databaseGauge("open_connections", "Open database connections.",
		func(p DatabasePool) float64 { return float64(p.Stats.OpenConnections) }),
	databaseGauge("max_open_connections", "Configured maximum open database connections.",
		func(p DatabasePool) float64 { return float64(p.Stats.MaxOpenConnections) }),
	databaseGauge("in_use_connections", "Database connections currently in use.",
		func(p DatabasePool) float64 { return float64(p.Stats.InUse) }),
	databaseGauge("idle_connections", "Idle database connections.",
		func(p DatabasePool) float64 { return float64(p.Stats.Idle) }),
	databaseCounter("connections_opened_total", "Database connections opened since start.",
		func(p DatabasePool) float64 { return float64(p.Opened) }),
	databaseCounter("waits_total", "Database connection waits.",
		func(p DatabasePool) float64 { return float64(p.Stats.WaitCount) }),
	databaseCounter("wait_duration_seconds_total", "Time spent waiting for database connections.",
		func(p DatabasePool) float64 { return p.Stats.WaitDuration.Seconds() }),
	databaseCounter("max_idle_closed_total", "Connections closed due to the idle pool limit.",
		func(p DatabasePool) float64 { return float64(p.Stats.MaxIdleClosed) }),
	databaseCounter("max_idle_time_closed_total", "Connections closed due to the idle timeout.",
		func(p DatabasePool) float64 { return float64(p.Stats.MaxIdleTimeClosed) }),
	databaseCounter("max_lifetime_closed_total", "Connections closed due to the lifetime limit.",
		func(p DatabasePool) float64 { return float64(p.Stats.MaxLifetimeClosed) }),
}

func databaseGauge(name, help string, value func(DatabasePool) float64) databaseMetric {
	return databaseMetric{desc: databaseDesc(name, help), kind: prometheus.GaugeValue, value: value}
}

func databaseCounter(name, help string, value func(DatabasePool) float64) databaseMetric {
	return databaseMetric{desc: databaseDesc(name, help), kind: prometheus.CounterValue, value: value}
}

func databaseDesc(name, help string) *prometheus.Desc {
	return prometheus.NewDesc(prometheus.BuildFQName("proctor", "database", name), help, []string{"pool"}, nil)
}

func (c databaseCollector) Describe(descs chan<- *prometheus.Desc) {
	for _, metric := range databaseMetrics {
		descs <- metric.desc
	}
}

func (c databaseCollector) Collect(metrics chan<- prometheus.Metric) {
	for _, pool := range c.pools() {
		for _, metric := range databaseMetrics {
			metrics <- prometheus.MustNewConstMetric(metric.desc, metric.kind, metric.value(pool), pool.Pool)
		}
	}
}

func outcome(err error) string {
	if err != nil {
		return "error"
	}
	return "success"
}
