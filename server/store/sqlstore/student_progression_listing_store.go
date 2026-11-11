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
	"strings"

	"github.com/lib/pq"

	"github.com/sudosylabs/proctor/server/model"
	"github.com/sudosylabs/proctor/server/store"
)

// studentProgressionSummaryRow is one listed progression with the display
// names of its two Classes.
type studentProgressionSummaryRow struct {
	onboardingImportRow
	SourceClassName      string `db:"source_class_name"`
	DestinationClassName string `db:"destination_class_name"`
}

// studentProgressionColumns are the aggregate columns qualified by the
// progression alias, so the Class joins cannot make them ambiguous.
var studentProgressionColumns = "oi." + strings.ReplaceAll(onboardingImportColumns, ",", ",oi.")

// studentProgressionListFrom joins each progression to its source and
// destination Class; both references are mandatory for the mode, so the joins
// neither drop nor duplicate a progression.
const studentProgressionListFrom = ` FROM onboarding_imports oi
	JOIN classes sc ON sc.id = oi.source_class_id
	JOIN classes dc ON dc.id = oi.destination_class_id`

// ListStudentProgressions returns one keyset page of unexpired student
// progressions whose source and destination Classes both lie inside the
// visibility, newest first, each with its Classes' display names.
func (s SQLInvitationStore) ListStudentProgressions(
	ctx context.Context,
	options store.StudentProgressionListOptions,
) ([]*store.StudentProgressionSummary, error) {
	if options.Limit < 1 ||
		options.Limit > store.OnboardingImportListMaximum ||
		(options.BeforeID.IsValid() != !options.BeforeCreatedAt.IsZero()) ||
		(!options.BeforeID.IsZero() && !options.BeforeID.IsValid()) {
		return nil, store.NewErrInvalidInput("student_progression", "list_options", nil)
	}

	conditions, args, err := studentProgressionListConditions(options)
	if err != nil {
		return nil, err
	}

	if options.BeforeID.IsValid() {
		before := model.TimeUTC(options.BeforeCreatedAt)
		conditions += " AND (oi.created_at < ? OR (oi.created_at = ? AND oi.id < ?))"
		args = append(args, before, before, options.BeforeID.String())
	}

	args = append(args, options.Limit)
	rows := []studentProgressionSummaryRow{}
	if err = s.GetMaster().Select(
		ctx,
		&rows,
		`SELECT `+studentProgressionColumns+`, sc.display_name AS source_class_name, dc.display_name AS destination_class_name`+
			studentProgressionListFrom+` WHERE `+conditions+` ORDER BY oi.created_at DESC, oi.id DESC LIMIT ?`,
		args...,
	); err != nil {
		return nil, fmt.Errorf("list student progressions: %w", err)
	}

	values := make([]*store.StudentProgressionSummary, 0, len(rows))
	for _, row := range rows {
		value, valueErr := row.value()
		if valueErr != nil {
			return nil, valueErr
		}

		values = append(values, &store.StudentProgressionSummary{
			Progression:          value,
			SourceClassName:      row.SourceClassName,
			DestinationClassName: row.DestinationClassName,
		})
	}

	return values, nil
}

// CountStudentProgressions counts through the same filter as
// ListStudentProgressions, without its keyset, order, or limit, reading at
// most one row past maximum.
func (s SQLInvitationStore) CountStudentProgressions(
	ctx context.Context,
	options store.StudentProgressionListOptions,
	maximum int,
) (int, error) {
	if !store.ValidCountMaximum(maximum) {
		return 0, store.NewErrInvalidInput("student_progression", "count_maximum", nil)
	}
	conditions, args, err := studentProgressionListConditions(options)
	if err != nil {
		return 0, err
	}
	return countBoundedSQL(
		ctx,
		s.GetMaster(),
		`SELECT 1`+studentProgressionListFrom+` WHERE `+conditions,
		args,
		maximum,
		"student progressions",
	)
}

// studentProgressionListConditions is every ListStudentProgressions filter but
// the keyset: the mode, expiry, visibility of both Classes, states, and the
// exact Period and Class filters. List and Count share it so a total always
// counts the listed rows. The expiry test matches GetOnboardingImport, so a
// listed progression can be read.
func studentProgressionListConditions(options store.StudentProgressionListOptions) (string, []any, error) {
	visibility := options.Visibility
	states := make([]string, 0, len(options.States))
	for _, state := range options.States {
		if !state.IsValid() {
			return "", nil, store.NewErrInvalidInput("student_progression", "list_options", nil)
		}

		states = append(states, string(state))
	}

	if len(states) > 8 ||
		len(visibility.AcademicUnitRootIDs) > 256 ||
		len(visibility.ClassIDs) > 256 ||
		(!visibility.InstitutionWide && len(visibility.AcademicUnitRootIDs) == 0 && len(visibility.ClassIDs) == 0) ||
		!validVisibilityIDs(visibility.AcademicUnitRootIDs) ||
		!validVisibilityIDs(visibility.ClassIDs) ||
		(!options.SourcePeriodID.IsZero() && !options.SourcePeriodID.IsValid()) ||
		(!options.SourceClassID.IsZero() && !options.SourceClassID.IsValid()) ||
		(!options.DestinationPeriodID.IsZero() && !options.DestinationPeriodID.IsValid()) ||
		(!options.DestinationClassID.IsZero() && !options.DestinationClassID.IsValid()) {
		return "", nil, store.NewErrInvalidInput("student_progression", "list_options", nil)
	}

	conditions := []string{"oi.mode='student_progression'", "oi.expires_at>CURRENT_TIMESTAMP"}
	args := []any{}
	if !visibility.InstitutionWide {
		for _, class := range []string{"sc", "dc"} {
			parts := []string{}
			if len(visibility.AcademicUnitRootIDs) > 0 {
				parts = append(parts, `EXISTS (
					WITH RECURSIVE descendants AS (
						SELECT id FROM academic_units WHERE id = ANY(?)
						UNION ALL SELECT child.id FROM academic_units child JOIN descendants parent ON child.parent_id = parent.id
					) SELECT 1 FROM programme_levels pl JOIN programmes p ON p.id = pl.programme_id
					JOIN descendants d ON d.id = p.academic_unit_id WHERE pl.id =`+class+`.programme_level_id
				)`)
				args = append(args, pq.Array(idStrings(visibility.AcademicUnitRootIDs)))
			}

			if len(visibility.ClassIDs) > 0 {
				parts = append(parts, class+`.id = ANY(?)`)
				args = append(args, pq.Array(idStrings(visibility.ClassIDs)))
			}

			conditions = append(conditions, "("+strings.Join(parts, " OR ")+")")
		}
	}

	if len(states) > 0 {
		conditions = append(conditions, "oi.state = ANY(?)")
		args = append(args, pq.Array(states))
	}

	for _, filter := range []struct {
		column string
		value  string
	}{
		{"oi.source_period_id", options.SourcePeriodID.String()},
		{"oi.source_class_id", options.SourceClassID.String()},
		{"oi.destination_period_id", options.DestinationPeriodID.String()},
		{"oi.destination_class_id", options.DestinationClassID.String()},
	} {
		if filter.value != "" {
			conditions = append(conditions, filter.column+"= ?")
			args = append(args, filter.value)
		}
	}

	return strings.Join(conditions, " AND "), args, nil
}
