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
	"github.com/sudosylabs/proctor/server/app/mail"
	"github.com/sudosylabs/proctor/server/app/realtime"
	"github.com/sudosylabs/proctor/server/app/realtime/realtimetest"
	"github.com/sudosylabs/proctor/server/model"
	"github.com/sudosylabs/proctor/server/store"
)

func TestFinalizeAndReleaseUseDistinctAuthorizationAndEffects(t *testing.T) {
	t.Parallel()
	f := newReviewFixture(t)
	f.persistence.mode = "finalize"
	finalized, err := f.service.FinalizeExamIntegrityReview(context.Background(), f.inv, TerminalCommand{SubmissionID: f.submissionID,
		ReviewID: f.reviewID, ExpectedReviewRevision: 2, IdempotencyKey: "test-key"})
	require.NoError(t, err, "finalized=%#v error=%v", finalized, err)
	require.Equal(t, model.SubmissionReviewFinalized, finalized.Review.State, "finalized=%#v error=%v", finalized, err)
	// Finalizing is a review decision, never a release one.
	require.Equal(t, []string{"success"}, f.decisions(model.ActionSubmissionReview), "finalization decisions")
	require.Empty(t, f.decisions(model.ActionSubmissionRelease), "finalization decisions")
	// Contract: finalizing tells managers the Review was finalized, then
	// invalidates the Sitting board.
	finalizedCalls := reviewCalls(t, realtime.NewExamIntegrityReviewFinalizedEvent, finalized)
	require.Equal(t, finalizedCalls, f.publisher.Calls())
	wantFinalize, prepareErr := prepareTerminalIdempotency(f.inv, store.ExamIntegrityReviewFinalizeOperation,
		"test-key", f.submissionID, f.reviewID, 2)
	require.NoError(t, prepareErr)
	require.Equal(t, wantFinalize, f.persistence.idempotency)
	f.persistence.mode = "release"
	released, err := f.service.ReleaseStudentExamResult(context.Background(), f.inv, TerminalCommand{SubmissionID: f.submissionID,
		ReviewID: f.reviewID, ExpectedReviewRevision: 3, IdempotencyKey: "test-key"})
	require.NoError(t, err, "released=%#v error=%v", released, err)
	require.Equal(t, model.SubmissionReviewReleased, released.Review.ReleaseState, "released=%#v error=%v", released, err)
	require.Equal(t, []string{"success"}, f.decisions(model.ActionSubmissionRelease), "release decisions")
	require.Equal(t, []string{"success"}, f.decisions(model.ActionSubmissionReview), "a release asks no further review decision")
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
	wantRelease, prepareErr := prepareTerminalIdempotency(f.inv, store.ExamIntegrityReviewReleaseOperation,
		"test-key", f.submissionID, f.reviewID, 3)
	require.NoError(t, prepareErr)
	require.Equal(t, wantRelease, f.persistence.idempotency)
	// The notice the Store commits is the real one prepared for the
	// candidate, for this Review at the expected revision, at release time.
	require.NotNil(t, f.persistence.release, "release")
	notice := f.persistence.release.Notice
	require.NotNil(t, notice, "release")
	wantOccurrence, occurrenceErr := model.ResultReleaseOccurrenceID(f.reviewID, 3)
	require.NoError(t, occurrenceErr)
	require.Equal(t, wantOccurrence, notice.Occurrence.ID, "notice=%#v", notice)
	require.Equal(t, model.MailTemplateExamResultReleased, notice.Delivery.TemplateKey, "notice=%#v", notice)
	require.Equal(t, f.candidateID, notice.Delivery.TargetUserID, "notice=%#v", notice)
	require.True(t, notice.Occurrence.CreatedAt.Equal(f.at.Add(time.Minute)), "notice=%#v", notice)
	require.Equal(t, 1, f.renders.Count(), "rendered notices")
	opened, openErr := mail.OpenDelivery(reviewMailSealer(t), notice.Delivery)
	require.NoError(t, openErr)
	require.Contains(t, opened.Text, "Exam: "+reviewExamTitle, "rendered notice")
	// f.at plus a minute, as the notice writes it.
	require.Contains(t, opened.Text, "Released: Saturday 15 August 2026, 12:01 UTC", "rendered notice")
	require.Equal(t, int64(reviewCandidateRevision), f.persistence.release.Recipient.Revision, "release")
}

func TestReleasePreparationFailureIsAuditedAsAttempt(t *testing.T) {
	t.Parallel()
	f := newReviewFixture(t)
	f.persistence.mode = "release"
	f.persistence.prepareErr = store.NewErrConflict("submission_review", "integrity_review_state", nil)
	_, err := f.service.ReleaseStudentExamResult(context.Background(), f.inv, TerminalCommand{SubmissionID: f.submissionID,
		ReviewID: f.reviewID, ExpectedReviewRevision: 3, IdempotencyKey: "test-key"})
	var fault *apperror.Error
	require.ErrorAs(t, err, &fault)
	require.Equal(t, "exam.integrity_review.state_conflict", fault.Code())
	// The release-time reservation is a durable write, so its attempt must be
	// audited before it runs and completed as failed when it is refused.
	attempts := f.control.Audits.Attempts()
	require.Len(t, attempts, 1, "audit=%#v release", attempts)
	require.Equal(t, model.AuditStatusFail, attempts[0].Status, "audit=%#v release", attempts[0])
	require.Equal(t, fault.Code(), attempts[0].ErrorCode, "audit=%#v release", attempts[0])
	require.Zero(t, f.renders.Count(), "a refused release prepared a notice")
	require.Nil(t, f.persistence.release, "audit=%#v release", attempts)
}

func TestReleaseReplayReturnsRetainedResultWithoutPreparingMail(t *testing.T) {
	t.Parallel()
	f := newReviewFixture(t)
	f.persistence.mode, f.persistence.replayed = "release", true
	released, err := f.service.ReleaseStudentExamResult(context.Background(), f.inv, TerminalCommand{SubmissionID: f.submissionID,
		ReviewID: f.reviewID, ExpectedReviewRevision: 3, IdempotencyKey: "test-key"})
	require.NoError(t, err, "released=%#v error=%v", released, err)
	require.True(t, released.Replayed, "released=%#v error=%v", released, err)
	require.Empty(t, f.publisher.Calls(), "a replayed release republished")
	require.Zero(t, f.renders.Count(), "a replayed release prepared a notice")
	require.NotNil(t, f.persistence.release, "released=%#v error=%v", released, err)
	require.Nil(t, f.persistence.release.Notice, "released=%#v error=%v", released, err)
}
