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
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/sudosylabs/proctor/server/app/apperror"
	"github.com/sudosylabs/proctor/server/app/realtime"
	"github.com/sudosylabs/proctor/server/app/realtime/realtimetest"
	"github.com/sudosylabs/proctor/server/model"
	"github.com/sudosylabs/proctor/server/store"
)

func TestEvaluateFocusLossHashesCredentialAuditsSafeScopeAndPublishesCommittedWarning(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	credential := model.NewCredentialToken()
	participationID := model.NewAttemptParticipationID()
	signalID, flagID := model.NewFocusLossSignalID(), model.NewIntegrityFlagID()
	receivedAt := f.at.Add(time.Second)
	signal, err := model.NewFocusLossSignal(signalID, f.attemptID, participationID, 3, 11, 2500,
		model.FocusLossSourceDocumentHidden, receivedAt)
	require.NoError(t, err)
	flag, err := model.NewIntegrityFlag(flagID, f.attemptID, 3, model.IntegrityPolicyFocusLoss, receivedAt)
	require.NoError(t, err)
	f.focusSignalID = signalID
	f.focusFlagID = flagID
	f.persistence.focusTarget = &store.ExamAttemptFocusLossTarget{ExamID: f.sitting.ExamID, SittingID: f.sitting.ID,
		ClassID: f.sitting.ClassID, CandidateUserID: f.userID, AttemptID: f.attemptID,
		ParticipationID: participationID, Generation: 3}
	f.persistence.focusResult = &store.ExamAttemptFocusLossResult{ExamID: f.sitting.ExamID, SittingID: f.sitting.ID,
		ClassID: f.sitting.ClassID, CandidateUserID: f.userID, AttemptID: f.attemptID,
		ParticipationID: participationID, Generation: 3, Signal: signal, AcceptedSequence: 11, DatabaseTime: receivedAt,
		CollectionEnabled: true, Qualified: true, ThresholdCrossed: true, PolicyOutcome: model.IntegrityOutcomeFlagAndWarn,
		RetainedEvidenceCount: 1, Flag: flag, FlagCreated: true, CandidateWarningCreated: true,
		ManagerNotificationRequired: true}

	result, err := f.service.EvaluateExamAttemptFocusLoss(context.Background(), f.inv, FocusLossCommand{
		SchemaVersion: model.FocusLossSignalSchemaVersion,
		AttemptID:     f.attemptID, ParticipationID: participationID, ConnectionID: f.connectionID,
		Generation: 3, Sequence: 11, DurationMilliseconds: 2500, Source: model.FocusLossSourceDocumentHidden,
		ContinuityCredential: credential,
	})
	require.NoError(t, err)
	require.Equal(t, f.attemptID, result.AttemptID, "result=%#v effects=%#v", result, f.publisher.Calls())
	require.Equal(t, int64(3), result.Generation, "result=%#v effects=%#v", result, f.publisher.Calls())
	require.Equal(t, int64(11), result.AcceptedSequence, "result=%#v effects=%#v", result, f.publisher.Calls())
	require.Equal(t, receivedAt, result.ReceivedAt, "result=%#v effects=%#v", result, f.publisher.Calls())
	require.False(t, result.Duplicate, "result=%#v effects=%#v", result, f.publisher.Calls())
	require.False(t, result.GapDetected, "result=%#v effects=%#v", result, f.publisher.Calls())
	require.False(t, result.PolicyDisabled, "result=%#v effects=%#v", result, f.publisher.Calls())
	require.True(t, result.Qualified, "result=%#v effects=%#v", result, f.publisher.Calls())
	require.True(t, result.FlagCreated, "result=%#v effects=%#v", result, f.publisher.Calls())
	require.True(t, result.CandidateWarningCreated, "result=%#v effects=%#v", result, f.publisher.Calls())
	require.False(t, result.SuspensionCreated, "result=%#v effects=%#v", result, f.publisher.Calls())
	pub := published(t)
	// Contract: managers learn of the Integrity Flag before the candidate is
	// warned, and the Sitting board is invalidated after both.
	require.Equal(t, []realtimetest.Call{
		pub(realtime.NewExamAttemptIntegrityFlaggedEvent(result.SittingID, result.AttemptID, result.CandidateUserID, result.Flag.ID,
			model.IntegrityPolicyFocusLoss, result.PolicyOutcome, result.RetainedEvidenceCount, result.EvidenceOverflowCount, result.ReceivedAt)),
		pub(realtime.NewCandidateExamAttemptFocusLossWarningEvent(result.SittingID, result.AttemptID, result.CandidateUserID, result.ReceivedAt)),
		sittingBoardChangedCall(t, result.ExamID, result.SittingID),
	}, f.publisher.Calls())
	principal := f.inv.Principal()
	focusAccess := f.persistence.focusAccess
	require.Equal(t, f.attemptID, focusAccess.AttemptID, "access")
	require.Equal(t, participationID, focusAccess.ParticipationID, "access")
	require.Equal(t, f.connectionID, focusAccess.ConnectionID, "access")
	require.Equal(t, principal.UserID, focusAccess.CandidateUserID, "access")
	require.Equal(t, principal.SessionID, focusAccess.SessionID, "access")
	require.Equal(t, int64(3), focusAccess.Generation, "access")
	require.Equal(t, model.HashToken(credential), focusAccess.ContinuityCredentialHash, "access")
	require.NotNil(t, f.persistence.focusSignal, "access")
	require.Equal(t, focusAccess, f.persistence.focusSignal.Access, "access")
	require.Equal(t, signalID, f.persistence.focusSignal.SignalID, "access")
	require.True(t, f.persistence.focusSignal.EvidenceID.IsValid(), "access")
	require.Equal(t, flagID, f.persistence.focusSignal.FlagID, "access")
	require.True(t, f.persistence.focusSignal.FlagID.IsValid(), "access")
	require.True(t, f.persistence.focusSignal.SuspensionID.IsValid(), "access")
	capture := fmt.Sprintf("%#v", f.audit.values)
	for _, forbidden := range []string{credential, model.HashToken(credential), principal.SessionID.String(),
		string(model.FocusLossSourceDocumentHidden), "2500"} {
		require.NotContains(t, capture, forbidden, "Focus Loss audit exposed")
	}
	// Contract (guardrails 8 and 14): the audit begins before the durable write,
	// and the write commits before its effect.
	require.Equal(t, []string{"focus.resolve", "audit", "focus.record", "effects"}, f.order)
}

