// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package app

import (
	"context"

	"github.com/sudosylabs/proctor/server/app/jobhandler"
	"github.com/sudosylabs/proctor/server/model"
)

// These adapters translate application-owned policy and use cases into the
// narrow capabilities consumed by concrete Jobs. They intentionally do not
// expose App, Dependencies, store.Catalog, or platform.Service.

type jobMailDeliveryRecorder struct{ recorder MailDeliveryRecorder }

func (a jobMailDeliveryRecorder) RecordJobMailDelivery(ctx context.Context, metric jobhandler.MailDeliveryMetric) {
	if a.recorder == nil {
		return
	}
	a.recorder.RecordMailDelivery(ctx, MailDeliveryMetric{
		TemplateKey: metric.TemplateKey, State: metric.State, OutcomeCode: metric.OutcomeCode,
		ProcessingLatency: metric.ProcessingLatency,
	})
}

func (a jobMailDeliveryRecorder) RecordJobMailAttempt(ctx context.Context, metric jobhandler.MailAttemptMetric) {
	if a.recorder == nil {
		return
	}
	a.recorder.RecordMailAttempt(ctx, MailAttemptMetric{TemplateKey: metric.TemplateKey, State: metric.State})
}

type jobMailHealth struct{ health *MailHealth }

func (a jobMailHealth) SetJobMailHealth(code string) {
	if a.health != nil {
		a.health.set(code)
	}
}

func jobMailDeliveryIsRelevant(ctx context.Context, delivery *model.MailDelivery) (bool, error) {
	relevance, err := evaluateMailDeliveryRelevance(ctx, delivery)
	return relevance == mailDeliveryRelevant, err
}
