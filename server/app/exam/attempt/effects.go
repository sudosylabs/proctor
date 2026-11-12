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

	"github.com/sudosylabs/proctor/server/app/realtime"
	"github.com/sudosylabs/proctor/server/model"
	"github.com/sudosylabs/proctor/server/store"
)

// publishCandidateActivityChanged tells the candidate's clients to refetch
// their Exam activity.
func (s *Service) publishCandidateActivityChanged(ctx context.Context,
	candidateID model.UserID,
) error {
	event, err := realtime.NewCandidateExamActivityChangedEvent(candidateID)
	if err != nil {
		return err
	}
	return s.publisher.Publish(ctx, event)
}

// publishOperationalInvalidations follows a change to what an Attempt may do,
// in this order: the candidate's Exam activity, the Sitting board, and the
// candidate's current-user context. A failure never stops the ones after it.
func (s *Service) publishOperationalInvalidations(
	ctx context.Context,
	examID model.ExamID,
	sittingID model.ExamSittingID,
	candidateID model.UserID,
) error {
	return errors.Join(
		s.publishCandidateActivityChanged(ctx, candidateID),
		realtime.PublishSittingBoardChanged(ctx, s.publisher, examID, sittingID),
		s.publisher.InvalidateCurrentUserContext(ctx, candidateID),
	)
}

// publishConnectionOpened first closes the Sessions and drops the cached
// access tokens that admission revoked, when there are any, and only then
// announces the opened Attempt Connection to managers, followed by the
// operational invalidations.
func (s *Service) publishConnectionOpened(ctx context.Context, result ConnectionResult) error {
	if len(result.RevokedSessionIDs) > 0 || len(result.RevokedAccessTokenDigests) > 0 {
		s.publisher.SessionsRevoked(ctx, result.Attempt.CandidateUserID, result.RevokedSessionIDs, result.RevokedAccessTokenDigests)
	}

	event, err := realtime.NewExamAttemptConnectionOpenedEvent(
		result.Attempt.SittingID,
		result.Attempt.ID,
		result.Attempt.CandidateUserID,
		result.Connection.ID,
		result.Connection.OpenedAt,
	)
	if err != nil {
		return err
	}

	return errors.Join(s.publisher.Publish(ctx, event), s.publishOperationalInvalidations(
		ctx,
		result.Attempt.ExamID,
		result.Attempt.SittingID,
		result.Attempt.CandidateUserID,
	))
}

// publishConnectionClosed tells managers which Attempt Connection closed and
// why, then invalidates the Sitting board.
func (s *Service) publishConnectionClosed(ctx context.Context, result ConnectionClosedResult) error {
	event, err := realtime.NewExamAttemptConnectionClosedEvent(
		result.SittingID,
		result.AttemptID,
		result.CandidateUserID,
		result.Connection.ID,
		result.Connection.CloseReason,
		result.Connection.ClosedAt.Time,
	)
	if err != nil {
		return err
	}

	return errors.Join(s.publisher.Publish(ctx, event),
		realtime.PublishSittingBoardChanged(ctx, s.publisher, result.ExamID, result.SittingID))
}

// publishParticipationExpired announces a Participation whose lease ran out
// and the policy Suspension it caused. When the expiry itself closed the
// Attempt Connection, managers learn of the close before the Suspension, and
// the Connection is unbound on every node only after every event and
// invalidation has been attempted, so the Desktop client still receives its
// Suspension. When the transport had already closed the Connection, there is
// no second close and nothing to unbind. Nothing is published unless every
// event can be built.
func (s *Service) publishParticipationExpired(ctx context.Context, result ParticipationExpiry) error {
	outcome := integrityOutcome{
		SittingID:       result.SittingID,
		AttemptID:       result.Attempt.ID,
		CandidateUserID: result.CandidateUserID,
		At:              result.DatabaseTime,
		Suspension: &integritySuspensionNotice{
			ConnectionID:    result.Connection.ID,
			FlagID:          result.Flag.ID,
			Suspension:      result.Suspension,
			AttemptRevision: result.Attempt.Revision,
		},
		Invalidate: func() error {
			return s.publishOperationalInvalidations(ctx, result.ExamID, result.SittingID, result.CandidateUserID)
		},
	}
	if result.ConnectionClosed {
		outcome.ClosedConnection = &result.Connection
	}

	return s.publishIntegrityOutcome(ctx, outcome)
}

