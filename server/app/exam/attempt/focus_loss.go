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
	"time"

	"github.com/sudosylabs/proctor/server/app/access"
	"github.com/sudosylabs/proctor/server/app/apperror"
	"github.com/sudosylabs/proctor/server/app/invocation"
	"github.com/sudosylabs/proctor/server/model"
	"github.com/sudosylabs/proctor/server/store"
)

type FocusLossCommand struct {
	SchemaVersion        int
	AttemptID            model.ExamAttemptID
	ParticipationID      model.AttemptParticipationID
	ConnectionID         model.AttemptConnectionID
	Generation           int64
	Sequence             int64
	DurationMilliseconds int64
	Source               model.FocusLossSource
	ContinuityCredential string
}

// FocusLossEvaluation is the safe application result of one accepted claim.
// It deliberately excludes claim duration/source, credential material, Session
// identity, evidence content, raw thresholds, and disabled diagnostic counts.
// Decision booleans describe the accepted sequence and therefore survive an
// exact duplicate; Duplicate suppresses post-commit effects, not the replayed
// acknowledgement.
type FocusLossEvaluation struct {
	ExamID                      model.ExamID
	SittingID                   model.ExamSittingID
	CandidateUserID             model.UserID
	SubmissionID                model.SubmissionID
	AttemptID                   model.ExamAttemptID
	ParticipationID             model.AttemptParticipationID
	DiscrepancyID               model.IntegrityDiscrepancyID
	Generation                  int64
	AcceptedSequence            int64
	ReceivedAt                  time.Time
	Duplicate                   bool
	GapDetected                 bool
	PolicyDisabled              bool
	Qualified                   bool
	ThresholdCrossed            bool
	PolicyOutcome               model.IntegrityOutcome
	RetainedEvidenceCount       int
	EvidenceOverflowCount       int64
	Flag                        model.IntegrityFlag
	FlagCreated                 bool
	CandidateWarningCreated     bool
	ManagerNotificationRequired bool
	Attempt                     model.ExamAttempt
	Participation               store.ExamAttemptParticipationView
	Connection                  store.ExamAttemptManagerConnection
	ConnectionClosed            bool
	Suspension                  store.ExamAttemptSuspensionView
	SuspensionCreated           bool
	DiscrepancyRecorded         bool
}

// EvaluateExamAttemptFocusLoss records one Focus Loss claim from the
// candidate's live Attempt Participation and applies the frozen policy, which
// may raise an Integrity Flag and open an Attempt Suspension. A claim that
// arrives after the Attempt or Sitting ended is evaluated without changing
// the Attempt.
func (s *Service) EvaluateExamAttemptFocusLoss(
	ctx context.Context,
	inv invocation.Invocation,
	command FocusLossCommand,
) (_ FocusLossEvaluation, resultErr error) {
	defer func() { s.recordOperational("focus_loss", resultErr) }()
	defer func() { resultErr = publicFailures.Conceal(resultErr) }()
	selector, err := focusLossSelector(inv, command)
	if err != nil {
		return FocusLossEvaluation{}, err
	}

	target, err := s.focusLoss.ResolveFocusLossTarget(ctx, selector)
	if err != nil {
		if endedFocusLossCandidate(err) {
			return s.evaluateEndedFocusLoss(ctx, inv, command, selector)
		}
		return FocusLossEvaluation{}, storeError(storeErrors, err)
	}

	if !validFocusLossTarget(target, selector) {
		return FocusLossEvaluation{}, apperror.New("exam.attempt.unavailable").Wrap(errors.New("inconsistent Focus Loss audit target"))
	}

	auditID, err := s.auditor.Begin(ctx, access.Mutation{
		Invocation: inv,
		Action:     model.ActionExamSittingParticipate,
		Resource:   model.Resource{Type: model.ResourceExamSitting, ID: target.SittingID.String()},
		ScopeType:  model.RoleScopeClass,
		ScopeID:    target.ClassID.String(),
		Operation:  store.ExamAttemptFocusLossOperation,
		Value: map[string]any{
			"exam_attempt_id": command.AttemptID.String(),
			"generation":      command.Generation,
			"sequence":        command.Sequence,
		},
	})
	if err != nil {
		return FocusLossEvaluation{}, err
	}

	stored, err := s.focusLoss.RecordFocusLoss(ctx, &store.ExamAttemptFocusLossSignal{
		Access:               selector,
		SignalID:             s.newFocusLossSignalID(),
		EvidenceID:           s.newEvidenceID(),
		FlagID:               s.newFlagID(),
		SuspensionID:         s.newSuspensionID(),
		Sequence:             command.Sequence,
		DurationMilliseconds: command.DurationMilliseconds,
		Source:               command.Source,
		AuditEventID:         auditID,
		AuditAt:              model.TimeUTC(s.now()),
	})
	if err != nil {
		if endedFocusLossCandidate(err) {
			if auditErr := s.auditor.Fail(ctx, auditID, "exam.attempt.state_conflict"); auditErr != nil {
				return FocusLossEvaluation{}, auditErr
			}
			return s.evaluateEndedFocusLoss(ctx, inv, command, selector)
		}

		return FocusLossEvaluation{}, s.failAudit(ctx, auditID, err)
	}

	result, err := projectFocusLoss(stored, command)
	if err != nil {
		return FocusLossEvaluation{}, err
	}

	if !result.Duplicate &&
		(result.GapDetected ||
			result.FlagCreated ||
			result.ManagerNotificationRequired ||
			result.CandidateWarningCreated ||
			result.SuspensionCreated) {
		if effectErr := s.publishFocusLossEvaluated(ctx, result); effectErr != nil {
			s.publisher.Report(ctx, "exam_attempt_focus_loss_evaluated", effectErr)
		}
	}

	return result, nil
}

