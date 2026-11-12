// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package route

import (
	"errors"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/sudosylabs/proctor/server/app/paging"
	"github.com/sudosylabs/proctor/server/model"
)

// Every collection uses one envelope and one pair of query parameters:
// `limit` (1..maximumPageLimit, default DefaultPageLimit) and an opaque
// `cursor` returned unchanged from the previous page's `next_cursor`, which is
// omitted on the last page.
const (
	DefaultPageLimit = 50
	maximumPageLimit = 200
)

// List is the one collection envelope.
type List[T any] struct {
	Items      []T    `json:"items"`
	NextCursor string `json:"next_cursor,omitempty"`
}

// CountedList is the collection envelope plus the first page's total. The count
// runs through the list's own filters and visibility and stops at
// store.MaximumListCount, so the total is exact below the bound and a lower
// bound at it. Later pages carry neither member.
type CountedList[T any] struct {
	Items             []T    `json:"items"`
	NextCursor        string `json:"next_cursor,omitempty"`
	Total             *int   `json:"total,omitempty"`
	TotalIsLowerBound *bool  `json:"total_is_lower_bound,omitempty"`
}

// PageTotal presents an application first-page total: both members on a counted
// page, neither on a later one. A page counted when it continued a cursor, or
// not counted when it began one, is unpresentable.
func PageTotal(total paging.Total, continued bool) (*int, *bool, error) {
	if total.Counted == continued {
		return nil, nil, ErrUnpresentable
	}
	if !total.Counted {
		return nil, nil, nil
	}
	count, lowerBound := total.Total, total.TotalIsLowerBound
	return &count, &lowerBound, nil
}

// WithPageTotal adds an application first-page total to an envelope.
func WithPageTotal[T any](envelope List[T], total paging.Total, continued bool) (CountedList[T], error) {
	count, lowerBound, err := PageTotal(total, continued)
	if err != nil {
		return CountedList[T]{}, err
	}
	return CountedList[T]{Items: envelope.Items, NextCursor: envelope.NextCursor, Total: count, TotalIsLowerBound: lowerBound}, nil
}

// PageRequest is the parsed `limit` and raw `cursor` of a collection read.
type PageRequest struct {
	Limit  int
	Cursor string
}

// Page parses the collection parameters. A repeated, empty, or out-of-range
// value is invalid.
func (r Request) Page() (PageRequest, error) {
	return ParsePage(r.HTTP)
}

// ParsePage applies the collection page rule of [Request.Page] to a plain
// request, for list parsers that read the rest of their query themselves.
func ParsePage(request *http.Request) (PageRequest, error) {
	values := request.URL.Query()
	page := PageRequest{Limit: DefaultPageLimit}
	for _, name := range []string{"limit", "cursor"} {
		if entries, present := values[name]; present && (len(entries) != 1 || entries[0] == "") {
			return PageRequest{}, InvalidRequest(errors.New(name + " must be one non-empty value"))
		}
	}
	if raw := values.Get("limit"); raw != "" {
		limit, err := strconv.Atoi(raw)
		if err != nil || limit < 1 || limit > maximumPageLimit {
			return PageRequest{}, InvalidRequest(errors.New("limit must be between 1 and 200"))
		}
		page.Limit = limit
	}
	page.Cursor = values.Get("cursor")
	return page, nil
}

// DecodePageCursor decodes the request's cursor with spec, or reports false on
// the first page.
func DecodePageCursor[T any](page PageRequest, spec CursorSpec[T]) (T, bool, error) {
	var zero T
	if page.Cursor == "" {
		return zero, false, nil
	}
	cursor, err := DecodeCursor(page.Cursor, spec)
	if err != nil {
		return zero, false, InvalidRequest(err)
	}
	return cursor, true, nil
}

