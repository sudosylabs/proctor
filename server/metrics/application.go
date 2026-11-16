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

	"github.com/sudosylabs/proctor/server/app/job"
	"github.com/sudosylabs/proctor/server/app/mail"
	"github.com/sudosylabs/proctor/server/app/operational"
	"github.com/sudosylabs/proctor/server/model"
)

// The application has no Prometheus dependency, so the module implements
// the application's recorder ports here, over its own collectors.

// RecordOperationalEvent counts one bounded use-case outcome.
func (m *Module) RecordOperationalEvent(event operational.Event) {
	if m != nil {
		m.applicationEvents.WithLabelValues(event.Subsystem(), event.Event(), event.Outcome()).Inc()
	}
}

var _ operational.Recorder = (*Module)(nil)

// JobRecorder adapts m to the job engine's recorder port.
func (m *Module) JobRecorder() job.Recorder { return jobRecorder{metrics: m} }

type jobRecorder struct{ metrics *Module }

func (r jobRecorder) Started(jobType model.JobType) {
	r.metrics.JobStarted(string(jobType))
}

func (r jobRecorder) Finished(jobType model.JobType, result job.ExecutionOutcome, duration time.Duration) {
	r.metrics.JobFinished(string(jobType), string(result), duration)
}

func (r jobRecorder) Record(activity job.Activity) {
	r.metrics.ObserveJobActivity(activity.Kind, activity.Name, activity.Operation, activity.Outcome, activity.Duration, activity.QueueLatency)
}

// mailRecorder exports mail delivery observations. It remembers the queue
// buckets it last set so a bucket that empties is reset to zero rather than
// left at its last count.
type mailRecorder struct {
	metrics *Module
	mu      sync.Mutex
	queues  map[mailQueueMetricIdentity]struct{}
}

// MailRecorder adapts m to the mail delivery recorder port.
func (m *Module) MailRecorder() mail.DeliveryRecorder {
	return &mailRecorder{metrics: m, queues: make(map[mailQueueMetricIdentity]struct{})}
}

func (r *mailRecorder) RecordMailDelivery(_ context.Context, metric mail.DeliveryMetric) {
	r.metrics.ObserveMailDelivery(string(metric.TemplateKey), string(metric.State), metric.OutcomeCode, metric.ProcessingLatency)
}

func (r *mailRecorder) RecordMailAttempt(_ context.Context, metric mail.AttemptMetric) {
	r.metrics.ObserveMailAttempt(string(metric.TemplateKey), string(metric.State), 1)
}

func (r *mailRecorder) RecordMailQueueSnapshot(_ context.Context, values []mail.QueueMetric) {
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

func (r *mailRecorder) RecordMailHealth(_ context.Context, metric mail.HealthMetric) {
	r.metrics.SetMailHealth(metric.Code)
}
