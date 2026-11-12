// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package attempt

import (
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/sudosylabs/proctor/server/app/apperror"
	"github.com/sudosylabs/proctor/server/app/invocation"
	"github.com/sudosylabs/proctor/server/model"
	"github.com/sudosylabs/proctor/server/store"
)

func TestAutomaticSealUsesBoundedSystemAuditAndPublishesOnlyFreshResult(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	target := store.ExamSubmissionAutomaticSealTarget{ExamID: f.sitting.ExamID, SittingID: f.sitting.ID,
		ClassID: f.sitting.ClassID, AcademicUnitID: model.NewAcademicUnitID(), CandidateUserID: f.userID,
		AttemptID: f.attemptID, WorkspaceID: model.NewExamAttemptWorkspaceID(), CurrentRevisionID: model.NewExamRevisionID(),
		ParticipationID: model.NewAttemptParticipationID(),
		Generation:      2, ConnectionID: f.connectionID}
	retained, retainedCode := model.NewSubmissionID(), model.NewSubmissionReceiptCode()
	f.submissionID, f.receiptCode = retained, retainedCode
	f.submissions.automaticTargets = []store.ExamSubmissionAutomaticSealTarget{target}
	f.submissions.automaticResult = &store.ExamSubmissionAutomaticSealResult{ExamSubmissionSealResult: store.ExamSubmissionSealResult{
		Receipt: store.ExamSubmissionReceipt{BrowserActivity: model.BrowserSubmissionSettlement{State: "not_applicable", InventoryRevision: 1}, SubmissionID: retained, ReceiptCode: retainedCode, AttemptID: target.AttemptID,
			ExamRevisionID: target.CurrentRevisionID, State: model.ExamAttemptSubmitted, WorkspaceCursor: 4,
			ManifestDigest: strings.Repeat("a", 64), SubmittedAt: f.at},
		ExamID: target.ExamID, SittingID: target.SittingID, ClassID: target.ClassID, CandidateUserID: target.CandidateUserID,
		ParticipationID: target.ParticipationID, Generation: target.Generation, ConnectionID: target.ConnectionID},
		ConnectionClosed: true}
	_, err := f.service.ListAutomaticSealTargets(context.Background(), invocation.System{}, target.SittingID, model.ExamAttemptID(""), 20)
	require.Equal(t, "exam.attempt.invalid", apperror.CodeOf(err, ""), "a seal-target page without its Job identity")
	items, err := f.service.ListAutomaticSealTargets(context.Background(), invocation.Job(model.NewJobID(), model.NewJobAttemptID()), target.SittingID, model.ExamAttemptID(""), 20)
	require.NoError(t, err, "targets=%#v options=%#v err", items, f.submissions.automaticOptions)
	require.Len(t, items, 1, "targets=%#v options=%#v err", items, f.submissions.automaticOptions)
	require.Equal(t, target, items[0], "targets=%#v options=%#v err", items, f.submissions.automaticOptions)
	require.Equal(t, 20, f.submissions.automaticOptions.Limit, "targets=%#v options=%#v err", items, f.submissions.automaticOptions)
	jobID, jobAttemptID := model.NewJobID(), model.NewJobAttemptID()
	result, err := f.service.SealForSittingClose(context.Background(), invocation.System{JobID: jobID, AttemptID: jobAttemptID}, target)
	require.NoError(t, err, "result=%#v input=%#v effects=%d err", result, f.submissions.automaticInput, len(f.publisher.Calls()))
	require.Equal(t, retained, result.Receipt.SubmissionID, "result=%#v input=%#v effects=%d err", result, f.submissions.automaticInput, len(f.publisher.Calls()))
	require.True(t, result.ConnectionClosed, "result=%#v input=%#v effects=%d err", result, f.submissions.automaticInput, len(f.publisher.Calls()))
	require.Equal(t, submittedCalls(t, result.SubmissionResult, true), f.publisher.Calls(), "result=%#v input=%#v effects=%d err", result, f.submissions.automaticInput, len(f.publisher.Calls()))
	require.NotNil(t, f.submissions.automaticInput, "result=%#v input=%#v effects=%d err", result, f.submissions.automaticInput, len(f.publisher.Calls()))
	require.Equal(t, target, f.submissions.automaticInput.Target, "result=%#v input=%#v effects=%d err", result, f.submissions.automaticInput, len(f.publisher.Calls()))
	require.Equal(t, model.ExamSubmissionSittingClosed, f.mail.request.Provenance, "automatic mail request=%#v input=%#v", f.mail.request, f.submissions.automaticInput)
	require.Equal(t, retained, f.mail.request.SubmissionID, "automatic mail request=%#v input=%#v", f.mail.request, f.submissions.automaticInput)
	require.Equal(t, retainedCode, f.mail.request.ReceiptCode, "automatic mail request=%#v input=%#v", f.mail.request, f.submissions.automaticInput)
	require.Equal(t, retainedCode, f.submissions.automaticInput.ReceiptCode, "automatic mail request=%#v input=%#v", f.mail.request, f.submissions.automaticInput)
	require.Equal(t, retainedCode, result.Receipt.ReceiptCode, "result=%#v", result)
	require.True(t, f.mail.request.SealedAt.Equal(f.at), "automatic mail request=%#v input=%#v", f.mail.request, f.submissions.automaticInput)
	require.True(t, model.TimeUTC(f.at).Equal(f.submissions.automaticInput.AuditAt), "automatic mail request=%#v input=%#v", f.mail.request, f.submissions.automaticInput)
	require.NotNil(t, f.submissions.automaticInput.Notice, "automatic mail request=%#v input=%#v", f.mail.request, f.submissions.automaticInput)
	require.Equal(t, int64(2), f.submissions.automaticInput.ExpectedRecipientRevision, "automatic mail request=%#v input=%#v", f.mail.request, f.submissions.automaticInput)
	require.True(t, f.submissions.automaticInput.DomainEventID.IsValid(), "the proposed domain event reaches the automatic seal")
	require.True(t, f.submissions.automaticInput.DomainEventJobID.IsValid(), "the proposed dispatch Job reaches the automatic seal")
	require.Equal(t, jobID.String(), f.systemAudit.values["job_id"])
	require.Equal(t, jobAttemptID.String(), f.systemAudit.values["job_attempt_id"])
	require.Equal(t, target.AttemptID.String(), f.systemAudit.values["exam_attempt_id"])
	f.submissions.automaticResult.Replayed = true
	f.submissions.automaticResult.ConnectionClosed = false
	_, err = f.service.SealForSittingClose(context.Background(), invocation.System{JobID: jobID, AttemptID: jobAttemptID}, target)
	require.NoError(t, err)
	require.Equal(t, submittedCalls(t, result.SubmissionResult, true), f.publisher.Calls(), "replay duplicated effects")
}
