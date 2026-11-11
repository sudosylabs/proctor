// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package server

import (
	"context"
	"sync"
	"time"

	"github.com/sudosylabs/proctor/server/app"
	"github.com/sudosylabs/proctor/server/app/identity"
	"github.com/sudosylabs/proctor/server/app/job"
	"github.com/sudosylabs/proctor/server/app/mail"
	"github.com/sudosylabs/proctor/server/filecontent"
	"github.com/sudosylabs/proctor/server/metrics"
	"github.com/sudosylabs/proctor/server/model"
)

// The metrics module may not import application packages, and the application
// has no Prometheus dependency. These recorders implement the application's
// recorder ports over the metrics module.

// jobMetricsRecorder implements the Job engine's recorder port.
type jobMetricsRecorder struct{ metrics *metrics.Module }

// applicationMetricsRecorder implements the application's operational event
// port.
type applicationMetricsRecorder struct{ metrics *metrics.Module }

var (
	_ job.Recorder                  = jobMetricsRecorder{}
	_ identity.PasswordWorkRecorder = metrics.WorkRecorder{}
	_ filecontent.WorkRecorder      = metrics.WorkRecorder{}
)

func (r jobMetricsRecorder) Started(jobType model.JobType) {
	r.metrics.JobStarted(string(jobType))
}

func (r jobMetricsRecorder) Finished(jobType model.JobType, result job.ExecutionOutcome, duration time.Duration) {
	r.metrics.JobFinished(string(jobType), string(result), duration)
}

func (r jobMetricsRecorder) Record(activity job.Activity) {
	r.metrics.ObserveJobActivity(activity.Kind, activity.Name, activity.Operation, activity.Outcome, activity.Duration, activity.QueueLatency)
}

func (r applicationMetricsRecorder) RecordOperationalEvent(event app.OperationalEvent) {
	r.metrics.ObserveApplication(event.Subsystem(), event.Event(), event.Outcome())
}

// prometheusMailRecorder implements the mail delivery recorder port.
type prometheusMailRecorder struct {
	metrics *metrics.Module
	mu      sync.Mutex
	queues  map[mailQueueMetricIdentity]struct{}
}

func newPrometheusMailRecorder(metrics *metrics.Module) *prometheusMailRecorder {
	return &prometheusMailRecorder{metrics: metrics, queues: make(map[mailQueueMetricIdentity]struct{})}
}

func (r *prometheusMailRecorder) RecordMailDelivery(_ context.Context, metric mail.DeliveryMetric) {
	r.metrics.ObserveMailDelivery(string(metric.TemplateKey), string(metric.State), metric.OutcomeCode, metric.ProcessingLatency)
}

func (r *prometheusMailRecorder) RecordMailAttempt(_ context.Context, metric mail.AttemptMetric) {
	r.metrics.ObserveMailAttempt(string(metric.TemplateKey), string(metric.State), 1)
}

func (r *prometheusMailRecorder) RecordMailQueueSnapshot(_ context.Context, values []mail.QueueMetric) {
	r.mu.Lock()
	aggregates, truncated := aggregateMailQueueMetrics(values)
	next := make(map[mailQueueMetricIdentity]struct{}, len(aggregates))
	for key, value := range aggregates {
		next[key] = struct{}{}
		r.metrics.SetMailQueue(string(key.template), string(key.state), value.count, value.oldestAge)
	}
	for key := range r.queues {
		if _, exists := next[key]; exists {
			continue
		}
		r.metrics.SetMailQueue(string(key.template), string(key.state), 0, 0)
	}
	r.metrics.SetMailQueueSnapshotTruncated(truncated)
	r.queues = next
	r.mu.Unlock()
}

type mailQueueAggregate struct {
	count     int64
	oldestAge time.Duration
}

type mailQueueMetricIdentity struct {
	template model.MailTemplateKey
	state    model.MailDeliveryState
}

func aggregateMailQueueMetrics(values []mail.QueueMetric) (map[mailQueueMetricIdentity]mailQueueAggregate, bool) {
	result := make(map[mailQueueMetricIdentity]mailQueueAggregate, len(values))
	truncated := false
	for _, value := range values {
		key := mailQueueMetricIdentity{template: value.TemplateKey, state: value.State}
		aggregate := result[key]
		aggregate.count += max(value.Count, int64(0))
		if value.OldestAge > aggregate.oldestAge {
			aggregate.oldestAge = value.OldestAge
		}
		result[key] = aggregate
		truncated = truncated || value.Truncated
	}
	return result, truncated
}

func (r *prometheusMailRecorder) RecordMailHealth(_ context.Context, metric mail.HealthMetric) {
	r.metrics.SetMailHealth(metric.Code)
}
