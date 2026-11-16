// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

// Package paging holds the bounded page every listing returns and the limit
// and probe rules behind it. Cursor encoding and wire envelopes belong to the
// transports. It may import only store and app/apperror.
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
// Descending reverses the order for a listing that offers it.
type NameQuery struct {
	AfterName  string
	AfterID    string
	Descending bool
	Limit      int
}

// Total is the count sent with a collection's first page only, stopped at
// store.MaximumListCount and flagged TotalIsLowerBound when it is. Later
// pages leave it zero; the client keeps the first page's total.
type Total struct {
	Counted           bool
	Total             int
	TotalIsLowerBound bool
}

// Counted is a Page with its first-page Total; Total is zero on later pages.
type Counted[T any] struct {
	Page[T]
	Total
}

// CountFirst counts a collection for its first page only. count must apply
// the page's filters and visibility. It reports false when the store's count
// contradicts the page it accompanies.
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

// CountWhole is the first-page total of a collection returned whole, capped
// at store.MaximumListCount like a store count.
func CountWhole(items int) Total {
	total := min(items, store.MaximumListCount)
	return Total{Counted: true, Total: total, TotalIsLowerBound: total == store.MaximumListCount}
}

// ResolveLimit validates a requested page size and also returns the probe
// size (limit+1) the store reads to learn whether another page follows.
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
