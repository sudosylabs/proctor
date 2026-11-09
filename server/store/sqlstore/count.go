// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package sqlstore

import (
	"context"
	"fmt"
	"strconv"

	sq "github.com/Masterminds/squirrel"
)

// countBounded counts the rows of a filtered selection with no order or limit
// of its own, reading at most one row past maximum and answering maximum when
// more match. The caller has validated maximum, which is inlined rather than
// bound so the planner sees a constant limit.
func countBounded(ctx context.Context, db *sqlxDBWrapper, rows sq.SelectBuilder, maximum int, label string) (int, error) {
	query, args, err := rows.PlaceholderFormat(sq.Question).ToSql()
	if err != nil {
		return 0, fmt.Errorf("build %s count: %w", label, err)
	}
	return countBoundedSQL(ctx, db, query, args, maximum, label)
}

// countBoundedSQL is countBounded over a selection written as SQL text with
// `?` placeholders and no ORDER BY or LIMIT of its own.
func countBoundedSQL(ctx context.Context, db *sqlxDBWrapper, rows string, args []any, maximum int, label string) (int, error) {
	var total int
	query := `SELECT LEAST(count(*), ` + strconv.Itoa(maximum) + `) FROM (` + rows +
		` LIMIT ` + strconv.Itoa(maximum+1) + `) bounded`
	if err := db.Get(ctx, &total, query, args...); err != nil {
		return 0, fmt.Errorf("count %s: %w", label, err)
	}
	return total, nil
}
