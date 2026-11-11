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
)

// reviewEventFact is the bounded fact every Review event carries: the
// identities, the Review's state, revision and release state, and when it
// changed. Notes, remarks and rationale never leave the Review.
func reviewEventFact(result Result) realtime.ExamIntegrityReviewEventFact {
	return realtime.ExamIntegrityReviewEventFact{
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

// publishSittingBoardChanged tells the managers of the reviewed Submission's
// Sitting to refresh its board.
func (s *Service) publishSittingBoardChanged(ctx context.Context, result Result) error {
	return realtime.PublishSittingBoardChanged(
		ctx,
		s.deps.Publisher,
		result.Authorization.ExamID,
		result.Authorization.SittingID,
	)
}

// publishReviewChanged tells managers that a draft Review changed, then
// invalidates the Sitting board.
func (s *Service) publishReviewChanged(ctx context.Context, result Result) error {
	event, err := realtime.NewExamIntegrityReviewChangedEvent(reviewEventFact(result))
	if err != nil {
		return err
	}

	return errors.Join(s.deps.Publisher.PublishRealtimeEvent(ctx, event), s.publishSittingBoardChanged(ctx, result))
}

// publishReviewFinalized tells managers that a Review was finalized, then
// invalidates the Sitting board.
func (s *Service) publishReviewFinalized(ctx context.Context, result Result) error {
	event, err := realtime.NewExamIntegrityReviewFinalizedEvent(reviewEventFact(result))
	if err != nil {
		return err
	}

	return errors.Join(s.deps.Publisher.PublishRealtimeEvent(ctx, event), s.publishSittingBoardChanged(ctx, result))
}

// publishResultReleased announces a released student result, in this order:
// the release to managers, the release to the candidate, the candidate's Exam
// activity invalidation, and the Sitting board invalidation. Nothing is
// published unless all three events can be built; after that a failed
// publication never stops the ones after it.
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
		s.deps.Publisher.PublishRealtimeEvent(ctx, manager),
		s.deps.Publisher.PublishRealtimeEvent(ctx, candidate),
		s.deps.Publisher.PublishRealtimeEvent(ctx, activity),
		s.publishSittingBoardChanged(ctx, result),
	)
}