func TestEvaluateFocusLossRetainsExplicitLateDiscrepancyAfterSubmission(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	participationID := model.NewAttemptParticipationID()
	credential := model.NewCredentialToken()
	submissionID, discrepancyID, signalID := model.NewSubmissionID(), model.NewIntegrityDiscrepancyID(), model.NewFocusLossSignalID()
	receivedAt := f.at.Add(time.Second)
	f.focusSignalID, f.discrepancyID = signalID, discrepancyID
	f.persistence.focusErr = store.NewErrConflict("exam_attempt", "exam_attempt_state", errors.New("submitted"))
	target := &store.ExamAttemptFocusLossDiscrepancyTarget{ExamAttemptFocusLossTarget: store.ExamAttemptFocusLossTarget{
		ExamID: f.sitting.ExamID, SittingID: f.sitting.ID, ClassID: f.sitting.ClassID, CandidateUserID: f.userID,
		AttemptID: f.attemptID, ParticipationID: participationID, Generation: 3}, SubmissionID: submissionID}
	discrepancy, err := model.NewIntegrityDiscrepancy(model.IntegrityDiscrepancySpecification{
		ID: discrepancyID, SubmissionID: submissionID, AttemptID: f.attemptID, ParticipationID: participationID,
		Generation: 3, Kind: model.IntegrityDiscrepancyLateFocusLoss,
		SignalID: signalID, Sequence: 12, DurationMilliseconds: 2100, Source: model.FocusLossSourceDocumentHidden,
		MissingBefore: 1, ReceivedAt: receivedAt})
	require.NoError(t, err)
	f.persistence.lateFocusTarget = target
	f.persistence.lateFocusResult = &store.ExamAttemptFocusLossDiscrepancyResult{Target: *target,
		Discrepancy: discrepancy}
	result, err := f.service.EvaluateExamAttemptFocusLoss(context.Background(), f.inv, FocusLossCommand{
		SchemaVersion: model.FocusLossSignalSchemaVersion, AttemptID: f.attemptID, ParticipationID: participationID,
		ConnectionID: f.connectionID, Generation: 3, Sequence: 12, DurationMilliseconds: 2100,
		Source: model.FocusLossSourceDocumentHidden, ContinuityCredential: credential})
	require.NoError(t, err)
	require.True(t, result.DiscrepancyRecorded, "result=%#v effects=%#v", result, f.publisher.Calls())
	require.Equal(t, submissionID, result.SubmissionID, "result=%#v effects=%#v", result, f.publisher.Calls())
	require.Equal(t, discrepancyID, result.DiscrepancyID, "result=%#v effects=%#v", result, f.publisher.Calls())
	require.Equal(t, int64(12), result.AcceptedSequence, "result=%#v effects=%#v", result, f.publisher.Calls())
	require.True(t, result.GapDetected, "result=%#v effects=%#v", result, f.publisher.Calls())
	require.Equal(t, receivedAt, result.ReceivedAt, "result=%#v effects=%#v", result, f.publisher.Calls())
	// Contract: a late report publishes only the recorded discrepancy to
	// managers, then invalidates the Sitting board.
	require.Equal(t, []realtimetest.Call{
		published(t)(realtime.NewExamIntegrityDiscrepancyRecordedEvent(result.SubmissionID, result.SittingID, result.AttemptID,
			result.CandidateUserID, result.DiscrepancyID, result.ReceivedAt)),
		sittingBoardChangedCall(t, result.ExamID, result.SittingID),
	}, f.publisher.Calls())
	require.NotNil(t, f.persistence.lateFocusInput)
	require.Equal(t, model.HashToken(credential), f.persistence.lateFocusInput.Access.ContinuityCredentialHash)
	require.False(t, f.persistence.lateFocusInput.DiscrepancyID.IsZero())
	require.Equal(t, signalID, f.persistence.lateFocusInput.SignalID)
	require.Equal(t, int64(12), f.persistence.lateFocusInput.Sequence)
	require.Equal(t, int64(2100), f.persistence.lateFocusInput.DurationMilliseconds)
	// Contract (guardrails 8 and 14): the audit begins before the durable write,
	// and the write commits before its effect.
	require.Equal(t, []string{"focus.resolve", "focus.late.resolve", "audit", "focus.late.record", "effects"}, f.order)
}

