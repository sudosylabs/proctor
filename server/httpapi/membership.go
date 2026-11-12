// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package httpapi

import (
	"net/http"
	"time"

	"github.com/sudosylabs/proctor/server/app/academic"
	"github.com/sudosylabs/proctor/server/app/paging"
	"github.com/sudosylabs/proctor/server/httpapi/route"
	"github.com/sudosylabs/proctor/server/model"
	"github.com/sudosylabs/proctor/server/store"
)

// The membership sorts: each scope's start sort (a Class member's enrollment,
// an Academic Unit member's membership start), its default, and the member's
// name.
const (
	membershipSortEnrolled = "enrolled"
	membershipSortSince    = "since"
	membershipSortName     = "name"
)

// membershipStartSort names the start sort of the scope's member list.
func membershipStartSort(scopeType model.ResourceType) string {
	if scopeType == model.ResourceClass {
		return membershipSortEnrolled
	}
	return membershipSortSince
}

// membershipPageQuery parses one membership page. The order and the cursor
// it continues are returned together; a cursor written under another sort or
// direction is an invalid request.
func membershipPageQuery(request *http.Request, scope model.Resource) (academic.MembershipPageQuery, listOrder, membershipPageCursor, error) {
	values := request.URL.Query()
	query := academic.MembershipPageQuery{Limit: 50}
	var cursor membershipPageCursor
	for _, key := range []string{"limit", "cursor", "active_at", "history"} {
		if entries, present := values[key]; present && (len(entries) != 1 || entries[0] == "") {
			return query, listOrder{}, cursor, route.InvalidRequest(nil)
		}
	}

	page, err := route.ParsePage(request)
	if err != nil {
		return query, listOrder{}, cursor, err
	}
	query.Limit = page.Limit

	order, err := parseListOrder(values, []string{membershipStartSort(scope.Type), membershipSortName}, listAscending)
	if err != nil {
		return query, order, cursor, err
	}
	query.Descending = order.Descending()
	if order.Sort == membershipSortName {
		query.Order = store.MembershipOrderName
	}

	if values.Has("active_at") {
		parsed, err := model.ParseRFC3339(values.Get("active_at"))
		if err != nil || parsed.IsZero() {
			return query, order, cursor, route.InvalidRequest(err)
		}

		at := model.MillisFromTime(parsed)
		query.ActiveAt = &at
	}

	if values.Has("history") {
		value := values.Get("history")
		if value != "true" && value != "false" {
			return query, order, cursor, route.InvalidRequest(nil)
		}

		history := value == "true"
		query.History = &history
	}

	if query.History != nil && *query.History && query.ActiveAt != nil {
		return query, order, cursor, route.InvalidRequest(nil)
	}

	if values.Has("cursor") {
		decoded, err := route.DecodeCursor(values.Get("cursor"), membershipPageCursorSpec())
		if err != nil {
			return query, order, cursor, route.InvalidRequest(err)
		}

		if decoded.ScopeType != scope.Type ||
			decoded.ScopeID != scope.ID ||
			(query.ActiveAt != nil && *query.ActiveAt != *decoded.ActiveAt) ||
			(query.History != nil && *query.History != (*decoded.ActiveAt == 0)) ||
			!order.continues(decoded.Sort, decoded.Dir) {
			return query, order, cursor, route.InvalidRequest(nil)
		}

		cursor = decoded
		history := *cursor.ActiveAt == 0
		query.History = &history
		if !history {
			query.ActiveAt = cursor.ActiveAt
		}
	}

	return query, order, cursor, nil
}

// membershipListResult presents one membership page; its continuation fixes
// the scope, effective time, and order of the first page, which alone carries
// the total.
func membershipListResult[I, T any](
	scope model.Resource,
	activeAt int64,
	order listOrder,
	members []I,
	hasMore bool,
	total paging.Total,
	continued bool,
	present func(I) T,
	key func(I) membershipPageKey,
) (route.Result, error) {
	envelope, err := route.NewList(members, hasMore, present, func(last I) (string, error) {
		position := key(last)
		cursor := membershipPageCursor{
			ScopeType: scope.Type,
			ScopeID:   scope.ID,
			ActiveAt:  &activeAt,
			Sort:      order.Sort,
			Dir:       order.Dir,
			AfterID:   position.ID,
		}
		if order.Sort == membershipSortName {
			cursor.AfterUserID = position.UserID.String()
		} else {
			cursor.AfterStartAt = model.FormatRFC3339(position.StartAt)
		}
		return route.EncodeCursor(cursor, membershipPageCursorSpec())
	})
	if err != nil {
		return route.Result{}, err
	}

	response, err := route.WithPageTotal(envelope, total, continued)
	if err != nil {
		return route.Result{}, err
	}

	return route.JSON(http.StatusOK, response), nil
}

// membershipPageKey is the position of one listed membership.
type membershipPageKey struct {
	ID      string
	UserID  model.UserID
	StartAt time.Time
}

// after returns the continuation selectors of a decoded cursor: the start
// instant of the start order, or the member of the name order.
func (c membershipPageCursor) after() (time.Time, model.UserID) {
	if c.AfterUserID != "" {
		return time.Time{}, model.UserID(c.AfterUserID)
	}
	startAt, _ := model.ParseRFC3339(c.AfterStartAt)
	return startAt, ""
}
