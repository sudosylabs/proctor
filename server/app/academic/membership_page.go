// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package academic

import (
	"time"

	"github.com/sudosylabs/proctor/server/app/apperror"
	"github.com/sudosylabs/proctor/server/model"
	"github.com/sudosylabs/proctor/server/store"
)

// MembershipPageQuery selects an effective instant or full retained history
// and the page order: by membership start (the zero Order) or by member name,
// reversed when Descending. Transport adapters resolve opaque continuations
// into explicit selectors.
type MembershipPageQuery struct {
	Limit      int
	ActiveAt   *int64
	History    *bool
	Order      store.MembershipOrder
	Descending bool
}

// membershipPageKeyset validates the order and continuation of a membership
// page: a continuation names its last membership (afterID) with that
// membership's start in the start order, or its member in the name order.
func membershipPageKeyset(query MembershipPageQuery, afterID string, afterStartAt time.Time, afterUserID model.UserID) (store.MembershipPageKeyset, error) {
	keyset := store.MembershipPageKeyset{Order: query.Order, Descending: query.Descending}
	if !keyset.Order.IsValid() {
		return keyset, apperror.New("request.invalid").WithField("field", "sort")
	}

	switch {
	case afterID == "":
		if !afterStartAt.IsZero() || !afterUserID.IsZero() {
			return keyset, apperror.New("request.invalid").WithField("field", "cursor")
		}
	case keyset.Order == store.MembershipOrderName:
		if !afterStartAt.IsZero() || !afterUserID.IsValid() {
			return keyset, apperror.New("request.invalid").WithField("field", "cursor")
		}
		keyset.AfterUserID = afterUserID
	default:
		if afterStartAt.IsZero() || !afterUserID.IsZero() {
			return keyset, apperror.New("request.invalid").WithField("field", "cursor")
		}
		keyset.AfterStartAt = model.TimeUTC(afterStartAt)
	}

	return keyset, nil
}

func resolveMembershipPage(query MembershipPageQuery, now time.Time) (int64, int, error) {
	limit := query.Limit
	if limit == 0 {
		limit = 50
	}
	if limit < 1 || limit > 200 {
		return 0, 0, apperror.New("request.invalid").WithField("field", "limit")
	}
	if query.ActiveAt != nil && *query.ActiveAt <= 0 {
		return 0, 0, apperror.New("request.invalid").WithField("field", "active_at")
	}
	if query.History != nil && *query.History && query.ActiveAt != nil {
		return 0, 0, apperror.New("request.invalid").WithField("field", "history")
	}
	activeAt := model.MillisFromTime(now)
	if query.ActiveAt != nil {
		activeAt = *query.ActiveAt
	}
	if query.History != nil && *query.History {
		activeAt = 0
	}
	return activeAt, limit, nil
}