func TestEvaluateFocusLossRejectsMissingOrUnknownClaimSchemaBeforePersistence(t *testing.T) {
	t.Parallel()
	for _, schemaVersion := range []int{0, model.FocusLossSignalSchemaVersion + 1} {
		schemaVersion := schemaVersion
		t.Run(fmt.Sprintf("schema_%d", schemaVersion), func(t *testing.T) {
			t.Parallel()
			f := newFixture(t)
			_, err := f.service.EvaluateExamAttemptFocusLoss(context.Background(), f.inv, FocusLossCommand{
				SchemaVersion: schemaVersion, AttemptID: f.attemptID,
				ParticipationID: model.NewAttemptParticipationID(), ConnectionID: f.connectionID,
				Generation: 1, Sequence: 1, DurationMilliseconds: 500,
				ContinuityCredential: model.NewCredentialToken(),
			})
			var fault *apperror.Error
			require.ErrorAs(t, err, &fault, "error=%v target=%#v signal=%#v effects=%#v", err, f.persistence.focusTarget, f.persistence.focusSignal, f.publisher.Calls())
			require.Equal(t, "exam.attempt.invalid", fault.Code(), "error=%v target=%#v signal=%#v effects=%#v", err, f.persistence.focusTarget, f.persistence.focusSignal, f.publisher.Calls())
			require.Nil(t, f.persistence.focusTarget, "error=%v target=%#v signal=%#v effects=%#v", err, f.persistence.focusTarget, f.persistence.focusSignal, f.publisher.Calls())
			require.Nil(t, f.persistence.focusSignal, "error=%v target=%#v signal=%#v effects=%#v", err, f.persistence.focusTarget, f.persistence.focusSignal, f.publisher.Calls())
			require.Empty(t, f.publisher.Calls(), "error=%v target=%#v signal=%#v effects=%#v", err, f.persistence.focusTarget, f.persistence.focusSignal, f.publisher.Calls())
		})
	}
}

