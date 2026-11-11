// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package attempt

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/sudosylabs/proctor/server/app/apperror"
	"github.com/sudosylabs/proctor/server/model"
	"github.com/sudosylabs/proctor/server/store"
)

func TestManagerEndSealsWithPrivateReasonOutsideCandidateReceipt(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	managerID := useManagerCall(f)
	f.persistence.attempt, _ = managerAttemptFixture(t, f, model.ExamAttemptActive, 4, "")
	target := store.ExamSubmissionAutomaticSealTarget{ExamID: f.sitting.ExamID, SittingID: f.sitting.ID,
		ClassID: f.sitting.ClassID, AcademicUnitID: model.NewAcademicUnitID(), CandidateUserID: f.userID,
		AttemptID: f.attemptID, WorkspaceID: model.NewExamAttemptWorkspaceID(), CurrentRevisionID: model.NewExamRevisionID(),
		ParticipationID: model.NewAttemptParticipationID(), Generation: 2, ConnectionID: f.connectionID}
	submissionID, receiptCode := model.NewSubmissionID(), model.NewSubmissionReceiptCode()
	f.submissionID, f.receiptCode = submissionID, receiptCode
	f.submissions.managerPreparation = &store.ExamSubmissionManagerEndPreparation{Target: target,
		ExpectedAttemptRevision: 4, SealAt: f.at}
	f.submissions.managerResult = &store.ExamSubmissionManagerEndResult{ExamSubmissionSealResult: store.ExamSubmissionSealResult{
		Receipt: store.ExamSubmissionReceipt{BrowserActivity: model.BrowserSubmissionSettlement{State: "not_applicable", InventoryRevision: 1}, SubmissionID: submissionID, ReceiptCode: receiptCode, AttemptID: target.AttemptID,
			ExamRevisionID: target.CurrentRevisionID, State: model.ExamAttemptSubmitted, WorkspaceCursor: 8,
			ManifestDigest: strings.Repeat("f", 64), SubmittedAt: f.at}, ExamID: target.ExamID, SittingID: target.SittingID,
		ClassID: target.ClassID, CandidateUserID: target.CandidateUserID, ParticipationID: target.ParticipationID,
		Generation: target.Generation, ConnectionID: target.ConnectionID}, ConnectionClosed: true}
	privateReason := "candidate requested an assisted early end"
	result, err := f.service.EndExamAttemptByManager(context.Background(), f.inv, ManagerEndCommand{ExamID: target.ExamID,
		SittingID: target.SittingID, AttemptID: target.AttemptID, ExpectedAttemptRevision: 4,
		PrivateReason: privateReason, IdempotencyKey: "manager-end-once"})
	require.NoError(t, err)
	require.Equal(t, submissionID, result.SubmissionID, "result=%#v input=%#v mail=%#v effects", result, f.submissions.managerInput, f.mail.request)
	require.Equal(t, receiptCode, result.ReceiptCode, "result=%#v input=%#v mail=%#v effects", result, f.submissions.managerInput, f.mail.request)
	require.Equal(t, receiptCode, f.submissions.managerInput.ReceiptCode, "result=%#v input=%#v mail=%#v effects", result, f.submissions.managerInput, f.mail.request)
	require.Equal(t, receiptCode, f.mail.request.ReceiptCode, "result=%#v input=%#v mail=%#v effects", result, f.submissions.managerInput, f.mail.request)
	require.Equal(t, submittedCalls(t, sealedSubmission(f.submissions.managerResult.ExamSubmissionSealResult, model.ExamSubmissionManagerEndedAttempt), false), f.publisher.Calls(), "result=%#v input=%#v mail=%#v effects", result, f.submissions.managerInput, f.mail.request)
	require.NotNil(t, f.submissions.managerInput, "result=%#v input=%#v mail=%#v effects", result, f.submissions.managerInput, f.mail.request)
	require.Equal(t, privateReason, f.submissions.managerInput.Request.PrivateReason, "result=%#v input=%#v mail=%#v effects", result, f.submissions.managerInput, f.mail.request)
	require.Equal(t, managerID, f.submissions.managerInput.Request.ActorUserID, "result=%#v input=%#v mail=%#v effects", result, f.submissions.managerInput, f.mail.request)
	require.Equal(t, model.ExamSubmissionManagerEndedAttempt, f.mail.request.Provenance, "result=%#v input=%#v mail=%#v effects", result, f.submissions.managerInput, f.mail.request)
	require.NotContains(t, fmt.Sprintf("%#v", f.audit.values), privateReason, "private reason entered ordinary audit fields: %#v", f.audit.values)
	wantIdempotency, prepareErr := prepareManagerEndIdempotency(f.inv, ManagerEndCommand{ExamID: target.ExamID,
		SittingID: target.SittingID, AttemptID: target.AttemptID, ExpectedAttemptRevision: 4,
		PrivateReason: privateReason, IdempotencyKey: "manager-end-once"})
	require.NoError(t, prepareErr)
	require.Equal(t, wantIdempotency, f.submissions.idempotency)
}

