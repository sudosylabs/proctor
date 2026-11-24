// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package metrics

import (
	"context"
	"database/sql"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/sudosylabs/proctor/server/app/job"
	"github.com/sudosylabs/proctor/server/app/operational"
	"github.com/sudosylabs/proctor/server/config"
	"github.com/sudosylabs/proctor/server/model"
	"github.com/sudosylabs/proctor/server/store"
)

func TestModuleServesAuthenticatedNodeMetricsWithoutDebugEndpoints(t *testing.T) {
	t.Parallel()

	settings := config.Default().Metrics
	settings.Enabled = true
	settings.ListenAddress = "127.0.0.1:0"
	settings.BearerToken = strings.Repeat("a", 32)
	module, err := New(settings, BuildInfo{Version: "test", Commit: "abc", GoVersion: "go-test"}, Sources{})
	require.NoError(t, err)
	module.ObserveCache("redis", "get", nil, time.Millisecond)
	module.ObserveVFS("s3", "open", context.DeadlineExceeded, time.Millisecond)
	module.ObserveSMTP("send", nil, time.Millisecond)
	module.ObserveHTTPRequest("/api/v1/discovery", http.MethodGet, http.StatusOK, time.Millisecond)
	module.ObserveHTTPPayload("/api/v1/discovery", http.MethodGet, 12, 24)
	module.ObserveCluster("broadcast", "realtime.event", nil, time.Millisecond)
	module.ObserveClusterMessage("send", "realtime.event", "success", 32)
	module.ObserveClusterMembership("join")
	module.ObserveClusterDiscovery("rejoin", "success")
	module.ObserveClusterAdmission("protocol_incompatible")
	module.ObserveClusterFanout(2)
	module.JobStarted("example_job")
	module.JobFinished("example_job", "succeeded", time.Millisecond)
	module.ObserveJobActivity("job", "example_job", "claim", "success", time.Millisecond, time.Second)
	module.ObserveVFSObject("s3", "open", 64)
	module.ObserveVFSList("s3", 3)
	module.AddVFSBytes("s3", "read", 64)
	module.ObserveVFSStream("s3", "complete")
	module.AddCacheBytes("redis", "read", 12)
	module.ObserveSMTPMessage("accepted", 2, 128)
	module.ObserveMailDelivery("account_recovery", "sent", "accepted", time.Second)
	module.ObserveMailAttempt("account_recovery", "sending", 1)
	module.SetMailQueue("account_recovery", "queued", 2, time.Minute)
	module.SetMailQueueSnapshotTruncated(true)
	module.SetMailHealth("mail.healthy")
	module.RecordOperationalEvent(operational.NewEvent("authentication", "login", nil))
	module.ObserveStoreRetry("retry")
	module.ConnectionOpened("success")
	module.Backpressure()
	module.ObserveWebSocketMessage("outbound", "event", "success", 32)
	module.ObserveWebSocketBroadcast("exam.updated", "published", 2)
	module.ObserveWebSocketReplay("resumed", 3)
	module.AddWebSocketSubscriptions(1)
	module.ConnectionClosed("closed")
	module.SetReady(true)
	request := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/metrics", nil)
	response := httptest.NewRecorder()
	module.handler.ServeHTTP(response, request)
	require.Equal(t, http.StatusUnauthorized, response.Code, "unauthenticated status")

	request = httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/metrics", nil)
	request.Header.Set("Authorization", "Bearer "+settings.BearerToken)
	response = httptest.NewRecorder()
	module.handler.ServeHTTP(response, request)
	body, err := io.ReadAll(response.Result().Body)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, response.Code, "authenticated status = %d: %s", response.Code, body)
	for _, name := range []string{
		"proctor_build_info", "proctor_ready", "proctor_cache_operations_total",
		"proctor_redis_operations_total",
		"proctor_vfs_operations_total", "proctor_smtp_operations_total",
		"proctor_websocket_backpressure_disconnects_total",
		"proctor_http_requests_total", "proctor_cluster_operations_total",
		"proctor_jobs_executions_total", "proctor_http_request_size_bytes", "proctor_http_response_size_bytes",
		"proctor_websocket_messages_total", "proctor_websocket_broadcasts_total",
		"proctor_websocket_replays_total", "proctor_websocket_subscriptions",
		"proctor_cluster_messages_total", "proctor_cluster_membership_events_total",
		"proctor_cluster_discovery_operations_total", "proctor_cluster_admission_rejections_total",
		"proctor_jobs_activities_total", "proctor_jobs_queue_latency_seconds",
		"proctor_vfs_bytes_total", "proctor_vfs_object_size_bytes", "proctor_vfs_streams_total",
		"proctor_cache_bytes_total", "proctor_smtp_messages_total", "proctor_mail_deliveries_total",
		"proctor_mail_queue", "proctor_mail_queue_snapshot_truncated", "proctor_mail_health", "proctor_application_events_total", "proctor_store_retries_total",
	} {
		require.Contains(t, string(body), name)
	}

	debugRequest := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/debug/pprof/", nil)
	debugRequest.Header.Set("Authorization", "Bearer "+settings.BearerToken)
	debugResponse := httptest.NewRecorder()
	module.handler.ServeHTTP(debugResponse, debugRequest)
	require.Equal(t, http.StatusNotFound, debugResponse.Code, "debug endpoint status")
}