// publishAttemptReallowed tells managers and then the candidate that a
// suspended Attempt may continue, followed by the operational invalidations.
func (s *Service) publishAttemptReallowed(ctx context.Context, result ReallowResult) error {
	managerEvent, err := realtime.NewExamAttemptReallowedEvent(
		result.SittingID,
		result.Attempt.ID,
		result.CandidateUserID,
		result.Suspension.ID,
		result.Attempt.Revision,
		result.Attempt.UpdatedAt,
	)
	if err != nil {
		return err
	}

	candidateEvent, err := realtime.NewCandidateExamAttemptReallowedEvent(
		result.SittingID,
		result.Attempt.ID,
		result.CandidateUserID,
		result.Attempt.UpdatedAt,
	)
	if err != nil {
		return err
	}

	return errors.Join(
		s.publisher.Publish(ctx, managerEvent),
		s.publisher.Publish(ctx, candidateEvent),
		s.publishOperationalInvalidations(ctx, result.ExamID, result.SittingID, result.CandidateUserID),
	)
}

// publishAttemptSuspendedByManager publishes a committed kick: the closed live
// Connection, if any, the manager and candidate suspension facts, operational
// invalidations, and removal of the exact live Connection binding.
func (s *Service) publishAttemptSuspendedByManager(ctx context.Context, result ManualSuspensionResult) error {
	var connectionID model.AttemptConnectionID
	if result.Connection != nil {
		connectionID = result.Connection.ID
	}

	managerEvent, err := realtime.NewExamAttemptKickedEvent(
		result.SittingID,
		result.Attempt.ID,
		result.CandidateUserID,
		connectionID,
		result.Suspension.ID,
		result.Attempt.Revision,
		result.Suspension.StartedAt,
	)
	if err != nil {
		return err
	}

	candidateEvent, err := realtime.NewCandidateExamAttemptSuspendedEvent(
		result.SittingID,
		result.Attempt.ID,
		result.CandidateUserID,
		result.Suspension.Source,
		result.Suspension.CandidateReason,
		result.Suspension.StartedAt,
	)
	if err != nil {
		return err
	}

	if !result.ConnectionClosed || result.Connection == nil {
		return errors.Join(
			s.publisher.Publish(ctx, managerEvent),
			s.publisher.Publish(ctx, candidateEvent),
			s.publishOperationalInvalidations(ctx, result.ExamID, result.SittingID, result.CandidateUserID),
		)
	}

	connectionEvent, err := realtime.NewExamAttemptConnectionClosedEvent(
		result.SittingID,
		result.Attempt.ID,
		result.CandidateUserID,
		result.Connection.ID,
		result.Connection.CloseReason,
		result.Connection.ClosedAt.Time,
	)
	if err != nil {
		return err
	}

	return errors.Join(
		s.publisher.Publish(ctx, connectionEvent),
		s.publisher.Publish(ctx, managerEvent),
		s.publisher.Publish(ctx, candidateEvent),
		s.publishOperationalInvalidations(
			ctx,
			result.ExamID,
			result.SittingID,
			result.CandidateUserID,
		),
		s.publisher.UnbindExamAttemptConnection(ctx, result.Connection.ID),
	)
}

// publishWorkspaceChanged tells the candidate's other clients which Attempt
// Workspace entry changed and at which journal cursor. The event carries no
// path or content.
func (s *Service) publishWorkspaceChanged(ctx context.Context, result WorkspaceMutationResult) error {
	event, err := realtime.NewCandidateExamAttemptWorkspaceChangedEvent(
		result.SittingID,
		result.AttemptID,
		result.CandidateUserID,
		result.Change.EntryID,
		result.Change.Operation,
		result.Change.Cursor,
		result.Change.ChangedAt,
	)
	if err != nil {
		return err
	}

	return errors.Join(s.publisher.Publish(ctx, event))
}