func endedFocusLossCandidate(err error) bool {
	var conflict *store.ErrConflict
	return errors.As(err, &conflict) &&
		(conflict.Constraint == "exam_attempt_state" ||
			conflict.Constraint == "exam_sitting_state")
}

func (s *Service) evaluateEndedFocusLoss(
	ctx context.Context,
	inv invocation.Invocation,
	command FocusLossCommand,
	selector store.ExamAttemptFocusLossAccess,
) (FocusLossEvaluation, error) {
	target, err := s.focusLoss.ResolveEndedFocusLossTarget(ctx, selector)
	if err != nil {
		return FocusLossEvaluation{}, storeError(storeErrors, err)
	}
	if target == nil || !target.SubmissionID.IsValid() || !validFocusLossTarget(&target.ExamAttemptFocusLossTarget, selector) {
		return FocusLossEvaluation{}, apperror.New("exam.attempt.unavailable").Wrap(errors.New("inconsistent late Focus Loss audit target"))
	}

	auditID, err := s.auditor.Begin(ctx, access.Mutation{
		Invocation: inv,
		Action:     model.ActionExamSittingParticipate,
		Resource:   model.Resource{Type: model.ResourceExamSitting, ID: target.SittingID.String()},
		ScopeType:  model.RoleScopeClass,
		ScopeID:    target.ClassID.String(),
		Operation:  store.ExamAttemptFocusLossDiscrepancyOperation,
		Value: map[string]any{
			"exam_attempt_id": command.AttemptID.String(),
			"submission_id":   target.SubmissionID.String(),
			"generation":      command.Generation,
			"sequence":        command.Sequence,
		},
	})
	if err != nil {
		return FocusLossEvaluation{}, err
	}

	discrepancyID := s.newDiscrepancyID()
	signalID := s.newFocusLossSignalID()
	stored, err := s.focusLoss.RecordEndedFocusLoss(ctx, &store.ExamAttemptFocusLossDiscrepancy{
		Access:               selector,
		DiscrepancyID:        discrepancyID,
		SignalID:             signalID,
		Sequence:             command.Sequence,
		DurationMilliseconds: command.DurationMilliseconds,
		Source:               command.Source,
		AuditEventID:         auditID,
		AuditAt:              model.TimeUTC(s.now()),
	})
	if err != nil {
		return FocusLossEvaluation{}, s.failAudit(ctx, auditID, err)
	}

	result := FocusLossEvaluation{
		ExamID:              target.ExamID,
		SittingID:           target.SittingID,
		CandidateUserID:     target.CandidateUserID,
		SubmissionID:        target.SubmissionID,
		AttemptID:           target.AttemptID,
		ParticipationID:     target.ParticipationID,
		DiscrepancyID:       stored.Discrepancy.ID,
		Generation:          target.Generation,
		AcceptedSequence:    stored.Discrepancy.Sequence,
		ReceivedAt:          stored.Discrepancy.ReceivedAt,
		Duplicate:           stored.Duplicate,
		GapDetected:         stored.Discrepancy.MissingBefore > 0,
		DiscrepancyRecorded: true,
	}
	if !result.Duplicate {
		if effectErr := s.publishFocusLossEvaluated(ctx, result); effectErr != nil {
			s.publisher.Report(ctx, "exam_attempt_focus_loss_discrepancy_recorded", effectErr)
		}
	}

	return result, nil
}

