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

	"github.com/stretchr/testify/require"

	"github.com/sudosylabs/proctor/server/app/apperror"
	"github.com/sudosylabs/proctor/server/model"
	"github.com/sudosylabs/proctor/server/store"
)

func TestSittingsAcrossExamsAreScopedAndCountOnlyOpenSittings(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	counts := &store.SittingCandidateStatusCounts{Total: 3, NeedsAttention: 1,
		ByPresenceState: map[store.SittingCandidatePresenceState]int64{store.SittingCandidateConnected: 3}}
	f.persistence.across = []store.ExamSittingAcrossItem{
		{SittingID: f.sittingID, ExamID: f.examID, State: model.ExamSittingOpen, Revision: 2, Counts: counts},
		{SittingID: model.NewExamSittingID(), ExamID: f.examID, State: model.ExamSittingScheduled, Revision: 1},
	}
	page, err := f.service.ListSittingsAcrossExams(context.Background(), f.inv, AcrossExamsQuery{
		States: []model.ExamSittingState{model.ExamSittingOpen, model.ExamSittingScheduled}, Limit: 1,
	})
	require.NoError(t, err)
	require.True(t, page.HasMore)
	require.Len(t, page.Items, 1)
	require.Equal(t, int64(1), page.Items[0].Counts.NeedsAttention)
	options := f.persistence.acrossOptions[0]
	require.Equal(t, 2, options.Limit, "the Store reads one row past the page")
	require.Equal(t, f.userID, options.ReaderUserID, "counts leave out the reader")
	require.Equal(t, []model.AcademicUnitID{f.unitID}, options.Visibility.OrdinaryAcademicUnitRootIDs)
	require.Equal(t, []model.AcademicUnitID{f.unitID}, options.Visibility.OverrideAcademicUnitRootIDs)
	counts.ByPresenceState[store.SittingCandidateConnected] = 0
	require.Equal(t, int64(3), page.Items[0].Counts.ByPresenceState[store.SittingCandidateConnected], "the page owns its counts")

	f.persistence.across = []store.ExamSittingAcrossItem{{SittingID: f.sittingID, ExamID: f.examID, State: model.ExamSittingOpen, Revision: 2}}
	_, err = f.service.ListSittingsAcrossExams(context.Background(), f.inv, AcrossExamsQuery{Limit: 10})
	require.True(t, apperror.Is(err, "exam.sitting.unavailable"), "an open sitting without counts is refused: %v", err)
}

func TestSittingsAcrossExamsDenyACallerWithoutSittingView(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	_, err := f.service.ListSittingsAcrossExams(context.Background(), f.stranger, AcrossExamsQuery{Limit: 10})
	require.Error(t, err)
	require.Empty(t, f.persistence.acrossOptions, "a caller with no grant reads nothing")

	_, err = f.service.ListSittingsAcrossExams(context.Background(), f.inv, AcrossExamsQuery{
		States: []model.ExamSittingState{model.ExamSittingOpen, model.ExamSittingOpen}, Limit: 10,
	})
	require.True(t, apperror.Is(err, "exam.sitting.invalid"))
}
