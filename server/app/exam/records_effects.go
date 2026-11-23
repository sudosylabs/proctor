// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package exam

import (
	"context"

	"github.com/sudosylabs/proctor/server/app/realtime"
	"github.com/sudosylabs/proctor/server/model"
)

// publishRetentionChanged tells retention viewers to refetch after a hold or
// a records completion committed. A failure is reported, never returned.
func (r *Records) publishRetentionChanged(
	ctx context.Context,
	build func(model.InstitutionID) (realtime.Event, error),
) {
	if r.publisher == nil {
		return
	}
	institution, err := r.authorizer.InstitutionResource(ctx)
	var event realtime.Event
	if err == nil {
		event, err = build(model.InstitutionID(institution.ID))
	}
	if err == nil {
		err = r.publisher.Publish(ctx, event)
	}
	if err != nil {
		r.publisher.Report(ctx, "retention.changed", err)
	}
}

func (r *Records) publishHoldChanged(ctx context.Context, hold *model.RetentionHold) {
	r.publishRetentionChanged(ctx, func(institutionID model.InstitutionID) (realtime.Event, error) {
		return realtime.NewRetentionHoldChangedEvent(institutionID, hold.ID, hold.Scope.ExamID)
	})
}

func (r *Records) publishRecordsChanged(ctx context.Context, examID model.ExamID) {
	r.publishRetentionChanged(ctx, func(institutionID model.InstitutionID) (realtime.Event, error) {
		return realtime.NewRetentionRecordsChangedEvent(institutionID, examID)
	})
}
