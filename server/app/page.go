// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package app

import (
	"github.com/sudosylabs/proctor/server/app/apperror"
	"github.com/sudosylabs/proctor/server/store"
)

// Collection pages hold at most MaximumPageLimit items; a zero limit selects
// DefaultPageLimit.
const (
	DefaultPageLimit = 50
	MaximumPageLimit = 200
)

// Page is one bounded page of a collection in its stable order. HasMore
// reports that another page follows the last item.
type Page[T any] struct {
	Items   []T
	HasMore bool
}

// NamePageQuery continues a listing ordered by name then identity: AfterName
// and AfterID are the last item of the previous page, both set or both empty.
type NamePageQuery struct {
	AfterName string
	AfterID   string
	Limit     int
}

// PageTotal is a counted collection's first-page total. A page read without a
// keyset sets Counted and carries Total: every item the filters and the
// caller's visibility match across all pages, stopped at
// store.MaximumListCount, where TotalIsLowerBound is set. Later pages leave
// it zero and the client keeps the first page's total.
type PageTotal struct {
	Counted           bool
	Total             int
	TotalIsLowerBound bool
}

// CountedPage is a Page with its first-page total.
type CountedPage[T any] struct {
	Page[T]
	PageTotal
}

// countFirstPage counts a collection for its first page and checks the count
// against the page it accompanies. count reads the same filters and
// visibility as the page, stopping at maximum. A later page is not counted.
// It reports false when the Store answered a count the page contradicts.
func countFirstPage(first bool, items int, count func(maximum int) (int, error)) (PageTotal, bool, error) {
	if !first {
		return PageTotal{}, true, nil
	}
	total, err := count(store.MaximumListCount)
	if err != nil {
		return PageTotal{}, true, err
	}
	if total < items || total > store.MaximumListCount {
		return PageTotal{}, false, nil
	}
	return PageTotal{Counted: true, Total: total, TotalIsLowerBound: total == store.MaximumListCount}, true, nil
}

// resolvePageLimit validates a requested page size and returns it with the
// probe size the Store reads to learn whether another page follows.
func resolvePageLimit(limit int, field string) (int, int, error) {
	if limit == 0 {
		limit = DefaultPageLimit
	}
	if limit < 1 || limit > MaximumPageLimit {
		return 0, 0, apperror.Invalid("request.invalid", field)
	}
	return limit, limit + 1, nil
}

// newPage trims a probe read of up to limit+1 items into one page.
func newPage[T any](items []T, limit int) Page[T] {
	if items == nil {
		items = []T{}
	}
	if len(items) > limit {
		return Page[T]{Items: items[:limit], HasMore: true}
	}
	return Page[T]{Items: items}
}
