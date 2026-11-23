// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package sqlstore

import (
	"strings"

	"github.com/lib/pq"
	"github.com/sudosylabs/proctor/server/model"
	"github.com/sudosylabs/proctor/server/store"
)

// examManagerVisibility is the SQL form of an ExamListVisibility, the same
// rule the exam catalogue applies: an ordinary grant reaches the exams the
// actor manages in a unit they belong to, under a granted root; an override
// grant reaches every exam under its roots. ctes defines the two unit trees,
// named after prefix, for a WITH RECURSIVE clause; predicate reads the exam
// through examAlias.
type examManagerVisibility struct {
	ctes          string
	cteArgs       []any
	predicate     string
	predicateArgs []any
}

func newExamManagerVisibility(prefix string, visibility store.ExamListVisibility, examAlias string) examManagerVisibility {
	ordinary, override := prefix+"_ordinary_units", prefix+"_override_units"
	result := examManagerVisibility{
		ctes: ordinary + ` AS (
				SELECT id FROM academic_units WHERE id = ANY(?) AND archived_at IS NULL
				UNION ALL
				SELECT child.id FROM academic_units child JOIN ` + ordinary + ` parent ON child.parent_id = parent.id
				WHERE child.archived_at IS NULL
			),
			` + override + ` AS (
				SELECT id FROM academic_units WHERE id = ANY(?) AND archived_at IS NULL
				UNION ALL
				SELECT child.id FROM academic_units child JOIN ` + override + ` parent ON child.parent_id = parent.id
				WHERE child.archived_at IS NULL
			)`,
		cteArgs: []any{
			pq.Array(idStrings(visibility.OrdinaryAcademicUnitRootIDs)),
			pq.Array(idStrings(visibility.OverrideAcademicUnitRootIDs)),
		},
	}

	var predicates []string
	if visibility.OverrideInstitutionWide {
		predicates = append(predicates, `TRUE`)
	} else if len(visibility.OverrideAcademicUnitRootIDs) > 0 {
		predicates = append(predicates, examAlias+`.academic_unit_id IN (SELECT id FROM `+override+`)`)
	}
	if visibility.OrdinaryInstitutionWide || len(visibility.OrdinaryAcademicUnitRootIDs) > 0 {
		predicates = append(predicates, `(
				EXISTS (SELECT 1 FROM exam_managers actor_manager
					WHERE actor_manager.exam_id = `+examAlias+`.id AND actor_manager.user_id = ?)
				AND EXISTS (SELECT 1 FROM academic_unit_members actor_member
					WHERE actor_member.academic_unit_id = `+examAlias+`.academic_unit_id
						AND actor_member.user_id = ?
						AND actor_member.archived_at IS NULL
						AND actor_member.start_at <= ?
						AND (actor_member.end_at IS NULL OR actor_member.end_at > ?))
				AND (? OR `+examAlias+`.academic_unit_id IN (SELECT id FROM `+ordinary+`))
			)`)
		at := model.TimeUTC(visibility.OrdinaryMembershipAt)
		result.predicateArgs = append(result.predicateArgs, visibility.ActorUserID.String(), visibility.ActorUserID.String(),
			at, at, visibility.OrdinaryInstitutionWide)
	}
	if len(predicates) == 0 {
		predicates = append(predicates, `FALSE`)
	}
	result.predicate = `(` + strings.Join(predicates, ` OR `) + `)`
	return result
}

// validExamManagerVisibility checks a visibility the way the exam catalogue
// does: an actor, a membership instant for an ordinary grant, and at most 256
// valid roots.
func validExamManagerVisibility(visibility store.ExamListVisibility) bool {
	hasOrdinary := visibility.OrdinaryInstitutionWide || len(visibility.OrdinaryAcademicUnitRootIDs) > 0
	return visibility.ActorUserID.IsValid() &&
		(!hasOrdinary || !visibility.OrdinaryMembershipAt.IsZero()) &&
		len(visibility.OrdinaryAcademicUnitRootIDs)+len(visibility.OverrideAcademicUnitRootIDs) <= 256 &&
		validVisibilityIDs(visibility.OrdinaryAcademicUnitRootIDs) &&
		validVisibilityIDs(visibility.OverrideAcademicUnitRootIDs)
}