func TestEvaluateFocusLossDisabledPolicyReturnsDiagnosticAcknowledgementWithoutEffect(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	participationID := model.NewAttemptParticipationID()
	credential := model.NewCredentialToken()
	signalID := model.NewFocusLossSignalID()
	signal, err := model.NewFocusLossSignal(signalID, f.attemptID, participationID, 1, 1, 700,
		model.FocusLossSourceWindowBlur, f.at.Add(time.Second))
	require.NoError(t, err)
	f.focusSignalID = signalID
	f.persistence.focusTarget = focusLossTargetFixture(f, participationID, 1)
	f.persistence.focusResult = &store.ExamAttemptFocusLossResult{ExamID: f.sitting.ExamID, SittingID: f.sitting.ID,
		ClassID: f.sitting.ClassID, CandidateUserID: f.userID, AttemptID: f.attemptID,
		ParticipationID: participationID, Generation: 1, Signal: signal, AcceptedSequence: 1,
		DatabaseTime: signal.ReceivedAt, CollectionEnabled: false, DiagnosticCount: 1}
	result, err := f.service.EvaluateExamAttemptFocusLoss(context.Background(), f.inv, FocusLossCommand{SchemaVersion: model.FocusLossSignalSchemaVersion, AttemptID: f.attemptID,
		ParticipationID: participationID, ConnectionID: f.connectionID, Generation: 1, Sequence: 1,
		DurationMilliseconds: 700, Source: model.FocusLossSourceWindowBlur, ContinuityCredential: credential})
	require.NoError(t, err, "result=%#v error=%v effects=%#v", result, err, f.publisher.Calls())
	require.True(t, result.PolicyDisabled, "result=%#v error=%v effects=%#v", result, err, f.publisher.Calls())
	require.False(t, result.Qualified, "result=%#v error=%v effects=%#v", result, err, f.publisher.Calls())
	require.False(t, result.FlagCreated, "result=%#v error=%v effects=%#v", result, err, f.publisher.Calls())
	require.False(t, result.CandidateWarningCreated, "result=%#v error=%v effects=%#v", result, err, f.publisher.Calls())
	require.False(t, result.SuspensionCreated, "result=%#v error=%v effects=%#v", result, err, f.publisher.Calls())
	require.Empty(t, f.publisher.Calls(), "result=%#v error=%v effects=%#v", result, err, f.publisher.Calls())
	capture := fmt.Sprintf("%#v", result)
	require.NotContains(t, capture, "window_blur")
	require.NotContains(t, capture, "700")
	require.NotContains(t, capture, credential)
	require.NotContains(t, capture, model.HashToken(credential))
}

