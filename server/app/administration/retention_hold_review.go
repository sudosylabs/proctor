// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package administration

import (
	"context"
	"time"

	"github.com/sudosylabs/proctor/server/app/job"
	"github.com/sudosylabs/proctor/server/app/realtime"
	"github.com/sudosylabs/proctor/server/store"
)

// RetentionHoldReviewTask sends a refresh hint when a hold becomes due for review.
func (s *Service) RetentionHoldReviewTask() job.PeriodicTask {
	return job.PeriodicTask{
		Name:     "retention-hold-review",
		Interval: time.Minute,
		Runner:   retentionHoldReview{service: s},
	}
}

type retentionHoldReview struct {
	service *Service
}

func (r retentionHoldReview) Run(ctx context.Context) error {
	s := r.service
	institution, err := s.institutions.GetSingleton(ctx)
	if store.IsNotFound(err) {
		return nil
	}
	if err != nil || institution == nil || !institution.ID.IsValid() {
		return retentionPolicyError(err)
	}
	holds, err := s.retention.ClaimDueHoldNotifications(ctx)
	if err != nil {
		return retentionError(err)
	}
	// Notifications are best-effort hints. Marking the batch in the store
	// first prevents every node from publishing it.
	for _, hold := range holds {
		event, err := realtime.NewRetentionHoldChangedEvent(institution.ID, hold.ID, hold.ExamID)
		if err == nil {
			err = s.publisher.Publish(ctx, event)
		}
		if err != nil {
			s.publisher.Report(ctx, "retention.changed", err)
		}
	}
	return nil
}
