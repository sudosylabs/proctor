// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package server

import (
	"context"
	"fmt"
	"sort"
	"sync"
	"time"

	"github.com/sudosylabs/proctor/server/app/mail"
	"github.com/sudosylabs/proctor/server/logging"
	"github.com/sudosylabs/proctor/server/model"
)

type mailDeliveryMetricKey struct {
	template model.MailTemplateKey
	state    model.MailDeliveryState
	code     string
}

type mailDeliveryMetricAggregate struct {
	count             uint64
	attempts          uint64
	processingLatency time.Duration
	maximumLatency    time.Duration
}

// operationalMailTelemetry is the production bounded metrics collector and
// structured-log adapter. Its dimensions come only from closed template,
// state, and public outcome vocabularies; it never retains identifiers,
// recipients, or message content.
type operationalMailTelemetry struct {
	log *logging.Logger

	mu           sync.Mutex
	deliveries   map[mailDeliveryMetricKey]mailDeliveryMetricAggregate
	queues       map[mailDeliveryMetricKey]mail.QueueMetric
	queueBuckets map[mailDeliveryMetricKey]string
	health       string
}

// newMailTelemetry returns the delivery recorder and metrics reader the
// application uses. Every observation goes to each of first, in order, and
// then to the operational collector the reader reports from.
func newMailTelemetry(log *logging.Logger, first ...mail.DeliveryRecorder) (mail.DeliveryRecorder, mail.MetricsReader) {
	operational := &operationalMailTelemetry{
		log:          log,
		deliveries:   make(map[mailDeliveryMetricKey]mailDeliveryMetricAggregate),
		queues:       make(map[mailDeliveryMetricKey]mail.QueueMetric),
		queueBuckets: make(map[mailDeliveryMetricKey]string),
	}
	if len(first) == 0 {
		return operational, operational
	}

	return append(mailDeliveryRecorders(first), operational), operational
}

func (r *operationalMailTelemetry) RecordMailDelivery(ctx context.Context, metric mail.DeliveryMetric) {
	key := mailDeliveryMetricKey{template: metric.TemplateKey, state: metric.State, code: metric.OutcomeCode}
	r.mu.Lock()
	aggregate := r.deliveries[key]
	aggregate.count++
	aggregate.processingLatency += metric.ProcessingLatency
	if metric.ProcessingLatency > aggregate.maximumLatency {
		aggregate.maximumLatency = metric.ProcessingLatency
	}

	r.deliveries[key] = aggregate
	r.mu.Unlock()
	if r.log != nil {
		r.log.InfoContext(
			ctx,
			"mail delivery outcome",
			logging.String("template_key", string(metric.TemplateKey)),
			logging.String("state", string(metric.State)),
			logging.String("outcome_code", metric.OutcomeCode),
			logging.Duration("processing_latency", metric.ProcessingLatency),
		)
	}
}

func (r *operationalMailTelemetry) RecordMailAttempt(ctx context.Context, metric mail.AttemptMetric) {
	key := mailDeliveryMetricKey{template: metric.TemplateKey, state: metric.State, code: "mail.attempt_started"}
	r.mu.Lock()
	aggregate := r.deliveries[key]
	aggregate.attempts++
	r.deliveries[key] = aggregate
	r.mu.Unlock()
	if r.log != nil {
		r.log.InfoContext(
			ctx,
			"mail delivery attempt started",
			logging.String("template_key", string(metric.TemplateKey)),
			logging.String("state", string(metric.State)),
		)
	}
}

func (r *operationalMailTelemetry) RecordMailQueueSnapshot(ctx context.Context, metrics []mail.QueueMetric) {
	next := make(map[mailDeliveryMetricKey]mail.QueueMetric, len(metrics))
	for _, metric := range metrics {
		key := mailDeliveryMetricKey{template: metric.TemplateKey, state: metric.State, code: metric.OutcomeCode}
		next[key] = metric
	}

	r.mu.Lock()
	previous := r.queues
	r.queues = next
	observations := make([]mail.QueueMetric, 0, len(next)+len(previous))
	for key, metric := range next {
		bucket := mailQueueAgeBucket(metric.OldestAge)
		prior, exists := previous[key]
		priorBucket := r.queueBuckets[key]
		r.queueBuckets[key] = bucket
		if !exists ||
			prior.Count != metric.Count ||
			prior.HealthCode != metric.HealthCode ||
			prior.Truncated != metric.Truncated ||
			priorBucket != bucket {
			observations = append(observations, metric)
		}
	}

	for key, metric := range previous {
		if _, exists := next[key]; exists {
			continue
		}

		metric.Count = 0
		metric.OldestAge = 0
		observations = append(observations, metric)
		delete(r.queueBuckets, key)
	}

	r.mu.Unlock()
	if r.log == nil {
		return
	}

	for _, metric := range observations {
		bucket := mailQueueAgeBucket(metric.OldestAge)
		r.log.InfoContext(
			ctx,
			"mail queue observation",
			logging.String("template_key", string(metric.TemplateKey)),
			logging.String("state", string(metric.State)),
			logging.String("outcome_code", metric.OutcomeCode),
			logging.Int64("count", metric.Count),
			logging.String("oldest_age_bucket", bucket),
			logging.String("health_code", metric.HealthCode),
			logging.Bool("truncated", metric.Truncated),
		)
	}
}

