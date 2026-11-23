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
	"strings"

	"github.com/lib/pq"

	"github.com/sudosylabs/proctor/server/model"
	"github.com/sudosylabs/proctor/server/store"
)

// This file holds the reads that summarize a page of academic units: which of
// them lie in a granted subtree, how many members each has, and who
// administers each. Every read takes the whole page at once.

// ListWithinSubtrees returns the listed units inside one of the roots'
// unarchived subtrees.
func (s SQLAcademicUnitStore) ListWithinSubtrees(
	ctx context.Context,
	ids, roots []model.AcademicUnitID,
) ([]model.AcademicUnitID, error) {
	if len(ids) > store.MaximumAcademicUnitCountUnits || !validVisibilityIDs(ids) || !validVisibilityIDs(roots) {
		return nil, store.NewErrInvalidInput("academic_unit", "subtree", nil)
	}
	if len(ids) == 0 || len(roots) == 0 {
		return []model.AcademicUnitID{}, nil
	}

	subtree, rootsArg := academicSubtreeCTE("granted_units", roots)
	found := []string{}
	if err := s.GetMaster().Select(ctx, &found, `
		WITH RECURSIVE `+subtree+`
		SELECT DISTINCT granted_units.id
		FROM granted_units
		WHERE granted_units.id = ANY(?)`,
		rootsArg, pq.Array(idStrings(ids)),
	); err != nil {
		return nil, fmt.Errorf("list academic units within subtrees: %w", err)
	}
	return typedIDs[model.AcademicUnitID](found), nil
}

// CountCurrentByAcademicUnits reads every count of the page in one statement.
// Each count reads at most one row past the maximum, so a large unit costs
// no more than the cap.
func (s SQLAcademicUnitMemberStore) CountCurrentByAcademicUnits(
	ctx context.Context,
	options store.AcademicUnitMemberCountOptions,
) ([]store.AcademicUnitMemberCount, error) {
	if len(options.AcademicUnitIDs) > store.MaximumAcademicUnitCountUnits ||
		options.ActiveAt.IsZero() ||
		!store.ValidCountMaximum(options.Maximum) ||
		!validVisibilityIDs(options.AcademicUnitIDs) {
		return nil, store.NewErrInvalidInput("academic_unit_member", "count", nil)
	}
	if len(options.AcademicUnitIDs) == 0 {
		return []store.AcademicUnitMemberCount{}, nil
	}

	at := model.TimeUTC(options.ActiveAt)
	maximum, probe := strconv.Itoa(options.Maximum), strconv.Itoa(options.Maximum+1)
	bounded := func(alias, affiliation string) string {
		return `CROSS JOIN LATERAL (
			SELECT LEAST(count(*), ` + maximum + `) AS count
			FROM (
				SELECT 1
				FROM academic_unit_members members
				WHERE members.academic_unit_id = units.id
					AND members.archived_at IS NULL
					AND members.start_at <= $1
					AND (members.end_at IS NULL OR members.end_at > $1)` + affiliation + `
				LIMIT ` + probe + `
			) bounded
		) ` + alias
	}

	columns := []string{"units.id AS academic_unit_id", "all_members.count AS members"}
	joins := []string{bounded("all_members", "")}
	for _, kind := range store.CountedAffiliationKinds {
		alias := "affiliation_" + string(kind)
		columns = append(columns, alias+".count AS "+string(kind))
		// The kind is a closed constant, never input.
		joins = append(joins, bounded(alias, `
					AND EXISTS (
						SELECT 1 FROM affiliations
						WHERE affiliations.user_id = members.user_id
							AND affiliations.kind = '`+string(kind)+`'
							AND affiliations.archived_at IS NULL
							AND affiliations.start_at <= $1
							AND (affiliations.end_at IS NULL OR affiliations.end_at > $1)
					)`))
	}

	rows := []struct {
		AcademicUnitID string `db:"academic_unit_id"`
		Members        int    `db:"members"`
		Student        int    `db:"student"`
		Staff          int    `db:"staff"`
		Teacher        int    `db:"teacher"`
		External       int    `db:"external"`
	}{}
	if err := s.GetMaster().Select(ctx, &rows, `
		SELECT `+strings.Join(columns, ", ")+`
		FROM (SELECT DISTINCT id FROM unnest($2::text[]) AS listed(id)) units
		`+strings.Join(joins, "\n\t\t")+`
		ORDER BY units.id`,
		at, pq.Array(idStrings(options.AcademicUnitIDs)),
	); err != nil {
		return nil, fmt.Errorf("count academic unit members: %w", err)
	}

	counts := make([]store.AcademicUnitMemberCount, 0, len(rows))
	for _, row := range rows {
		counts = append(counts, store.AcademicUnitMemberCount{
			AcademicUnitID: model.AcademicUnitID(row.AcademicUnitID),
			Members:        row.Members,
			ByAffiliation: map[model.AffiliationKind]int{
				model.AffiliationStudent:  row.Student,
				model.AffiliationStaff:    row.Staff,
				model.AffiliationTeacher:  row.Teacher,
				model.AffiliationExternal: row.External,
			},
		})
	}
	return counts, nil
}

