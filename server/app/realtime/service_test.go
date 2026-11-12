// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package realtime

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/sudosylabs/proctor/server/app/apperror"
	"github.com/sudosylabs/proctor/server/model"
)

func TestRealtimePublishRequiresClusterFanout(t *testing.T) {
	t.Parallel()

	service := newTestService(t, &sessionAttemptTargetsFake{})
	_ = service.AttachSink(&recordingSink{})
	err := service.Publish(context.Background(), Event{
		Name:   "current_user.context.changed",
		UserID: model.UserID(model.NewID()),
	})
	require.Error(t, err, "Publish() error")
	require.True(t, apperror.Is(err, "websocket.internal"), "Publish() error")
}

func TestRealtimePublishMapsInvalidPublication(t *testing.T) {
	t.Parallel()

	service := newTestService(t, &sessionAttemptTargetsFake{})
	err := service.Publish(context.Background(), Event{Name: "missing.target"})
	require.Error(t, err, "Publish() error")
	require.True(t, apperror.Is(err, "websocket.request.invalid"), "Publish() error")
}

func TestSessionRevocationPublishesAttemptProjectionInvalidations(t *testing.T) {
	t.Parallel()
	candidateID := model.NewUserID()
	sessionID := model.NewSessionID()
	target := SessionAttemptTarget{
		CandidateUserID: candidateID,
		ExamID:          model.NewExamID(),
		SittingID:       model.NewExamSittingID(),
	}
	attempts := &sessionAttemptTargetsFake{targets: []SessionAttemptTarget{target}}
	service := newTestService(t, attempts)
	sink := &recordingSink{}
	require.NoError(t, service.AttachSink(sink))
	require.NoError(t, service.AttachClusterFanout(&recordingFanout{}))

	service.SessionsRevoked(
		context.Background(), candidateID, []model.SessionID{sessionID}, nil,
	)
	require.Equal(t, candidateID, attempts.candidateID, "attempt invalidation query = candidate")
	require.Len(t, attempts.sessionIDs, 1, "attempt invalidation query = candidate")
	require.Equal(t, sessionID, attempts.sessionIDs[0], "attempt invalidation query = candidate")
	require.Len(t, sink.events, 4)
	require.Equal(t, "current_user.sessions.changed", sink.events[0].Name)
	require.Equal(t, candidateID, sink.events[0].UserID)
	require.Equal(t, "candidate.exam_activity.changed", sink.events[1].Name)
	require.Equal(t, candidateID, sink.events[1].UserID)
	require.Equal(t, "manager.sitting_board.changed", sink.events[2].Name)
	require.Equal(t, target.SittingID.String(), sink.events[2].Resource.ID)
	require.Equal(t, "current_user.context.changed", sink.events[3].Name)
	require.Equal(t, candidateID, sink.events[3].UserID)
}

func TestSessionRevocationTellsOnlyTheUserTheirSessionsChanged(t *testing.T) {
	t.Parallel()
	userID := model.NewUserID()
	service := newTestService(t, &sessionAttemptTargetsFake{})
	sink := &recordingSink{}
	require.NoError(t, service.AttachSink(sink))
	require.NoError(t, service.AttachClusterFanout(&recordingFanout{}))

	service.SessionsRevoked(context.Background(), userID, []model.SessionID{model.NewSessionID()}, nil)

	require.Len(t, sink.events, 1)
	require.Equal(t, "current_user.sessions.changed", sink.events[0].Name)
	require.Equal(t, userID, sink.events[0].UserID)
	require.Empty(t, sink.events[0].Action)
	require.Equal(t, model.Resource{}, sink.events[0].Resource)
	require.JSONEq(t, `{"schema_version":1}`, string(sink.events[0].Data))
}

func TestSessionRevocationOfEveryConnectionPublishesNoSessionsEvent(t *testing.T) {
	t.Parallel()
	service := newTestService(t, &sessionAttemptTargetsFake{})
	sink := &recordingSink{}
	require.NoError(t, service.AttachSink(sink))
	require.NoError(t, service.AttachClusterFanout(&recordingFanout{}))

	// No Session IDs closes every connection of the User, so none is left
	// to tell.
	service.SessionsRevoked(context.Background(), model.NewUserID(), nil, nil)

	require.Empty(t, sink.events)
}

func TestServiceRequiresItsCollaborators(t *testing.T) {
	t.Parallel()

	delivery := mustNewDelivery(t, &recordingInvalidator{}, newDiscardLogger(t))
	diagnostics := newDiscardLogger(t)
	attempts := &sessionAttemptTargetsFake{}
	_, err := New(Dependencies{Diagnostics: diagnostics, Attempts: attempts})
	require.Error(t, err, "nil delivery was accepted")
	_, err = New(Dependencies{Delivery: delivery, Attempts: attempts})
	require.Error(t, err, "nil diagnostics were accepted")
	_, err = New(Dependencies{Delivery: delivery, Diagnostics: diagnostics})
	require.Error(t, err, "nil session attempt targets were accepted")
}

type sessionAttemptTargetsFake struct {
	candidateID model.UserID
	sessionIDs  []model.SessionID
	targets     []SessionAttemptTarget
	err         error
}

func (f *sessionAttemptTargetsFake) ListSessionAttemptTargets(
	_ context.Context,
	candidateID model.UserID,
	sessionIDs []model.SessionID,
) ([]SessionAttemptTarget, error) {
	f.candidateID = candidateID
	f.sessionIDs = append([]model.SessionID(nil), sessionIDs...)
	return append([]SessionAttemptTarget(nil), f.targets...), f.err
}

func newTestService(t *testing.T, attempts SessionAttemptTargets) *Service {
	t.Helper()
	service, err := New(Dependencies{
		Delivery:    mustNewDelivery(t, &recordingInvalidator{}, newDiscardLogger(t)),
		Diagnostics: newDiscardLogger(t),
		Attempts:    attempts,
	})
	require.NoError(t, err)
	return service
}