func focusLossSelector(inv invocation.Invocation, command FocusLossCommand) (store.ExamAttemptFocusLossAccess, error) {
	selector, err := candidateSelector(inv, CandidateAccess{
		AttemptID:            command.AttemptID,
		ConnectionID:         command.ConnectionID,
		ContinuityCredential: command.ContinuityCredential,
	})
	if err != nil {
		return store.ExamAttemptFocusLossAccess{}, err
	}

	if command.SchemaVersion != model.FocusLossSignalSchemaVersion ||
		!command.ParticipationID.IsValid() ||
		command.Generation < 1 ||
		command.Sequence < 1 ||
		command.DurationMilliseconds < 1 ||
		command.DurationMilliseconds > model.FocusLossMaximumDurationMilliseconds ||
		!command.Source.IsValid() {
		return store.ExamAttemptFocusLossAccess{}, apperror.Invalid("exam.attempt.invalid", "focus_loss")
	}

	return store.ExamAttemptFocusLossAccess{
		AttemptID:                selector.AttemptID,
		ParticipationID:          command.ParticipationID,
		Generation:               command.Generation,
		CandidateUserID:          selector.CandidateUserID,
		SessionID:                selector.SessionID,
		ConnectionID:             selector.ConnectionID,
		ContinuityCredentialHash: selector.ContinuityCredentialHash,
	}, nil
}

func validFocusLossTarget(target *store.ExamAttemptFocusLossTarget, focusLossAccess store.ExamAttemptFocusLossAccess) bool {
	return target != nil &&
		target.ExamID.IsValid() &&
		target.SittingID.IsValid() &&
		target.ClassID.IsValid() &&
		target.CandidateUserID == focusLossAccess.CandidateUserID &&
		target.AttemptID == focusLossAccess.AttemptID &&
		target.ParticipationID == focusLossAccess.ParticipationID &&
		target.Generation == focusLossAccess.Generation
}

func projectFocusLoss(stored *store.ExamAttemptFocusLossResult, command FocusLossCommand) (FocusLossEvaluation, error) {
	if !stored.Accepted(command.Sequence) {
		return FocusLossEvaluation{}, apperror.New("exam.attempt.unavailable").Wrap(errors.New("inconsistent Focus Loss outcome"))
	}

	if !validFocusLossEnforcement(stored) {
		return FocusLossEvaluation{}, apperror.New("exam.attempt.unavailable").Wrap(errors.New("inconsistent Focus Loss enforcement outcome"))
	}

	result := FocusLossEvaluation{
		ExamID:                      stored.ExamID,
		SittingID:                   stored.SittingID,
		CandidateUserID:             stored.CandidateUserID,
		AttemptID:                   stored.AttemptID,
		ParticipationID:             stored.ParticipationID,
		Generation:                  stored.Generation,
		AcceptedSequence:            stored.AcceptedSequence,
		ReceivedAt:                  stored.DatabaseTime,
		Duplicate:                   stored.Duplicate,
		GapDetected:                 stored.MissingBefore > 0,
		PolicyDisabled:              !stored.CollectionEnabled,
		Qualified:                   stored.Qualified,
		ThresholdCrossed:            stored.ThresholdCrossed,
		PolicyOutcome:               stored.PolicyOutcome,
		RetainedEvidenceCount:       stored.RetainedEvidenceCount,
		FlagCreated:                 stored.FlagCreated,
		CandidateWarningCreated:     stored.CandidateWarningCreated,
		ManagerNotificationRequired: stored.ManagerNotificationRequired,
		ConnectionClosed:            stored.ConnectionClosed,
	}
	if stored.Flag != nil {
		result.Flag = *stored.Flag
	}
	if stored.Overflow != nil {
		result.EvidenceOverflowCount = stored.Overflow.Count
	}
	if stored.Attempt != nil {
		result.Attempt = *stored.Attempt
	}
	if stored.Participation != nil {
		result.Participation = *stored.Participation
	}
	if stored.Connection != nil {
		result.Connection = *stored.Connection
	}

	if stored.Suspension != nil {
		result.Suspension = *stored.Suspension
		result.SuspensionCreated = true
	}

	return result, nil
}