func TestEvaluateFocusLossDuplicateAcknowledgesPriorDecisionWithoutRepeatingEffects(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	participationID := model.NewAttemptParticipationID()
	signal, err := model.NewFocusLossSignal(model.NewFocusLossSignalID(), f.attemptID, participationID, 2, 8, 2500,
		model.FocusLossSourceDocumentHidden, f.at.Add(time.Second))
	require.NoError(t, err)
	flag, err := model.NewIntegrityFlag(model.NewIntegrityFlagID(), f.attemptID, 2, model.IntegrityPolicyFocusLoss, signal.ReceivedAt)
	require.NoError(t, err)
	f.persistence.focusTarget = focusLossTargetFixture(f, participationID, 2)
	f.persistence.focusResult = &store.ExamAttemptFocusLossResult{ExamID: f.sitting.ExamID, SittingID: f.sitting.ID,
		ClassID: f.sitting.ClassID, CandidateUserID: f.userID, AttemptID: f.attemptID,
		ParticipationID: participationID, Generation: 2, Signal: signal, AcceptedSequence: 8,
		DatabaseTime: signal.ReceivedAt, CollectionEnabled: true, Qualified: true, ThresholdCrossed: true,
		PolicyOutcome: model.IntegrityOutcomeFlagAndWarn, RetainedEvidenceCount: 1, Flag: flag,
		FlagCreated: true, CandidateWarningCreated: true, ManagerNotificationRequired: true, Duplicate: true}
	result, err := f.service.EvaluateExamAttemptFocusLoss(context.Background(), f.inv, FocusLossCommand{SchemaVersion: model.FocusLossSignalSchemaVersion, AttemptID: f.attemptID,
		ParticipationID: participationID, ConnectionID: f.connectionID, Generation: 2, Sequence: 8,
		DurationMilliseconds: 2500, Source: model.FocusLossSourceDocumentHidden,
		ContinuityCredential: model.NewCredentialToken()})
	require.NoError(t, err, "result=%#v error=%v effects=%#v", result, err, f.publisher.Calls())
	require.True(t, result.Duplicate, "result=%#v error=%v effects=%#v", result, err, f.publisher.Calls())
	require.True(t, result.FlagCreated, "result=%#v error=%v effects=%#v", result, err, f.publisher.Calls())
	require.True(t, result.CandidateWarningCreated, "result=%#v error=%v effects=%#v", result, err, f.publisher.Calls())
	require.True(t, result.ManagerNotificationRequired, "result=%#v error=%v effects=%#v", result, err, f.publisher.Calls())
	require.False(t, result.SuspensionCreated, "result=%#v error=%v effects=%#v", result, err, f.publisher.Calls())
	require.Empty(t, f.publisher.Calls(), "result=%#v error=%v effects=%#v", result, err, f.publisher.Calls())
}

func TestEvaluateFocusLossDuplicateReplaysPriorSuspensionWithoutRepeatingEffects(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	participationID := model.NewAttemptParticipationID()
	command, stored := focusLossSuspensionFixture(t, f, participationID)
	stored.Duplicate = true
	f.persistence.focusTarget = focusLossTargetFixture(f, participationID, command.Generation)
	f.persistence.focusResult = stored

	result, err := f.service.EvaluateExamAttemptFocusLoss(context.Background(), f.inv, command)
	require.NoError(t, err, "result=%#v error=%v effects=%#v", result, err, f.publisher.Calls())
	require.True(t, result.Duplicate, "result=%#v error=%v effects=%#v", result, err, f.publisher.Calls())
	require.True(t, result.FlagCreated, "result=%#v error=%v effects=%#v", result, err, f.publisher.Calls())
	require.True(t, result.ManagerNotificationRequired, "result=%#v error=%v effects=%#v", result, err, f.publisher.Calls())
	require.True(t, result.ConnectionClosed, "result=%#v error=%v effects=%#v", result, err, f.publisher.Calls())
	require.True(t, result.SuspensionCreated, "result=%#v error=%v effects=%#v", result, err, f.publisher.Calls())
	require.Empty(t, f.publisher.Calls(), "result=%#v error=%v effects=%#v", result, err, f.publisher.Calls())
}

