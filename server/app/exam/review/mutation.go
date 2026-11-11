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

// Result is the committed state a Review mutation returns: the Review, the
// Decision when the mutation saved one, and the authorization projection the
// Store resolved. Replayed reports a retained idempotent outcome, for which
// no effect is published.
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

	// The Candidate was denied on this projection, so it must be the
	// requested Submission's, whatever the Store returned.
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
