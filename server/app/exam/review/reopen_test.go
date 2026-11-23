// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package review

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/sudosylabs/proctor/server/app/apperror"
	"github.com/sudosylabs/proctor/server/app/realtime"
	"github.com/sudosylabs/proctor/server/model"
	"github.com/sudosylabs/proctor/server/store"
)

func TestReopenReturnsAFinalizedReviewToDraft(t *testing.T) {
	t.Parallel()
	f := newReviewFixture(t)
	reopened, err := f.service.ReopenExamIntegrityReview(context.Background(), f.inv, ReopenCommand{
		SubmissionID: f.submissionID, ReviewID: f.reviewID, ExpectedReviewRevision: 3,
		Reason: "  The clip needs another look.  ", IdempotencyKey: "reopen-key",
	})
	require.NoError(t, err)
	require.Equal(t, model.SubmissionReviewDraft, reopened.Review.State)
	require.Equal(t, []string{"success"}, f.decisions(model.ActionSubmissionReview), "reopening is a review decision")
	require.Empty(t, f.decisions(model.ActionSubmissionRelease))
	require.Equal(t, "The clip needs another look.", f.persistence.reopen.Reason, "the reason is trimmed")
	require.Equal(t, f.userID, f.persistence.reopen.ActorUserID)
	require.Equal(t, store.ExamIntegrityReviewReopenOperation, f.persistence.idempotency.Operation)
	// Contract: reopening tells managers the Review changed, then
	// invalidates the Sitting board.
	require.Equal(t, reviewCalls(t, realtime.NewExamIntegrityReviewChangedEvent, reopened), f.publisher.Calls())
	audit := f.control.Audits.Events()
	require.NotEmpty(t, audit)
	encoded, err := json.Marshal(audit)
	require.NoError(t, err)
	require.NotContains(t, string(encoded), "another look", "the private reason never reaches the audit")
}

func TestReopenRefusesAReleasedResult(t *testing.T) {
	t.Parallel()
	f := newReviewFixture(t)
	f.persistence.reopenErr = store.NewErrConflict("submission_review", "integrity_review_released", nil)
	_, err := f.service.ReopenExamIntegrityReview(context.Background(), f.inv, ReopenCommand{
		SubmissionID: f.submissionID, ReviewID: f.reviewID, ExpectedReviewRevision: 4,
		Reason: "Too late.", IdempotencyKey: "reopen-key",
	})
	var fault *apperror.Error
	require.ErrorAs(t, err, &fault)
	require.Equal(t, "exam.integrity_review.result_released", fault.Code())
	require.Empty(t, f.publisher.Calls(), "a refused reopen publishes nothing")
}

func TestReopenNeedsAReason(t *testing.T) {
	t.Parallel()
	f := newReviewFixture(t)
	_, err := f.service.ReopenExamIntegrityReview(context.Background(), f.inv, ReopenCommand{
		SubmissionID: f.submissionID, ReviewID: f.reviewID, ExpectedReviewRevision: 3, Reason: "   ", IdempotencyKey: "reopen-key",
	})
	var fault *apperror.Error
	require.ErrorAs(t, err, &fault)
	require.Equal(t, "exam.integrity_review.invalid", fault.Code())
	require.Nil(t, f.persistence.reopen, "an invalid reopen never reaches the store")
}

func (f *reviewStoreFake) Reopen(_ context.Context, input *store.ExamIntegrityReviewReopen, command *store.CommandIdempotency) (*store.ExamIntegrityReviewMutationResult, error) {
	f.reopen, f.idempotency = input, command
	if f.reopenErr != nil {
		return nil, f.reopenErr
	}
	result, _ := f.Finalize(context.Background(), nil, nil)
	_ = result.Review.Reopen(3, f.f.at.Add(time.Minute))
	return result, nil
}
