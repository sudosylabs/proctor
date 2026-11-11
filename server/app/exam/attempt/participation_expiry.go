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

	"github.com/sudosylabs/proctor/server/app/apperror"
	"github.com/sudosylabs/proctor/server/app/invocation"
	"github.com/sudosylabs/proctor/server/model"
	"github.com/sudosylabs/proctor/server/store"
)

// ExpiryScanResult counts one bounded expiry scan: the items found due, those
// whose expiry committed, and how many of the committed were replays of an
// expiry already recorded.
type ExpiryScanResult struct {
	Due       int
	Completed int
	Replayed  int
}

// ParticipationExpiry is the durable outcome of one Connection Loss: the
// suspended Attempt, its expired Attempt Participation and latest Attempt
// Connection, and the Integrity Evidence, Integrity Flag, and policy Attempt
// Suspension recorded for it, decided at DatabaseTime.
type ParticipationExpiry struct {
	ExamID           model.ExamID
	SittingID        model.ExamSittingID
	ClassID          model.ClassID
	CandidateUserID  model.UserID
	Attempt          model.ExamAttempt
	Participation    store.ExamAttemptParticipationView
	Connection       store.ExamAttemptManagerConnection
	ConnectionClosed bool
	Evidence         model.IntegrityEvidence
	Flag             model.IntegrityFlag
	Suspension       store.ExamAttemptSuspensionView
	DatabaseTime     time.Time
	Replayed         bool
}

// ScanExpiredParticipations performs one bounded runtime maintenance pass.
// Every listed candidate is rechecked by the named conditional Store
// operation; PostgreSQL, never this loop or its clock, decides expiry.
func (s *Service) ScanExpiredParticipations(ctx context.Context, system invocation.System, limit int) (ExpiryScanResult, error) {
	if !system.Valid() || limit < 1 || limit > 200 {
		return ExpiryScanResult{}, apperror.Invalid("exam.attempt.invalid", "expiry_limit")
	}
	due, err := s.attempts.ListExpiredParticipations(ctx, limit)
	if err != nil {
		return ExpiryScanResult{}, storeError(storeErrors, err)
	}
	result := ExpiryScanResult{Due: len(due)}
	for _, item := range due {
		expired, expireErr := s.expireParticipation(ctx, item)
		if expireErr != nil {
			return result, expireErr
		}
		result.Completed++
		if expired.Replayed {
			result.Replayed++
		}
	}
	return result, nil
}

func (s *Service) expireParticipation(ctx context.Context, due store.ExamAttemptParticipationExpiryDue) (ParticipationExpiry, error) {
	if !validExpiryDue(due) {
		return ParticipationExpiry{}, apperror.New("exam.attempt.unavailable").Wrap(errors.New("invalid Participation expiry target"))
	}

	auditID, err := s.systemAuditor.Begin(
		ctx,
		model.ActionExamSittingManage,
		model.Resource{Type: model.ResourceExamSitting, ID: due.SittingID.String()},
		model.RoleScopeClass,
		due.ClassID.String(),
		store.ExamAttemptExpireParticipationOperation,
		map[string]any{
			"exam_id":          due.ExamID.String(),
			"exam_sitting_id":  due.SittingID.String(),
			"exam_attempt_id":  due.AttemptID.String(),
			"participation_id": due.ParticipationID.String(),
			"generation":       due.Generation,
		},
	)
	if err != nil {
		return ParticipationExpiry{}, err
	}

	at := model.TimeUTC(s.now())
	stored, err := s.attempts.ExpireParticipation(ctx, &store.ExamAttemptParticipationExpiry{
		AttemptID:       due.AttemptID,
		ParticipationID: due.ParticipationID,
		Generation:      due.Generation,
		EvidenceID:      s.newEvidenceID(),
		FlagID:          s.newFlagID(),
		SuspensionID:    s.newSuspensionID(),
		AuditEventID:    auditID,
		AuditAt:         at,
	})
	if err != nil {
		mapped := storeError(storeErrors, err)
		code := apperror.CodeOf(mapped, "exam.attempt.unavailable")
		if auditErr := s.systemAuditor.Fail(ctx, auditID, code); auditErr != nil {
			return ParticipationExpiry{}, errors.Join(mapped, auditErr)
		}

		return ParticipationExpiry{}, mapped
	}

	result, err := projectExpiry(stored, due)
	if err != nil {
		return ParticipationExpiry{}, err
	}

	if !result.Replayed {
		if effectErr := s.publishParticipationExpired(ctx, result); effectErr != nil {
			s.publisher.Report(ctx, "exam_attempt_participation_expired", effectErr)
		}
	}

	return result, nil
}