func TestModuleServesProfilesOnlyWhenEnabledAndAuthenticated(t *testing.T) {
	t.Parallel()

	settings := config.Default().Metrics
	settings.Enabled = true
	settings.BearerToken = strings.Repeat("a", 32)
	settings.ProfilingEnabled = true
	module, err := New(settings, BuildInfo{}, Sources{})
	require.NoError(t, err)
	serve := func(method, path, token string) *httptest.ResponseRecorder {
		request := httptest.NewRequestWithContext(t.Context(), method, path, nil)
		if token != "" {
			request.Header.Set("Authorization", "Bearer "+token)
		}
		response := httptest.NewRecorder()
		module.handler.ServeHTTP(response, request)
		return response
	}

	for _, path := range []string{"/debug/pprof/", "/debug/pprof/heap", "/debug/pprof/goroutine?debug=1", "/debug/pprof/symbol"} {
		t.Run("unauthenticated "+path, func(t *testing.T) {
			require.Equal(t, http.StatusUnauthorized, serve(http.MethodGet, path, "").Code)
			require.Equal(t, http.StatusUnauthorized, serve(http.MethodGet, path, strings.Repeat("b", 32)).Code)
		})
		t.Run("authenticated "+path, func(t *testing.T) {
			require.Equal(t, http.StatusOK, serve(http.MethodGet, path, settings.BearerToken).Code)
		})
	}
	t.Run("command line is never served", func(t *testing.T) {
		require.Equal(t, http.StatusNotFound, serve(http.MethodGet, "/debug/pprof/cmdline", settings.BearerToken).Code)
	})
	t.Run("only symbol accepts POST", func(t *testing.T) {
		require.Equal(t, http.StatusOK, serve(http.MethodPost, "/debug/pprof/symbol", settings.BearerToken).Code)
		require.Equal(t, http.StatusNotFound, serve(http.MethodPost, "/debug/pprof/heap", settings.BearerToken).Code)
		require.Equal(t, http.StatusNotFound, serve(http.MethodPost, "/metrics", settings.BearerToken).Code)
	})
	t.Run("scrape endpoint is unchanged", func(t *testing.T) {
		require.Equal(t, http.StatusOK, serve(http.MethodGet, "/metrics", settings.BearerToken).Code)
	})
}

