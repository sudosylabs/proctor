// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package exam

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

func TestExamArchiveInvalidatesEveryAffectedCollection(t *testing.T) {
	t.Parallel()
	fixture := newAuthoringFixture(t)
	sittingID, candidateID := model.NewExamSittingID(), model.NewUserID()
	fixture.sittings.targets = []store.ExamSittingInvalidationTarget{{ExamID: fixture.examID, SittingID: sittingID}}
	fixture.sittings.candidateIDs = []model.UserID{candidateID}
	require.NoError(t, fixture.service.publishExamArchived(context.Background(), fixture.examID, 4, time.Unix(100, 0)))
	// Contract: the archived fact reaches the Exam's viewers first; each
	// Sitting's board is then invalidated before its candidates' Exam activity.
	require.Equal(t, []realtimetest.Call{
		{Method: realtimetest.Publish, Event: realtime.Event{
			Name:     "exam_archived",
			Action:   model.ActionExamView,
			Resource: model.Resource{Type: model.ResourceExam, ID: fixture.examID.String()},
			Data:     json.RawMessage(`{"exam_id":"` + fixture.examID.String() + `","exam_revision":4,"archived_at":"1970-01-01T00:01:40Z"}`),
		}},
		{Method: realtimetest.Publish, Event: realtime.Event{
			Name:     "manager.sitting_board.changed",
			Action:   model.ActionExamSittingView,
			Resource: model.Resource{Type: model.ResourceExamSitting, ID: sittingID.String()},
			Data:     json.RawMessage(`{"schema_version":1,"exam_id":"` + fixture.examID.String() + `","exam_sitting_id":"` + sittingID.String() + `"}`),
		}},
		{Method: realtimetest.Publish, Event: realtime.Event{
			Name: "candidate.exam_activity.changed", UserID: candidateID, Data: json.RawMessage(`{"schema_version":1}`),
		}},
	}, fixture.publisher.Calls())
}
