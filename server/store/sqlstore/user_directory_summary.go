// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package sqlstore

import (
	"context"
	"errors"
	"fmt"

	"github.com/lib/pq"

	"github.com/sudosylabs/proctor/server/model"
	"github.com/sudosylabs/proctor/server/store"
)

// maximumSummaryUsers bounds one summary read to the largest directory page.
const maximumSummaryUsers = 200

// validSummaryUserIDs reports whether a page's User IDs are canonical and
// within one page.
func validSummaryUserIDs(ids []model.UserID) bool {
	return len(ids) <= maximumSummaryUsers && validVisibilityIDs(ids)
}

// ListDirectorySummaries returns, for each listed User with any, the current
// Affiliations and visible current Academic Units, each capped by its limit
// and counted past it, in one statement.
func (s SQLUserStore) ListDirectorySummaries(
	ctx context.Context,
	options store.UserDirectorySummaryOptions,
) ([]store.UserDirectorySummary, error) {
	visibility := options.Visibility
	if !validSummaryUserIDs(options.UserIDs) ||
		visibility.ActiveAt.IsZero() ||
		options.AffiliationLimit < 1 ||
		options.AffiliationLimit > 10 ||
		options.AcademicUnitLimit < 1 ||
		options.AcademicUnitLimit > 10 ||
		len(visibility.AcademicUnitRootIDs) > 256 ||
		!validVisibilityIDs(visibility.AcademicUnitRootIDs) {
		return nil, store.NewErrInvalidInput("user", "directory_summary_options", nil)
	}

	if len(options.UserIDs) == 0 {
		return []store.UserDirectorySummary{}, nil
	}

	activeAt := model.TimeUTC(visibility.ActiveAt)
	userIDs := pq.Array(idStrings(options.UserIDs))
	type summaryRow struct {
		UserID   string `db:"user_id"`
		Entry    string `db:"entry"`
		Value    string `db:"value"`
		Name     string `db:"name"`
		Total    int    `db:"total"`
		Position int    `db:"position"`
	}
	rows := []summaryRow{}
	scopeUnits, scopeRoots := academicSubtreeCTE("summary_scope_units", visibility.AcademicUnitRootIDs)
	if err := s.GetMaster().Select(
		ctx,
		&rows,
		`WITH RECURSIVE `+scopeUnits+`, current_affiliations AS (
		SELECT user_id, kind, min(start_at) AS start_at FROM affiliations
		WHERE user_id = ANY(?)
			AND archived_at IS NULL
			AND start_at <= ?
			AND (end_at IS NULL OR end_at > ?)
		GROUP BY user_id, kind
	), ranked_affiliations AS (
		SELECT user_id, kind,
			row_number() OVER (PARTITION BY user_id ORDER BY start_at, kind) AS position,
			count(*) OVER (PARTITION BY user_id) AS total
		FROM current_affiliations
	), current_units AS (
		SELECT DISTINCT member.user_id, unit.id, unit.display_name FROM academic_unit_members member
		JOIN academic_units unit ON unit.id = member.academic_unit_id AND unit.archived_at IS NULL
		WHERE member.user_id = ANY(?)
			AND member.archived_at IS NULL
			AND member.start_at <= ?
			AND (member.end_at IS NULL OR member.end_at > ?)
			AND (? OR unit.id IN (SELECT id FROM summary_scope_units))
	), ranked_units AS (
		SELECT user_id, id, display_name,
			row_number() OVER (PARTITION BY user_id ORDER BY display_name, id) AS position,
			count(*) OVER (PARTITION BY user_id) AS total
		FROM current_units
	)
	SELECT user_id, 'affiliation' AS entry, kind AS value, '' AS name, total, position
	FROM ranked_affiliations WHERE position <= ?
	UNION ALL
	SELECT user_id, 'academic_unit', id, display_name, total, position
	FROM ranked_units WHERE position <= ?
	ORDER BY user_id, entry DESC, position`,
		scopeRoots,
		userIDs,
		activeAt,
		activeAt,
		userIDs,
		activeAt,
		activeAt,
		visibility.InstitutionWide,
		options.AffiliationLimit,
		options.AcademicUnitLimit,
	); err != nil {
		return nil, fmt.Errorf("list User directory summaries: %w", err)
	}

	summaries := []store.UserDirectorySummary{}
	for _, row := range rows {
		userID, err := model.ParseUserID(row.UserID)
		if err != nil {
			return nil, invalidPersistedState("user", "directory_summary_user_id", err)
		}
		if len(summaries) == 0 || summaries[len(summaries)-1].UserID != userID {
			summaries = append(summaries, store.UserDirectorySummary{UserID: userID})
		}

		summary := &summaries[len(summaries)-1]
		switch row.Entry {
		case "affiliation":
			kind := model.AffiliationKind(row.Value)
			if !kind.IsValid() {
				return nil, invalidPersistedState("affiliation", "kind", errors.New("invalid Affiliation kind"))
			}

			summary.Affiliations = append(summary.Affiliations, kind)
			summary.AffiliationsMore = max(row.Total-options.AffiliationLimit, 0)
		case "academic_unit":
			unitID, parseErr := model.ParseAcademicUnitID(row.Value)
			if parseErr != nil {
				return nil, invalidPersistedState("academic_unit", "id", parseErr)
			}

			summary.AcademicUnits = append(summary.AcademicUnits, store.UserDirectoryAcademicUnit{ID: unitID, DisplayName: row.Name})
			summary.AcademicUnitsMore = max(row.Total-options.AcademicUnitLimit, 0)
		default:
			return nil, invalidPersistedState("user", "directory_summary_entry", errors.New("unknown summary entry"))
		}
	}

	return summaries, nil
}

// ListIdentities returns the username, display name and email of each listed
// User that exists.
func (s SQLUserStore) ListIdentities(ctx context.Context, ids []model.UserID) ([]store.UserIdentity, error) {
	if !validSummaryUserIDs(ids) {
		return nil, store.NewErrInvalidInput("user", "identity_ids", nil)
	}
	if len(ids) == 0 {
		return []store.UserIdentity{}, nil
	}

	type identityRow struct {
		ID          string `db:"id"`
		Username    string `db:"username"`
		DisplayName string `db:"display_name"`
		Email       string `db:"email"`
	}
	rows := []identityRow{}
	if err := s.GetMaster().Select(
		ctx,
		&rows,
		`SELECT id, username, display_name, email FROM users WHERE id = ANY(?)`,
		pq.Array(idStrings(ids)),
	); err != nil {
		return nil, fmt.Errorf("list User identities: %w", err)
	}

	identities := make([]store.UserIdentity, 0, len(rows))
	for _, row := range rows {
		id, err := model.ParseUserID(row.ID)
		if err != nil {
			return nil, invalidPersistedState("user", "id", err)
		}

		identities = append(identities, store.UserIdentity{ID: id, Username: row.Username, DisplayName: row.DisplayName, Email: row.Email})
	}

	return identities, nil
}
