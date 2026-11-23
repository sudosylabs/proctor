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

	"github.com/stretchr/testify/require"

	"github.com/sudosylabs/proctor/server/app/apperror"
	"github.com/sudosylabs/proctor/server/app/invocation"
	"github.com/sudosylabs/proctor/server/model"
	"github.com/sudosylabs/proctor/server/store"
)

func sittingSubmissionQuery(f *reviewFixture) SubmissionListQuery {
	return SubmissionListQuery{ExamID: f.examID, SittingID: f.sittingID, Sort: store.SubmissionListBySubmitted, Limit: 10}
}

func TestSittingSubmissionsShowReviewsOnlyWithReviewAuthority(t *testing.T) {
	t.Parallel()
	reviewer := newReviewFixture(t)
	page, err := reviewer.service.ListSittingSubmissions(context.Background(), reviewer.inv, sittingSubmissionQuery(reviewer))
	require.NoError(t, err)
	require.True(t, page.ReviewVisible)
	require.NotNil(t, page.Counts, "the first page is counted")
	listed := reviewer.persistence.lists[0]
	require.True(t, listed.ReviewVisible)
	require.True(t, listed.IncludeCounts)
	require.Equal(t, reviewer.userID, listed.ExcludeCandidateUserID, "the caller's own Submission is never listed")
	require.Equal(t, []string{"success"}, reviewer.decisions(model.ActionSubmissionView))
	require.Equal(t, []string{"success"}, reviewer.decisions(model.ActionSubmissionReview))

	viewer := newReviewFixtureWith(t, model.ActionSubmissionView)
	page, err = viewer.service.ListSittingSubmissions(context.Background(), viewer.inv, sittingSubmissionQuery(viewer))
	require.NoError(t, err)
	require.False(t, page.ReviewVisible, "a viewer who may not review sees no reviews")
	require.False(t, viewer.persistence.lists[0].ReviewVisible)

	filtered := sittingSubmissionQuery(viewer)
	filtered.Filter.ReviewStates = []store.SubmissionListReviewState{store.SubmissionListReviewDraft}
	_, err = viewer.service.ListSittingSubmissions(context.Background(), viewer.inv, filtered)
	require.Error(t, err, "filtering by review would reveal what the viewer may not see")
	require.Len(t, viewer.persistence.lists, 1, "a refused filter reads nothing")
}

func TestSittingSubmissionsRefuseAnOutsiderAndAnotherExamsSitting(t *testing.T) {
	t.Parallel()
	f := newReviewFixture(t)
	outsider := invocation.New(model.Principal{UserID: model.NewUserID(), SessionID: model.NewSessionID(),
		CredentialID: model.PrincipalCredentialID(model.NewID()), CredentialType: model.CredentialSessionAccess,
		AuthenticationMethod: "password", AuthenticationStrength: model.AuthenticationSingleFactor,
		ClientType: model.SessionClientWeb, AuthenticatedAt: f.at}, model.RequestMetadata{})
	_, err := f.service.ListSittingSubmissions(context.Background(), outsider, sittingSubmissionQuery(f))
	require.True(t, apperror.Is(err, "resource.not_found"), "error = %v", err)
	require.Empty(t, f.persistence.lists, "an outsider reads nothing")

	other := sittingSubmissionQuery(f)
	other.ExamID = model.NewExamID()
	_, err = f.service.ListSittingSubmissions(context.Background(), f.inv, other)
	require.Error(t, err)
	require.Empty(t, f.persistence.lists, "a sitting named under another exam is not listed")
}

func TestBulkReleaseGivesEachSubmissionItsOutcome(t *testing.T) {
	t.Parallel()
	f := newReviewFixture(t)
	finalized := &store.SubmissionListReview{ID: f.reviewID, State: model.SubmissionReviewFinalized,
		ReleaseState: model.SubmissionReviewWithheld, Revision: 2, FinalizedByUserID: f.userID}
	f.persistence.listed = []store.SubmissionListItem{{
		SubmissionID: f.submissionID, ExamID: f.examID, SittingID: f.sittingID, AttemptID: f.attemptID,
		ReviewVisible: true, Review: finalized,
	}}
	foreign := model.NewSubmissionID()
	result, err := f.service.ReleaseSittingResults(context.Background(), f.inv, ResultReleaseBatchCommand{
		ExamID: f.examID, SittingID: f.sittingID, SubmissionIDs: []model.SubmissionID{f.submissionID, foreign},
		IdempotencyKey: "release-all",
	})
	require.NoError(t, err)
	require.Equal(t, []ResultReleaseBatchItem{
		{SubmissionID: f.submissionID, Outcome: ResultReleaseReleased},
		{SubmissionID: foreign, Outcome: ResultReleaseDenied},
	}, result.Items)
	require.Len(t, f.persistence.releases, 1, "only the finalized Submission is released")
	require.Equal(t, int64(2), f.persistence.releases[0].ExpectedRevision)
	wantKey, err := prepareTerminalIdempotency(f.inv, store.ExamIntegrityReviewReleaseOperation,
		releaseBatchItemKey("release-all", f.submissionID), f.submissionID, f.reviewID, 2)
	require.NoError(t, err)
	require.Equal(t, wantKey, f.persistence.idempotency, "each item's key derives from the batch key")

	operations := []string{}
	for _, attempt := range f.control.Audits.Attempts() {
		operations = append(operations, attempt.Operation())
	}
	require.ElementsMatch(t, []string{ResultReleaseBatchOperation, store.ExamIntegrityReviewReleaseOperation}, operations,
		"one audit for the batch and one for the released Submission")
	submissions, board := 0, 0
	for _, call := range f.publisher.Calls() {
		switch call.Event.Name {
		case "exam_sitting.submissions.changed":
			submissions++
		case "manager.sitting_board.changed":
			board++
		}
	}
	require.Equal(t, 1, submissions, "the batch refreshes the submission list once")
	require.Equal(t, 1, board, "the batch refreshes the sitting board once")
}

