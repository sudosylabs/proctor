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
	"maps"
	"slices"
	"time"

	"github.com/sudosylabs/proctor/server/app/apperror"
	"github.com/sudosylabs/proctor/server/app/invocation"
	"github.com/sudosylabs/proctor/server/model"
	"github.com/sudosylabs/proctor/server/store"
)

// AcrossExamsQuery selects one page of sittings across exams, after the
// keyset of the last one seen.
type AcrossExamsQuery struct {
	States                []model.ExamSittingState
	StartsBefore          time.Time
	EndsAfter             time.Time
	AcademicUnitID        model.AcademicUnitID
	AfterScheduledStartAt time.Time
	AfterSittingID        model.ExamSittingID
	Limit                 int
}

// AcrossExamsPage is one page; HasMore reports that another follows.
type AcrossExamsPage struct {
	store.ExamSittingAcrossPage
	HasMore bool
}

// ListSittingsAcrossExams lists the sittings of every exam where the caller
// holds exam.sitting.view, earliest start first, as the exam catalogue
// scopes exams: an ordinary grant reaches exams the caller manages in a unit
// they belong to, an override grant every exam under it. One decision is
// recorded at the institution. Open and paused sittings carry the candidate
// board's counts, without the caller, and how many candidates need attention.
func (s *Service) ListSittingsAcrossExams(
	ctx context.Context,
	inv invocation.Invocation,
	query AcrossExamsQuery,
) (_ AcrossExamsPage, resultErr error) {
	defer func() { resultErr = publicFailures.Conceal(resultErr) }()
	principal := inv.Principal()
	if principal.Validate() != nil ||
		query.Limit < 1 || query.Limit > 200 ||
		query.AfterScheduledStartAt.IsZero() != query.AfterSittingID.IsZero() ||
		!query.AfterSittingID.IsZero() && !query.AfterSittingID.IsValid() ||
		!query.AcademicUnitID.IsZero() && !query.AcademicUnitID.IsValid() ||
		!query.StartsBefore.IsZero() && !query.EndsAfter.IsZero() && !query.EndsAfter.Before(query.StartsBefore) {
		return AcrossExamsPage{}, apperror.Invalid("exam.sitting.invalid", "sitting_list")
	}
	for i, state := range query.States {
		if !state.IsValid() || slices.Contains(query.States[:i], state) {
			return AcrossExamsPage{}, apperror.Invalid("exam.sitting.invalid", "state")
		}
	}

	visibility, err := s.authorizer.AuthorizeExamManagerList(ctx, inv, model.ActionExamSittingView, model.ActionExamSittingViewOverride)
	if err != nil {
		return AcrossExamsPage{}, err
	}
	stored, err := s.sittings.ListAcrossExams(ctx, store.ExamSittingAcrossListOptions{
		Visibility: visibility, ReaderUserID: principal.UserID, States: slices.Clone(query.States),
		StartsBefore: model.TimeUTC(query.StartsBefore), EndsAfter: model.TimeUTC(query.EndsAfter),
		AcademicUnitID: query.AcademicUnitID, AfterScheduledStartAt: model.TimeUTC(query.AfterScheduledStartAt),
		AfterSittingID: query.AfterSittingID, Limit: query.Limit + 1,
	})
	if err != nil {
		return AcrossExamsPage{}, storeErrors.Translate(err)
	}
	if stored == nil || stored.Items == nil || len(stored.Items) > query.Limit+1 {
		return AcrossExamsPage{}, apperror.New("exam.sitting.unavailable").Wrap(errors.New("inconsistent sitting page"))
	}

	page := AcrossExamsPage{HasMore: len(stored.Items) > query.Limit}
	page.ServerTime = stored.ServerTime
	page.Items = slices.Clone(stored.Items[:min(len(stored.Items), query.Limit)])
	for i := range page.Items {
		item := &page.Items[i]
		live := item.State == model.ExamSittingOpen || item.State == model.ExamSittingPaused
		if live != (item.Counts != nil) {
			return AcrossExamsPage{}, apperror.New("exam.sitting.unavailable").Wrap(errors.New("sitting counts mismatch its state"))
		}
		if item.Counts != nil {
			counts := *item.Counts
			counts.ByPresenceState = maps.Clone(item.Counts.ByPresenceState)
			item.Counts = &counts
		}
	}
	return page, nil
}
