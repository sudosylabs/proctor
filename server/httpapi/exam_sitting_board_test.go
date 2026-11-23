// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package httpapi

import (
	"context"

	"github.com/sudosylabs/proctor/server/model"
	"github.com/sudosylabs/proctor/server/store"
)

// The board reads below report nothing beyond the sitting itself: no
// candidates, history or corrections.

func (s *sittingLedger) CountCandidates(_ context.Context, ids []model.ExamSittingID) (map[model.ExamSittingID]int, error) {
	counts := make(map[model.ExamSittingID]int, len(ids))
	for _, id := range ids {
		counts[id] = 0
	}
	return counts, nil
}

func (s *sittingLedger) ListHistory(context.Context, model.ExamSittingID, int) ([]store.ExamSittingHistoryEntry, error) {
	return []store.ExamSittingHistoryEntry{}, nil
}

func (s *sittingLedger) ListCorrections(context.Context, model.ExamSittingID) ([]store.ExamSittingCorrectionSummary, error) {
	return []store.ExamSittingCorrectionSummary{}, nil
}

func (s *attemptLedger) ListManagerEnds(context.Context, model.ExamSittingID, []model.ExamAttemptID) ([]store.ExamAttemptManagerEnd, error) {
	return []store.ExamAttemptManagerEnd{}, nil
}

func (s *attemptLedger) ListSittingCandidateDetails(context.Context, store.SittingCandidateDetailOptions) ([]store.SittingCandidateDetail, error) {
	return []store.SittingCandidateDetail{}, nil
}
