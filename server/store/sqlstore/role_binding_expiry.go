// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package sqlstore

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/sudosylabs/proctor/server/model"
	"github.com/sudosylabs/proctor/server/store"
)

func (s SQLRoleBindingStore) ListExpiries(ctx context.Context, cursor store.RoleBindingExpiryCursor, limit int) (store.RoleBindingExpiryPage, error) {
	if limit < 1 || limit > 200 || (!cursor.ID.IsZero() && (!cursor.ID.IsValid() || cursor.At.IsZero())) {
		return store.RoleBindingExpiryPage{}, store.NewErrInvalidInput("role_binding", "expiry_page", nil)
	}
	var after any
	if !cursor.At.IsZero() {
		after = model.TimeUTC(cursor.At)
	}
	var rows []struct {
		CheckedAt time.Time      `db:"checked_at"`
		EndAt     sql.NullTime   `db:"end_at"`
		ID        sql.NullString `db:"id"`
		UserID    sql.NullString `db:"user_id"`
	}
	err := s.GetMaster().Select(ctx, &rows, `WITH boundary AS (
		SELECT GREATEST(statement_timestamp(), ?::timestamptz) AS checked_at,
			COALESCE(?::timestamptz, statement_timestamp() - interval '1 minute') AS after_at
	)
	SELECT boundary.checked_at, expired.end_at, expired.id, expired.user_id
	FROM boundary LEFT JOIN LATERAL (
		SELECT end_at, id, user_id
		FROM role_bindings
		WHERE archived_at IS NULL AND end_at IS NOT NULL
			AND end_at <= boundary.checked_at
			AND (end_at, id) > (boundary.after_at, ?)
			AND (?::boolean OR end_at > boundary.after_at)
		ORDER BY end_at, id
		LIMIT ?
	) expired ON TRUE ORDER BY expired.end_at, expired.id`, after, after, cursor.ID.String(), !cursor.ID.IsZero(), limit)
	if err != nil {
		return store.RoleBindingExpiryPage{}, fmt.Errorf("list role binding expiries: %w", err)
	}

	page := store.RoleBindingExpiryPage{CheckedAt: model.TimeUTC(rows[0].CheckedAt)}
	for _, row := range rows {
		if row.ID.Valid {
			page.Items = append(page.Items, store.RoleBindingExpiry{
				Cursor: store.RoleBindingExpiryCursor{At: model.TimeUTC(row.EndAt.Time), ID: model.RoleBindingID(row.ID.String)},
				UserID: model.UserID(row.UserID.String),
			})
		}
	}
	return page, nil
}
