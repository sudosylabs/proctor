// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package sitting

import (
	"context"
	"errors"
	"time"

	"github.com/sudosylabs/proctor/server/app/realtime"
	"github.com/sudosylabs/proctor/server/model"
	"github.com/sudosylabs/proctor/server/store"
)

// publishScheduled announces a newly scheduled Sitting to its managers, then
// invalidates its board and its candidates' Exam activity. at is the instant
// the schedule was committed.
func (s *Service) publishScheduled(ctx context.Context, sitting *model.ExamSitting, at time.Time) error {
	event, err := realtime.NewExamSittingScheduledEvent(sitting.ExamID, sitting.ID, sitting.State, sitting.Revision, at)
	if err != nil {
		return err
	}

	return s.publishWithInvalidations(ctx, sitting, event)
}

// publishScheduleUpdated announces a changed schedule to the Sitting's
// managers, then invalidates its board and its candidates' Exam activity.
func (s *Service) publishScheduleUpdated(ctx context.Context, sitting *model.ExamSitting, at time.Time) error {
	event, err := realtime.NewExamSittingScheduleUpdatedEvent(sitting.ExamID, sitting.ID, sitting.State, sitting.Revision, at)
	if err != nil {
		return err
	}

	return s.publishWithInvalidations(ctx, sitting, event)
}

// publishCanceled announces a canceled Sitting to its managers, then
// invalidates its board and its candidates' Exam activity.
func (s *Service) publishCanceled(ctx context.Context, sitting *model.ExamSitting, at time.Time) error {
	event, err := realtime.NewExamSittingCanceledEvent(sitting.ExamID, sitting.ID, sitting.State, sitting.Revision, at)
	if err != nil {
		return err
	}

	return s.publishWithInvalidations(ctx, sitting, event)
}

// publishLifecycleChanged announces a lifecycle transition, in this order:
// the transition to the Sitting's managers, the same fact to its candidates,
// the board invalidation, and the Exam activity invalidation of every
// candidate. Nothing is published unless both facts can be built; after that
// a failed publication never stops the ones after it.
func (s *Service) publishLifecycleChanged(
	ctx context.Context,
	sitting *model.ExamSitting,
	transition store.ExamSittingLifecycleTransitionCode,
) error {
	managerEvent, err := realtime.NewExamSittingLifecycleChangedEvent(
		sitting.ExamID,
		sitting.ID,
		sitting.State,
		sitting.Revision,
		string(transition),
		sitting.ScheduledEndAt,
		sitting.UpdatedAt,
	)
	if err != nil {
		return err
	}

	candidateEvent, err := realtime.NewCandidateExamSittingLifecycleChangedEvent(
		sitting.ExamID,
		sitting.ID,
		sitting.State,
		sitting.Revision,
		string(transition),
		sitting.ScheduledEndAt,
		sitting.UpdatedAt,
	)
	if err != nil {
		return err
	}

	return errors.Join(
		s.deps.Publisher.PublishRealtimeEvent(ctx, managerEvent),
		s.deps.Publisher.PublishRealtimeEvent(ctx, candidateEvent),
		realtime.PublishSittingBoardChanged(ctx, s.deps.Publisher, sitting.ExamID, sitting.ID),
		realtime.PublishCandidateActivityChangedForSitting(ctx, s.deps.Publisher, s.deps.Persistence, sitting.ID),
	)
}

// publishWithInvalidations publishes one schedule fact, then the Sitting
// board invalidation, then the Exam activity invalidation of every candidate
// of the Sitting. A failed publication never stops the ones after it; the
// failures are returned joined in that order.
func (s *Service) publishWithInvalidations(
	ctx context.Context,
	sitting *model.ExamSitting,
	event realtime.RealtimeEvent,
) error {
	return errors.Join(
		s.deps.Publisher.PublishRealtimeEvent(ctx, event),
		realtime.PublishSittingBoardChanged(ctx, s.deps.Publisher, sitting.ExamID, sitting.ID),
		realtime.PublishCandidateActivityChangedForSitting(ctx, s.deps.Publisher, s.deps.Persistence, sitting.ID),
	)
}
