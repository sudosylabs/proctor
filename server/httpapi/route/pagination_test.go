// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package route

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/sudosylabs/proctor/server/app/paging"
	"github.com/sudosylabs/proctor/server/model"
)

func paginationTestRequest(t *testing.T, target string) Request {
	t.Helper()
	return Request{HTTP: httptest.NewRequestWithContext(t.Context(), http.MethodGet, target, nil)}
}

func TestPageRequestUsesOneLimitAndCursorIdiom(t *testing.T) {
	t.Parallel()
	page, err := paginationTestRequest(t, "/things").Page()
	require.NoError(t, err)
	require.Equal(t, PageRequest{Limit: DefaultPageLimit}, page)
	page, err = paginationTestRequest(t, "/things?limit=200&cursor=abc").Page()
	require.NoError(t, err)
	require.Equal(t, PageRequest{Limit: 200, Cursor: "abc"}, page)
	for _, query := range []string{"limit=0", "limit=201", "limit=x", "limit=1&limit=2", "cursor=", "cursor=a&cursor=b"} {
		_, err = paginationTestRequest(t, "/things?"+query).Page()
		require.Error(t, err, "accepted %s", query)
	}
}

func TestNewListResponseOmitsNextCursorOnTheLastPage(t *testing.T) {
	t.Parallel()
	identity := func(value string) string { return value }
	next := func(last string) (string, error) { return "after-" + last, nil }
	last, err := NewList([]string(nil), false, identity, next)
	require.NoError(t, err)
	encoded, err := json.Marshal(last)
	require.NoError(t, err)
	require.JSONEq(t, `{"items":[]}`, string(encoded))
	more, err := NewList([]string{"a", "b"}, true, identity, next)
	require.NoError(t, err)
	require.Equal(t, List[string]{Items: []string{"a", "b"}, NextCursor: "after-b"}, more)
	_, err = NewList([]string{}, true, identity, next)
	require.Error(t, err, "a page that claims more without items made no progress")
}

func TestBoundedListResultPagesTheStableOrder(t *testing.T) {
	t.Parallel()
	base := time.Date(2026, time.October, 1, 12, 0, 0, 0, time.UTC)
	type item struct {
		at time.Time
		id string
	}
	items := []item{
		{base, model.NewID()}, {base.Add(2 * time.Second), model.NewID()}, {base.Add(time.Second), model.NewID()},
	}
	key := func(value item) BoundedKey { return BoundedKey{At: value.at, ID: value.id} }
	present := func(value item) string { return value.id }

	first, err := BoundedList(paginationTestRequest(t, "/things?limit=2"), "thing", items, key, present)
	require.NoError(t, err)
	require.Equal(t, []string{items[1].id, items[2].id}, first.Items, "newest first")
	require.NotEmpty(t, first.NextCursor)

	// Removing an item already returned does not move the continuation.
	remaining := []item{items[0], items[2]}
	second, err := BoundedList(paginationTestRequest(t, "/things?limit=2&cursor="+url.QueryEscape(first.NextCursor)), "thing", remaining, key, present)
	require.NoError(t, err)
	require.Equal(t, []string{items[0].id}, second.Items)
	require.Empty(t, second.NextCursor, "last page")

	_, err = BoundedList(paginationTestRequest(t, "/things?cursor=not-a-cursor"), "thing", items, key, present)
	require.Error(t, err)

	names := []string{"beta", "alpha", "gamma"}
	byName, err := BoundedList(paginationTestRequest(t, "/things?limit=1"), "name", names,
		func(name string) BoundedKey { return BoundedKey{Name: name, ID: name} }, func(name string) string { return name })
	require.NoError(t, err)
	require.Equal(t, []string{"alpha"}, byName.Items, "ascending by name")
	byName, err = BoundedList(paginationTestRequest(t, "/things?limit=5&cursor="+url.QueryEscape(byName.NextCursor)), "name", names,
		func(name string) BoundedKey { return BoundedKey{Name: name, ID: name} }, func(name string) string { return name })
	require.NoError(t, err)
	require.Equal(t, []string{"beta", "gamma"}, byName.Items)
}

func TestPageTotalMembersRefuseAMismatchedPage(t *testing.T) {
	t.Parallel()
	for name, test := range map[string]struct {
		total     paging.Total
		continued bool
	}{
		"a continued page counted":    {total: paging.Total{Counted: true, Total: 1}, continued: true},
		"a first page left uncounted": {total: paging.Total{}, continued: false},
	} {
		t.Run(name, func(t *testing.T) {
			_, _, err := PageTotal(test.total, test.continued)
			require.ErrorIs(t, err, ErrUnpresentable)
		})
	}
	total, lowerBound, err := PageTotal(paging.Total{}, true)
	require.NoError(t, err)
	require.Nil(t, total)
	require.Nil(t, lowerBound)
}
