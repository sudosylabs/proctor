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

// NoShowPage is one page of no-shows; HasMore reports that a later page
// exists.
type NoShowPage struct {
	Items   []store.ExamSittingNoShow
	HasMore bool
}

// ListNoShowsQuery pages the enrolled candidates of one Sitting who never
// started an Attempt.
type ListNoShowsQuery struct {
	ExamID               model.ExamID
	SittingID            model.ExamSittingID
	AfterCandidateUserID model.UserID
	Limit                int
}

// ListExamSittingNoShows returns one page of a Closing or Closed Sitting's
// enrolled candidates who never started an Attempt, to a caller authorized to
// view the Sitting.
func (s *Service) ListExamSittingNoShows(
	ctx context.Context,
	inv invocation.Invocation,
	query ListNoShowsQuery,
) (_ NoShowPage, resultErr error) {
	defer func() { resultErr = publicFailures.Conceal(resultErr) }()
	if !query.ExamID.IsValid() ||
		!query.SittingID.IsValid() ||
		(!query.AfterCandidateUserID.IsZero() && !query.AfterCandidateUserID.IsValid()) ||
		query.Limit < 1 ||
		query.Limit > 200 {
		return NoShowPage{}, apperror.Invalid("exam.sitting.invalid", "no_show_list")
	}

	snapshot, err := s.GetExamSitting(ctx, inv, query.ExamID, query.SittingID)
	if err != nil {
		return NoShowPage{}, err
	}
	if snapshot.Sitting == nil || (snapshot.Sitting.State != model.ExamSittingClosing && snapshot.Sitting.State != model.ExamSittingClosed) {
		return NoShowPage{}, apperror.New("exam.sitting.state_conflict")
	}

	items, err := s.sittings.ListNoShows(ctx, store.ExamSittingNoShowListOptions{
		SittingID:            query.SittingID,
		AfterCandidateUserID: query.AfterCandidateUserID,
		Limit:                query.Limit + 1,
	})
	if err != nil {
		return NoShowPage{}, storeErrors.Translate(err)
	}
	if len(items) > query.Limit+1 {
		return NoShowPage{}, apperror.New("exam.sitting.unavailable").Wrap(errors.New("Exam Sitting Store returned an oversized no-show page"))
	}

	hasMore := len(items) > query.Limit
	if hasMore {
		items = items[:query.Limit]
	}

	previous := query.AfterCandidateUserID
	for _, item := range items {
		if !item.CandidateUserID.IsValid() || (!previous.IsZero() && item.CandidateUserID.String() <= previous.String()) {
			return NoShowPage{}, apperror.New("exam.sitting.unavailable").Wrap(errors.New("Exam Sitting Store returned an invalid no-show page"))
		}

		previous = item.CandidateUserID
	}

	if items == nil {
		items = []store.ExamSittingNoShow{}
	}

	return NoShowPage{Items: items, HasMore: hasMore}, nil
}
