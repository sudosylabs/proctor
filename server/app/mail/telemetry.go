// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package mail

import (
	"context"
	"time"

	"github.com/sudosylabs/proctor/server/model"
)

// DeliveryMetric is one delivery reaching a state: delivered, failed,
// suppressed or expired, with how long it took from creation.
type DeliveryMetric struct {
	TemplateKey       model.MailTemplateKey
	State             model.MailDeliveryState
	OutcomeCode       string
	ProcessingLatency time.Duration
}

// AttemptMetric is one send attempt of a delivery.
type AttemptMetric struct {
	TemplateKey model.MailTemplateKey
	State       model.MailDeliveryState
}

// QueueMetric is the size and age of one template and state in the delivery
// queue, as the maintenance monitor last observed it.
type QueueMetric struct {
	TemplateKey model.MailTemplateKey
	State       model.MailDeliveryState
	OutcomeCode string
	Count       int64
	OldestAge   time.Duration
	HealthCode  string
	Truncated   bool
}

// HealthMetric is the mail health code the maintenance monitor last decided.
type HealthMetric struct{ Code string }

// DeliveryMetricAggregate totals the DeliveryMetrics of one template, state
// and outcome since the process started.
type DeliveryMetricAggregate struct {
	TemplateKey              model.MailTemplateKey
	State                    model.MailDeliveryState
	OutcomeCode              string
	Count                    uint64
	AttemptCount             uint64
	ProcessingLatency        time.Duration
	MaximumProcessingLatency time.Duration
}

// MetricsSnapshot is what GetMailMetrics returns.
type MetricsSnapshot struct {
	Deliveries []DeliveryMetricAggregate
	Queues     []QueueMetric
	HealthCode string
}

// DeliveryRecorder is a bounded telemetry sink. Its contracts omit delivery
// IDs and every recipient or content value, keeping metric cardinality and
// logs safe.
type DeliveryRecorder interface {
	RecordMailDelivery(context.Context, DeliveryMetric)
	RecordMailAttempt(context.Context, AttemptMetric)
	RecordMailQueueSnapshot(context.Context, []QueueMetric)
	RecordMailHealth(context.Context, HealthMetric)
}

// MetricsReader exposes the in-process snapshot behind GetMailMetrics. An
// export-only recorder need not implement it.
type MetricsReader interface {
	Snapshot() MetricsSnapshot
}
