// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package sitting

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/sudosylabs/proctor/server/app/realtime"
	"github.com/sudosylabs/proctor/server/app/realtime/realtimetest"
	"github.com/sudosylabs/proctor/server/model"
	"github.com/sudosylabs/proctor/server/store"
)

func TestExamSittingLifecycleEffectPublishesOnlySafeBoundedMetadata(t *testing.T) {
	t.Parallel()
	fixture := newFixture(t)
	candidateID := model.NewUserID()
	fixture.persistence.candidateIDs = []model.UserID{candidateID}
	at := time.Date(2026, time.August, 16, 9, 0, 0, 0, time.UTC)
	paused := &model.ExamSitting{
		ID: fixture.sittingID, ExamID: fixture.examID, State: model.ExamSittingPaused, Revision: 4,
		ScheduledEndAt: at.Add(2 * time.Hour), UpdatedAt: at, ReasonCode: model.ExamSittingReasonManagerCanceled,
	}
	require.NoError(t, fixture.service.publishLifecycleChanged(context.Background(), paused, store.ExamSittingTransitionManagerPaused))
	sitting := model.Resource{Type: model.ResourceExamSitting, ID: fixture.sittingID.String()}
	changed := json.RawMessage(`{"exam_id":"` + fixture.examID.String() + `","exam_sitting_id":"` + fixture.sittingID.String() +
		`","state":"paused","revision":4,"reason_code":"manager_paused","scheduled_end_at":"2026-08-16T11:00:00Z","changed_at":"2026-08-16T09:00:00Z"}`)
	// Contract: managers and candidates receive the same bounded transition,
	// managers first; the Sitting board and then each candidate's Exam activity
	// are invalidated after it.
	require.Equal(t, []realtimetest.Call{
		{Method: realtimetest.PublishRealtimeEvent, Event: realtime.RealtimeEvent{
			Name: "exam_sitting_lifecycle_changed", Action: model.ActionExamSittingView, Resource: sitting, Data: changed}},
		{Method: realtimetest.PublishRealtimeEvent, Event: realtime.RealtimeEvent{
			Name: "exam_sitting_lifecycle_changed", Action: model.ActionExamSittingParticipate, Resource: sitting, Data: changed}},
		{Method: realtimetest.PublishRealtimeEvent, Event: realtime.RealtimeEvent{
			Name: "manager.sitting_board.changed", Action: model.ActionExamSittingView, Resource: sitting,
			Data: json.RawMessage(`{"schema_version":1,"exam_id":"` + fixture.examID.String() + `","exam_sitting_id":"` + fixture.sittingID.String() + `"}`)}},
		{Method: realtimetest.PublishRealtimeEvent, Event: realtime.RealtimeEvent{
			Name: "candidate.exam_activity.changed", UserID: candidateID, Data: json.RawMessage(`{"schema_version":1}`)}},
	}, fixture.publisher.Calls())
}
