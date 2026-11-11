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

	"github.com/sudosylabs/proctor/server/app/realtime"
	"github.com/sudosylabs/proctor/server/model"
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
	managerEvent, err := realtime.NewExamAttemptPolicySuspendedEvent(
		result.SittingID,
		result.Attempt.ID,
		result.CandidateUserID,
		result.Connection.ID,
		result.Flag.ID,
		result.Suspension.ID,
		result.Attempt.Revision,
		result.DatabaseTime,
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
		result.DatabaseTime,
	)
	if err != nil {
		return err
	}

	if !result.ConnectionClosed {
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
// discrepancy and a Sitting board invalidation. Otherwise the events go out
// in this order, each only when it applies: the closed Attempt Connection,
// the Integrity Flag to managers, the policy Suspension to managers and then
// to the candidate, and the candidate's warning. The Sitting board, the
// candidate's Exam activity and the unbinding of the closed Connection follow.
// Nothing is published unless every event that applies can be built.
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

	events := make([]realtime.Event, 0, 5)
	if result.ConnectionClosed {
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

		events = append(events, event)
	}

	if result.ManagerNotificationRequired {
		event, err := realtime.NewExamAttemptIntegrityFlaggedEvent(
			result.SittingID,
			result.AttemptID,
			result.CandidateUserID,
			result.Flag.ID,
			result.PolicyOutcome,
			result.RetainedEvidenceCount,
			result.EvidenceOverflowCount,
			result.ReceivedAt,
		)
		if err != nil {
			return err
		}

		events = append(events, event)
	}

	if result.SuspensionCreated {
		managerEvent, err := realtime.NewExamAttemptPolicySuspendedEvent(
			result.SittingID,
			result.AttemptID,
			result.CandidateUserID,
			result.Connection.ID,
			result.Flag.ID,
			result.Suspension.ID,
			result.Attempt.Revision,
			result.ReceivedAt,
		)
		if err != nil {
			return err
		}

		candidateEvent, err := realtime.NewCandidateExamAttemptSuspendedEvent(
			result.SittingID,
			result.AttemptID,
			result.CandidateUserID,
			result.Suspension.Source,
			result.Suspension.CandidateReason,
			result.ReceivedAt,
		)
		if err != nil {
			return err
		}

		events = append(events, managerEvent, candidateEvent)
	}

	if result.CandidateWarningCreated {
		event, err := realtime.NewCandidateExamAttemptFocusLossWarningEvent(
			result.SittingID,
			result.AttemptID,
			result.CandidateUserID,
			result.ReceivedAt,
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

	if result.GapDetected || result.FlagCreated || result.SuspensionCreated {
		joined = errors.Join(joined, realtime.PublishSittingBoardChanged(ctx, s.publisher, result.ExamID, result.SittingID))
	}
	if result.SuspensionCreated {
		joined = errors.Join(joined, s.publishCandidateActivityChanged(ctx, result.CandidateUserID))
	}
	if result.ConnectionClosed {
		joined = errors.Join(joined, s.publisher.UnbindExamAttemptConnection(ctx, result.Connection.ID))
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
