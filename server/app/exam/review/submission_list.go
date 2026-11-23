// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package review

import (
	"context"
	"errors"
	"slices"

	"github.com/sudosylabs/proctor/server/app/apperror"
	"github.com/sudosylabs/proctor/server/app/invocation"
	"github.com/sudosylabs/proctor/server/model"
	"github.com/sudosylabs/proctor/server/store"
)

// SubmissionListQuery selects one page of submissions. ExamID and SittingID
// name a sitting; both zero list every exam the caller may see.
type SubmissionListQuery struct {
	ExamID    model.ExamID
	SittingID model.ExamSittingID
	Filter    store.SubmissionListFilter
	Sort      store.SubmissionListSort
	After     *store.SubmissionListCursor
	Limit     int
}

// SubmissionListPage is one page; HasMore reports that another follows.
// ReviewVisible says whether the reader may see reviews on a sitting's list,
// where it holds for every row.
type SubmissionListPage struct {
	store.SubmissionListPage
	ReviewVisible bool
	HasMore       bool
}

// ListSittingSubmissions lists one sitting's sealed submissions, other than
// the caller's own, under submission.view. Reviews show, and review filters
// apply, only with submission.review; asking to filter by review without it
// is refused rather than ignored. The first page also counts the sitting.
func (s *Service) ListSittingSubmissions(
	ctx context.Context,
	inv invocation.Invocation,
	query SubmissionListQuery,
) (_ SubmissionListPage, resultErr error) {
	defer func() { resultErr = publicFailures.Conceal(resultErr) }()
	if !query.ExamID.IsValid() || !query.SittingID.IsValid() || !validSubmissionListQuery(query) {
		return SubmissionListPage{}, apperror.Invalid("exam.integrity_review.invalid", "submission_list")
	}
	if s.authorizer.Sittings == nil {
		return SubmissionListPage{}, apperror.New("exam.integrity_review.unavailable")
	}
	authorization, err := s.authorizer.Sittings.AuthorizeSittingSubmissions(ctx, inv, query.ExamID, query.SittingID)
	if err != nil {
		return SubmissionListPage{}, err
	}
	if !authorization.ReviewVisible && (len(query.Filter.ReviewStates) > 0 || len(query.Filter.ReleaseStates) > 0) {
		return SubmissionListPage{}, apperror.New("authorization.denied")
	}

	stored, err := s.reviews.ListSubmissions(ctx, store.SubmissionListOptions{
		ExamID: query.ExamID, SittingID: query.SittingID, ReviewVisible: authorization.ReviewVisible,
		ExcludeCandidateUserID: inv.Principal().UserID, Filter: cloneSubmissionListFilter(query.Filter),
		Sort: query.Sort, After: query.After, IncludeCounts: query.After == nil, Limit: query.Limit + 1,
	})
	if err != nil {
		return SubmissionListPage{}, storeErrors.Translate(err)
	}
	page, err := submissionListPage(stored, query, query.After == nil)
	if err != nil {
		return SubmissionListPage{}, err
	}
	for _, item := range page.Items {
		if item.SittingID != query.SittingID || item.ExamID != query.ExamID || item.ReviewVisible != authorization.ReviewVisible {
			return SubmissionListPage{}, apperror.New("exam.integrity_review.unavailable").Wrap(errors.New("submission row outside its sitting"))
		}
	}
	page.ReviewVisible = authorization.ReviewVisible
	return page, nil
}

// ListSubmissions lists sealed submissions across every exam the caller may
// see under submission.view, other than their own. A row's review shows, and
// review filters match it, only where the caller holds submission.review on
// its exam.
func (s *Service) ListSubmissions(
	ctx context.Context,
	inv invocation.Invocation,
	query SubmissionListQuery,
) (_ SubmissionListPage, resultErr error) {
	defer func() { resultErr = publicFailures.Conceal(resultErr) }()
	if !query.ExamID.IsZero() || !query.SittingID.IsZero() || !validSubmissionListQuery(query) {
		return SubmissionListPage{}, apperror.Invalid("exam.integrity_review.invalid", "submission_list")
	}
	if s.authorizer.Sittings == nil {
		return SubmissionListPage{}, apperror.New("exam.integrity_review.unavailable")
	}
	view, review, err := s.authorizer.Sittings.AuthorizeSubmissionList(ctx, inv)
	if err != nil {
		return SubmissionListPage{}, err
	}
	stored, err := s.reviews.ListSubmissions(ctx, store.SubmissionListOptions{
		Visibility: &view, ReviewVisibility: &review, ExcludeCandidateUserID: inv.Principal().UserID,
		Filter: cloneSubmissionListFilter(query.Filter), Sort: query.Sort, After: query.After, Limit: query.Limit + 1,
	})
	if err != nil {
		return SubmissionListPage{}, storeErrors.Translate(err)
	}
	return submissionListPage(stored, query, false)
}

func validSubmissionListQuery(query SubmissionListQuery) bool {
	if query.Limit < 1 || query.Limit > store.SubmissionListMaximum ||
		query.Sort != store.SubmissionListBySubmitted && query.Sort != store.SubmissionListByName ||
		len(query.Filter.ReviewStates) > 3 || len(query.Filter.ReleaseStates) > 2 {
		return false
	}
	for i, state := range query.Filter.ReviewStates {
		if !state.IsValid() || slices.Contains(query.Filter.ReviewStates[:i], state) {
			return false
		}
	}
	for i, state := range query.Filter.ReleaseStates {
		if state != model.SubmissionReviewWithheld && state != model.SubmissionReviewReleased ||
			slices.Contains(query.Filter.ReleaseStates[:i], state) {
			return false
		}
	}
	if after := query.After; after != nil &&
		(!after.SubmissionID.IsValid() ||
			query.Sort == store.SubmissionListBySubmitted && after.SubmittedAt.IsZero() ||
			query.Sort == store.SubmissionListByName && after.Name == "") {
		return false
	}
	return true
}

func cloneSubmissionListFilter(filter store.SubmissionListFilter) store.SubmissionListFilter {
	clone := store.SubmissionListFilter{
		ReviewStates:  slices.Clone(filter.ReviewStates),
		ReleaseStates: slices.Clone(filter.ReleaseStates),
	}
	if filter.HasFlags != nil {
		value := *filter.HasFlags
		clone.HasFlags = &value
	}
	return clone
}

func submissionListPage(stored *store.SubmissionListPage, query SubmissionListQuery, counted bool) (SubmissionListPage, error) {
	if stored == nil || stored.Items == nil || len(stored.Items) > query.Limit+1 || counted != (stored.Counts != nil) {
		return SubmissionListPage{}, apperror.New("exam.integrity_review.unavailable").Wrap(errors.New("inconsistent submission page"))
	}
	page := SubmissionListPage{HasMore: len(stored.Items) > query.Limit}
	page.Items = slices.Clone(stored.Items[:min(len(stored.Items), query.Limit)])
	for i := range page.Items {
		if review := page.Items[i].Review; review != nil {
			if !page.Items[i].ReviewVisible {
				return SubmissionListPage{}, apperror.New("exam.integrity_review.unavailable").Wrap(errors.New("hidden review in submission page"))
			}
			value := *review
			page.Items[i].Review = &value
		}
	}
	if stored.Counts != nil {
		counts := *stored.Counts
		page.Counts = &counts
	}
	return page, nil
}
