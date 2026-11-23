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

	"github.com/sudosylabs/proctor/server/model"
	"github.com/sudosylabs/proctor/server/store"
)

func TestReadShowsHistoryToManagersOnly(t *testing.T) {
	t.Parallel()
	fixture := newFixture(t)
	fixture.persistence.snapshot = &store.ExamSittingSnapshot{Sitting: fixture.sitting(t)}
	fixture.persistence.candidateCount = store.MaximumListCount
	fixture.persistence.history = []store.ExamSittingHistoryEntry{{
		Kind: store.ExamSittingHistoryPaused, At: testNow, ActorUserID: fixture.userID, Reason: "Fire alarm",
	}}
	fixture.persistence.corrections = []store.ExamSittingCorrectionSummary{{RevisionID: model.NewExamRevisionID(), Number: 2,
		EffectiveAt: testNow, AcknowledgementRequired: true, AcknowledgedCount: 12, RequiredCount: 30}}

	read, err := fixture.service.ReadExamSitting(context.Background(), fixture.inv, fixture.examID, fixture.sittingID)
	require.NoError(t, err)
	require.Equal(t, CandidateCount{Count: store.MaximumListCount, IsLowerBound: true}, read.Candidates)
	require.True(t, read.HistoryVisible)
	require.Equal(t, fixture.persistence.history, read.History)
	require.Equal(t, fixture.persistence.corrections, read.Corrections)
}

func TestReadHidesHistoryFromAViewerWhoCannotManage(t *testing.T) {
	t.Parallel()
	viewer := newFixture(t, model.ActionExamSittingView)
	viewer.persistence.snapshot = &store.ExamSittingSnapshot{Sitting: viewer.sitting(t)}
	viewer.persistence.history = []store.ExamSittingHistoryEntry{{
		Kind: store.ExamSittingHistoryCanceled, At: testNow, ActorUserID: viewer.userID, Reason: "Moved to the spring term.",
	}}

	read, err := viewer.service.ReadExamSitting(context.Background(), viewer.inv, viewer.examID, viewer.sittingID)
	require.NoError(t, err)
	require.False(t, read.HistoryVisible)
	require.Empty(t, read.History, "private reasons stay with the sitting's managers")
	require.False(t, viewer.persistence.historyRead, "the history is not even read")
}

func (f *sittingStoreFake) CountCandidates(_ context.Context, ids []model.ExamSittingID) (map[model.ExamSittingID]int, error) {
	counts := map[model.ExamSittingID]int{}
	for _, id := range ids {
		counts[id] = f.candidateCount
	}
	return counts, nil
}

func (f *sittingStoreFake) ListHistory(context.Context, model.ExamSittingID, int) ([]store.ExamSittingHistoryEntry, error) {
	f.historyRead = true
	return f.history, nil
}

func (f *sittingStoreFake) ListCorrections(context.Context, model.ExamSittingID) ([]store.ExamSittingCorrectionSummary, error) {
	return f.corrections, nil
}
