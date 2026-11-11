// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package review

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/sudosylabs/proctor/server/app/apperror"
	"github.com/sudosylabs/proctor/server/app/realtime"
	"github.com/sudosylabs/proctor/server/app/realtime/realtimetest"
	"github.com/sudosylabs/proctor/server/model"
	"github.com/sudosylabs/proctor/server/store"
)

func TestFinalizeAndReleaseUseDistinctAuthorizationAndEffects(t *testing.T) {
	t.Parallel()
	f := newReviewFixture(t)
	f.persistence.mode = "finalize"
	finalized, err := f.service.FinalizeExamIntegrityReview(context.Background(), f.invocation, TerminalCommand{SubmissionID: f.submissionID,
		ReviewID: f.reviewID, ExpectedReviewRevision: 2, IdempotencyKey: "test-key"})
	require.NoError(t, err, "finalized=%#v error=%v auth=%#v", finalized, err, f.authorizer)
	require.Equal(t, model.SubmissionReviewFinalized, finalized.Review.State, "finalized=%#v error=%v auth=%#v", finalized, err, f.authorizer)
	require.Equal(t, 1, f.authorizer.reviewCalls, "finalized=%#v error=%v auth=%#v", finalized, err, f.authorizer)
	// Contract: finalizing tells managers the Review was finalized, then
	// invalidates the Sitting board.
	finalizedCalls := reviewCalls(t, realtime.NewExamIntegrityReviewFinalizedEvent, finalized)
	require.Equal(t, finalizedCalls, f.publisher.Calls())
	wantFinalize, prepareErr := prepareTerminalIdempotency(f.invocation, store.ExamIntegrityReviewFinalizeOperation,
		"test-key", f.submissionID, f.reviewID, 2)
	require.NoError(t, prepareErr)
	require.Equal(t, wantFinalize, f.persistence.idempotency)
	f.persistence.mode = "release"
	released, err := f.service.ReleaseStudentExamResult(context.Background(), f.invocation, TerminalCommand{SubmissionID: f.submissionID,
		ReviewID: f.reviewID, ExpectedReviewRevision: 3, IdempotencyKey: "test-key"})
	require.NoError(t, err, "released=%#v error=%v auth=%#v", released, err, f.authorizer)
	require.Equal(t, model.SubmissionReviewReleased, released.Review.ReleaseState, "released=%#v error=%v auth=%#v", released, err, f.authorizer)
	require.Equal(t, 1, f.authorizer.releaseCalls, "released=%#v error=%v auth=%#v", released, err, f.authorizer)
	managerRelease, eventErr := realtime.NewExamStudentResultReleasedEvent(reviewFact(released))
	require.NoError(t, eventErr)
	candidateRelease, eventErr := realtime.NewCandidateStudentResultReleasedEvent(reviewFact(released))
	require.NoError(t, eventErr)
	activity, eventErr := realtime.NewCandidateExamActivityChangedEvent(released.Authorization.CandidateUserID)
	require.NoError(t, eventErr)
	board, eventErr := realtime.NewManagerSittingBoardChangedEvent(released.Authorization.ExamID, released.Authorization.SittingID)
	require.NoError(t, eventErr)
	// Contract: after the finalization's publications, the release reaches
	// managers before the candidate, then the candidate's Exam activity and
	// the Sitting board are invalidated.
	require.Equal(t, append(finalizedCalls,
		realtimetest.Call{Method: realtimetest.Publish, Event: managerRelease},
		realtimetest.Call{Method: realtimetest.Publish, Event: candidateRelease},
		realtimetest.Call{Method: realtimetest.Publish, Event: activity},
		realtimetest.Call{Method: realtimetest.Publish, Event: board},
	), f.publisher.Calls())
	wantRelease, prepareErr := prepareTerminalIdempotency(f.invocation, store.ExamIntegrityReviewReleaseOperation,
		"test-key", f.submissionID, f.reviewID, 3)
	require.NoError(t, prepareErr)
	require.Equal(t, wantRelease, f.persistence.idempotency)
	require.Equal(t, f.userID, f.mail.request.CandidateUserID, "mail=%#v release", f.mail.request)
	require.Equal(t, f.reviewID, f.mail.request.ReviewID, "mail=%#v release", f.mail.request)
	require.Equal(t, int64(3), f.mail.request.ExpectedReviewRevision, "mail=%#v release", f.mail.request)
	require.True(t, f.mail.request.ReleasedAt.Equal(f.at.Add(time.Minute)), "mail=%#v release", f.mail.request)
	require.NotNil(t, f.persistence.release, "mail=%#v release", f.mail.request)
	require.NotNil(t, f.persistence.release.Notice, "mail=%#v release", f.mail.request)
	require.Equal(t, int64(2), f.persistence.release.ExpectedRecipientRevision, "mail=%#v release", f.mail.request)
}

func TestReleasePreparationFailureIsAuditedAsAttempt(t *testing.T) {
	t.Parallel()
	f := newReviewFixture(t)
	f.persistence.mode = "release"
	f.persistence.prepareErr = store.NewErrConflict("submission_review", "integrity_review_state", nil)
	_, err := f.service.ReleaseStudentExamResult(context.Background(), f.invocation, TerminalCommand{SubmissionID: f.submissionID,
		ReviewID: f.reviewID, ExpectedReviewRevision: 3, IdempotencyKey: "test-key"})
	var fault *apperror.Error
	require.ErrorAs(t, err, &fault)
	require.Equal(t, "exam.integrity_review.state_conflict", fault.Code())
	// The release-time reservation is a durable write, so its attempt must be
	// audited before it runs and completed as failed when it is refused.
	require.Equal(t, 1, f.auditor.begun, "audit=%#v mail=%d release", f.auditor, f.mail.calls)
	require.Equal(t, fault.Code(), f.auditor.failedCode, "audit=%#v mail=%d release", f.auditor, f.mail.calls)
	require.Equal(t, 0, f.mail.calls, "audit=%#v mail=%d release", f.auditor, f.mail.calls)
	require.Nil(t, f.persistence.release, "audit=%#v mail=%d release", f.auditor, f.mail.calls)
}

func TestReleaseReplayReturnsRetainedResultWithoutPreparingMail(t *testing.T) {
	t.Parallel()
	f := newReviewFixture(t)
	f.persistence.mode, f.persistence.replayed = "release", true
	released, err := f.service.ReleaseStudentExamResult(context.Background(), f.invocation, TerminalCommand{SubmissionID: f.submissionID,
		ReviewID: f.reviewID, ExpectedReviewRevision: 3, IdempotencyKey: "test-key"})
	require.NoError(t, err, "released=%#v error=%v mail=%#v release", released, err, f.mail)
	require.True(t, released.Replayed, "released=%#v error=%v mail=%#v release", released, err, f.mail)
	require.Empty(t, f.publisher.Calls(), "a replayed release republished")
	require.Equal(t, 0, f.mail.calls, "released=%#v error=%v mail=%#v release", released, err, f.mail)
	require.NotNil(t, f.persistence.release, "released=%#v error=%v mail=%#v release", released, err, f.mail)
	require.Nil(t, f.persistence.release.Notice, "released=%#v error=%v mail=%#v release", released, err, f.mail)
}