func TestManagerEndConcealsSelfAccessAndFailsClosedOnAuditFailure(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		name     string
		auditErr error
	}{
		{name: "records concealed denial"},
		{name: "audit failure fails closed", auditErr: errors.New("audit unavailable")},
	} {
		t.Run(test.name, func(t *testing.T) {
			f := newFixture(t)
			f.persistence.attempt, _ = managerAttemptFixture(t, f, model.ExamAttemptActive, 4, "")
			f.audit.failErr = test.auditErr
			privateReason := "manager must never end their own Attempt"
			_, err := f.service.EndExamAttemptByManager(context.Background(), f.inv, ManagerEndCommand{
				ExamID: f.sitting.ExamID, SittingID: f.sitting.ID, AttemptID: f.attemptID,
				ExpectedAttemptRevision: 4, PrivateReason: privateReason, IdempotencyKey: "self-manager-end",
			})
			if test.auditErr != nil {
				require.ErrorIs(t, err, test.auditErr)
			} else {
				require.True(t, apperror.Is(err, "resource.not_found"))
				require.True(t, chainHasCode(err, "exam.attempt.not_found"))
			}
			require.Equal(t, model.ActionExamSittingManage, f.audit.action)
			require.Equal(t, model.Resource{Type: model.ResourceExamSitting, ID: f.sitting.ID.String()}, f.audit.resource)
			require.Equal(t, model.RoleScopeClass, f.audit.scopeType)
			require.Equal(t, f.sitting.ClassID.String(), f.audit.scopeID)
			require.Equal(t, store.ExamSubmissionManagerEndOperation, f.audit.operation)
			require.Equal(t, "exam.attempt.not_found", f.audit.failedCode)
			require.Equal(t, f.sitting.ExamID.String(), f.audit.values["exam_id"])
			require.Equal(t, f.sitting.ID.String(), f.audit.values["exam_sitting_id"])
			require.Equal(t, f.attemptID.String(), f.audit.values["exam_attempt_id"])
			require.Equal(t, int64(4), f.audit.values["expected_attempt_revision"])
			require.NotContains(t, fmt.Sprintf("%#v", f.audit.values), privateReason, "self-denial leaked or mutated: audit=%#v submissions=%#v mail=%#v effects", f.audit.values, f.submissions, f.mail.request)
			require.Nil(t, f.submissions.managerPreparation, "self-denial leaked or mutated: audit=%#v submissions=%#v mail=%#v effects", f.audit.values, f.submissions, f.mail.request)
			require.Nil(t, f.submissions.managerInput, "self-denial leaked or mutated: audit=%#v submissions=%#v mail=%#v effects", f.audit.values, f.submissions, f.mail.request)
			require.False(t, f.mail.request.SubmissionID.IsValid(), "self-denial leaked or mutated: audit=%#v submissions=%#v mail=%#v effects", f.audit.values, f.submissions, f.mail.request)
			require.Empty(t, f.publisher.Calls(), "self-denial leaked or mutated: audit=%#v submissions=%#v mail=%#v effects", f.audit.values, f.submissions, f.mail.request)
		})
	}
}
