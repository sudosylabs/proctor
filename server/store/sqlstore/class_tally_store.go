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

	"github.com/lib/pq"

	"github.com/sudosylabs/proctor/server/model"
	"github.com/sudosylabs/proctor/server/store"
)

// classTallyColumns maps each container to the column that names it. The
// values are fixed identifiers, never input.
var classTallyColumns = map[store.ClassTallyContainer]string{
	store.ClassTallyByProgramme:      "programmes.id",
	store.ClassTallyByProgrammeLevel: "classes.programme_level_id",
	store.ClassTallyByAcademicPeriod: "classes.academic_period_id",
}

// Tally reads every container of the page in one statement. Each count reads
// at most one row past the maximum, and the visibility's unit subtrees expand
// in the same statement, so no class outside it is counted.
func (s SQLClassStore) Tally(ctx context.Context, options store.ClassTallyOptions) ([]store.ClassTally, error) {
	column, known := classTallyColumns[options.By]
	visibility := options.Visibility
	if !known ||
		len(options.ContainerIDs) > store.MaximumAcademicUnitCountUnits ||
		!store.ValidCountMaximum(options.Maximum) ||
		len(visibility.AcademicUnitRootIDs)+len(visibility.ClassIDs) > 256 ||
		!validVisibilityIDs(visibility.AcademicUnitRootIDs) ||
		!validVisibilityIDs(visibility.ClassIDs) {
		return nil, store.NewErrInvalidInput("class", "tally", nil)
	}
	if len(options.ContainerIDs) == 0 {
		return []store.ClassTally{}, nil
	}

	counted := "SELECT classes.id"
	students := ""
	args := []any{pq.Array(idStrings(visibility.AcademicUnitRootIDs)), pq.Array(options.ContainerIDs)}
	if !options.StudentsAt.IsZero() {
		at := model.TimeUTC(options.StudentsAt)
		counted = "SELECT DISTINCT class_members.user_id"
		students = `
				JOIN class_members ON class_members.class_id = classes.id
					AND class_members.archived_at IS NULL
					AND class_members.start_at <= ?
					AND (class_members.end_at IS NULL OR class_members.end_at > ?)`
		args = append(args, at, at)
	}
	args = append(args, visibility.InstitutionWide, pq.Array(idStrings(visibility.ClassIDs)))

	rows := []struct {
		ContainerID string `db:"container_id"`
		Count       int    `db:"count"`
	}{}
	if err := s.GetMaster().Select(ctx, &rows, `
		WITH RECURSIVE tally_visible_units AS (
			SELECT id FROM academic_units WHERE id = ANY(?) AND archived_at IS NULL
			UNION ALL
			SELECT child.id FROM academic_units child
			JOIN tally_visible_units parent ON child.parent_id = parent.id
			WHERE child.archived_at IS NULL
		)
		SELECT containers.id AS container_id, counted.count
		FROM (SELECT DISTINCT id FROM unnest(?::text[]) AS listed(id)) containers
		CROSS JOIN LATERAL (
			SELECT LEAST(count(*), `+strconv.Itoa(options.Maximum)+`) AS count
			FROM (
				`+counted+`
				FROM classes
				JOIN programme_levels ON programme_levels.id = classes.programme_level_id
				JOIN programmes ON programmes.id = programme_levels.programme_id`+students+`
				WHERE `+column+` = containers.id
					AND classes.archived_at IS NULL
					AND programme_levels.archived_at IS NULL
					AND programmes.archived_at IS NULL
					AND (
						?
						OR programmes.academic_unit_id IN (SELECT id FROM tally_visible_units)
						OR classes.id = ANY(?)
					)
				LIMIT `+strconv.Itoa(options.Maximum+1)+`
			) bounded
		) counted
		ORDER BY containers.id`, args...); err != nil {
		return nil, fmt.Errorf("tally classes by %s: %w", options.By, err)
	}

	tallies := make([]store.ClassTally, 0, len(rows))
	for _, row := range rows {
		tallies = append(tallies, store.ClassTally{ContainerID: row.ContainerID, Count: row.Count})
	}
	return tallies, nil
}

// CountByProgrammes reads every programme of the page in one statement.
func (s SQLProgrammeLevelStore) CountByProgrammes(
	ctx context.Context,
	programmeIDs []model.ProgrammeID,
	maximum int,
) ([]store.ProgrammeLevelCount, error) {
	if len(programmeIDs) > store.MaximumAcademicUnitCountUnits || !store.ValidCountMaximum(maximum) || !validVisibilityIDs(programmeIDs) {
		return nil, store.NewErrInvalidInput("programme_level", "count", nil)
	}
	if len(programmeIDs) == 0 {
		return []store.ProgrammeLevelCount{}, nil
	}

	rows := []struct {
		ProgrammeID string `db:"programme_id"`
		Count       int    `db:"count"`
	}{}
	if err := s.GetMaster().Select(ctx, &rows, `
		SELECT programmes.id AS programme_id, counted.count
		FROM (SELECT DISTINCT id FROM unnest(?::text[]) AS listed(id)) programmes
		CROSS JOIN LATERAL (
			SELECT LEAST(count(*), `+strconv.Itoa(maximum)+`) AS count
			FROM (
				SELECT 1 FROM programme_levels
				WHERE programme_levels.programme_id = programmes.id
					AND programme_levels.archived_at IS NULL
				LIMIT `+strconv.Itoa(maximum+1)+`
			) bounded
		) counted
		ORDER BY programmes.id`, pq.Array(idStrings(programmeIDs))); err != nil {
		return nil, fmt.Errorf("count programme levels: %w", err)
	}

	counts := make([]store.ProgrammeLevelCount, 0, len(rows))
	for _, row := range rows {
		counts = append(counts, store.ProgrammeLevelCount{ProgrammeID: model.ProgrammeID(row.ProgrammeID), Count: row.Count})
	}
	return counts, nil
}
