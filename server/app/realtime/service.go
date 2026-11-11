// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package realtime

import (
	"context"
	"errors"

	"github.com/sudosylabs/proctor/server/app/apperror"
	"github.com/sudosylabs/proctor/server/logging"
	"github.com/sudosylabs/proctor/server/model"
)

// SessionAttemptTarget is one Attempt a Session revocation affects: its
// Candidate and the Exam and Sitting whose board lists it.
type SessionAttemptTarget struct {
	CandidateUserID model.UserID
	ExamID          model.ExamID
	SittingID       model.ExamSittingID
}

// SessionAttemptTargets finds the Attempts whose Candidate and Sitting board
// must hear that some of the Candidate's Sessions were revoked. It only reads;
// the parent application answers it from the Exam Attempt Store.
type SessionAttemptTargets interface {
	// ListSessionAttemptTargets returns the Candidate's Attempts that were
	// suspended through a Participation bound to one of the given Sessions.
	ListSessionAttemptTargets(ctx context.Context, candidateID model.UserID, sessionIDs []model.SessionID) ([]SessionAttemptTarget, error)
}

// Dependencies are the collaborators of Service; New validates them once and
// Service keeps them in deps.
type Dependencies struct {
	Delivery    *Delivery
	Diagnostics *logging.Logger
	Attempts    SessionAttemptTargets
}

// Service is the Publisher the application areas hold. It translates
// Delivery's publication failures into application errors and adds the
// Attempt and current-user invalidations that follow a Session revocation or
// an authorization change.
type Service struct {
	deps Dependencies
}

// New constructs the Publisher over a Delivery that composition attaches its
// sink and cluster fan-out to.
func New(deps Dependencies) (*Service, error) {
	if deps.Delivery == nil {
		return nil, errors.New("realtime delivery is required")
	}
	if deps.Diagnostics == nil {
		return nil, errors.New("realtime diagnostics are required")
	}
	if deps.Attempts == nil {
		return nil, errors.New("realtime session attempt targets are required")
	}
	return &Service{deps: deps}, nil
}

// AttachClusterFanout wires the composition-owned cluster adapter and
// registers peer handlers. It must be called once before the node becomes
// ready. Peer handlers apply only local effects so Broadcast never loops.
func (s *Service) AttachClusterFanout(fanout ClusterFanout) error {
	return s.deps.Delivery.SetClusterFanout(fanout)
}

// AttachSink wires the composition-owned local WebSocket sink. It must
// be called once before the node becomes ready.
func (s *Service) AttachSink(sink Sink) error {
	return s.deps.Delivery.SetSink(sink)
}

// Publish delivers a transport-neutral event locally first, then
// fans it out to peers best-effort. Callers must invoke this only after
// durable commit.
func (s *Service) Publish(ctx context.Context, event Event) error {
	err := s.deps.Delivery.Publish(ctx, event)
	if err == nil {
		return nil
	}
	var invalid *InvalidPublicationError
	if errors.As(err, &invalid) {
		return invalidRealtimeRequest(invalid.Error())
	}
	return internalRealtimeError(err)
}

// UnbindExamAttemptConnection unbinds one Attempt Connection locally and then
// on peers, leaving its WebSocket open.
func (s *Service) UnbindExamAttemptConnection(ctx context.Context, connectionID model.AttemptConnectionID) error {
	err := s.deps.Delivery.UnbindExamAttemptConnection(ctx, connectionID)
	if err == nil {
		return nil
	}
	var invalid *InvalidPublicationError
	if errors.As(err, &invalid) {
		return invalidRealtimeRequest(invalid.Error())
	}
	return internalRealtimeError(err)
}

// Report records an effect that failed after commit, for the use case that
// still succeeds. operation names what failed.
func (s *Service) Report(ctx context.Context, operation string, err error) {
	if s.deps.Diagnostics == nil {
		return
	}
	fields := []logging.Field{logging.String("event", operation)}
	if err != nil {
		fields = append(fields, logging.Err(err))
	}
	s.deps.Diagnostics.ErrorContext(ctx, "transient realtime publication failed", fields...)
}

