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

	"github.com/sudosylabs/proctor/server/app/apperror"
	"github.com/sudosylabs/proctor/server/app/invocation"
	"github.com/sudosylabs/proctor/server/model"
	"github.com/sudosylabs/proctor/server/store"
)

// Result is the committed state of a review mutation. Decision is nil unless
// the mutation saved one; Replayed marks a retained idempotent outcome, for
// which no event is published.
type Result struct {
	Authorization store.ExamIntegrityReviewAuthorization
	Review        *model.SubmissionReview
	Decision      *model.IntegrityReviewDecision
	Replayed      bool
}

func (s *Service) authorizeReviewMutation(
	ctx context.Context,
	inv invocation.Invocation,
	submissionID model.SubmissionID,
) (bool, *store.ExamIntegrityReviewAuthorization, error) {
	management, err := s.authorizer.AuthorizeSubmissionReview(ctx, inv, submissionID)
	if err != nil {
		return false, nil, err
	}

	return s.resolveMutationAuthorization(ctx, submissionID, management.Override)
}

func (s *Service) authorizeReleaseMutation(
	ctx context.Context,
	inv invocation.Invocation,
	submissionID model.SubmissionID,
) (bool, *store.ExamIntegrityReviewAuthorization, error) {
	management, err := s.authorizer.AuthorizeSubmissionRelease(ctx, inv, submissionID)
	if err != nil {
		return false, nil, err
	}

	return s.resolveMutationAuthorization(ctx, submissionID, management.Override)
}

func (s *Service) resolveMutationAuthorization(
	ctx context.Context,
	submissionID model.SubmissionID,
	override bool,
) (bool, *store.ExamIntegrityReviewAuthorization, error) {
	authorization, err := s.reviews.Resolve(ctx, submissionID)
	if err != nil {
		return false, nil, storeErrors.Translate(err)
	}

	// The self-review denial ran on an earlier resolve, so this projection must
	// be the requested submission's too, whatever the store returned.
	if !authorization.ConsistentWith(submissionID) {
		return false, nil, apperror.New("exam.integrity_review.unavailable").Wrap(errors.New("inconsistent Review authorization"))
	}

	return override, authorization, nil
}

func projectMutation(stored *store.ExamIntegrityReviewMutationResult) (Result, error) {
	review := *stored.Review
	result := Result{Authorization: stored.Authorization, Review: &review, Replayed: stored.Replayed}
	if stored.Decision != nil {
		decision := *stored.Decision
		result.Decision = &decision
	}

	return result, nil
}
