// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package sitting

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/sudosylabs/proctor/server/model"
	"github.com/sudosylabs/proctor/server/store"
)

func TestGetAuthorizesExactSittingResource(t *testing.T) {
	t.Parallel()
	fixture := newFixture(t)
	fixture.persistence.snapshot = &store.ExamSittingSnapshot{Sitting: fixture.sitting(t)}

	got, err := fixture.service.GetExamSitting(context.Background(), fixture.inv, fixture.examID, fixture.sittingID)
	require.NoError(t, err)
	require.NotNil(t, got.Sitting)
	require.Equal(t, fixture.sittingID, got.Sitting.ID)
	require.Equal(t, model.ActionExamSittingView, fixture.authorizer.action, "authorization")
	require.Equal(t, model.Resource{Type: model.ResourceExamSitting, ID: fixture.sittingID.String()}, fixture.authorizer.resource, "authorization")
	require.Equal(t, fixture.examID, fixture.persistence.getExamID, "store Get() ids")
	require.Equal(t, fixture.sittingID, fixture.persistence.getSittingID, "store Get() ids")
}

func TestGetRejectsMismatchedExamOwnership(t *testing.T) {
	t.Parallel()
	fixture := newFixture(t)
	foreign := fixture.sitting(t)
	foreign.ExamID = model.NewExamID()
	fixture.persistence.snapshot = &store.ExamSittingSnapshot{Sitting: foreign}

	_, err := fixture.service.GetExamSitting(context.Background(), fixture.inv, fixture.examID, fixture.sittingID)
	assertFaultCode(t, err, "exam.sitting.unavailable")
}

func TestListAppliesBoundedFiltersAndLookAhead(t *testing.T) {
	t.Parallel()
	fixture := newFixture(t)
	first := fixture.sitting(t)
	second := *first
	second.ID = model.NewExamSittingID()
	second.ScheduledStartAt = first.ScheduledStartAt.Add(-time.Hour)
	second.ScheduledEndAt = first.ScheduledEndAt.Add(-time.Hour)
	fixture.persistence.items = []store.ExamSittingSnapshot{{Sitting: first}, {Sitting: &second}}
	overlapStart, overlapEnd := testNow.Add(30*time.Minute), testNow.Add(4*time.Hour)

	page, err := fixture.service.ListExamSittings(context.Background(), fixture.inv, ListQuery{
		ExamID: fixture.examID, ClassID: fixture.classID, States: []model.ExamSittingState{model.ExamSittingScheduled},
		OverlapStartAt: overlapStart, OverlapEndAt: overlapEnd, Limit: 1,
	})
	require.NoError(t, err)
	require.Len(t, page.Items, 1)
	require.True(t, page.HasMore)
	require.Equal(t, first.ID, page.Items[0].Sitting.ID)
	require.Equal(t, model.ActionExamView, fixture.authorizer.action, "authorization")
	require.Equal(t, model.Resource{Type: model.ResourceExam, ID: fixture.examID.String()}, fixture.authorizer.resource, "authorization")
	want := store.ExamSittingListOptions{ExamID: fixture.examID, ClassID: fixture.classID,
		States: []model.ExamSittingState{model.ExamSittingScheduled}, OverlapStartAt: overlapStart,
		OverlapEndAt: overlapEnd, Limit: 2}
	require.Equal(t, want, fixture.persistence.listOptions)
}

func TestListRejectsUnboundedAndPartialFilters(t *testing.T) {
	t.Parallel()
	examID, sittingID := model.NewExamID(), model.NewExamSittingID()
	tests := map[string]ListQuery{
		"zero limit":        {ExamID: examID},
		"excess limit":      {ExamID: examID, Limit: 201},
		"invalid class":     {ExamID: examID, ClassID: model.ClassID("invalid"), Limit: 20},
		"partial cursor at": {ExamID: examID, BeforeScheduledStartAt: testNow, Limit: 20},
		"partial cursor id": {ExamID: examID, BeforeSittingID: sittingID, Limit: 20},
		"partial overlap":   {ExamID: examID, OverlapStartAt: testNow, Limit: 20},
		"reversed overlap":  {ExamID: examID, OverlapStartAt: testNow, OverlapEndAt: testNow.Add(-time.Minute), Limit: 20},
		"invalid state":     {ExamID: examID, States: []model.ExamSittingState{"unknown"}, Limit: 20},
		"duplicate state":   {ExamID: examID, States: []model.ExamSittingState{model.ExamSittingScheduled, model.ExamSittingScheduled}, Limit: 20},
		"too many states": {ExamID: examID, States: []model.ExamSittingState{
			model.ExamSittingScheduled, model.ExamSittingOpen, model.ExamSittingPaused, model.ExamSittingClosing,
			model.ExamSittingClosed, model.ExamSittingCanceled, model.ExamSittingScheduled,
		}, Limit: 20},
	}
	for name, query := range tests {
		name, query := name, query
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			_, err := listOptions(query)
			require.Error(t, err, "listOptions() accepted invalid query")
		})
	}
}
