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

// ExpiryScanResult counts one bounded expiry scan; replays are counted among
// the committed.
type ExpiryScanResult struct {
	Due       int
	Completed int
	Replayed  int
}

// ParticipationExpiry is the durable outcome of one connection loss, decided
// at DatabaseTime.
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

// ScanExpiredParticipations performs one bounded pass. Each listed item is
// rechecked by a conditional store operation; the database, never this loop
// or its clock, decides expiry.
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

// expireLapsedParticipation records connection loss exactly as the scanner
// would for a generation whose lapse a store operation refused to act on (a
// late renewal, a kick), rather than waiting for the next pass. It returns
// connection_lost once the transition is durable or another path already
// ended the generation.
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

func expiredAttemptMatches(attempt *model.ExamAttempt) bool {
	return attempt.State == model.ExamAttemptSuspended
}

func expiredParticipationMatches(participation *store.ExamAttemptParticipationView) bool {
	return participation.LifecycleInOrder() &&
		participation.State == model.AttemptParticipationEnded &&
		participation.EndReason == model.AttemptParticipationEndLeaseExpired
}

// closedByExpiry is true exactly when the expiry itself closed the connection.
func expiryConnectionClosed(connection *store.ExamAttemptManagerConnection, closedByExpiry bool) bool {
	return connection.State == model.AttemptConnectionClosed &&
		connection.ClosedAt.Valid &&
		!connection.ClosedAt.Time.Before(connection.OpenedAt) &&
		closedByExpiry == (connection.CloseReason == model.AttemptConnectionCloseLeaseExpired)
}

func connectionLossSuspensionMatches(suspension *store.ExamAttemptSuspensionView) bool {
	return suspension.State == model.AttemptSuspensionActive &&
		suspension.Source == model.AttemptSuspensionSourcePolicy &&
		suspension.CandidateReason == model.AttemptSuspensionCandidateReasonSecureContinuityLost &&
		!suspension.EndedAt.Valid &&
		suspension.ReallowedByUserID.IsZero()
}

func expiryDecidedAfterLease(databaseTime time.Time, due store.ExamAttemptParticipationExpiryDue) bool {
	return !databaseTime.Before(due.LeaseExpiresAt)
}
