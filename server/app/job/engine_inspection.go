// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package job

import (
	"context"
	"errors"
	"slices"
	"time"

	"github.com/sudosylabs/proctor/server/model"
	"github.com/sudosylabs/proctor/server/store"
)

type ListQuery struct {
	// Types, when set, keeps only these job types. A type that is not
	// operator-visible matches nothing rather than failing, so the answer is
	// the same as for a type with no Jobs.
	Types    []model.JobType
	Statuses []model.JobStatus
	// Ascending reads oldest first; the default is newest first.
	Ascending       bool
	BeforeCreatedAt time.Time
	BeforeID        model.JobID
	Limit           int
}

type AttemptListQuery struct {
	JobID               model.JobID
	BeforeNumber, Limit int
}

// ErrQueryInvalid reports a list, count or attempts query outside its bounds,
// such as a limit over 200, an incomplete keyset or an unknown status.
var ErrQueryInvalid = errors.New("job query is invalid")

// List returns one page of operator-visible jobs, newest first unless
// Ascending, 50 when Limit is zero. Only the first page is counted.
func (r *Engine) List(ctx context.Context, query ListQuery) (Page, error) {
	if query.Limit == 0 {
		query.Limit = 50
	}
	if query.Limit < 1 || query.Limit > 200 || (!query.BeforeID.IsZero() && (!query.BeforeID.IsValid() || query.BeforeCreatedAt.IsZero())) {
		return Page{}, ErrQueryInvalid
	}

	for _, status := range query.Statuses {
		if !publicStatus(status) {
			return Page{}, ErrQueryInvalid
		}
	}

	for _, jobType := range query.Types {
		if !jobType.IsValid() {
			return Page{}, ErrQueryInvalid
		}
	}
	types := r.operatorTypes()
	if len(query.Types) > 0 {
		types = slices.DeleteFunc(types, func(jobType model.JobType) bool {
			return !slices.Contains(query.Types, jobType)
		})
		if len(types) == 0 {
			return Page{Items: []View{}, Counted: query.BeforeID.IsZero()}, nil
		}
	}

	options := store.JobListOptions{
		Types:           types,
		Statuses:        query.Statuses,
		Ascending:       query.Ascending,
		BeforeCreatedAt: query.BeforeCreatedAt,
		BeforeID:        query.BeforeID,
		Limit:           query.Limit + 1,
	}
	records, err := r.deps.Store.List(ctx, options)
	if err != nil {
		return Page{}, err
	}

	page := Page{HasMore: len(records) > query.Limit}
	records = records[:min(len(records), query.Limit)]
	page.Items = make([]View, 0, len(records))
	for _, record := range records {
		if record == nil {
			return Page{}, errors.New("job store returned a nil record")
		}
		if r.operatorVisible(record.Type) {
			page.Items = append(page.Items, projectJob(record))
		}
	}

	if !query.BeforeID.IsZero() {
		return page, nil
	}
	if page.Total, err = r.deps.Store.Count(ctx, options, store.MaximumListCount); err != nil {
		return Page{}, err
	}
	if page.Total < len(page.Items) || page.Total > store.MaximumListCount {
		return Page{}, errors.New("job store count contradicts its page")
	}

	page.Counted, page.TotalIsLowerBound = true, page.Total == store.MaximumListCount
	return page, nil
}

// CountRecentFailures counts operator-visible failed jobs last updated within
// the span before database time, stopping at maximum.
func (r *Engine) CountRecentFailures(ctx context.Context, within time.Duration, maximum int) (int, error) {
	if within <= 0 || maximum < 1 || maximum > store.MaximumListCount {
		return 0, ErrQueryInvalid
	}
	return r.deps.Store.Count(ctx, store.JobListOptions{
		Types:         r.operatorTypes(),
		Statuses:      []model.JobStatus{model.JobStatusFailed},
		UpdatedWithin: within,
		Limit:         1,
	}, maximum)
}

// Get reports a job whose type is not operator-visible as not found.
func (r *Engine) Get(ctx context.Context, id model.JobID) (View, error) {
	record, err := r.operatorJob(ctx, id)
	if err != nil {
		return View{}, err
	}
	return projectJob(record), nil
}

// Attempts returns one page of an operator-visible job's attempts, 50 when
// Limit is zero.
func (r *Engine) Attempts(ctx context.Context, query AttemptListQuery) (AttemptPage, error) {
	if _, err := r.operatorJob(ctx, query.JobID); err != nil {
		return AttemptPage{}, err
	}
	if query.Limit == 0 {
		query.Limit = 50
	}
	if query.Limit < 1 || query.Limit > 200 || query.BeforeNumber < 0 {
		return AttemptPage{}, ErrQueryInvalid
	}

	records, err := r.deps.Store.ListAttemptsPage(ctx, store.JobAttemptListOptions{
		JobID:        query.JobID,
		BeforeNumber: query.BeforeNumber,
		Limit:        query.Limit + 1,
	})
	if err != nil {
		return AttemptPage{}, err
	}

	page := AttemptPage{HasMore: len(records) > query.Limit}
	records = records[:min(len(records), query.Limit)]
	page.Items = make([]AttemptView, 0, len(records))
	for _, record := range records {
		page.Items = append(page.Items, projectAttempt(record))
	}

	return page, nil
}

func (r *Engine) operatorJob(ctx context.Context, id model.JobID) (*model.Job, error) {
	record, err := r.deps.Store.Get(ctx, id)
	if err != nil {
		return nil, err
	}
	if record == nil || !r.operatorVisible(record.Type) {
		return nil, store.NewErrNotFound("job", id.String())
	}
	return record, nil
}

func (r *Engine) operatorTypes() []model.JobType {
	result := make([]model.JobType, 0)
	for _, jobType := range r.registry.Types() {
		if r.operatorVisible(jobType) {
			result = append(result, jobType)
		}
	}
	return result
}

func (r *Engine) operatorVisible(jobType model.JobType) bool {
	descriptor, err := r.registry.Descriptor(jobType)
	return err == nil && descriptor.Visibility == VisibilityOperator
}
