// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package review

import (
	"context"
	"errors"

	"github.com/sudosylabs/proctor/server/app/realtime"
	"github.com/sudosylabs/proctor/server/model"
)

// reviewEventFact deliberately leaves out notes, remarks and rationale; they
// never leave the review.
func reviewEventFact(result Result) realtime.ExamIntegrityReviewEventFact {
	return realtime.ExamIntegrityReviewEventFact{
		ExamID:       result.Authorization.ExamID,
		SittingID:    result.Authorization.SittingID,
		SubmissionID: result.Authorization.SubmissionID,
		AttemptID:    result.Authorization.AttemptID,
		CandidateID:  result.Authorization.CandidateUserID,
		ReviewID:     result.Review.ID,
		State:        result.Review.State,
		Revision:     result.Review.Revision,
		ReleaseState: result.Review.ReleaseState,
		ChangedAt:    result.Review.UpdatedAt,
	}
}

// publishSittingBoardChanged also refreshes the sitting's submission list,
// which shows each review's state; a bulk release publishes that once for its
// batch instead.
func (s *Service) publishSittingBoardChanged(ctx context.Context, result Result) error {
	if result.inBatch {
		return nil
	}
	board := realtime.PublishSittingBoardChanged(
		ctx,
		s.publisher,
		result.Authorization.ExamID,
		result.Authorization.SittingID,
	)
	return errors.Join(board, s.publishSittingSubmissionsChanged(ctx, result.Authorization.ExamID, result.Authorization.SittingID))
}

func (s *Service) publishSittingSubmissionsChanged(ctx context.Context, examID model.ExamID, sittingID model.ExamSittingID) error {
	event, err := realtime.NewSittingSubmissionsChangedEvent(examID, sittingID)
	if err != nil {
		return err
	}
	return s.publisher.PublishExamChange(ctx, event, examID)
}

func (s *Service) publishReviewChanged(ctx context.Context, result Result) error {
	event, err := realtime.NewExamIntegrityReviewChangedEvent(reviewEventFact(result))
	if err != nil {
		return err
	}

	return errors.Join(s.publisher.Publish(ctx, event), s.publishSittingBoardChanged(ctx, result))
}

func (s *Service) publishReviewFinalized(ctx context.Context, result Result) error {
	event, err := realtime.NewExamIntegrityReviewFinalizedEvent(reviewEventFact(result))
	if err != nil {
		return err
	}

	return errors.Join(s.publisher.Publish(ctx, event), s.publishSittingBoardChanged(ctx, result))
}

// publishResultReleased publishes to managers, then the candidate, then the
// candidate's activity and the board invalidations. Nothing is published unless
// all three events build; after that a failed publication never stops the ones
// after it.
func (s *Service) publishResultReleased(ctx context.Context, result Result) error {
	fact := reviewEventFact(result)
	manager, err := realtime.NewExamStudentResultReleasedEvent(fact)
	if err != nil {
		return err
	}

	candidate, err := realtime.NewCandidateStudentResultReleasedEvent(fact)
	if err != nil {
		return err
	}

	activity, err := realtime.NewCandidateExamActivityChangedEvent(result.Authorization.CandidateUserID)
	if err != nil {
		return err
	}

	return errors.Join(
		s.publisher.Publish(ctx, manager),
		s.publisher.Publish(ctx, candidate),
		s.publisher.Publish(ctx, activity),
		s.publishSittingBoardChanged(ctx, result),
	)
}

// publishUnlessReplayed reports a failed publication rather than returning it,
// so the command still succeeds.
func (s *Service) publishUnlessReplayed(
	ctx context.Context,
	operation string,
	result Result,
	replayed bool,
	deliver func(context.Context, Result) error,
) {
	if replayed {
		return
	}
	if err := deliver(ctx, result); err != nil {
		s.publisher.Report(ctx, operation, err)
	}
}
