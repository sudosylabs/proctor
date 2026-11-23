// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package exam

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/sudosylabs/proctor/server/model"
	"github.com/sudosylabs/proctor/server/store"
)

func TestListExamsCountsTheFirstPageWithTheSameFiltersAndVisibility(t *testing.T) {
	t.Parallel()
	fixture := newAuthoringFixture(t, model.ActionExamView)
	fixture.persistence.summaries = []store.ExamSummary{{ID: fixture.examID, AcademicUnitID: fixture.unitID, CreatorUserID: fixture.userID,
		OwnerUserID: fixture.userID, Title: "Systems", UpdatedAt: authoringNow(), Revision: 1, ManagerCount: 1}}
	unscheduled := false
	page, err := fixture.service.ListExams(context.Background(), fixture.inv, ListQuery{HasSittings: &unscheduled, Limit: 50})
	require.NoError(t, err)
	require.True(t, page.Counted)
	require.Equal(t, 1, page.Total.Total)
	require.False(t, page.TotalIsLowerBound)
	require.NotNil(t, fixture.persistence.countOptions, "the first page is counted")
	require.Equal(t, fixture.persistence.listOptions.Visibility, fixture.persistence.countOptions.Visibility, "the count sees what the page sees")
	require.Equal(t, &unscheduled, fixture.persistence.countOptions.HasSittings)
}

func TestListExamsCountsActionsOnlyWithinTheViewersScopes(t *testing.T) {
	t.Parallel()
	fixture := newAuthoringFixture(t, model.ActionExamView, model.ActionExamSittingView)
	fixture.persistence.summaries = []store.ExamSummary{{ID: fixture.examID, AcademicUnitID: fixture.unitID, CreatorUserID: fixture.userID,
		OwnerUserID: fixture.userID, Title: "Systems", UpdatedAt: authoringNow(), Revision: 1, ManagerCount: 1}}
	waiting := 2
	fixture.persistence.actionCounts = map[model.ExamID]store.ExamActionCounts{fixture.examID: {NeedsAttention: &waiting}}

	page, err := fixture.service.ListExams(context.Background(), fixture.inv, ListQuery{Limit: 50})
	require.NoError(t, err)
	require.Equal(t, &waiting, page.Actions[fixture.examID].NeedsAttention)
	asked := fixture.persistence.actionOptions
	require.NotNil(t, asked)
	require.Equal(t, []model.ExamID{fixture.examID}, asked.ExamIDs, "one read for the page")
	require.Equal(t, fixture.userID, asked.ViewerUserID)
	require.Equal(t, []model.AcademicUnitID{fixture.unitID}, asked.SittingVisibility.OrdinaryAcademicUnitRootIDs,
		"sitting counts reach where the viewer holds exam.sitting.view")
	require.Empty(t, asked.ReviewVisibility.OrdinaryAcademicUnitRootIDs, "a viewer without submission.review gets no review counts")
	require.False(t, asked.ReviewVisibility.OrdinaryInstitutionWide)
}
