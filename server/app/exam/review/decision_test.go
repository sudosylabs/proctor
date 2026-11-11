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

	"github.com/sudosylabs/proctor/server/app/realtime"
	"github.com/sudosylabs/proctor/server/model"
)

func TestSaveDecisionAuthorizesAuditsAtomicallyAndSuppressesPrivateRationale(t *testing.T) {
	t.Parallel()
	f := newReviewFixture(t)
	result, err := f.service.SaveExamIntegrityDecision(context.Background(), f.invocation, SaveDecisionCommand{
		SubmissionID: f.submissionID, FlagID: f.flagID, ExpectedReviewRevision: 0,
		ExpectedDecisionRevision: 0, Outcome: model.IntegrityReviewConfirmed,
		PrivateRationale: "The retained continuity evidence was verified.", IdempotencyKey: "test-key",
	})
	require.NoError(t, err)
	require.NotNil(t, result.Review, "result=%#v mutation=%#v effects", result, f.persistence.decision)
	require.NotNil(t, result.Decision, "result=%#v mutation=%#v effects", result, f.persistence.decision)
	require.NotNil(t, f.persistence.decision, "result=%#v mutation=%#v effects", result, f.persistence.decision)
	// Contract: a fresh decision tells managers the Review changed, then
	// invalidates the Sitting board.
	require.Equal(t, reviewCalls(t, realtime.NewExamIntegrityReviewChangedEvent, result), f.publisher.Calls())
	require.Equal(t, f.submissionID, f.authorizer.reviewSubmission, "authorization=%#v mutation=%#v", f.authorizer, f.persistence.decision)
	require.False(t, f.persistence.decision.ManagerOverride, "authorization=%#v mutation=%#v", f.authorizer, f.persistence.decision)
	require.Equal(t, f.userID, f.persistence.decision.ActorUserID, "authorization=%#v mutation=%#v", f.authorizer, f.persistence.decision)
	require.NotEmpty(t, f.persistence.decision.AuditEventID, "authorization=%#v mutation=%#v", f.authorizer, f.persistence.decision)
	for _, forbidden := range []string{"private_rationale", "manager_notes", "student_remarks_markdown"} {
		_, exists := f.auditor.value[forbidden]
		require.False(t, exists, "private field %q in audit %#v", forbidden, f.auditor.value)
	}
	wantIdempotency, prepareErr := prepareDecisionIdempotency(f.invocation, SaveDecisionCommand{
		SubmissionID: f.submissionID, FlagID: f.flagID, ExpectedReviewRevision: 0,
		ExpectedDecisionRevision: 0, Outcome: model.IntegrityReviewConfirmed,
		PrivateRationale: "The retained continuity evidence was verified.", IdempotencyKey: "test-key",
	})
	require.NoError(t, prepareErr)
	require.Equal(t, wantIdempotency, f.persistence.idempotency)
}

func TestUpdateDraftPassesOwnedIdempotencyToStore(t *testing.T) {
	t.Parallel()
	f := newReviewFixture(t)
	command := UpdateDraftCommand{SubmissionID: f.submissionID, ReviewID: f.reviewID, ExpectedReviewRevision: 1,
		ManagerNotes: "private", StudentRemarksMarkdown: "Visible", IdempotencyKey: "draft-key"}
	_, err := f.service.UpdateExamIntegrityReview(context.Background(), f.invocation, command)
	require.NoError(t, err)
	want, err := prepareDraftIdempotency(f.invocation, command)
	require.NoError(t, err)
	require.Equal(t, want, f.persistence.idempotency)
}

func TestExactDecisionReplayReturnsRetainedResultWithoutEffect(t *testing.T) {
	t.Parallel()
	f := newReviewFixture(t)
	f.persistence.replayed = true
	result, err := f.service.SaveExamIntegrityDecision(context.Background(), f.invocation, SaveDecisionCommand{
		SubmissionID: f.submissionID, FlagID: f.flagID, Outcome: model.IntegrityReviewConfirmed,
		PrivateRationale: "The retained continuity evidence was verified.", IdempotencyKey: "test-key",
	})
	require.NoError(t, err, "result=%#v error=%v effects", result, err)
	require.True(t, result.Replayed, "result=%#v error=%v effects", result, err)
	require.Empty(t, f.publisher.Calls(), "a replayed decision republished")
}