func TestEvaluateFocusLossProjectsCommittedPolicySuspensionAndPublishesOnce(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	participationID := model.NewAttemptParticipationID()
	command, stored := focusLossSuspensionFixture(t, f, participationID)
	f.persistence.focusTarget = focusLossTargetFixture(f, participationID, command.Generation)
	f.persistence.focusResult = stored

	result, err := f.service.EvaluateExamAttemptFocusLoss(context.Background(), f.inv, command)
	require.NoError(t, err)
	require.True(t, result.Qualified, "result=%#v effects=%#v", result, f.publisher.Calls())
	require.True(t, result.ThresholdCrossed, "result=%#v effects=%#v", result, f.publisher.Calls())
	require.Equal(t, model.IntegrityOutcomeFlagAndSuspend, result.PolicyOutcome, "result=%#v effects=%#v", result, f.publisher.Calls())
	require.True(t, result.FlagCreated, "result=%#v effects=%#v", result, f.publisher.Calls())
	require.True(t, result.ManagerNotificationRequired, "result=%#v effects=%#v", result, f.publisher.Calls())
	require.True(t, result.ConnectionClosed, "result=%#v effects=%#v", result, f.publisher.Calls())
	require.True(t, result.SuspensionCreated, "result=%#v effects=%#v", result, f.publisher.Calls())
	require.False(t, result.CandidateWarningCreated, "result=%#v effects=%#v", result, f.publisher.Calls())
	require.Equal(t, f.connectionID, result.Connection.ID, "result=%#v effects=%#v", result, f.publisher.Calls())
	require.Equal(t, model.AttemptSuspensionCandidateReasonFocusLossPolicy, result.Suspension.CandidateReason, "result=%#v effects=%#v", result, f.publisher.Calls())
	pub := published(t)
	// Contract: the Connection close leads, the Integrity Flag and the
	// Suspension reach managers before the candidate's Suspension, the Sitting
	// board and the candidate's Exam activity are invalidated next, and the
	// closed Connection is unbound last.
	require.Equal(t, []realtimetest.Call{
		pub(realtime.NewExamAttemptConnectionClosedEvent(result.SittingID, result.AttemptID, result.CandidateUserID, result.Connection.ID,
			result.Connection.CloseReason, result.Connection.ClosedAt.Time)),
		pub(realtime.NewExamAttemptIntegrityFlaggedEvent(result.SittingID, result.AttemptID, result.CandidateUserID, result.Flag.ID,
			model.IntegrityPolicyFocusLoss, result.PolicyOutcome, result.RetainedEvidenceCount, result.EvidenceOverflowCount, result.ReceivedAt)),
		pub(realtime.NewExamAttemptPolicySuspendedEvent(result.SittingID, result.AttemptID, result.CandidateUserID, result.Connection.ID,
			result.Flag.ID, result.Suspension.ID, result.Attempt.Revision, result.ReceivedAt)),
		pub(realtime.NewCandidateExamAttemptSuspendedEvent(result.SittingID, result.AttemptID, result.CandidateUserID,
			result.Suspension.Source, result.Suspension.CandidateReason, result.ReceivedAt)),
		sittingBoardChangedCall(t, result.ExamID, result.SittingID),
		pub(realtime.NewCandidateExamActivityChangedEvent(result.CandidateUserID)),
		{Method: realtimetest.UnbindExamAttemptConnection, ConnectionID: result.Connection.ID},
	}, f.publisher.Calls())
}

// TestEvaluateFocusLossRejectsEnforcementForAnotherConnection records that
// the application no longer re-checks which Connection a Focus Loss
// suspension closed: the outcome is passed through. The PostgreSQL
// conformance suite asserts that identity on every real RecordFocusLoss
// result (storetest requireFocusLossEnforcementLinks).
func TestEvaluateFocusLossRejectsEnforcementForAnotherConnection(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	participationID := model.NewAttemptParticipationID()
	command, stored := focusLossSuspensionFixture(t, f, participationID)
	foreign := model.NewAttemptConnectionID()
	stored.Connection.ID = foreign
	f.persistence.focusTarget = focusLossTargetFixture(f, participationID, command.Generation)
	f.persistence.focusResult = stored

	result, err := f.service.EvaluateExamAttemptFocusLoss(context.Background(), f.inv, command)
	require.NoError(t, err, "result=%#v error=%v effects=%#v", result, err, f.publisher.Calls())
	require.Equal(t, foreign, result.Connection.ID, "result=%#v error=%v effects=%#v", result, err, f.publisher.Calls())
}

