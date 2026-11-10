// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package app

import (
	"context"
	"errors"

	"github.com/sudosylabs/proctor/server/app/apperror"
	"github.com/sudosylabs/proctor/server/app/identity"
	"github.com/sudosylabs/proctor/server/app/realtime"
	"github.com/sudosylabs/proctor/server/model"
	"github.com/sudosylabs/proctor/server/store"
)

// realtimeDiagnostics extends the child module's bounded diagnostics only for
// application-owned reporting of failed post-commit effects.
type realtimeDiagnostics interface {
	realtime.Diagnostics
	ErrorContextWithEvent(ctx context.Context, message, event string, err error)
}

// realtimeServiceDependencies are the collaborators of RealtimeService;
// newRealtimeServiceWithDelivery validates them once and RealtimeService keeps them in deps.
type realtimeServiceDependencies struct {
	Delivery    *realtime.Service
	Diagnostics realtimeDiagnostics
}

// RealtimeService translates the child module's publication failures into
// parent application errors and retains parent-owned effect adapters.
type RealtimeService struct {
	deps realtimeServiceDependencies

	attempts interface {
		ListSessionRevocationInvalidationTargets(context.Context, model.UserID, []model.SessionID) ([]store.ExamAttemptInvalidationTarget, error)
	}
}

func newRealtimeServiceWithDelivery(deps realtimeServiceDependencies) (*RealtimeService, error) {
	if deps.Delivery == nil {
		return nil, errors.New("realtime delivery is required")
	}
	if deps.Diagnostics == nil {
		return nil, errors.New("realtime diagnostics are required")
	}
	return &RealtimeService{deps: deps}, nil
}

// AttachRealtimeClusterFanout wires the composition-owned cluster adapter and
// registers peer handlers. It must be called once before the node becomes
// ready. Peer handlers apply only local effects so Broadcast never loops.
func (s *RealtimeService) AttachRealtimeClusterFanout(fanout realtime.ClusterFanout) error {
	return s.deps.Delivery.SetClusterFanout(fanout)
}

// AttachRealtimeSink wires the composition-owned local WebSocket sink. It must
// be called once before the node becomes ready.
func (s *RealtimeService) AttachRealtimeSink(sink realtime.Sink) error {
	return s.deps.Delivery.SetSink(sink)
}

// PublishRealtimeEvent delivers a transport-neutral event locally first, then
// fans it out to peers best-effort. Callers must invoke this only after
// durable commit.
func (s *RealtimeService) PublishRealtimeEvent(ctx context.Context, event realtime.RealtimeEvent) error {
	err := s.deps.Delivery.Publish(ctx, event)
	if err == nil {
		return nil
	}
	var invalid *realtime.InvalidPublicationError
	if errors.As(err, &invalid) {
		return invalidRealtimeRequest(invalid.Error())
	}
	return internalRealtimeError(err)
}

// UnbindExamAttemptConnection unbinds one Attempt Connection locally and then
// on peers, leaving its WebSocket open.
func (s *RealtimeService) UnbindExamAttemptConnection(ctx context.Context, connectionID model.AttemptConnectionID) error {
	err := s.deps.Delivery.UnbindExamAttemptConnection(ctx, connectionID)
	if err == nil {
		return nil
	}
	var invalid *realtime.InvalidPublicationError
	if errors.As(err, &invalid) {
		return invalidRealtimeRequest(invalid.Error())
	}
	return internalRealtimeError(err)
}

func (s *RealtimeService) reportTransientFailure(
	ctx context.Context,
	event string,
	err error,
) {
	if s.deps.Diagnostics == nil {
		return
	}
	s.deps.Diagnostics.ErrorContextWithEvent(ctx, "transient realtime publication failed", event, err)
}

