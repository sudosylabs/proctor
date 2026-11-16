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
	"github.com/sudosylabs/proctor/server/cluster"
	"github.com/sudosylabs/proctor/server/logging"
	"github.com/sudosylabs/proctor/server/model"
	"github.com/sudosylabs/proctor/server/store"
)

// Dependencies are the collaborators New requires.
type Dependencies struct {
	Delivery    *Delivery
	Diagnostics *logging.Logger
	// Attempts finds the attempts whose candidate and sitting board must hear
	// of a session revocation. It is only read.
	Attempts store.ExamAttemptStore
}

// Service is the production Publisher. It turns delivery failures into
// application errors and adds the attempt and current-user notices a session
// revocation or authorization change owes.
type Service struct {
	deps Dependencies
}

// New returns a Service whose sink and cluster fan-out are attached later.
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

// AttachClusterFanout must be called once before the node becomes ready. Peer
// handlers apply only local effects, so a broadcast never loops.
func (s *Service) AttachClusterFanout(fanout cluster.Transport) error {
	return s.deps.Delivery.SetClusterFanout(fanout)
}

// AttachSink must be called once before the node becomes ready.
func (s *Service) AttachSink(sink Sink) error {
	return s.deps.Delivery.SetSink(sink)
}

// Publish delivers locally first, then to peers best effort. Call it only
// after the change has committed.
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

// UnbindExamAttemptConnection unbinds one attempt connection locally and then
// on peers, leaving its socket open.
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

// Report records an effect that failed after commit; the use case still
// succeeds.
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

// SessionsRevoked also tells the user's remaining connections and the
// affected attempts' candidate and sitting board. Publication failures are
// reported, not returned.
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

	// The revoked sessions' connections are already closed, so only the
	// user's other connections hear this.
	sessionsEvent, err := NewCurrentUserSessionsChangedEvent(userID)
	if err == nil {
		err = s.Publish(ctx, sessionsEvent)
	}
	if err != nil {
		s.Report(ctx, "session_revocation.sessions_changed", err)
	}

	parsedSessionIDs := make([]model.SessionID, len(sessionIDs))
	for index, sessionID := range sessionIDs {
		parsedSessionIDs[index], err = model.ParseSessionID(sessionID.String())
		if err != nil {
			s.Report(ctx, "session_revocation.attempt_invalidations", err)
			return
		}
	}

	targets, err := s.deps.Attempts.ListSessionRevocationInvalidationTargets(ctx, candidateID, parsedSessionIDs)
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

// AuthenticationCacheInvalidated clears cached authentications locally and
// then on peers, without closing sockets.
func (s *Service) AuthenticationCacheInvalidated(
	ctx context.Context,
	userID model.UserID,
	accessTokenHashes []string,
) {
	s.deps.Delivery.AuthenticationCacheInvalidated(ctx, userID, accessTokenHashes)
}

// InvalidateAuthorization also tells the user's clients to refresh their
// activity and current-user context. Publication failures are reported, not
// returned.
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

// InvalidateCurrentUserContext tells the user's clients to refetch their
// current-user context.
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