func validFocusLossEnforcement(result *store.ExamAttemptFocusLossResult) bool {
	if !result.CollectionEnabled {
		return !result.Qualified &&
			!result.ThresholdCrossed &&
			result.PolicyOutcome == "" &&
			result.Flag == nil &&
			result.Attempt == nil &&
			result.Participation == nil &&
			result.Connection == nil &&
			result.Suspension == nil &&
			!result.FlagCreated &&
			!result.CandidateWarningCreated &&
			!result.ManagerNotificationRequired &&
			!result.ConnectionClosed
	}

	if !result.Qualified &&
		(result.ThresholdCrossed ||
			result.PolicyOutcome != "" ||
			result.Flag != nil ||
			result.FlagCreated ||
			result.CandidateWarningCreated ||
			result.ManagerNotificationRequired ||
			result.Attempt != nil ||
			result.Participation != nil ||
			result.Connection != nil ||
			result.ConnectionClosed ||
			result.Suspension != nil) {
		return false
	}

	if (result.FlagCreated ||
		result.ManagerNotificationRequired ||
		result.CandidateWarningCreated ||
		result.Suspension != nil ||
		result.ConnectionClosed) &&
		(!result.ThresholdCrossed || !result.Qualified) {
		return false
	}

	if result.ThresholdCrossed {
		switch result.PolicyOutcome {
		case model.IntegrityOutcomeFlag, model.IntegrityOutcomeFlagAndWarn, model.IntegrityOutcomeFlagAndSuspend:
		default:
			return false
		}

		if result.Flag == nil || result.RetainedEvidenceCount == 0 && result.Overflow == nil {
			return false
		}
	} else if result.PolicyOutcome != "" {
		return false
	}

	if result.FlagCreated != result.ManagerNotificationRequired ||
		result.CandidateWarningCreated &&
			(!result.ThresholdCrossed || result.PolicyOutcome != model.IntegrityOutcomeFlagAndWarn) {
		return false
	}

	if result.Participation != nil && !result.Participation.LifecycleInOrder() {
		return false
	}

	if result.Connection != nil && !validFocusLossConnection(result.Connection) {
		return false
	}

	if result.ConnectionClosed &&
		(result.Connection == nil ||
			result.Connection.State != model.AttemptConnectionClosed ||
			result.Connection.CloseReason != model.AttemptConnectionClosePolicySuspended) {
		return false
	}

	if result.Suspension != nil &&
		(!result.Suspension.ActiveOrAbsent() ||
			result.Suspension.CandidateReason != model.AttemptSuspensionCandidateReasonFocusLossPolicy) {
		return false
	}

	if result.PolicyOutcome == model.IntegrityOutcomeFlagAndSuspend && result.ThresholdCrossed {
		return result.Attempt.State == model.ExamAttemptSuspended &&
			result.Participation.State == model.AttemptParticipationEnded &&
			result.Participation.EndReason == model.AttemptParticipationEndPolicySuspended &&
			result.Connection.State == model.AttemptConnectionClosed &&
			result.Suspension != nil
	}

	return result.Suspension == nil && !result.ConnectionClosed
}

func validFocusLossConnection(connection *store.ExamAttemptManagerConnection) bool {
	switch connection.State {
	case model.AttemptConnectionOpen:
		return !connection.ClosedAt.Valid && connection.CloseReason == ""
	case model.AttemptConnectionClosed:
		return connection.ClosedAt.Valid && !connection.ClosedAt.Time.Before(connection.OpenedAt)
	default:
		return false
	}
}
