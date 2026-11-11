// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package sitting

import (
	"context"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/sudosylabs/proctor/server/model"
	"github.com/sudosylabs/proctor/server/store"
)

func TestListNoShowsAuthorizesClosingSittingAndReturnsBoundedPage(t *testing.T) {
	t.Parallel()
	fixture := newFixture(t)
	sitting, err := model.NewExamSitting(fixture.sittingID, fixture.examID, fixture.revisionID, fixture.classID,
		testNow.Add(-2*time.Hour), testNow.Add(-time.Hour), testNow.Add(-3*time.Hour))
	require.NoError(t, err)
	err = sitting.Open(testNow.Add(-2 * time.Hour))
	require.NoError(t, err)
	err = sitting.EnterClosing(model.ExamSittingReasonScheduledEndReached, testNow.Add(-time.Hour))
	require.NoError(t, err)
	fixture.persistence.snapshot = &store.ExamSittingSnapshot{Sitting: sitting, AcademicUnitID: fixture.unitID}
	ids := []model.UserID{model.NewUserID(), model.NewUserID(), model.NewUserID()}
	slices.SortFunc(ids, func(left, right model.UserID) int { return strings.Compare(left.String(), right.String()) })
	fixture.persistence.noShows = []store.ExamSittingNoShow{{CandidateUserID: ids[0]}, {CandidateUserID: ids[1]}, {CandidateUserID: ids[2]}}
	page, err := fixture.service.ListExamSittingNoShows(context.Background(), fixture.inv, ListNoShowsQuery{ExamID: fixture.examID, SittingID: fixture.sittingID, AfterCandidateUserID: model.UserID(""), Limit: 2})
	require.NoError(t, err, "page=%#v options=%#v err", page, fixture.persistence.noShowOptions)
	require.Len(t, page.Items, 2, "page=%#v options=%#v err", page, fixture.persistence.noShowOptions)
	require.True(t, page.HasMore, "page=%#v options=%#v err", page, fixture.persistence.noShowOptions)
	require.Equal(t, ids[1], page.Items[1].CandidateUserID, "page=%#v options=%#v err", page, fixture.persistence.noShowOptions)
	require.Equal(t, 3, fixture.persistence.noShowOptions.Limit, "page=%#v options=%#v err", page, fixture.persistence.noShowOptions)
}