func TestEvaluateFocusLossRejectsInconsistentSuspensionAggregate(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name   string
		mutate func(*store.ExamAttemptFocusLossResult)
	}{
		{name: "attempt remains active", mutate: func(result *store.ExamAttemptFocusLossResult) {
			result.Attempt.State = model.ExamAttemptActive
		}},
		{name: "suspension already re-allowed", mutate: func(result *store.ExamAttemptFocusLossResult) {
			result.Suspension.ReallowedByUserID = model.NewUserID()
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			f := newFixture(t)
			participationID := model.NewAttemptParticipationID()
			command, stored := focusLossSuspensionFixture(t, f, participationID)
			test.mutate(stored)
			f.persistence.focusTarget = focusLossTargetFixture(f, participationID, command.Generation)
			f.persistence.focusResult = stored

			result, err := f.service.EvaluateExamAttemptFocusLoss(context.Background(), f.inv, command)
			require.Error(t, err, "result=%#v error=%v effects=%#v", result, err, f.publisher.Calls())
			require.Equal(t, FocusLossEvaluation{}, result, "result=%#v error=%v effects=%#v", result, err, f.publisher.Calls())
			require.Empty(t, f.publisher.Calls(), "result=%#v error=%v effects=%#v", result, err, f.publisher.Calls())
		})
	}
}

func TestEvaluateFocusLossRejectsThresholdWithoutItsOpenFlag(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	participationID := model.NewAttemptParticipationID()
	receivedAt := f.at.Add(time.Second)
	signal, err := model.NewFocusLossSignal(model.NewFocusLossSignalID(), f.attemptID, participationID,
		2, 4, 2500, model.FocusLossSourceDocumentHidden, receivedAt)
	require.NoError(t, err)
	f.persistence.focusTarget = focusLossTargetFixture(f, participationID, 2)
	f.persistence.focusResult = &store.ExamAttemptFocusLossResult{ExamID: f.sitting.ExamID, SittingID: f.sitting.ID,
		ClassID: f.sitting.ClassID, CandidateUserID: f.userID, AttemptID: f.attemptID,
		ParticipationID: participationID, Generation: 2, Signal: signal, AcceptedSequence: 4,
		DatabaseTime: receivedAt, CollectionEnabled: true, Qualified: true, ThresholdCrossed: true,
		PolicyOutcome: model.IntegrityOutcomeFlag, RetainedEvidenceCount: 1}

	result, err := f.service.EvaluateExamAttemptFocusLoss(context.Background(), f.inv, FocusLossCommand{SchemaVersion: model.FocusLossSignalSchemaVersion, AttemptID: f.attemptID,
		ParticipationID: participationID, ConnectionID: f.connectionID, Generation: 2, Sequence: 4,
		DurationMilliseconds: 2500, Source: model.FocusLossSourceDocumentHidden,
		ContinuityCredential: model.NewCredentialToken()})
	require.Error(t, err, "result=%#v error=%v effects=%#v", result, err, f.publisher.Calls())
	require.Equal(t, FocusLossEvaluation{}, result, "result=%#v error=%v effects=%#v", result, err, f.publisher.Calls())
	require.Empty(t, f.publisher.Calls(), "result=%#v error=%v effects=%#v", result, err, f.publisher.Calls())
}

func TestEvaluateFocusLossMapsChangedSameSequenceToStableConflict(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	participationID := model.NewAttemptParticipationID()
	f.persistence.focusTarget = focusLossTargetFixture(f, participationID, 2)
	f.persistence.focusErr = store.NewErrConflict("exam_attempt_focus_loss", "focus_loss_sequence", errors.New("changed claim"))

	_, err := f.service.EvaluateExamAttemptFocusLoss(context.Background(), f.inv, FocusLossCommand{SchemaVersion: model.FocusLossSignalSchemaVersion, AttemptID: f.attemptID,
		ParticipationID: participationID, ConnectionID: f.connectionID, Generation: 2, Sequence: 8,
		DurationMilliseconds: 2500, Source: model.FocusLossSourceDocumentHidden,
		ContinuityCredential: model.NewCredentialToken()})
	var fault *apperror.Error
	require.ErrorAs(t, err, &fault, "error=%v effects=%#v", err, f.publisher.Calls())
	require.Equal(t, "exam.attempt.focus_loss_conflict", fault.Code(), "error=%v effects=%#v", err, f.publisher.Calls())
	require.Empty(t, f.publisher.Calls(), "error=%v effects=%#v", err, f.publisher.Calls())
}

