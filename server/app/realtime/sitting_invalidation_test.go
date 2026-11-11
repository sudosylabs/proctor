// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package realtime_test

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/sudosylabs/proctor/server/app/realtime"
	"github.com/sudosylabs/proctor/server/app/realtime/realtimetest"
	"github.com/sudosylabs/proctor/server/model"
)

// candidatePagesFake serves the candidates of one Sitting in the pages a test
// gives it and records the cursor of every read.
type candidatePagesFake struct {
	pages  [][]model.UserID
	err    error
	afters []model.UserID
	limits []int
}

func (f *candidatePagesFake) ListCandidateInvalidationTargetsBySitting(
	_ context.Context,
	_ model.ExamSittingID,
	after model.UserID,
	limit int,
) ([]model.UserID, error) {
	f.afters = append(f.afters, after)
	f.limits = append(f.limits, limit)
	if len(f.afters) > len(f.pages) {
		return nil, f.err
	}
	return f.pages[len(f.afters)-1], nil
}

func candidateIDs(count int) []model.UserID {
	ids := make([]model.UserID, count)
	for index := range ids {
		ids[index] = model.NewUserID()
	}
	return ids
}

func activityCalls(t *testing.T, candidates ...model.UserID) []realtimetest.Call {
	t.Helper()
	calls := make([]realtimetest.Call, 0, len(candidates))
	for _, candidateID := range candidates {
		event, err := realtime.NewCandidateExamActivityChangedEvent(candidateID)
		require.NoError(t, err)
		calls = append(calls, realtimetest.Call{Method: realtimetest.Publish, Event: event})
	}
	return calls
}

func TestCandidateActivityInvalidationReadsUntilAShortPage(t *testing.T) {
	t.Parallel()
	full, rest := candidateIDs(200), candidateIDs(1)
	candidates := &candidatePagesFake{pages: [][]model.UserID{full, rest}}
	publisher := &realtimetest.Publisher{}
	sittingID := model.NewExamSittingID()

	require.NoError(t, realtime.PublishCandidateActivityChangedForSitting(context.Background(), publisher, candidates, sittingID))
	// Contract: a full page is followed by a read after its last candidate, and
	// the short page ends the loop.
	require.Equal(t, []model.UserID{"", full[199]}, candidates.afters)
	require.Equal(t, []int{200, 200}, candidates.limits)
	// Contract: every candidate is told once, in the order the Store returned.
	require.Equal(t, activityCalls(t, append(full, rest...)...), publisher.Calls())
}

func TestCandidateActivityInvalidationKeepsPublishingAfterAFailureAndJoinsAFailedRead(t *testing.T) {
	t.Parallel()
	full := candidateIDs(200)
	full[1] = "not-a-user"
	unreadable := errors.New("candidates unavailable")
	candidates := &candidatePagesFake{pages: [][]model.UserID{full}, err: unreadable}
	unavailable := errors.New("realtime unavailable")
	publisher := &realtimetest.Publisher{Fail: func(call realtimetest.Call) error {
		if call.Event.UserID == full[0] {
			return unavailable
		}
		return nil
	}}

	err := realtime.PublishCandidateActivityChangedForSitting(context.Background(), publisher, candidates, model.NewExamSittingID())
	require.ErrorIs(t, err, unavailable)
	require.ErrorIs(t, err, unreadable)
	require.ErrorContains(t, err, "candidate Exam activity invalidation requires a valid User identity")
	// Contract: the failed publication and the invalid identity stop nothing;
	// every other candidate of the page is still told, in order.
	require.Equal(t, activityCalls(t, append([]model.UserID{full[0]}, full[2:]...)...), publisher.Calls())
}

func TestSittingInvalidationsRequireTheirCollaborators(t *testing.T) {
	t.Parallel()
	publisher := &realtimetest.Publisher{}
	examID, sittingID := model.NewExamID(), model.NewExamSittingID()

	require.Error(t, realtime.PublishSittingBoardChanged(context.Background(), nil, examID, sittingID))
	require.Error(t, realtime.PublishCandidateActivityChangedForSitting(context.Background(), publisher, nil, sittingID))
	require.Error(t, realtime.PublishCandidateActivityChangedForSitting(context.Background(), nil, &candidatePagesFake{}, sittingID))
	require.Error(t, realtime.PublishSittingBoardChanged(context.Background(), publisher, "", sittingID))
	require.Empty(t, publisher.Calls())

	require.NoError(t, realtime.PublishSittingBoardChanged(context.Background(), publisher, examID, sittingID))
	board, err := realtime.NewManagerSittingBoardChangedEvent(examID, sittingID)
	require.NoError(t, err)
	require.Equal(t, []realtimetest.Call{{Method: realtimetest.Publish, Event: board}}, publisher.Calls())
}
