// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package jobs

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/sudosylabs/proctor/server/app/job"
	"github.com/sudosylabs/proctor/server/app/mail"
	"github.com/sudosylabs/proctor/server/model"
	"github.com/sudosylabs/proctor/server/store"
)

type mailMaintenanceFake struct {
	store.MailStore
	expired []*store.MailMaintenanceResult
	cleaned []*store.MailMaintenanceResult
	calls   []string
}

type sittingMailMaintenanceFake struct {
	store.ExamSittingStore
	results []*store.ExamSittingMailMaintenanceResult
	calls   int
}

func (f *sittingMailMaintenanceFake) MaintainMailExpansions(context.Context, int) (*store.ExamSittingMailMaintenanceResult, error) {
	f.calls++
	value := f.results[0]
	f.results = f.results[1:]
	return value, nil
}

func (f *mailMaintenanceFake) SuppressExpired(context.Context, int) (*store.MailMaintenanceResult, error) {
	f.calls = append(f.calls, "suppress_expired")
	value := f.expired[0]
	f.expired = f.expired[1:]
	return value, nil
}

func (f *mailMaintenanceFake) CleanupTerminal(context.Context, int) (*store.MailMaintenanceResult, error) {
	f.calls = append(f.calls, "cleanup_terminal")
	value := f.cleaned[0]
	f.cleaned = f.cleaned[1:]
	return value, nil
}

// recordingMailMetrics records the delivery metrics the cleanup Job emits; it
// records no attempt, queue or health metric.
type recordingMailMetrics struct {
	mail.DeliveryRecorder
	deliveries []mail.DeliveryMetric
}

func (r *recordingMailMetrics) RecordMailDelivery(_ context.Context, metric mail.DeliveryMetric) {
	r.deliveries = append(r.deliveries, metric)
}

func TestMailCleanupJobBoundsPagesAndReportsSafeCounts(t *testing.T) {
	deliveries := &mailMaintenanceFake{
		expired: []*store.MailMaintenanceResult{{Affected: 2, More: true, Deliveries: []store.MailMaintenanceDelivery{{TemplateKey: model.MailTemplateSystemTest, State: model.MailDeliverySuppressed, PublicFailureCode: model.MailDeliveryExpiredCode, AttemptCount: 1, ProcessingLatency: time.Minute}}}, {Affected: 1}},
		cleaned: []*store.MailMaintenanceResult{{Affected: 3}},
	}
	sittings := &sittingMailMaintenanceFake{results: []*store.ExamSittingMailMaintenanceResult{
		{FanoutsTerminalized: 4, DeliveriesSuppressed: 2, More: true},
		{FanoutsTerminalized: 1, DeliveriesSuppressed: 1},
	}}
	command, _ := json.Marshal(MailCleanupCommand{PageSize: 10, MaxPages: 2})
	record, err := model.NewJob(model.NewJobID(), model.JobTypeMailCleanup, command, "cleanup", time.Now(), time.Now(), 5)
	require.NoError(t, err)
	metrics := &recordingMailMetrics{}
	descriptor := NewMailCleanup(deliveries, sittings, metrics, nil, nil).Descriptor
	outcome := descriptor.Handler.Run(context.Background(), job.NewExecution(record, nil, nil, nil))
	require.Equal(t, job.OutcomeSucceeded, outcome.Kind)
	require.Equal(t, `{"fanouts_terminalized":5,"fanout_deliveries_suppressed":3,"expired":3,"deleted":3}`, string(outcome.Result))
	require.Equal(t, 2, sittings.calls, "Sitting fan-out maintenance calls")
	require.Equal(t, []string{"cleanup_terminal", "suppress_expired", "suppress_expired"}, deliveries.calls, "one terminal cleanup and two bounded expiry pages")
	require.Len(t, metrics.deliveries, 1)
	require.Equal(t, model.MailDeliveryExpiredCode, metrics.deliveries[0].OutcomeCode)
}