func focusLossSuspensionFixture(t *testing.T, f *fixture,
	participationID model.AttemptParticipationID,
) (FocusLossCommand, *store.ExamAttemptFocusLossResult) {
	t.Helper()
	generation, sequence := int64(4), int64(9)
	receivedAt := f.at.Add(time.Second)
	signal, err := model.NewFocusLossSignal(model.NewFocusLossSignalID(), f.attemptID, participationID,
		generation, sequence, 2500, model.FocusLossSourceApplicationBackgrounded, receivedAt)
	require.NoError(t, err)
	flag, err := model.NewIntegrityFlag(model.NewIntegrityFlagID(), f.attemptID, generation,
		model.IntegrityPolicyFocusLoss, receivedAt)
	require.NoError(t, err)
	attempt, err := model.NewExamAttempt(f.attemptID, f.sitting.ExamID, f.sitting.ID, f.userID,
		f.revision.ID, f.at.Add(-time.Hour))
	require.NoError(t, err)
	err = attempt.Suspend(receivedAt)
	require.NoError(t, err)
	participation := &store.ExamAttemptParticipationView{ID: participationID, AttemptID: f.attemptID,
		State: model.AttemptParticipationEnded, Generation: generation, RenewalSequence: sequence,
		StartedAt: f.at.Add(-time.Minute), UpdatedAt: receivedAt,
		LeaseExpiresAt: f.at.Add(model.AttemptParticipationInitialLease), EndedAt: model.OptionalTimeFrom(receivedAt),
		EndReason: model.AttemptParticipationEndPolicySuspended}
	connection := &store.ExamAttemptManagerConnection{ID: f.connectionID, State: model.AttemptConnectionClosed,
		OpenedAt: f.at.Add(-time.Minute), ClosedAt: model.OptionalTimeFrom(receivedAt),
		CloseReason: model.AttemptConnectionClosePolicySuspended}
	suspension := &store.ExamAttemptSuspensionView{ID: model.NewAttemptSuspensionID(), AttemptID: f.attemptID,
		ParticipationID: participationID, FlagID: flag.ID, Generation: generation, State: model.AttemptSuspensionActive,
		Source: model.AttemptSuspensionSourcePolicy, CandidateReason: model.AttemptSuspensionCandidateReasonFocusLossPolicy,
		StartedAt: receivedAt}
	command := FocusLossCommand{SchemaVersion: model.FocusLossSignalSchemaVersion,
		AttemptID: f.attemptID, ParticipationID: participationID, ConnectionID: f.connectionID,
		Generation: generation, Sequence: sequence, DurationMilliseconds: 2500,
		Source: model.FocusLossSourceApplicationBackgrounded, ContinuityCredential: model.NewCredentialToken()}
	return command, &store.ExamAttemptFocusLossResult{ExamID: f.sitting.ExamID, SittingID: f.sitting.ID,
		ClassID: f.sitting.ClassID, CandidateUserID: f.userID, AttemptID: f.attemptID,
		ParticipationID: participationID, Generation: generation, Signal: signal, AcceptedSequence: sequence,
		DatabaseTime: receivedAt, CollectionEnabled: true, Qualified: true, ThresholdCrossed: true,
		PolicyOutcome: model.IntegrityOutcomeFlagAndSuspend, RetainedEvidenceCount: 1, Flag: flag,
		FlagCreated: true, ManagerNotificationRequired: true, Attempt: attempt, Participation: participation,
		Connection: connection, ConnectionClosed: true, Suspension: suspension}
}

func focusLossTargetFixture(f *fixture, participationID model.AttemptParticipationID,
	generation int64,
) *store.ExamAttemptFocusLossTarget {
	return &store.ExamAttemptFocusLossTarget{ExamID: f.sitting.ExamID, SittingID: f.sitting.ID,
		ClassID: f.sitting.ClassID, CandidateUserID: f.userID, AttemptID: f.attemptID,
		ParticipationID: participationID, Generation: generation}
}