// publishFocusLossEvaluated announces what one Focus Loss report caused. A
// late report against a sealed Submission publishes only the recorded
// discrepancy and a Sitting board invalidation. Otherwise it publishes the
// integrity outcome; its invalidations are the Sitting board and the
// candidate's Exam activity.
func (s *Service) publishFocusLossEvaluated(ctx context.Context, result FocusLossEvaluation) error {
	if result.DiscrepancyRecorded {
		event, err := realtime.NewExamIntegrityDiscrepancyRecordedEvent(
			result.SubmissionID,
			result.SittingID,
			result.AttemptID,
			result.CandidateUserID,
			result.DiscrepancyID,
			result.ReceivedAt,
		)
		if err != nil {
			return err
		}

		return errors.Join(s.publisher.Publish(ctx, event),
			realtime.PublishSittingBoardChanged(ctx, s.publisher, result.ExamID, result.SittingID))
	}

	outcome := integrityOutcome{
		SittingID:       result.SittingID,
		AttemptID:       result.AttemptID,
		CandidateUserID: result.CandidateUserID,
		At:              result.ReceivedAt,
		Warning:         result.CandidateWarningCreated,
		Invalidate: func() error {
			var joined error
			if result.GapDetected || result.FlagCreated || result.SuspensionCreated {
				joined = realtime.PublishSittingBoardChanged(ctx, s.publisher, result.ExamID, result.SittingID)
			}
			if result.SuspensionCreated {
				joined = errors.Join(joined, s.publishCandidateActivityChanged(ctx, result.CandidateUserID))
			}
			return joined
		},
	}
	if result.ConnectionClosed {
		outcome.ClosedConnection = &result.Connection
	}
	if result.ManagerNotificationRequired {
		outcome.Flagged = &integrityFlagNotice{
			FlagID:           result.Flag.ID,
			Kind:             model.IntegrityPolicyFocusLoss,
			Outcome:          result.PolicyOutcome,
			RetainedEvidence: result.RetainedEvidenceCount,
			EvidenceOverflow: result.EvidenceOverflowCount,
		}
	}
	if result.SuspensionCreated {
		outcome.Suspension = &integritySuspensionNotice{
			ConnectionID:    result.Connection.ID,
			FlagID:          result.Flag.ID,
			Suspension:      result.Suspension,
			AttemptRevision: result.Attempt.Revision,
		}
	}

	return s.publishIntegrityOutcome(ctx, outcome)
}

// integrityOutcome is what one integrity policy decision caused, as the
// realtime effects every policy publishes the same way.
type integrityOutcome struct {
	SittingID       model.ExamSittingID
	AttemptID       model.ExamAttemptID
	CandidateUserID model.UserID
	At              time.Time
	// ClosedConnection is the Connection this decision closed, or nil.
	ClosedConnection *store.ExamAttemptManagerConnection
	// Flagged tells managers of the Flag, or is nil when they are not told.
	Flagged *integrityFlagNotice
	// Suspension is the policy Suspension this decision opened, or nil.
	Suspension *integritySuspensionNotice
	// Warning is whether the candidate is warned.
	Warning bool
	// Invalidate publishes the policy's own invalidations, after the events
	// and before the closed Connection is unbound.
	Invalidate func() error
}

type integrityFlagNotice struct {
	FlagID           model.IntegrityFlagID
	Kind             model.IntegrityPolicyKind
	Outcome          model.IntegrityOutcome
	RetainedEvidence int
	EvidenceOverflow int64
}

type integritySuspensionNotice struct {
	ConnectionID    model.AttemptConnectionID
	FlagID          model.IntegrityFlagID
	Suspension      store.ExamAttemptSuspensionView
	AttemptRevision int64
}

// publishIntegrityOutcome publishes what an integrity decision caused, each
// event only when it applies and always in this order: the closed Attempt
// Connection, the Integrity Flag to managers, the policy Suspension to
// managers and then to the candidate, and the candidate's warning. The
// policy's invalidations follow, and the closed Connection is unbound on
// every node last, so the Desktop client still receives its Suspension.
// Nothing is published unless every event that applies can be built.
func (s *Service) publishIntegrityOutcome(ctx context.Context, outcome integrityOutcome) error {
	events := make([]realtime.Event, 0, 5)
	if connection := outcome.ClosedConnection; connection != nil {
		event, err := realtime.NewExamAttemptConnectionClosedEvent(
			outcome.SittingID,
			outcome.AttemptID,
			outcome.CandidateUserID,
			connection.ID,
			connection.CloseReason,
			connection.ClosedAt.Time,
		)
		if err != nil {
			return err
		}

		events = append(events, event)
	}

	if flagged := outcome.Flagged; flagged != nil {
		event, err := realtime.NewExamAttemptIntegrityFlaggedEvent(
			outcome.SittingID,
			outcome.AttemptID,
			outcome.CandidateUserID,
			flagged.FlagID,
			flagged.Kind,
			flagged.Outcome,
			flagged.RetainedEvidence,
			flagged.EvidenceOverflow,
			outcome.At,
		)
		if err != nil {
			return err
		}

		events = append(events, event)
	}

	if suspended := outcome.Suspension; suspended != nil {
		managerEvent, err := realtime.NewExamAttemptPolicySuspendedEvent(
			outcome.SittingID,
			outcome.AttemptID,
			outcome.CandidateUserID,
			suspended.ConnectionID,
			suspended.FlagID,
			suspended.Suspension.ID,
			suspended.AttemptRevision,
			outcome.At,
		)
		if err != nil {
			return err
		}

		candidateEvent, err := realtime.NewCandidateExamAttemptSuspendedEvent(
			outcome.SittingID,
			outcome.AttemptID,
			outcome.CandidateUserID,
			suspended.Suspension.Source,
			suspended.Suspension.CandidateReason,
			outcome.At,
		)
		if err != nil {
			return err
		}

		events = append(events, managerEvent, candidateEvent)
	}

	if outcome.Warning {
		event, err := realtime.NewCandidateExamAttemptFocusLossWarningEvent(
			outcome.SittingID,
			outcome.AttemptID,
			outcome.CandidateUserID,
			outcome.At,
		)
		if err != nil {
			return err
		}

		events = append(events, event)
	}

	var joined error
	for _, event := range events {
		joined = errors.Join(joined, s.publisher.Publish(ctx, event))
	}
	if outcome.Invalidate != nil {
		joined = errors.Join(joined, outcome.Invalidate())
	}
	if outcome.ClosedConnection != nil {
		joined = errors.Join(joined, s.publisher.UnbindExamAttemptConnection(ctx, outcome.ClosedConnection.ID))
	}

	return joined
}