func TestBulkReleaseReplayKeepsTheOriginalFinalizedSelection(t *testing.T) {
	t.Parallel()
	f := newReviewFixture(t)
	finalized := &store.SubmissionListReview{ID: f.reviewID, State: model.SubmissionReviewFinalized,
		ReleaseState: model.SubmissionReviewWithheld, Revision: 2, FinalizedByUserID: f.userID}
	original := store.SubmissionListItem{
		SubmissionID: f.submissionID, ExamID: f.examID, SittingID: f.sittingID, AttemptID: f.attemptID,
		ReviewVisible: true, Review: finalized,
	}
	f.persistence.listed = []store.SubmissionListItem{original}
	command := ResultReleaseBatchCommand{
		ExamID: f.examID, SittingID: f.sittingID, AllFinalized: true, IdempotencyKey: "original-selection",
	}
	first, err := f.service.ReleaseSittingResults(t.Context(), f.inv, command)
	require.NoError(t, err)
	require.Len(t, first.Items, 1)
	require.Equal(t, ResultReleaseReleased, first.Items[0].Outcome)
	require.Len(t, f.persistence.releases, 1)

	changed := original
	changed.SubmissionID = model.NewSubmissionID()
	f.persistence.listed = []store.SubmissionListItem{changed}
	published := f.publisher.Calls()
	replayed, err := f.service.ReleaseSittingResults(t.Context(), f.inv, command)
	require.NoError(t, err)
	require.Equal(t, first, replayed)
	require.Len(t, f.persistence.releases, 1, "replay skips item mutations")
	require.Len(t, f.persistence.lists, 1, "replay skips reading current submission rows")
	require.Len(t, f.control.Audits.Attempts(), 2, "replay writes no additional mutation audit")
	require.Equal(t, published, f.publisher.Calls(), "replay publishes no events")

	command.AllFinalized, command.SubmissionIDs = false, []model.SubmissionID{changed.SubmissionID}
	_, err = f.service.ReleaseSittingResults(t.Context(), f.inv, command)
	require.True(t, apperror.Is(err, "idempotency.conflict"), "error = %v", err)
	require.Len(t, f.control.Audits.Attempts(), 2)
}

func TestBulkReleaseReportsDraftsAndEarlierReleases(t *testing.T) {
	t.Parallel()
	for name, test := range map[string]struct {
		review *store.SubmissionListReview
		want   ResultReleaseOutcome
	}{
		"no review": {want: ResultReleaseNotFinalized},
		"draft":     {review: &store.SubmissionListReview{State: model.SubmissionReviewDraft, ReleaseState: model.SubmissionReviewWithheld, Revision: 1}, want: ResultReleaseNotFinalized},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			f := newReviewFixture(t)
			if test.review != nil {
				test.review.ID = f.reviewID
			}
			f.persistence.listed = []store.SubmissionListItem{{SubmissionID: f.submissionID, ExamID: f.examID,
				SittingID: f.sittingID, ReviewVisible: true, Review: test.review}}
			result, err := f.service.ReleaseSittingResults(context.Background(), f.inv, ResultReleaseBatchCommand{
				ExamID: f.examID, SittingID: f.sittingID, SubmissionIDs: []model.SubmissionID{f.submissionID},
				IdempotencyKey: "release-" + name,
			})
			require.NoError(t, err)
			require.Equal(t, test.want, result.Items[0].Outcome)
			require.Empty(t, f.persistence.releases)
		})
	}
}

func TestBulkReleaseNeedsReleaseAuthorityOnTheSitting(t *testing.T) {
	t.Parallel()
	f := newReviewFixtureWith(t, model.ActionSubmissionView, model.ActionSubmissionReview)
	_, err := f.service.ReleaseSittingResults(context.Background(), f.inv, ResultReleaseBatchCommand{
		ExamID: f.examID, SittingID: f.sittingID, AllFinalized: true, IdempotencyKey: "release-denied",
	})
	require.True(t, apperror.Is(err, "resource.not_found"), "error = %v", err)
	require.Empty(t, f.persistence.lists, "nothing is read for a caller who may not release")

	_, err = f.service.ReleaseSittingResults(context.Background(), f.inv, ResultReleaseBatchCommand{
		ExamID: f.examID, SittingID: f.sittingID, AllFinalized: true, SubmissionIDs: []model.SubmissionID{f.submissionID},
		IdempotencyKey: "release-both",
	})
	require.True(t, apperror.Is(err, "exam.integrity_review.invalid"), "naming Submissions and all_finalized together: %v", err)
}
