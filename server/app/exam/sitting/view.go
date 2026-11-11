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
	"time"

	"github.com/sudosylabs/proctor/server/app/apperror"
	"github.com/sudosylabs/proctor/server/app/invocation"
	"github.com/sudosylabs/proctor/server/model"
	"github.com/sudosylabs/proctor/server/store"
)

type ListQuery struct {
	ExamID                 model.ExamID
	ClassID                model.ClassID
	States                 []model.ExamSittingState
	OverlapStartAt         time.Time
	OverlapEndAt           time.Time
	BeforeScheduledStartAt time.Time
	BeforeSittingID        model.ExamSittingID
	Limit                  int
}

// Page is one page of Sittings, latest scheduled start first; HasMore reports
// that an earlier page exists.
type Page struct {
	Items   []store.ExamSittingSnapshot
	HasMore bool
}

// GetExamSitting returns one Sitting to a caller authorized to view it,
// directly or through an override.
func (s *Service) GetExamSitting(
	ctx context.Context,
	inv invocation.Invocation,
	examID model.ExamID,
	sittingID model.ExamSittingID,
) (_ store.ExamSittingSnapshot, resultErr error) {
	defer func() { resultErr = publicFailures.Conceal(resultErr) }()
	if !examID.IsValid() || !sittingID.IsValid() {
		return store.ExamSittingSnapshot{}, apperror.Invalid("exam.sitting.invalid", "identity")
	}

	at := model.TimeUTC(s.now())
	if _, err := s.authorizeManagerAction(
		ctx,
		inv,
		examID,
		model.Resource{Type: model.ResourceExamSitting, ID: sittingID.String()},
		at,
		model.ActionExamSittingView,
		model.ActionExamSittingViewOverride,
	); err != nil {
		return store.ExamSittingSnapshot{}, err
	}

	snapshot, err := s.sittings.Get(ctx, examID, sittingID)
	if err != nil {
		return store.ExamSittingSnapshot{}, storeErrors.Translate(err)
	}

	value, err := requireSnapshot(snapshot)
	if err != nil {
		return store.ExamSittingSnapshot{}, err
	}

	if value.Sitting.ExamID != examID || value.Sitting.ID != sittingID {
		return store.ExamSittingSnapshot{}, apperror.New("exam.sitting.unavailable").Wrap(
			errors.New("Exam Sitting Store returned a mismatched snapshot"),
		)
	}

	return value, nil
}

// ListExamSittings returns one page of an Exam's Sittings, optionally
// filtered by state, to a caller authorized to view the Exam.
func (s *Service) ListExamSittings(ctx context.Context, inv invocation.Invocation, query ListQuery) (_ Page, resultErr error) {
	defer func() { resultErr = publicFailures.Conceal(resultErr) }()
	query.States = append([]model.ExamSittingState(nil), query.States...)
	options, err := listOptions(query)
	if err != nil {
		return Page{}, err
	}
	at := model.TimeUTC(s.now())
	if _, err = s.authorizeExam(ctx, inv, query.ExamID, at, model.ActionExamView, model.ActionExamViewOverride); err != nil {
		return Page{}, err
	}
	options.Limit++
	items, err := s.sittings.List(ctx, options)
	if err != nil {
		return Page{}, storeErrors.Translate(err)
	}
	for index := range items {
		if items[index].Sitting == nil || items[index].Sitting.ExamID != query.ExamID || items[index].Sitting.Validate() != nil {
			return Page{}, apperror.New("exam.sitting.unavailable").Wrap(errors.New("Exam Sitting Store returned an invalid list item"))
		}
	}
	page := Page{Items: items, HasMore: len(items) > query.Limit}
	if page.HasMore {
		page.Items = page.Items[:query.Limit]
	}
	if page.Items == nil {
		page.Items = []store.ExamSittingSnapshot{}
	}
	return page, nil
}

func listOptions(query ListQuery) (store.ExamSittingListOptions, error) {
	invalidIdentity := !query.ExamID.IsValid() || !query.ClassID.IsZero() && !query.ClassID.IsValid()
	invalidCursor := query.BeforeScheduledStartAt.IsZero() != query.BeforeSittingID.IsZero() ||
		!query.BeforeSittingID.IsZero() && !query.BeforeSittingID.IsValid()
	invalidOverlap := query.OverlapStartAt.IsZero() != query.OverlapEndAt.IsZero() ||
		!query.OverlapStartAt.IsZero() && !query.OverlapStartAt.Before(query.OverlapEndAt)
	if invalidIdentity || invalidCursor || invalidOverlap || query.Limit < 1 || query.Limit > 200 || len(query.States) > 6 {
		return store.ExamSittingListOptions{}, apperror.Invalid("exam.sitting.invalid", "list_query")
	}

	states := append([]model.ExamSittingState(nil), query.States...)
	seen := make(map[model.ExamSittingState]struct{}, len(states))
	for _, state := range states {
		if !state.IsValid() {
			return store.ExamSittingListOptions{}, apperror.Invalid("exam.sitting.invalid", "state")
		}
		if _, duplicate := seen[state]; duplicate {
			return store.ExamSittingListOptions{}, apperror.Invalid("exam.sitting.invalid", "state")
		}

		seen[state] = struct{}{}
	}

	return store.ExamSittingListOptions{
		ExamID:                 query.ExamID,
		ClassID:                query.ClassID,
		States:                 states,
		OverlapStartAt:         model.TimeUTC(query.OverlapStartAt),
		OverlapEndAt:           model.TimeUTC(query.OverlapEndAt),
		BeforeScheduledStartAt: model.TimeUTC(query.BeforeScheduledStartAt),
		BeforeSittingID:        query.BeforeSittingID,
		Limit:                  query.Limit,
	}, nil
}
