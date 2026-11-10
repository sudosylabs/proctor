// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package app

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/sudosylabs/proctor/server/app/realtime"
	"github.com/sudosylabs/proctor/server/model"
	"github.com/sudosylabs/proctor/server/store"
)

func TestExamSittingLifecycleEffectPublishesOnlySafeBoundedMetadata(t *testing.T) {
	t.Parallel()
	service := newTestRealtimeService(t, noopAuthenticationCache{})
	sink := &recordingRealtimeSink{}
	require.NoError(t, service.AttachRealtimeSink(sink))
	require.NoError(t, service.AttachRealtimeClusterFanout(&recordingRealtimeCluster{}))
	examID, sittingID := model.NewExamID(), model.NewExamSittingID()
	candidateID := model.NewUserID()
	collections := examCollectionInvalidationEffects{
		sittings: &examSittingInvalidationStoreFake{candidateIDs: []model.UserID{candidateID}},
		realtime: service,
	}
	at := time.Date(2026, time.August, 16, 9, 0, 0, 0, time.UTC)
	err := (examSittingRealtimeEffects{realtime: service, collections: collections}).LifecycleChanged(context.Background(), examID, sittingID,
		model.ExamSittingPaused, 4, store.ExamSittingTransitionManagerPaused, at.Add(2*time.Hour), at)
	require.NoError(t, err)
	sink.mu.Lock()
	events := append([]realtime.RealtimeEvent(nil), sink.events...)
	sink.mu.Unlock()
	require.Len(t, events, 4)
	require.Equal(t, "exam_sitting_lifecycle_changed", events[0].Name)
	require.Equal(t, model.ActionExamSittingView, events[0].Action)
	require.Equal(t, "exam_sitting_lifecycle_changed", events[1].Name)
	require.Equal(t, model.ActionExamSittingParticipate, events[1].Action)
	require.Equal(t, `{"exam_id":"`+examID.String()+`","exam_sitting_id":"`+sittingID.String()+`","state":"paused","revision":4,"reason_code":"manager_paused","scheduled_end_at":"2026-08-16T11:00:00Z","changed_at":"2026-08-16T09:00:00Z"}`, string(events[0].Data))
	require.Equal(t, string(events[0].Data), string(events[1].Data))
	require.Equal(t, "manager.sitting_board.changed", events[2].Name)
	require.Equal(t, model.ActionExamSittingView, events[2].Action)
	require.Equal(t, `{"schema_version":1,"exam_id":"`+examID.String()+`","exam_sitting_id":"`+sittingID.String()+`"}`, string(events[2].Data))
	require.Equal(t, "candidate.exam_activity.changed", events[3].Name)
	require.Equal(t, candidateID, events[3].UserID)
}

type examSittingInvalidationStoreFake struct {
	store.ExamSittingStore
	candidateIDs []model.UserID
	targets      []store.ExamSittingInvalidationTarget
}

func (f *examSittingInvalidationStoreFake) ListInvalidationTargetsByExam(
	context.Context,
	model.ExamID,
	model.ExamSittingID,
	int,
) ([]store.ExamSittingInvalidationTarget, error) {
	return append([]store.ExamSittingInvalidationTarget(nil), f.targets...), nil
}

func (f *examSittingInvalidationStoreFake) ListCandidateInvalidationTargetsBySitting(
	context.Context,
	model.ExamSittingID,
	model.UserID,
	int,
) ([]model.UserID, error) {
	return append([]model.UserID(nil), f.candidateIDs...), nil
}