// expireLapsedParticipation completes the expiry transition for a generation
// whose lapse a Store operation refused to act on (a late renewal, a kick),
// so Connection Loss is recorded exactly as the scanner would record it
// rather than waiting for its next pass. It returns the caller's stable
// connection_lost failure once the transition is durable, or once another
// path already ended the generation.
func (s *Service) expireLapsedParticipation(
	ctx context.Context,
	attemptID model.ExamAttemptID,
	participationID model.AttemptParticipationID,
	generation int64,
	cause error,
) error {
	due, err := s.attempts.ResolveParticipationExpiry(ctx, attemptID, participationID, generation)
	if err != nil {
		if store.IsNotFound(err) {
			return apperror.New("exam.attempt.connection_lost").Wrap(cause)
		}
		return storeError(storeErrors, err)
	}

	if _, err = s.expireParticipation(ctx, *due); err != nil {
		return err
	}

	return apperror.New("exam.attempt.connection_lost").Wrap(cause)
}

func validExpiryDue(due store.ExamAttemptParticipationExpiryDue) bool {
	return due.ExamID.IsValid() &&
		due.SittingID.IsValid() &&
		due.ClassID.IsValid() &&
		due.CandidateUserID.IsValid() &&
		due.AttemptID.IsValid() &&
		due.ParticipationID.IsValid() &&
		due.Generation > 0 &&
		!due.LeaseExpiresAt.IsZero()
}

func projectExpiry(
	stored *store.ExamAttemptParticipationExpiryResult,
	due store.ExamAttemptParticipationExpiryDue,
) (ParticipationExpiry, error) {
	if !expiredAttemptMatches(stored.Attempt) ||
		!expiredParticipationMatches(stored.Participation) ||
		!expiryConnectionClosed(stored.Connection, stored.ConnectionClosed) ||
		!connectionLossSuspensionMatches(stored.Suspension) ||
		!expiryDecidedAfterLease(stored.DatabaseTime, due) {
		return ParticipationExpiry{}, apperror.New("exam.attempt.unavailable").Wrap(errors.New("inconsistent Participation expiry outcome"))
	}

	return ParticipationExpiry{
		ExamID:           stored.ExamID,
		SittingID:        stored.SittingID,
		ClassID:          stored.ClassID,
		CandidateUserID:  stored.CandidateUserID,
		Attempt:          *stored.Attempt,
		Participation:    *stored.Participation,
		Connection:       *stored.Connection,
		ConnectionClosed: stored.ConnectionClosed,
		Evidence:         *stored.Evidence,
		Flag:             *stored.Flag,
		Suspension:       *stored.Suspension,
		DatabaseTime:     model.TimeUTC(stored.DatabaseTime),
		Replayed:         stored.Replayed,
	}, nil
}

// expiredAttemptMatches reports whether the Attempt is suspended.
func expiredAttemptMatches(attempt *model.ExamAttempt) bool {
	return attempt.State == model.ExamAttemptSuspended
}

// expiredParticipationMatches reports whether the generation ended in order,
// as lease_expired.
func expiredParticipationMatches(participation *store.ExamAttemptParticipationView) bool {
	return participation.LifecycleInOrder() &&
		participation.State == model.AttemptParticipationEnded &&
		participation.EndReason == model.AttemptParticipationEndLeaseExpired
}

// expiryConnectionClosed reports whether the generation's latest Connection
// is closed in order, and closedByExpiry is true exactly when the expiry
// itself closed it.
func expiryConnectionClosed(connection *store.ExamAttemptManagerConnection, closedByExpiry bool) bool {
	return connection.State == model.AttemptConnectionClosed &&
		connection.ClosedAt.Valid &&
		!connection.ClosedAt.Time.Before(connection.OpenedAt) &&
		closedByExpiry == (connection.CloseReason == model.AttemptConnectionCloseLeaseExpired)
}

// connectionLossSuspensionMatches reports whether the episode is an active,
// unreallowed Connection Loss policy suspension.
func connectionLossSuspensionMatches(suspension *store.ExamAttemptSuspensionView) bool {
	return suspension.State == model.AttemptSuspensionActive &&
		suspension.Source == model.AttemptSuspensionSourcePolicy &&
		suspension.CandidateReason == model.AttemptSuspensionCandidateReasonSecureContinuityLost &&
		!suspension.EndedAt.Valid &&
		suspension.ReallowedByUserID.IsZero()
}

// expiryDecidedAfterLease reports whether PostgreSQL decided the expiry at or
// after the lease deadline.
func expiryDecidedAfterLease(databaseTime time.Time, due store.ExamAttemptParticipationExpiryDue) bool {
	return !databaseTime.Before(due.LeaseExpiresAt)
}
