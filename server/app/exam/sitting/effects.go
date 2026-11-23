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

// at is the instant the schedule was committed.
func (s *Service) publishScheduled(ctx context.Context, sitting *model.ExamSitting, at time.Time) error {
	event, err := realtime.NewExamSittingScheduledEvent(sitting.ExamID, sitting.ID, sitting.State, sitting.Revision, at)
	if err != nil {
		return err
	}

	return s.publishWithInvalidations(ctx, sitting, event)
}

func (s *Service) publishScheduleUpdated(ctx context.Context, sitting *model.ExamSitting, at time.Time) error {
	event, err := realtime.NewExamSittingScheduleUpdatedEvent(sitting.ExamID, sitting.ID, sitting.State, sitting.Revision, at)
	if err != nil {
		return err
	}

	return s.publishWithInvalidations(ctx, sitting, event)
}

func (s *Service) publishCanceled(ctx context.Context, sitting *model.ExamSitting, at time.Time) error {
	event, err := realtime.NewExamSittingCanceledEvent(sitting.ExamID, sitting.ID, sitting.State, sitting.Revision, at)
	if err != nil {
		return err
	}

	return s.publishWithInvalidations(ctx, sitting, event)
}

// publishLifecycleChanged publishes to managers, then candidates, then the
// board and candidate activity invalidations. Nothing is published unless both
// events build; after that a failed publication never stops the ones after it.
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
		s.publisher.Publish(ctx, managerEvent),
		s.publisher.Publish(ctx, candidateEvent),
		realtime.PublishSittingBoardChanged(ctx, s.publisher, sitting.ExamID, sitting.ID),
		realtime.PublishCandidateActivityChangedForSitting(ctx, s.publisher, s.sittings, sitting.ID),
	)
}

// publishWithInvalidations publishes event, then the board and candidate
// activity invalidations. A failed publication never stops the ones after it;
// failures come back joined in that order.
func (s *Service) publishWithInvalidations(
	ctx context.Context,
	sitting *model.ExamSitting,
	event realtime.Event,
) error {
	return errors.Join(
		s.publisher.Publish(ctx, event),
		realtime.PublishSittingBoardChanged(ctx, s.publisher, sitting.ExamID, sitting.ID),
		realtime.PublishCandidateActivityChangedForSitting(ctx, s.publisher, s.sittings, sitting.ID),
	)
}

// publishLifecycle skips unchanged and replayed results. A failed publication
// is reported, not returned, so the command or job still succeeds.
func (s *Service) publishLifecycle(ctx context.Context, result *store.ExamSittingLifecycleResult, operation string) {
	if result == nil || !result.Changed || result.Replayed || result.Value == nil || result.Value.Sitting == nil {
		return
	}

	if err := s.publishLifecycleChanged(ctx, result.Value.Sitting, result.Transition); err != nil {
		s.publisher.Report(ctx, "exam_sitting_"+operation, err)
	}
	// The administration count includes open, paused and closing sittings.
	// Only entering or leaving that set changes what policy managers read.
	switch result.Transition {
	case store.ExamSittingTransitionOpened, store.ExamSittingTransitionClosedNoAttempts, store.ExamSittingTransitionSealingCompleted:
		if err := s.publishOpenSittingsChanged(ctx); err != nil {
			s.publisher.Report(ctx, "desktop_compatibility_policy.changed", err)
		}
	}
}

func (s *Service) publishOpenSittingsChanged(ctx context.Context) error {
	institution, err := s.authorizer.Institution(ctx)
	if err != nil {
		return err
	}
	event, err := realtime.NewDesktopCompatibilityPolicyChangedEvent(institution.ID)
	if err != nil {
		return err
	}
	return s.publisher.Publish(ctx, event)
}
