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

	"github.com/sudosylabs/proctor/server/model"
)

func TestBoardChangesAreFollowedByOneThrottledExamCountsEvent(t *testing.T) {
	t.Parallel()
	service := newTestService(t, &sessionAttemptTargetsFake{})
	sink := &recordingSink{}
	require.NoError(t, service.AttachSink(sink))
	require.NoError(t, service.AttachClusterFanout(&recordingFanout{}))
	examID := model.NewExamID()

	for range 3 {
		require.NoError(t, PublishSittingBoardChanged(context.Background(), service, examID, model.NewExamSittingID()))
	}
	submissions, err := NewSittingSubmissionsChangedEvent(examID, model.NewExamSittingID())
	require.NoError(t, err)
	require.NoError(t, service.Publish(context.Background(), submissions))

	var counts []Event
	for _, event := range sink.events {
		if event.Name == "exam.counts.changed" {
			counts = append(counts, event)
		}
	}
	require.Len(t, counts, 1, "within one window an exam's counts event is sent once now; the rest fold into one trailing event")
	require.Equal(t, model.ActionExamView, counts[0].Action)
	require.Equal(t, model.Resource{Type: model.ResourceExam, ID: examID.String()}, counts[0].Resource)
	require.JSONEq(t, `{"exam_id":"`+examID.String()+`"}`, string(counts[0].Data))
}

func TestOtherEventsCarryNoExamCounts(t *testing.T) {
	t.Parallel()
	service := newTestService(t, &sessionAttemptTargetsFake{})
	sink := &recordingSink{}
	require.NoError(t, service.AttachSink(sink))
	require.NoError(t, service.AttachClusterFanout(&recordingFanout{}))
	event, err := NewCandidateExamActivityChangedEvent(model.NewUserID())
	require.NoError(t, err)
	require.NoError(t, service.Publish(context.Background(), event))
	for _, delivered := range sink.events {
		require.NotEqual(t, "exam.counts.changed", delivered.Name)
	}
}