// SessionsRevoked propagates revoked Sessions and access tokens, then tells
// the affected Attempts' candidate and Sitting board that their activity
// changed. Publication failures are reported, not returned.
func (s *Service) SessionsRevoked(
	ctx context.Context,
	userID model.UserID,
	sessionIDs []model.SessionID,
	accessTokenHashes []string,
) {
	s.deps.Delivery.SessionsRevoked(ctx, userID, sessionIDs, accessTokenHashes)
	if len(sessionIDs) == 0 {
		return
	}

	candidateID, err := model.ParseUserID(userID.String())
	if err != nil {
		s.Report(ctx, "session_revocation.attempt_invalidations", err)
		return
	}

	parsedSessionIDs := make([]model.SessionID, len(sessionIDs))
	for index, sessionID := range sessionIDs {
		parsedSessionIDs[index], err = model.ParseSessionID(sessionID.String())
		if err != nil {
			s.Report(ctx, "session_revocation.attempt_invalidations", err)
			return
		}
	}

	targets, err := s.deps.Attempts.ListSessionAttemptTargets(ctx, candidateID, parsedSessionIDs)
	if err != nil {
		s.Report(ctx, "session_revocation.attempt_invalidations", err)
		return
	}

	for _, target := range targets {
		candidateEvent, candidateErr := NewCandidateExamActivityChangedEvent(target.CandidateUserID)
		managerEvent, managerErr := NewManagerSittingBoardChangedEvent(target.ExamID, target.SittingID)
		publishErr := errors.Join(candidateErr, managerErr)
		if candidateErr == nil {
			publishErr = errors.Join(publishErr, s.Publish(ctx, candidateEvent))
		}
		if managerErr == nil {
			publishErr = errors.Join(publishErr, s.Publish(ctx, managerEvent))
		}

		publishErr = errors.Join(publishErr, s.InvalidateCurrentUserContext(ctx, target.CandidateUserID))
		if publishErr != nil {
			s.Report(ctx, "session_revocation.attempt_invalidations", publishErr)
		}
	}
}

// AuthenticationCacheInvalidated clears a User's cached authentications
// locally and then on peers, without closing sockets.
func (s *Service) AuthenticationCacheInvalidated(
	ctx context.Context,
	userID model.UserID,
	accessTokenHashes []string,
) {
	s.deps.Delivery.AuthenticationCacheInvalidated(ctx, userID, accessTokenHashes)
}

// InvalidateAuthorization propagates a change to a User's authorization and
// tells their clients to refresh their activity and current-user context.
// Publication failures are reported, not returned.
func (s *Service) InvalidateAuthorization(ctx context.Context, userID model.UserID) {
	s.deps.Delivery.InvalidateAuthorization(ctx, userID)
	if userID == "" {
		return
	}
	parsed, err := model.ParseUserID(userID.String())
	if err == nil {
		candidateEvent, candidateErr := NewCandidateExamActivityChangedEvent(parsed)
		err = errors.Join(candidateErr, s.InvalidateCurrentUserContext(ctx, parsed))
		if candidateErr == nil {
			err = errors.Join(err, s.Publish(ctx, candidateEvent))
		}
	}
	if err != nil {
		s.Report(ctx, "authorization.current_user_context_invalidation", err)
	}
}

// InvalidateCurrentUserContext tells a User's clients that their current-user
// context changed.
func (s *Service) InvalidateCurrentUserContext(ctx context.Context, userID model.UserID) error {
	event, err := NewCurrentUserContextChangedEvent(userID)
	if err != nil {
		return err
	}
	return s.Publish(ctx, event)
}

var _ Publisher = (*Service)(nil)

func invalidRealtimeRequest(field string) error {
	return apperror.New("websocket.request.invalid").WithField("field", field)
}

func internalRealtimeError(err error) error {
	return apperror.New("websocket.internal").Wrap(err)
}
