// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package mail

import (
	"context"
	"errors"
	"time"

	"github.com/sudosylabs/proctor/server/model"
	"github.com/sudosylabs/proctor/server/store"
)

// queueDelayThreshold is how long the oldest queued delivery may wait before
// the queue counts as delayed.
const queueDelayThreshold = 5 * time.Minute

type deliveryProber interface{ Probe(context.Context) error }

// MaintenanceMonitor is the periodic mail maintenance task. Each run
// suppresses outstanding deliveries while mail is disabled, probes the sender,
// decides the Health code from the probe and the age of the queue, and
// records the health and queue metrics.
type MaintenanceMonitor struct {
	Mail     store.MailStore
	Sender   Sender
	Health   *Health
	Recorder DeliveryRecorder
	Now      func() time.Time
}

// Run performs one maintenance pass.
func (m MaintenanceMonitor) Run(ctx context.Context) error {
	if m.Mail == nil || m.Sender == nil || m.Health == nil || m.Now == nil {
		return errors.New("mail maintenance dependencies are unavailable")
	}

	enabled := m.Sender.Enabled()
	healthCode := HealthDisabled
	if !enabled {
		suppressed, err := m.Mail.SuppressOutstanding(ctx, &store.MailOutstandingSuppression{Code: model.MailDeliveryDisabledCode, Limit: 500})
		if err != nil {
			return err
		}

		RecordMaintenanceDeliveries(ctx, m.Recorder, suppressed)
	} else {
		healthCode = HealthHealthy
	}

	if prober, ok := m.Sender.(deliveryProber); enabled && ok {
		if err := prober.Probe(ctx); err != nil {
			healthCode = HealthSMTPOutage
		}
	}

	snapshot, err := m.Mail.QueueSnapshot(ctx)
	if err != nil {
		return err
	}

	now := model.TimeUTC(m.Now())
	oldestAge := time.Duration(0)
	if snapshot != nil && !snapshot.OldestQueuedAt.IsZero() && now.After(snapshot.OldestQueuedAt) {
		oldestAge = now.Sub(snapshot.OldestQueuedAt)
		if oldestAge >= queueDelayThreshold && healthCode == HealthHealthy {
			healthCode = HealthQueueDelayed
		}
	}

	m.Health.Set(healthCode)
	if m.Recorder != nil {
		m.Recorder.RecordMailHealth(ctx, HealthMetric{Code: healthCode})
	}

	if m.Recorder != nil {
		metrics := make([]QueueMetric, 0)
		if snapshot != nil {
			metrics = make([]QueueMetric, 0, len(snapshot.Counts))
			for _, count := range snapshot.Counts {
				itemAge := time.Duration(0)
				if !count.OldestObservedAt.IsZero() && now.After(count.OldestObservedAt) {
					itemAge = now.Sub(count.OldestObservedAt)
				}

				metrics = append(metrics, QueueMetric{
					TemplateKey: count.TemplateKey,
					State:       count.State,
					Count:       count.Count,
					OldestAge:   itemAge,
					HealthCode:  healthCode,
					OutcomeCode: count.PublicFailureCode,
					Truncated:   snapshot.More,
				})
			}
		}

		m.Recorder.RecordMailQueueSnapshot(ctx, metrics)
	}

	return nil
}

// RecordMaintenanceDeliveries records each delivery a maintenance operation
// suppressed or expired. The monitor and the mail cleanup Job share it.
func RecordMaintenanceDeliveries(ctx context.Context, recorder DeliveryRecorder, result *store.MailMaintenanceResult) {
	if recorder == nil || result == nil {
		return
	}

	for _, delivery := range result.Deliveries {
		recorder.RecordMailDelivery(ctx, DeliveryMetric{
			TemplateKey:       delivery.TemplateKey,
			State:             delivery.State,
			OutcomeCode:       delivery.PublicFailureCode,
			ProcessingLatency: delivery.ProcessingLatency,
		})
	}
}