// SessionsRevoked propagates revoked Sessions and access tokens, then tells
// the affected Attempts' candidate and Sitting board that their activity
// changed. Publication failures are reported, not returned.
func (s *RealtimeService) SessionsRevoked(
	ctx context.Context,
	userID model.UserID,
	sessionIDs []model.SessionID,
	accessTokenHashes []string,
) {
	s.deps.Delivery.SessionsRevoked(ctx, userID, sessionIDs, accessTokenHashes)
	if s.attempts == nil || len(sessionIDs) == 0 {
		return
	}
	candidateID, err := model.ParseUserID(userID.String())
	if err != nil {
		s.reportTransientFailure(ctx, "session_revocation.attempt_invalidations", err)
		return
	}
	parsedSessionIDs := make([]model.SessionID, len(sessionIDs))
	for index, sessionID := range sessionIDs {
		parsedSessionIDs[index], err = model.ParseSessionID(sessionID.String())
		if err != nil {
			s.reportTransientFailure(ctx, "session_revocation.attempt_invalidations", err)
			return
		}
	}
	targets, err := s.attempts.ListSessionRevocationInvalidationTargets(ctx, candidateID, parsedSessionIDs)
	if err != nil {
		s.reportTransientFailure(ctx, "session_revocation.attempt_invalidations", err)
		return
	}
	for _, target := range targets {
		candidateEvent, candidateErr := realtime.NewCandidateExamActivityChangedEvent(target.CandidateUserID)
		managerEvent, managerErr := realtime.NewManagerSittingBoardChangedEvent(target.ExamID, target.SittingID)
		publishErr := errors.Join(candidateErr, managerErr)
		if candidateErr == nil {
			publishErr = errors.Join(publishErr, s.PublishRealtimeEvent(ctx, candidateEvent))
		}
		if managerErr == nil {
			publishErr = errors.Join(publishErr, s.PublishRealtimeEvent(ctx, managerEvent))
		}
		publishErr = errors.Join(publishErr, s.InvalidateCurrentUserContext(ctx, target.CandidateUserID))
		if publishErr != nil {
			s.reportTransientFailure(ctx, "session_revocation.attempt_invalidations", publishErr)
		}
	}
}

// AuthenticationCacheInvalidated clears a User's cached authentications
// locally and then on peers, without closing sockets.
func (s *RealtimeService) AuthenticationCacheInvalidated(
	ctx context.Context,
	userID model.UserID,
	accessTokenHashes []string,
) {
	s.deps.Delivery.AuthenticationCacheInvalidated(ctx, userID, accessTokenHashes)
}

// InvalidateAuthorization propagates a change to a User's authorization and
// tells their clients to refresh their activity and current-user context.
// Publication failures are reported, not returned.
func (s *RealtimeService) InvalidateAuthorization(ctx context.Context, userID model.UserID) {
	s.deps.Delivery.InvalidateAuthorization(ctx, userID)
	if userID == "" {
		return
	}
	parsed, err := model.ParseUserID(userID.String())
	if err == nil {
		candidateEvent, candidateErr := realtime.NewCandidateExamActivityChangedEvent(parsed)
		err = errors.Join(candidateErr, s.InvalidateCurrentUserContext(ctx, parsed))
		if candidateErr == nil {
			err = errors.Join(err, s.PublishRealtimeEvent(ctx, candidateEvent))
		}
	}
	if err != nil {
		s.reportTransientFailure(ctx, "authorization.current_user_context_invalidation", err)
	}
}

// InvalidateCurrentUserContext tells a User's clients that their current-user
// context changed.
func (s *RealtimeService) InvalidateCurrentUserContext(ctx context.Context, userID model.UserID) error {
	event, err := realtime.NewCurrentUserContextChangedEvent(userID)
	if err != nil {
		return err
	}
	return s.PublishRealtimeEvent(ctx, event)
}

var _ identity.SecurityEffects = (*RealtimeService)(nil)

func invalidRealtimeRequest(field string) error {
	return apperror.New("websocket.request.invalid").WithField("field", field)
}

func internalRealtimeError(err error) error {
	return apperror.New("websocket.internal").Wrap(err)
}
