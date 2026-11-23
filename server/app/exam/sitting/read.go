// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package sitting

import (
	"context"
	"errors"

	"github.com/sudosylabs/proctor/server/app/apperror"
	"github.com/sudosylabs/proctor/server/app/invocation"
	"github.com/sudosylabs/proctor/server/model"
	"github.com/sudosylabs/proctor/server/store"
)

// CandidateCount is how many candidates a sitting has, stopped at
// store.MaximumListCount and flagged IsLowerBound when it is.
type CandidateCount struct {
	Count        int
	IsLowerBound bool
}

// Read is a sitting as a read presents it. Every viewer gets the candidate
// count and the live corrections with their acknowledgement counts. Only a
// caller who may manage the sitting gets History, the managers' actions with
// their private reasons; HistoryVisible says whether it was read.
type Read struct {
	store.ExamSittingSnapshot
	Candidates     CandidateCount
	Corrections    []store.ExamSittingCorrectionSummary
	History        []store.ExamSittingHistoryEntry
	HistoryVisible bool
}

// ListedSitting is one sitting of a page with its candidate count.
type ListedSitting struct {
	store.ExamSittingSnapshot
	Candidates CandidateCount
}

// ReadExamSitting returns one sitting to a caller allowed to view it, with
// its candidate count and corrections, and its history when the caller may
// also manage it.
func (s *Service) ReadExamSitting(
	ctx context.Context,
	inv invocation.Invocation,
	examID model.ExamID,
	sittingID model.ExamSittingID,
) (_ Read, resultErr error) {
	defer func() { resultErr = publicFailures.Conceal(resultErr) }()
	snapshot, err := s.GetExamSitting(ctx, inv, examID, sittingID)
	if err != nil {
		return Read{}, err
	}

	read := Read{ExamSittingSnapshot: snapshot}
	counts, err := s.candidateCounts(ctx, []model.ExamSittingID{sittingID})
	if err != nil {
		return Read{}, err
	}
	read.Candidates = counts[sittingID]

	if read.Corrections, err = s.sittings.ListCorrections(ctx, sittingID); err != nil {
		return Read{}, storeErrors.Translate(err)
	}

	// Private reasons are for the sitting's managers; a viewer who may not
	// manage it reads the sitting without them.
	if _, err = s.authorizeSittingManage(ctx, inv, sittingID); err != nil {
		var fault *apperror.Error
		if !errors.As(err, &fault) || fault.Code() != "authorization.denied" {
			return Read{}, err
		}
		return read, nil
	}

	if read.History, err = s.sittings.ListHistory(ctx, sittingID, store.ExamSittingHistoryMaximum); err != nil {
		return Read{}, storeErrors.Translate(err)
	}
	read.HistoryVisible = true
	return read, nil
}

// ListExamSittingsWithCounts is ListExamSittings with each sitting's
// candidate count, read for the whole page in one query.
func (s *Service) ListExamSittingsWithCounts(
	ctx context.Context,
	inv invocation.Invocation,
	query ListQuery,
) (_ []ListedSitting, hasMore bool, resultErr error) {
	defer func() { resultErr = publicFailures.Conceal(resultErr) }()
	page, err := s.ListExamSittings(ctx, inv, query)
	if err != nil {
		return nil, false, err
	}

	ids := make([]model.ExamSittingID, 0, len(page.Items))
	for _, item := range page.Items {
		ids = append(ids, item.Sitting.ID)
	}
	counts, err := s.candidateCounts(ctx, ids)
	if err != nil {
		return nil, false, err
	}

	items := make([]ListedSitting, 0, len(page.Items))
	for _, item := range page.Items {
		items = append(items, ListedSitting{ExamSittingSnapshot: item, Candidates: counts[item.Sitting.ID]})
	}
	return items, page.HasMore, nil
}

func (s *Service) candidateCounts(ctx context.Context, ids []model.ExamSittingID) (map[model.ExamSittingID]CandidateCount, error) {
	stored, err := s.sittings.CountCandidates(ctx, ids)
	if err != nil {
		return nil, storeErrors.Translate(err)
	}

	counts := make(map[model.ExamSittingID]CandidateCount, len(ids))
	for _, id := range ids {
		count, found := stored[id]
		if !found {
			return nil, apperror.New("exam.sitting.unavailable").Wrap(errors.New("Exam Sitting Store counted no candidates for a listed sitting"))
		}
		counts[id] = CandidateCount{Count: count, IsLowerBound: count == store.MaximumListCount}
	}
	return counts, nil
}
