// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package sqlstore

import (
	sq "github.com/Masterminds/squirrel"

	"github.com/sudosylabs/proctor/server/model"
	"github.com/sudosylabs/proctor/server/store"
)

// validMembershipPageKeyset reports whether a keyset continues its own order:
// no continuation sets nothing, a start continuation sets only its start, and
// a name continuation sets only its member.
func validMembershipPageKeyset(keyset store.MembershipPageKeyset, continued bool) bool {
	if !keyset.Order.IsValid() {
		return false
	}
	if !continued {
		return keyset.AfterStartAt.IsZero() && keyset.AfterUserID.IsZero()
	}
	if keyset.Order == store.MembershipOrderName {
		return keyset.AfterStartAt.IsZero() && keyset.AfterUserID.IsValid()
	}
	return !keyset.AfterStartAt.IsZero() && keyset.AfterUserID.IsZero()
}

// orderMembershipPage orders a membership selection from table by the
// keyset and continues it past afterID. The name order joins the member's
// User and resumes from that User's current sort name.
func orderMembershipPage(query sq.SelectBuilder, table string, keyset store.MembershipPageKeyset, afterID string) sq.SelectBuilder {
	direction, comparison := "ASC", ">"
	if keyset.Descending {
		direction, comparison = "DESC", "<"
	}

	key := table + ".start_at"
	var after any = model.TimeUTC(keyset.AfterStartAt)
	if keyset.Order == store.MembershipOrderName {
		key = "membership_users.sort_name"
		after = sq.Expr("(SELECT sort_name FROM users WHERE id = ?)", keyset.AfterUserID.String())
		query = query.Join("users membership_users ON membership_users.id = " + table + ".user_id")
	}

	if afterID != "" {
		query = query.Where(sq.Expr("("+key+", "+table+".id) "+comparison+" (?, ?)", after, afterID))
	}

	return query.OrderBy(key+" "+direction, table+".id "+direction)
}
