// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package server

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/sudosylabs/proctor/server/app/mail"
	"github.com/sudosylabs/proctor/server/model"
)

func TestMailQueueMetricsAggregateFailureBucketsAndExposeTruncation(t *testing.T) {
	t.Parallel()

	values := []mail.QueueMetric{
		{TemplateKey: model.MailTemplateSystemTest, State: model.MailDeliveryQueued, OutcomeCode: "temporary", Count: 2, OldestAge: time.Minute},
		{TemplateKey: model.MailTemplateSystemTest, State: model.MailDeliveryQueued, OutcomeCode: "permanent", Count: 3, OldestAge: 2 * time.Minute, Truncated: true},
	}
	aggregates, truncated := aggregateMailQueueMetrics(values)
	aggregate := aggregates[mailQueueMetricIdentity{template: model.MailTemplateSystemTest, state: model.MailDeliveryQueued}]
	require.Equal(t, int64(5), aggregate.count, "aggregate = %#v truncated", aggregate)
	require.Equal(t, 2*time.Minute, aggregate.oldestAge, "aggregate = %#v truncated", aggregate)
	require.True(t, truncated, "aggregate = %#v truncated", aggregate)
}