// ListAcademicUnitAdministrators reads every unit of the page in one
// statement. A person holding several administering bindings at one unit is
// listed once, at their earliest.
func (s SQLRoleBindingStore) ListAcademicUnitAdministrators(
	ctx context.Context,
	options store.AcademicUnitAdministratorOptions,
) ([]store.AcademicUnitAdministrators, error) {
	if len(options.AcademicUnitIDs) > store.MaximumAcademicUnitCountUnits ||
		options.ActiveAt.IsZero() ||
		options.Limit < 1 || options.Limit > 50 ||
		!validVisibilityIDs(options.AcademicUnitIDs) {
		return nil, store.NewErrInvalidInput("role_binding", "administrator_options", nil)
	}
	if len(options.AcademicUnitIDs) == 0 {
		return []store.AcademicUnitAdministrators{}, nil
	}

	rows := []struct {
		AcademicUnitID string         `db:"academic_unit_id"`
		UserIDs        pq.StringArray `db:"user_ids"`
		Total          int            `db:"total"`
	}{}
	if err := s.GetMaster().Select(ctx, &rows, `
		WITH administrators AS (
			SELECT role_bindings.scope_id AS academic_unit_id, role_bindings.user_id,
				min(role_bindings.start_at) AS since
			FROM role_bindings
			JOIN roles ON roles.id = role_bindings.role_id
			WHERE role_bindings.scope_type = $1
				AND role_bindings.scope_id = ANY($2::text[])
				AND role_bindings.archived_at IS NULL
				AND role_bindings.start_at <= $3
				AND (role_bindings.end_at IS NULL OR role_bindings.end_at > $3)
				AND roles.archived_at IS NULL
				AND $4 = ANY(roles.permissions)
			GROUP BY role_bindings.scope_id, role_bindings.user_id
		)
		SELECT academic_unit_id,
			(array_agg(user_id ORDER BY since, user_id))[1:$5::integer] AS user_ids,
			count(*) AS total
		FROM administrators
		GROUP BY academic_unit_id
		ORDER BY academic_unit_id`,
		string(model.RoleScopeAcademicUnit),
		pq.Array(idStrings(options.AcademicUnitIDs)),
		model.TimeUTC(options.ActiveAt),
		string(model.ActionAcademicUnitManage),
		options.Limit,
	); err != nil {
		return nil, fmt.Errorf("list academic unit administrators: %w", err)
	}

	result := make([]store.AcademicUnitAdministrators, 0, len(rows))
	for _, row := range rows {
		userIDs := make([]model.UserID, 0, len(row.UserIDs))
		for _, raw := range row.UserIDs {
			userID, err := model.ParseUserID(raw)
			if err != nil {
				return nil, invalidPersistedState("role_binding", "user_id", err)
			}
			userIDs = append(userIDs, userID)
		}
		result = append(result, store.AcademicUnitAdministrators{
			AcademicUnitID: model.AcademicUnitID(row.AcademicUnitID),
			UserIDs:        userIDs,
			More:           row.Total - len(userIDs),
		})
	}
	return result, nil
}
