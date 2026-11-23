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

	"github.com/lib/pq"

	"github.com/sudosylabs/proctor/server/model"
	"github.com/sudosylabs/proctor/server/store"
)

// userMembershipRow is one entry of the union ListMemberships reads; the
// columns of the other kind are null.
type userMembershipRow struct {
	Kind               string         `db:"kind"`
	ID                 string         `db:"id"`
	StartAt            sql.NullTime   `db:"start_at"`
	EndAt              sql.NullTime   `db:"end_at"`
	AcademicUnitID     sql.NullString `db:"academic_unit_id"`
	AcademicUnitName   sql.NullString `db:"academic_unit_name"`
	ClassID            sql.NullString `db:"class_id"`
	ClassName          sql.NullString `db:"class_name"`
	ProgrammeLevelID   sql.NullString `db:"programme_level_id"`
	ProgrammeLevelName sql.NullString `db:"programme_level_name"`
	ProgrammeID        sql.NullString `db:"programme_id"`
	ProgrammeName      sql.NullString `db:"programme_name"`
	AcademicPeriodID   sql.NullString `db:"academic_period_id"`
	AcademicPeriodName sql.NullString `db:"academic_period_name"`
}

// ListMemberships reads both kinds in one keyset-paged union, so a hidden
// membership is filtered in SQL and never shifts a page boundary the caller
// could observe.
func (s SQLUserStore) ListMemberships(ctx context.Context, options store.UserMembershipListOptions) ([]store.UserMembership, error) {
	if !validUserMembershipListOptions(options) {
		return nil, store.NewErrInvalidInput("user", "membership_list_options", nil)
	}
	activeAt := model.TimeUTC(options.ActiveAt)
	unitScope, unitRoots := academicSubtreeCTE("membership_unit_scope", options.UnitVisibility.AcademicUnitRootIDs)
	classScope, classRoots := academicSubtreeCTE("membership_class_scope", options.ClassVisibility.AcademicUnitRootIDs)
	query := `WITH RECURSIVE ` + unitScope + `, ` + classScope + `
		SELECT kind, id, start_at, end_at, academic_unit_id, academic_unit_name, class_id, class_name,
			programme_level_id, programme_level_name, programme_id, programme_name, academic_period_id, academic_period_name
		FROM (
			SELECT 'academic_unit' AS kind, aum.id, aum.start_at, aum.end_at,
				au.id AS academic_unit_id, au.display_name AS academic_unit_name,
				NULL::varchar AS class_id, NULL::varchar AS class_name,
				NULL::varchar AS programme_level_id, NULL::varchar AS programme_level_name,
				NULL::varchar AS programme_id, NULL::varchar AS programme_name,
				NULL::varchar AS academic_period_id, NULL::varchar AS academic_period_name
			FROM academic_unit_members aum
			JOIN academic_units au ON au.id = aum.academic_unit_id AND au.archived_at IS NULL
			WHERE aum.user_id = ?
				AND aum.archived_at IS NULL
				AND aum.start_at <= ?
				AND (aum.end_at IS NULL OR aum.end_at > ?)
				AND (? OR au.id IN (SELECT id FROM membership_unit_scope))
			UNION ALL
			SELECT 'class', cm.id, cm.start_at, cm.end_at,
				NULL, NULL,
				c.id, c.display_name,
				pl.id, pl.display_name,
				p.id, p.display_name,
				ap.id, ap.display_name
			FROM class_members cm
			JOIN classes c ON c.id = cm.class_id AND c.archived_at IS NULL
			JOIN programme_levels pl ON pl.id = c.programme_level_id AND pl.archived_at IS NULL
			JOIN programmes p ON p.id = pl.programme_id AND p.archived_at IS NULL
			JOIN academic_periods ap ON ap.id = cm.academic_period_id
			WHERE cm.user_id = ?
				AND cm.archived_at IS NULL
				AND cm.start_at <= ?
				AND (cm.end_at IS NULL OR cm.end_at > ?)
				AND (? OR p.academic_unit_id IN (SELECT id FROM membership_class_scope) OR c.id = ANY(?))
		) memberships`
	args := []any{
		unitRoots, classRoots,
		options.UserID.String(), activeAt, activeAt, options.UnitVisibility.InstitutionWide,
		options.UserID.String(), activeAt, activeAt, options.ClassVisibility.InstitutionWide,
		pq.Array(idStrings(options.ClassVisibility.ClassIDs)),
	}
	if options.AfterID != "" {
		query += ` WHERE (kind, start_at, id) > (?, ?, ?)`
		args = append(args, string(options.AfterKind), model.TimeUTC(options.AfterStartAt), options.AfterID)
	}
	query += ` ORDER BY kind, start_at, id LIMIT ?`
	args = append(args, options.Limit)

	rows := []userMembershipRow{}
	if err := s.GetMaster().Select(ctx, &rows, query, args...); err != nil {
		return nil, fmt.Errorf("list user memberships: %w", err)
	}
	memberships := make([]store.UserMembership, 0, len(rows))
	for _, row := range rows {
		memberships = append(memberships, store.UserMembership{
			Kind:               store.UserMembershipKind(row.Kind),
			ID:                 row.ID,
			StartAt:            model.TimeUTC(row.StartAt.Time),
			EndAt:              OptionalTimeFromNullTime(row.EndAt),
			AcademicUnitID:     model.AcademicUnitID(row.AcademicUnitID.String),
			AcademicUnitName:   row.AcademicUnitName.String,
			ClassID:            model.ClassID(row.ClassID.String),
			ClassName:          row.ClassName.String,
			ProgrammeLevelID:   model.ProgrammeLevelID(row.ProgrammeLevelID.String),
			ProgrammeLevelName: row.ProgrammeLevelName.String,
			ProgrammeID:        model.ProgrammeID(row.ProgrammeID.String),
			ProgrammeName:      row.ProgrammeName.String,
			AcademicPeriodID:   model.AcademicPeriodID(row.AcademicPeriodID.String),
			AcademicPeriodName: row.AcademicPeriodName.String,
		})
	}
	return memberships, nil
}

func validUserMembershipListOptions(options store.UserMembershipListOptions) bool {
	unit, class := options.UnitVisibility, options.ClassVisibility
	continued := options.AfterID != "" || options.AfterKind != "" || !options.AfterStartAt.IsZero()
	return options.UserID.IsValid() &&
		!options.ActiveAt.IsZero() &&
		options.Limit >= 1 && options.Limit <= 201 &&
		(!continued || (options.AfterKind.IsValid() && model.IsValidOpaqueID(options.AfterID) && !options.AfterStartAt.IsZero())) &&
		len(unit.AcademicUnitRootIDs)+len(class.AcademicUnitRootIDs)+len(class.ClassIDs) <= 256 &&
		validVisibilityIDs(unit.AcademicUnitRootIDs) &&
		validVisibilityIDs(class.AcademicUnitRootIDs) &&
		validVisibilityIDs(class.ClassIDs)
}
