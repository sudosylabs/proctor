// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package storetest

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/sudosylabs/proctor/server/model"
	"github.com/sudosylabs/proctor/server/store"
)

// TestExamIntegrityReviewReopen verifies that a finalized review returns to
// draft with its decisions, can be finalized again, and can't be reopened
// once its result is released.
func TestExamIntegrityReviewReopen(t *testing.T, ss store.Store) {
	t.Helper()
	ctx := context.Background()
	reviews := ss.ExamIntegrityReview()
	fixture, _, flagged, sealed, _ := newIntegrityReviewFixture(t, ctx, ss, "review-reopen")
	submissionID := sealed.Receipt.SubmissionID
	reviewID := model.NewSubmissionReviewID()
	audit := func(action model.Action) model.AuditEventID {
		return saveIntegrityReviewAudit(t, ctx, ss, fixture, submissionID, action).ID
	}

	decided, err := reviews.SaveDecision(ctx, &store.ExamIntegrityReviewDecisionMutation{
		SubmissionID: submissionID, ReviewID: reviewID, DecisionID: model.NewIntegrityReviewDecisionID(), FlagID: flagged.Flag.ID,
		ActorUserID: fixture.manager.ID, Outcome: model.IntegrityReviewConfirmed, PrivateRationale: "the evidence agrees",
		ChangedAt: model.NowUTC(), AuditEventID: audit(model.ActionSubmissionReview), AuditAt: model.NowUTC(),
	}, examCommand(fixture.manager.ID, store.ExamIntegrityReviewDecisionOperation, "reopen-decision", "reopen-decision"))
	require.NoError(t, err)
	finalize := func(revision int64, key string) *store.ExamIntegrityReviewMutationResult {
		t.Helper()
		finalized, finalizeErr := reviews.Finalize(ctx, &store.ExamIntegrityReviewFinalize{
			SubmissionID: submissionID, ReviewID: reviewID, ActorUserID: fixture.manager.ID, ExpectedRevision: revision,
			ChangedAt: model.NowUTC(), AuditEventID: audit(model.ActionSubmissionReview), AuditAt: model.NowUTC(),
		}, examCommand(fixture.manager.ID, store.ExamIntegrityReviewFinalizeOperation, key, key))
		require.NoError(t, finalizeErr)
		return finalized
	}
	var lastReopenAudit model.AuditEventID
	reopen := func(revision int64, key string) (*store.ExamIntegrityReviewMutationResult, error) {
		lastReopenAudit = audit(model.ActionSubmissionReview)
		return reviews.Reopen(ctx, &store.ExamIntegrityReviewReopen{
			SubmissionID: submissionID, ReviewID: reviewID, ActorUserID: fixture.manager.ID, ExpectedRevision: revision,
			Reason: "the clip needs another look", ChangedAt: model.NowUTC(),
			AuditEventID: lastReopenAudit, AuditAt: model.NowUTC(),
		}, examCommand(fixture.manager.ID, store.ExamIntegrityReviewReopenOperation, key, key))
	}

	_, err = reopen(decided.Review.Revision, "reopen-draft")
	var conflict *store.ErrConflict
	require.ErrorAs(t, err, &conflict, "a draft review can't be reopened")

	finalized := finalize(decided.Review.Revision, "reopen-finalize")
	_, err = reopen(finalized.Review.Revision-1, "reopen-stale")
	require.ErrorAs(t, err, &conflict)
	require.Equal(t, "integrity_review_revision", conflict.Constraint, "a stale reopen is fenced")

	reopened, err := reopen(finalized.Review.Revision, "reopen")
	require.NoError(t, err)
	require.Equal(t, model.SubmissionReviewDraft, reopened.Review.State)
	require.Equal(t, model.SubmissionReviewWithheld, reopened.Review.ReleaseState)
	require.Equal(t, finalized.Review.Revision+1, reopened.Review.Revision)
	require.False(t, reopened.Review.FinalizedAt.Valid)
	requireSuccessfulAudit(t, ctx, ss, lastReopenAudit)
	assertReviewAuditPrivate(t, ctx, ss, lastReopenAudit, "the clip needs another look")
	snapshot, err := reviews.Get(ctx, submissionID)
	require.NoError(t, err)
	require.Len(t, snapshot.Decisions, 1, "reopening keeps the decisions")
	require.Equal(t, decided.Decision.ID, snapshot.Decisions[0].ID)

	refinalized := finalize(reopened.Review.Revision, "reopen-refinalize")
	require.Equal(t, model.SubmissionReviewFinalized, refinalized.Review.State)
	require.Equal(t, 1, refinalized.Review.FlagCount)

	preparation, err := reviews.PrepareRelease(ctx, &store.ExamIntegrityReviewReleasePrepare{
		SubmissionID: submissionID, ReviewID: reviewID, ExpectedRevision: refinalized.Review.Revision,
	})
	require.NoError(t, err)
	release := &store.ExamIntegrityReviewRelease{
		SubmissionID: submissionID, ReviewID: reviewID, ActorUserID: fixture.manager.ID,
		ExpectedRevision: refinalized.Review.Revision, ChangedAt: preparation.ReleaseAt,
		AuditEventID: audit(model.ActionSubmissionRelease), AuditAt: preparation.ReleaseAt,
	}
	attachResultReleaseMail(t, fixture.candidate, release)
	released, err := reviews.Release(ctx, release, examCommand(fixture.manager.ID,
		store.ExamIntegrityReviewReleaseOperation, "reopen-release", "reopen-release"))
	require.NoError(t, err)

	_, err = reopen(released.Review.Revision, "reopen-released")
	require.ErrorAs(t, err, &conflict)
	require.Equal(t, "integrity_review_released", conflict.Constraint, "a released result is final")
}