// NewList maps one page of items into the envelope. When hasMore is set, next
// encodes the continuation after the page's last item; a page that claims more
// without items made no progress and cannot be presented.
func NewList[I, T any](items []I, hasMore bool, present func(I) T, next func(I) (string, error)) (List[T], error) {
	response := List[T]{Items: make([]T, 0, len(items))}
	for _, item := range items {
		response.Items = append(response.Items, present(item))
	}
	if !hasMore {
		return response, nil
	}
	if len(items) == 0 {
		return List[T]{}, Unpresentable(errors.New("collection page made no progress"))
	}
	cursor, err := next(items[len(items)-1])
	if err != nil {
		return List[T]{}, Unpresentable(err)
	}
	response.NextCursor = cursor
	return response, nil
}

// BoundedKey is one item's position in a complete bounded collection (a User's
// Sessions, the Institution's Roles) that the application returns whole. Such a
// collection is still paged on the wire, as keyset slices of its stable order:
// newest first by At then ID, or ascending by Name then ID.
type BoundedKey struct {
	At   time.Time
	Name string
	ID   string
}

// boundedPageCursor is the continuation of a bounded collection page.
type boundedPageCursor struct {
	At   string `json:"at,omitempty"`
	Name string `json:"name,omitempty"`
	ID   string `json:"id"`
}

func boundedPageCursorSpec(label string) CursorSpec[boundedPageCursor] {
	return CursorSpec[boundedPageCursor]{
		Label:                label,
		MaximumEncodedLength: 2048,
		Members:              []string{"at", "name", "id"},
		Validate: func(cursor boundedPageCursor) error {
			if cursor.ID == "" ||
				len(cursor.ID) > 128 ||
				!utf8.ValidString(cursor.ID) ||
				len(cursor.Name) > 1024 ||
				!utf8.ValidString(cursor.Name) ||
				(cursor.At == "") == (cursor.Name == "") {
				return errors.New("invalid " + label + " keyset")
			}

			if cursor.At != "" {
				if at, err := model.ParseRFC3339(cursor.At); err != nil || at.IsZero() {
					return errors.New("invalid " + label + " keyset")
				}
			}

			return nil
		},
	}
}

func (k BoundedKey) cursor() boundedPageCursor {
	if k.Name != "" {
		return boundedPageCursor{Name: k.Name, ID: k.ID}
	}
	return boundedPageCursor{At: model.FormatRFC3339(k.At), ID: k.ID}
}

func (c boundedPageCursor) key() BoundedKey {
	at, _ := model.ParseRFC3339(c.At)
	return BoundedKey{At: at, Name: c.Name, ID: c.ID}
}

// compareBoundedPageKeys orders name keys ascending and instant keys newest
// first, identity breaking ties in the same direction.
func compareBoundedPageKeys(a, b BoundedKey) int {
	if a.Name != "" || b.Name != "" {
		if order := strings.Compare(a.Name, b.Name); order != 0 {
			return order
		}
		return strings.Compare(a.ID, b.ID)
	}
	if order := b.At.Compare(a.At); order != 0 {
		return order
	}
	return strings.Compare(b.ID, a.ID)
}

// BoundedList pages a complete bounded collection by the request's limit and
// cursor and presents the page in the collection envelope.
func BoundedList[I, T any](
	request Request,
	label string,
	items []I,
	key func(I) BoundedKey,
	present func(I) T,
) (List[T], error) {
	page, err := request.Page()
	if err != nil {
		return List[T]{}, err
	}

	spec := boundedPageCursorSpec(label)
	cursor, continued, err := DecodePageCursor(page, spec)
	if err != nil {
		return List[T]{}, err
	}

	ordered := slices.Clone(items)
	slices.SortStableFunc(ordered, func(a, b I) int { return compareBoundedPageKeys(key(a), key(b)) })
	start := 0
	if continued {
		pivot := cursor.key()
		start, _ = slices.BinarySearchFunc(ordered, pivot, func(item I, target BoundedKey) int {
			return compareBoundedPageKeys(key(item), target)
		})
		if start < len(ordered) && compareBoundedPageKeys(key(ordered[start]), pivot) == 0 {
			start++
		}
	}

	ordered = ordered[start:]
	hasMore := len(ordered) > page.Limit
	if hasMore {
		ordered = ordered[:page.Limit]
	}

	return NewList(ordered, hasMore, present, func(last I) (string, error) {
		return EncodeCursor(key(last).cursor(), spec)
	})
}