func (r *operationalMailTelemetry) RecordMailHealth(ctx context.Context, metric mail.HealthMetric) {
	r.mu.Lock()
	changed := r.health != metric.Code
	r.health = metric.Code
	r.mu.Unlock()
	if changed && r.log != nil {
		r.log.InfoContext(ctx, "mail subsystem health", logging.String("health_code", metric.Code))
	}
}

func (r *operationalMailTelemetry) Snapshot() mail.MetricsSnapshot {
	r.mu.Lock()
	defer r.mu.Unlock()
	snapshot := mail.MetricsSnapshot{
		Deliveries: make([]mail.DeliveryMetricAggregate, 0, len(r.deliveries)),
		Queues:     make([]mail.QueueMetric, 0, len(r.queues)),
		HealthCode: r.health,
	}
	for key, aggregate := range r.deliveries {
		snapshot.Deliveries = append(snapshot.Deliveries, mail.DeliveryMetricAggregate{
			TemplateKey:              key.template,
			State:                    key.state,
			OutcomeCode:              key.code,
			Count:                    aggregate.count,
			AttemptCount:             aggregate.attempts,
			ProcessingLatency:        aggregate.processingLatency,
			MaximumProcessingLatency: aggregate.maximumLatency,
		})
	}

	for _, metric := range r.queues {
		snapshot.Queues = append(snapshot.Queues, metric)
	}

	sort.Slice(snapshot.Deliveries, func(i, j int) bool {
		left, right := snapshot.Deliveries[i], snapshot.Deliveries[j]
		if left.TemplateKey != right.TemplateKey {
			return left.TemplateKey < right.TemplateKey
		}
		if left.State != right.State {
			return left.State < right.State
		}

		return left.OutcomeCode < right.OutcomeCode
	})
	sort.Slice(snapshot.Queues, func(i, j int) bool {
		left, right := snapshot.Queues[i], snapshot.Queues[j]
		if left.TemplateKey != right.TemplateKey {
			return left.TemplateKey < right.TemplateKey
		}
		if left.State != right.State {
			return left.State < right.State
		}

		return left.OutcomeCode < right.OutcomeCode
	})
	return snapshot
}

func mailQueueAgeBucket(age time.Duration) string {
	for _, boundary := range []time.Duration{
		5 * time.Minute,
		15 * time.Minute,
		30 * time.Minute,
		time.Hour,
		3 * time.Hour,
		12 * time.Hour,
		24 * time.Hour,
	} {
		if age < boundary {
			return fmt.Sprintf("lt_%s", boundary)
		}
	}

	return "gte_24h"
}

// mailDeliveryRecorders reports every observation to each recorder in order.
type mailDeliveryRecorders []mail.DeliveryRecorder

func (r mailDeliveryRecorders) RecordMailDelivery(ctx context.Context, metric mail.DeliveryMetric) {
	for _, recorder := range r {
		recorder.RecordMailDelivery(ctx, metric)
	}
}

func (r mailDeliveryRecorders) RecordMailAttempt(ctx context.Context, metric mail.AttemptMetric) {
	for _, recorder := range r {
		recorder.RecordMailAttempt(ctx, metric)
	}
}

func (r mailDeliveryRecorders) RecordMailQueueSnapshot(ctx context.Context, metrics []mail.QueueMetric) {
	for _, recorder := range r {
		recorder.RecordMailQueueSnapshot(ctx, metrics)
	}
}

func (r mailDeliveryRecorders) RecordMailHealth(ctx context.Context, metric mail.HealthMetric) {
	for _, recorder := range r {
		recorder.RecordMailHealth(ctx, metric)
	}
}
