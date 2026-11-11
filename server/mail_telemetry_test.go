// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package server

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/sudosylabs/proctor/server/app/mail"
	"github.com/sudosylabs/proctor/server/model"
)

func TestProductionMailTelemetryRetainsBoundedSafeMetrics(t *testing.T) {
	t.Parallel()
	recorder, reader := newMailTelemetry(nil)
	operational, ok := reader.(*operationalMailTelemetry)
	require.True(t, ok, "production metrics reader type = %T", reader)
	require.Equal(t, recorder, operational, "production recorder type = %T, want shared operational telemetry", recorder)
	ctx := context.Background()
	key := mailDeliveryMetricKey{template: model.MailTemplateSystemTest, state: model.MailDeliveryQueued, code: "mail.transport.temporary"}
	attemptKey := mailDeliveryMetricKey{template: model.MailTemplateSystemTest, state: model.MailDeliverySending, code: "mail.attempt_started"}
	for range 2 {
		recorder.RecordMailDelivery(ctx, mail.DeliveryMetric{
			TemplateKey: key.template, State: key.state, OutcomeCode: key.code,
			ProcessingLatency: time.Second,
		})
		recorder.RecordMailAttempt(ctx, mail.AttemptMetric{TemplateKey: attemptKey.template, State: attemptKey.state})
	}
	recorder.RecordMailQueueSnapshot(ctx, []mail.QueueMetric{{
		TemplateKey: key.template, State: key.state, OutcomeCode: key.code,
		Count: 7, OldestAge: 6 * time.Minute, HealthCode: mail.HealthQueueDelayed,
	}})
	recorder.RecordMailHealth(ctx, mail.HealthMetric{Code: mail.HealthQueueDelayed})
	snapshot := reader.Snapshot()

	operational.mu.Lock()
	aggregate := operational.deliveries[key]
	attemptAggregate := operational.deliveries[attemptKey]
	require.Equal(t, uint64(2), aggregate.count, "operational telemetry = deliveries %#v queues %#v buckets %#v health", operational.deliveries, operational.queues, operational.queueBuckets)
	require.Equal(t, uint64(0), aggregate.attempts, "operational telemetry = deliveries %#v queues %#v buckets %#v health", operational.deliveries, operational.queues, operational.queueBuckets)
	require.Equal(t, 2*time.Second, aggregate.processingLatency, "operational telemetry = deliveries %#v queues %#v buckets %#v health", operational.deliveries, operational.queues, operational.queueBuckets)
	require.Equal(t, time.Second, aggregate.maximumLatency, "operational telemetry = deliveries %#v queues %#v buckets %#v health", operational.deliveries, operational.queues, operational.queueBuckets)
	require.Equal(t, uint64(0), attemptAggregate.count, "operational telemetry = deliveries %#v queues %#v buckets %#v health", operational.deliveries, operational.queues, operational.queueBuckets)
	require.Equal(t, uint64(2), attemptAggregate.attempts, "operational telemetry = deliveries %#v queues %#v buckets %#v health", operational.deliveries, operational.queues, operational.queueBuckets)
	require.Equal(t, int64(7), operational.queues[key].Count, "operational telemetry = deliveries %#v queues %#v buckets %#v health", operational.deliveries, operational.queues, operational.queueBuckets)
	require.Equal(t, "lt_15m0s", operational.queueBuckets[key], "operational telemetry = deliveries %#v queues %#v buckets %#v health", operational.deliveries, operational.queues, operational.queueBuckets)
	require.Equal(t, mail.HealthQueueDelayed, operational.health, "operational telemetry = deliveries %#v queues %#v buckets %#v health", operational.deliveries, operational.queues, operational.queueBuckets)
	require.Len(t, snapshot.Deliveries, 2, "operational telemetry = deliveries %#v queues %#v buckets %#v health", operational.deliveries, operational.queues, operational.queueBuckets)
	require.Len(t, snapshot.Queues, 1, "operational telemetry = deliveries %#v queues %#v buckets %#v health", operational.deliveries, operational.queues, operational.queueBuckets)
	operational.mu.Unlock()
	recorder.RecordMailQueueSnapshot(ctx, nil)
	require.Empty(t, reader.Snapshot().Queues)
}
