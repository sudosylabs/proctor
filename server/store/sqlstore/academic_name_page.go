// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package sqlstore

import (
	"context"
	"strings"

	sq "github.com/Masterminds/squirrel"

	"github.com/sudosylabs/proctor/server/model"
	"github.com/sudosylabs/proctor/server/store"
)

// maximumAcademicPageProbe is the largest page plus the one probe row that
// tells the caller whether another page follows.
const maximumAcademicPageProbe = 201

// academicNamePage applies options to a listing of table: the optional term
// filter, the exclusive (name, id) continuation, the (name, id) order in
// either direction, and the limit. It reports false for invalid options.
func academicNamePage(query sq.SelectBuilder, table string, options store.AcademicNameListOptions) (sq.SelectBuilder, bool) {
	if options.Limit < 1 ||
		options.Limit > maximumAcademicPageProbe ||
		(options.AfterName == "") != (options.AfterID == "") ||
		(options.AfterID != "" && !model.IsValidOpaqueID(options.AfterID)) {
		return query, false
	}
	query = academicNameFilter(query, table, options.Term)
	direction, comparison := "ASC", ">"
	if options.Descending {
		direction, comparison = "DESC", "<"
	}
	if options.AfterID != "" {
		query = query.Where("("+table+".name, "+table+".id) "+comparison+" (?, ?)", options.AfterName, options.AfterID)
	}
	return query.OrderBy(table+".name "+direction, table+".id "+direction).Limit(uint64(options.Limit)), true
}

// academicNameFilter applies the optional term filter, the only filter an
// academic name listing adds to its parent constraint, so a listing and its
// count share it.
func academicNameFilter(query sq.SelectBuilder, table string, term string) sq.SelectBuilder {
	if term = strings.TrimSpace(term); term != "" {
		pattern := directorySearchPattern(term)
		query = query.Where("("+table+".name ILIKE ? ESCAPE '!' OR "+table+".display_name ILIKE ? ESCAPE '!')", pattern, pattern)
	}
	return query
}

// countAcademicNamePage counts the rows of a parent-constrained selection of
// table that academicNamePage would page through for options, ignoring the
// continuation, order, and limit and stopping at maximum.
func countAcademicNamePage(
	ctx context.Context,
	db *sqlxDBWrapper,
	rows sq.SelectBuilder,
	table string,
	options store.AcademicNameListOptions,
	maximum int,
) (int, error) {
	if !store.ValidCountMaximum(maximum) {
		return 0, store.NewErrInvalidInput(strings.TrimSuffix(table, "s"), "count", nil)
	}
	return countBounded(ctx, db, academicNameFilter(rows, table, options.Term), maximum, strings.ReplaceAll(table, "_", " "))
}