// publishAttemptSubmitted announces a sealed Submission to managers and to
// the candidate, follows with the operational invalidations, and last
// unbinds the Attempt Connection that submitted.
func (s *Service) publishAttemptSubmitted(ctx context.Context, result SubmissionResult) error {
	publishErr, constructionErr := s.publishSubmittedFacts(ctx, result)
	if constructionErr != nil {
		return constructionErr
	}

	return errors.Join(publishErr, s.publishOperationalInvalidations(
		ctx,
		result.ExamID,
		result.SittingID,
		result.CandidateUserID,
	), s.publisher.UnbindExamAttemptConnection(ctx, result.ConnectionID))
}

// publishSubmittedFacts publishes the manager and candidate Submission facts,
// managers first. constructionErr reports an event that could not be built,
// in which case nothing was published.
func (s *Service) publishSubmittedFacts(ctx context.Context,
	result SubmissionResult,
) (publishErr, constructionErr error) {
	managerEvent, err := realtime.NewExamAttemptSubmittedEvent(
		result.SittingID,
		result.Receipt.AttemptID,
		result.CandidateUserID,
		result.Receipt.SubmissionID,
		result.Receipt.WorkspaceCursor,
		result.Receipt.ManifestDigest,
		result.Provenance,
		result.Receipt.SubmittedAt,
	)
	if err != nil {
		return nil, err
	}

	candidateEvent, err := realtime.NewCandidateExamAttemptSubmittedEvent(
		result.SittingID,
		result.Receipt.AttemptID,
		result.CandidateUserID,
		result.Receipt.SubmissionID,
		result.Receipt.WorkspaceCursor,
		result.Receipt.ManifestDigest,
		result.Provenance,
		result.Receipt.SubmittedAt,
	)
	if err != nil {
		return nil, err
	}

	return errors.Join(s.publisher.Publish(ctx, managerEvent), s.publisher.Publish(ctx, candidateEvent)), nil
}

// publishAttemptSealedForSittingClose announces a Submission sealed because
// its Sitting closed: the manager and candidate Submission facts, the closed
// Attempt Connection when sealing closed one, the operational invalidations,
// and last the unbinding of that Connection.
func (s *Service) publishAttemptSealedForSittingClose(ctx context.Context, result AutomaticSubmissionResult) error {
	publishErr, constructionErr := s.publishSubmittedFacts(ctx, result.SubmissionResult)
	if constructionErr != nil {
		return constructionErr
	}

	if result.ConnectionClosed {
		connectionEvent, eventErr := realtime.NewExamAttemptConnectionClosedEvent(
			result.SittingID,
			result.Receipt.AttemptID,
			result.CandidateUserID,
			result.ConnectionID,
			model.AttemptConnectionCloseSittingClosed,
			result.Receipt.SubmittedAt,
		)
		if eventErr != nil {
			return eventErr
		}

		publishErr = errors.Join(publishErr, s.publisher.Publish(ctx, connectionEvent))
	}

	return errors.Join(publishErr, s.publishOperationalInvalidations(
		ctx,
		result.ExamID,
		result.SittingID,
		result.CandidateUserID,
	), s.publisher.UnbindExamAttemptConnection(ctx, result.ConnectionID))
}
