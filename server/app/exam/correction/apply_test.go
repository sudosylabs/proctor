// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package correction

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/sudosylabs/proctor/server/app/apperror"
	"github.com/sudosylabs/proctor/server/app/realtime"
	"github.com/sudosylabs/proctor/server/app/realtime/realtimetest"
	"github.com/sudosylabs/proctor/server/model"
)

func TestApplyUsesOneAtomicStoreCommandAndSuppressesReplayEffects(t *testing.T) {
	t.Parallel()
	f := newCorrectionFixture(t)
	f.persistence.applyReplayed = true
	result, err := f.service.ApplyExamSittingCorrection(context.Background(), f.invocation, ApplyCommand{ExamID: f.examID, SittingID: f.sittingID, ExpectedSittingRevision: 3, ExpectedCurrentRevisionID: f.baseRevisionID, Instructions: OptionalInstructions{Present: true, Markdown: "Updated"}, Resources: []ResourceManifestItem{}, AffectedCapabilities: []model.CandidateCapability{model.CandidateCapabilityBrowser, model.CandidateCapabilitySubmission, model.CandidateCapabilityWorkspace}, CandidateSummary: "The instructions were corrected.", AcknowledgementRequired: true, PrivateReason: "Correct a discovered ambiguity", IdempotencyKey: "test-key"})
	require.NoError(t, err)
	require.Equal(t, f.baseRevisionID, result.PreviousRevisionID)
	require.False(t, result.RevisionID.IsZero())
	require.Equal(t, int64(4), result.SittingRevision)
	require.True(t, result.EffectiveAt.Equal(f.at.Add(-time.Minute)))
	require.Empty(t, f.publisher.Calls())
	require.NotNil(t, f.persistence.application)
	require.NotNil(t, f.persistence.application.InstructionsMarkdown)
	require.Equal(t, "Updated", *f.persistence.application.InstructionsMarkdown)
	require.NotEmpty(t, f.persistence.application.PrivateReason)
	require.Empty(t, f.publisher.Calls(), "replay effects")
	for _, forbidden := range []string{"instructions_markdown", "resources", "private_reason", "stage_id", "sha256"} {
		_, ok := f.auditor.value[forbidden]
		require.False(t, ok, "unsafe audit field %q", forbidden)
	}
	wantIdempotency, prepareErr := prepareApplyIdempotency(f.invocation, ApplyCommand{ExamID: f.examID, SittingID: f.sittingID,
		ExpectedSittingRevision: 3, ExpectedCurrentRevisionID: f.baseRevisionID,
		Instructions: OptionalInstructions{Present: true, Markdown: "Updated"}, Resources: []ResourceManifestItem{},
		AffectedCapabilities: []model.CandidateCapability{model.CandidateCapabilityBrowser, model.CandidateCapabilitySubmission, model.CandidateCapabilityWorkspace}, CandidateSummary: "The instructions were corrected.", AcknowledgementRequired: true,
		PrivateReason: "Correct a discovered ambiguity", IdempotencyKey: "test-key"})
	require.NoError(t, prepareErr)
	require.Equal(t, wantIdempotency, f.persistence.idempotency)
}

func TestApplyPublishesOnlyAfterCommitAndReportsTransientFailure(t *testing.T) {
	t.Parallel()
	f := newCorrectionFixture(t)
	unavailable := errors.New("realtime unavailable")
	f.publisher.Fail = func(realtimetest.Call) error { return unavailable }
	candidateID := model.NewUserID()
	f.sittings.candidateIDs = []model.UserID{candidateID}
	result, err := f.service.ApplyExamSittingCorrection(context.Background(), f.invocation, ApplyCommand{ExamID: f.examID, SittingID: f.sittingID, ExpectedSittingRevision: 3, ExpectedCurrentRevisionID: f.baseRevisionID, Instructions: OptionalInstructions{Present: true, Markdown: "Updated"}, Resources: []ResourceManifestItem{}, AffectedCapabilities: []model.CandidateCapability{model.CandidateCapabilityBrowser, model.CandidateCapabilitySubmission, model.CandidateCapabilityWorkspace}, CandidateSummary: "The instructions were corrected.", PrivateReason: "Correct a discovered ambiguity", IdempotencyKey: "test-key"})
	require.NoError(t, err)
	require.NotNil(t, f.persistence.application)
	// Contract (guardrails 8 and 14): authorization precedes the audit, the audit
	// begins before the durable write, and the write commits before its effects;
	// the candidates are read only after the three Sitting-wide publications.
	require.Equal(t, []string{"access", "membership", "authorize", "audit.begin", "apply", "effect", "effect", "effect", "candidates", "effect"}, f.order)
	managerEvent, err := realtime.NewExamSittingContentCorrectedEvent(f.examID, f.sittingID, f.baseRevisionID, result.RevisionID, 4, result.EffectiveAt)
	require.NoError(t, err)
	candidateEvent, err := realtime.NewCandidateExamSittingContentCorrectedEvent(f.examID, f.sittingID, f.baseRevisionID, result.RevisionID, 4, result.EffectiveAt)
	require.NoError(t, err)
	boardEvent, err := realtime.NewManagerSittingBoardChangedEvent(f.examID, f.sittingID)
	require.NoError(t, err)
	activityEvent, err := realtime.NewCandidateExamActivityChangedEvent(candidateID)
	require.NoError(t, err)
	// Contract: managers learn of the correction before candidates, the board
	// and candidate activity invalidations follow, every publication is
	// attempted despite the failures, and the joined failure is reported once.
	require.Equal(t, []realtimetest.Call{
		{Method: realtimetest.Publish, Event: managerEvent},
		{Method: realtimetest.Publish, Event: candidateEvent},
		{Method: realtimetest.Publish, Event: boardEvent},
		{Method: realtimetest.Publish, Event: activityEvent},
		{Method: realtimetest.Report, Operation: "exam_sitting_content_corrected", Err: errors.Join(unavailable, unavailable, unavailable, errors.Join(unavailable))},
	}, f.publisher.Calls())
}

func TestApplyRejectsNonCanonicalPrivateReasonBeforeAuthorization(t *testing.T) {
	t.Parallel()
	f := newCorrectionFixture(t)
	_, err := f.service.ApplyExamSittingCorrection(context.Background(), f.invocation, ApplyCommand{ExamID: f.examID, SittingID: f.sittingID, ExpectedSittingRevision: 3, ExpectedCurrentRevisionID: f.baseRevisionID, PrivateReason: " padded ", IdempotencyKey: "test-key"})
	var fault *apperror.Error
	require.ErrorAs(t, err, &fault)
	require.Equal(t, "exam.sitting.correction.invalid", fault.Code())
	require.Empty(t, f.order)
}
