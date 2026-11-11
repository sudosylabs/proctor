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
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/sudosylabs/proctor/server/model"
	"github.com/sudosylabs/proctor/server/store"
)

type mailMaintenanceFake struct {
	store.MailStore
	suppressed  int
	outstanding *store.MailMaintenanceResult
	snapshot    *store.MailQueueSnapshot
}

func (f *mailMaintenanceFake) SuppressOutstanding(ctx context.Context, input *store.MailOutstandingSuppression) (*store.MailMaintenanceResult, error) {
	f.suppressed++
	if f.outstanding != nil {
		return f.outstanding, nil
	}
	return &store.MailMaintenanceResult{Affected: 1}, nil
}

func (f *mailMaintenanceFake) QueueSnapshot(context.Context) (*store.MailQueueSnapshot, error) {
	return f.snapshot, nil
}

type probingMailSender struct {
	mailSenderFake
	probeErr error
}

func (s *probingMailSender) Probe(context.Context) error { return s.probeErr }

func TestDisabledMailMaintenanceSuppressesOutstandingWithoutAffectingReadiness(t *testing.T) {
	metrics := &recordingMailMetrics{}
	mail := &mailMaintenanceFake{outstanding: &store.MailMaintenanceResult{Affected: 1, Deliveries: []store.MailMaintenanceDelivery{{
		TemplateKey: model.MailTemplateSystemTest, State: model.MailDeliverySuppressed,
		PublicFailureCode: model.MailDeliveryDisabledCode, ProcessingLatency: time.Minute,
	}}}}
	health := NewHealth(false)
	monitor := MaintenanceMonitor{Mail: mail, Sender: &mailSenderFake{}, Health: health, Recorder: metrics, Now: time.Now}
	require.NoError(t, monitor.Run(context.Background()))
	require.Equal(t, 1, mail.suppressed, "suppressed")
	require.Equal(t, HealthDisabled, health.Code(), "suppressed")
	require.False(t, health.Degraded(), "suppressed")
	require.Len(t, metrics.deliveries, 1, "disabled metrics = deliveries")
	require.Equal(t, model.MailDeliveryDisabledCode, metrics.deliveries[0].OutcomeCode, "disabled metrics = deliveries")
	require.Len(t, metrics.health, 1, "disabled metrics = deliveries")
	require.Equal(t, HealthDisabled, metrics.health[0].Code, "disabled metrics = deliveries")
}

func TestMailMaintenanceReportsSMTPOutageAndQueueDelayAsSubsystemDegradation(t *testing.T) {
	now := time.Date(2026, 8, 17, 12, 0, 0, 0, time.UTC)
	mail := &mailMaintenanceFake{snapshot: &store.MailQueueSnapshot{OldestQueuedAt: now.Add(-10 * time.Minute)}}
	health := NewHealth(true)
	sender := &probingMailSender{mailSenderFake: mailSenderFake{enabled: true}, probeErr: errors.New("smtp unavailable")}
	require.NoError(t, (MaintenanceMonitor{Mail: mail, Sender: sender, Health: health, Now: func() time.Time { return now }}).Run(context.Background()))
	require.Equal(t, HealthSMTPOutage, health.Code())
	require.True(t, health.Degraded())
	sender.probeErr = nil
	require.NoError(t, (MaintenanceMonitor{Mail: mail, Sender: sender, Health: health, Now: func() time.Time { return now }}).Run(context.Background()))
	require.Equal(t, HealthQueueDelayed, health.Code())
	require.True(t, health.Degraded())
}

func TestMailMaintenanceEmitsBoundedMetricsAndLogsHealthOnlyOnTransitions(t *testing.T) {
	now := time.Date(2026, 8, 17, 12, 0, 0, 0, time.UTC)
	metrics := &recordingMailMetrics{}
	mail := &mailMaintenanceFake{snapshot: &store.MailQueueSnapshot{
		Counts: []store.MailQueueCount{
			{TemplateKey: model.MailTemplateSystemTest, State: model.MailDeliveryQueued, Count: 500, OldestObservedAt: now.Add(-time.Minute)},
			{TemplateKey: model.MailTemplateSystemTest, State: model.MailDeliverySending, Count: 1, OldestObservedAt: now.Add(-30 * time.Second)},
		},
		OldestQueuedAt: now.Add(-time.Minute), More: true,
	}}
	sender := &probingMailSender{mailSenderFake: mailSenderFake{enabled: true}}
	health := NewHealth(true)
	monitor := MaintenanceMonitor{Mail: mail, Sender: sender, Health: health, Recorder: metrics, Now: func() time.Time { return now }}
	require.NoError(t, monitor.Run(context.Background()))
	require.Len(t, metrics.health, 1, "initial metrics = health")
	require.Equal(t, HealthHealthy, metrics.health[0].Code, "initial metrics = health")
	require.Len(t, metrics.queues, 2, "initial metrics = health")
	require.True(t, metrics.queues[0].Truncated, "initial metrics = health")
	require.Equal(t, int64(500), metrics.queues[0].Count, "initial metrics = health")
	require.Equal(t, time.Minute, metrics.queues[0].OldestAge, "initial metrics = health")
	require.Equal(t, 30*time.Second, metrics.queues[1].OldestAge, "initial metrics = health")
	mail.snapshot = &store.MailQueueSnapshot{}
	require.NoError(t, monitor.Run(context.Background()))
	require.Empty(t, metrics.queues)
	sender.probeErr = errors.New("smtp unavailable")
	require.NoError(t, monitor.Run(context.Background()))
	require.NoError(t, monitor.Run(context.Background()))
	require.Len(t, metrics.health, 4)
	require.Equal(t, HealthSMTPOutage, metrics.health[2].Code)
	require.Equal(t, HealthSMTPOutage, metrics.health[3].Code)
}
