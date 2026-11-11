// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

// Package paging holds the bounded collection page every application area
// returns: Page, its counted form Counted with a first-page Total, the
// name-ordered NameQuery keyset, and the limit and probe rules a listing
// applies. It owns no listing, cursor encoding, or wire envelope; transports
// map a Page onto their own collection shape. It may import only store and
// app/apperror.
package paging

import (
	"github.com/sudosylabs/proctor/server/app/apperror"
	"github.com/sudosylabs/proctor/server/store"
)

// Collection pages hold at most MaximumLimit items; a zero limit selects
// DefaultLimit.
const (
	DefaultLimit = 50
	MaximumLimit = 200
)

// Page is one bounded page of a collection in its stable order. HasMore
// reports that another page follows the last item.
type Page[T any] struct {
	Items   []T
	HasMore bool
}

// NameQuery continues a listing ordered by name then identity: AfterName
// and AfterID are the last item of the previous page, both set or both empty.
type NameQuery struct {
	AfterName string
	AfterID   string
	Limit     int
}

// Total is a counted collection's first-page total. A page read without a
// keyset sets Counted and carries Total: every item the filters and the
// caller's visibility match across all pages, stopped at
// store.MaximumListCount, where TotalIsLowerBound is set. Later pages leave
// it zero and the client keeps the first page's total.
type Total struct {
	Counted           bool
	Total             int
	TotalIsLowerBound bool
}

// Counted is a Page with its first-page Total.
type Counted[T any] struct {
	Page[T]
	Total
}

// CountFirst counts a collection for its first page and checks the count
// against the page it accompanies. count reads the same filters and
// visibility as the page, stopping at maximum. A later page is not counted.
// It reports false when the Store answered a count the page contradicts.
func CountFirst(first bool, items int, count func(maximum int) (int, error)) (Total, bool, error) {
	if !first {
		return Total{}, true, nil
	}
	total, err := count(store.MaximumListCount)
	if err != nil {
		return Total{}, true, err
	}
	if total < items || total > store.MaximumListCount {
		return Total{}, false, nil
	}
	return Total{Counted: true, Total: total, TotalIsLowerBound: total == store.MaximumListCount}, true, nil
}

// ResolveLimit validates a requested page size and returns it with the
// probe size the Store reads to learn whether another page follows.
func ResolveLimit(limit int, field string) (int, int, error) {
	if limit == 0 {
		limit = DefaultLimit
	}
	if limit < 1 || limit > MaximumLimit {
		return 0, 0, apperror.Invalid("request.invalid", field)
	}
	return limit, limit + 1, nil
}

// New trims a probe read of up to limit+1 items into one page.
func New[T any](items []T, limit int) Page[T] {
	if items == nil {
		items = []T{}
	}
	if len(items) > limit {
		return Page[T]{Items: items[:limit], HasMore: true}
	}
	return Page[T]{Items: items}
}
