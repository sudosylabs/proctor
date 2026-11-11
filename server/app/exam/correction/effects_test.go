// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package correction

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/sudosylabs/proctor/server/app/realtime"
	"github.com/sudosylabs/proctor/server/app/realtime/realtimetest"
	"github.com/sudosylabs/proctor/server/model"
)

func TestCorrectionEffectPublishesManagerAndCandidateRefetchFacts(t *testing.T) {
	t.Parallel()
	f := newCorrectionFixture(t)
	candidateID := model.NewUserID()
	f.sittings.candidateIDs = []model.UserID{candidateID}
	previousRevisionID, revisionID := model.NewExamRevisionID(), model.NewExamRevisionID()
	at := time.Date(2026, time.August, 17, 12, 0, 0, 0, time.UTC)
	err := f.service.publishCorrected(context.Background(), Result{
		ExamID: f.examID, SittingID: f.sittingID, PreviousRevisionID: previousRevisionID,
		RevisionID: revisionID, SittingRevision: 7, EffectiveAt: at,
	})
	require.NoError(t, err)
	sitting := model.Resource{Type: model.ResourceExamSitting, ID: f.sittingID.String()}
	corrected := json.RawMessage(`{"exam_id":"` + f.examID.String() + `","exam_sitting_id":"` + f.sittingID.String() +
		`","previous_revision_id":"` + previousRevisionID.String() + `","revision_id":"` + revisionID.String() +
		`","sitting_revision":7,"effective_at":"2026-08-17T12:00:00Z"}`)
	board := json.RawMessage(`{"schema_version":1,"exam_id":"` + f.examID.String() + `","exam_sitting_id":"` + f.sittingID.String() + `"}`)
	// Contract: managers and candidates receive the same corrected-content
	// fact, managers first; the Sitting board and each candidate's Exam
	// activity are invalidated after it.
	require.Equal(t, []realtimetest.Call{
		{Method: realtimetest.PublishRealtimeEvent, Event: realtime.RealtimeEvent{
			Name: "exam_sitting_content_corrected", Action: model.ActionExamSittingView, Resource: sitting, Data: corrected}},
		{Method: realtimetest.PublishRealtimeEvent, Event: realtime.RealtimeEvent{
			Name: "exam_sitting_content_corrected", Action: model.ActionExamSittingParticipate, Resource: sitting, Data: corrected}},
		{Method: realtimetest.PublishRealtimeEvent, Event: realtime.RealtimeEvent{
			Name: "manager.sitting_board.changed", Action: model.ActionExamSittingView, Resource: sitting, Data: board}},
		{Method: realtimetest.PublishRealtimeEvent, Event: realtime.RealtimeEvent{
			Name: "candidate.exam_activity.changed", UserID: candidateID, Data: json.RawMessage(`{"schema_version":1}`)}},
	}, f.publisher.Calls())
}