func TestModuleExportsDatabasePoolSaturation(t *testing.T) {
	t.Parallel()

	module, err := New(config.Default().Metrics, BuildInfo{}, Sources{Database: func() []DatabasePool {
		return []DatabasePool{
			{Pool: "proctor", Opened: 17, Stats: sql.DBStats{
				MaxOpenConnections: 50, OpenConnections: 12, InUse: 9, Idle: 3,
				WaitCount: 4, WaitDuration: 1500 * time.Millisecond,
			}},
			{Pool: "proctor-control", Opened: 2, Stats: sql.DBStats{MaxOpenConnections: 2, OpenConnections: 2, Idle: 2}},
		}
	}})
	require.NoError(t, err)
	response := httptest.NewRecorder()
	module.handler.ServeHTTP(response, httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/metrics", nil))
	require.Equal(t, http.StatusOK, response.Code)
	for _, sample := range []string{
		`proctor_database_in_use_connections{pool="proctor"} 9`,
		`proctor_database_idle_connections{pool="proctor"} 3`,
		`proctor_database_open_connections{pool="proctor"} 12`,
		`proctor_database_max_open_connections{pool="proctor"} 50`,
		`proctor_database_waits_total{pool="proctor"} 4`,
		`proctor_database_wait_duration_seconds_total{pool="proctor"} 1.5`,
		`proctor_database_connections_opened_total{pool="proctor"} 17`,
		`proctor_database_open_connections{pool="proctor-control"} 2`,
		`proctor_database_connections_opened_total{pool="proctor-control"} 2`,
	} {
		require.Contains(t, response.Body.String(), sample+"\n")
	}
}

func TestModuleExportsJobQueueAndServingLease(t *testing.T) {
	t.Parallel()

	module, err := New(config.Default().Metrics, BuildInfo{}, Sources{})
	require.NoError(t, err)
	recorder, ok := module.JobRecorder().(job.QueueRecorder)
	require.True(t, ok, "the job recorder reports the queue")
	recorder.Queued(map[model.JobType]store.JobQueueDepth{model.JobTypeMailDeliver: {Due: 7, Scheduled: 3}, model.JobTypeCleanup: {}})
	recorder.Woke(job.WakeNotify)
	recorder.Woke(job.WakeNotify)
	recorder.LostAttemptsRecovered(4)
	lease := module.ServingLeaseRecorder()
	lease("renewed", 30*time.Millisecond)
	lease("failed", 3*time.Second)

	response := httptest.NewRecorder()
	module.handler.ServeHTTP(response, httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/metrics", nil))
	require.Equal(t, http.StatusOK, response.Code)
	for _, sample := range []string{
		`proctor_jobs_queued{state="due",type="mail.deliver"} 7`,
		`proctor_jobs_queued{state="scheduled",type="mail.deliver"} 3`,
		`proctor_jobs_queued{state="due",type="job.cleanup"} 0`,
		`proctor_jobs_dispatcher_wakes_total{source="notify"} 2`,
		`proctor_jobs_lost_attempts_recovered_total 4`,
		`proctor_serving_lease_renewals_total{outcome="renewed"} 1`,
		`proctor_serving_lease_renewals_total{outcome="failed"} 1`,
		`proctor_serving_lease_renew_duration_seconds_bucket{le="0.05"} 1`,
		`proctor_serving_lease_renew_duration_seconds_bucket{le="5"} 2`,
	} {
		require.Contains(t, response.Body.String(), sample+"\n")
	}
}

func TestDisabledModuleOwnsNoListener(t *testing.T) {
	t.Parallel()

	settings := config.Default().Metrics
	module, err := New(settings, BuildInfo{}, Sources{})
	require.NoError(t, err)
	require.NoError(t, module.Start(context.Background()))
	require.Nil(t, module.listener, "disabled metrics module opened a listener")
	require.NoError(t, module.Close(context.Background()))
	require.NoError(t, module.Close(context.Background()))
}

func TestModuleCloseEndsWithTheCallersBudgetBeforeItsOwnTimeout(t *testing.T) {
	t.Parallel()

	settings := config.Default().Metrics
	settings.Enabled = true
	settings.ListenAddress = "127.0.0.1:0"
	// The module's own bound would allow a far longer drain than the budget.
	settings.ShutdownTimeout = config.Duration{Duration: time.Minute}
	module, err := New(settings, BuildInfo{}, Sources{})
	require.NoError(t, err)
	entered, release := make(chan struct{}), make(chan struct{})
	t.Cleanup(func() { close(release) })
	// A scrape that never finishes keeps the graceful drain waiting.
	module.handler = http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		close(entered)
		<-release
	})
	require.NoError(t, module.Start(context.Background()))
	go func() {
		request, requestErr := http.NewRequestWithContext(context.Background(), http.MethodGet, "http://"+module.listener.Addr().String()+"/metrics", nil)
		if requestErr != nil {
			return
		}
		if response, scrapeErr := http.DefaultClient.Do(request); scrapeErr == nil {
			_ = response.Body.Close()
		}
	}()
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		require.FailNow(t, "scrape did not reach the metrics handler")
	}

	const budget = 100 * time.Millisecond
	// Start the clock before the deadline so a slow host cannot make Close()
	// look early.
	started := time.Now()
	ctx, cancel := context.WithTimeout(context.Background(), budget)
	defer cancel()
	err = module.Close(ctx)
	elapsed := time.Since(started)
	require.ErrorIs(t, err, context.DeadlineExceeded, "Close() outcome for a drain that never finishes")
	require.GreaterOrEqual(t, elapsed, budget)
	// Generous for loaded race runs, yet far short of the module's own minute.
	require.Less(t, elapsed, time.Second, "Close() outlived the shutdown budget it was handed")
}
