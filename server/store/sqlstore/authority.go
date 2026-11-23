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
	"time"

	"github.com/sudosylabs/proctor/server/model"
)

// The store repeats authority decisions from package access only where they
// must hold under the transaction's row locks at the instant it decided at, so
// a role, role binding, membership or exam manager change cannot slip between
// the application's check and the commit. Change these together with package
// access (roleBindingApplies, accumulateBindingScope, personalAccessTokenAllows).
//
// A role binding counts when it and its role are unarchived and
// start_at <= at < end_at (no end_at is open-ended). Which binding scopes can
// grant an action comes from model.ActionDefinition (grantScopesOf). Under a
// personal access token ceiling, acting needs the resource within the
// ceiling; delegating clips each binding to the ceiling. No ceiling reaches
// the institution.
//
// The recheck sees a scope, not the resource, so it does not repeat
// everything the application decided:
//   - it cannot refuse a ceilinged token outright on a sitting, submission,
//     user or institution resource; it refuses only the institution scope.
//   - it does not recheck PersonalAccessTokenForbidden or AcceptsResource.
//   - invitation management names an actor, not a principal, so no token or
//     ceiling is rechecked there.
//   - the ancestry walk stops at an archived unit but still counts bindings
//     below it, where the application refuses any archived ancestor.
//
// The lock* helpers return the executor's error unchanged (sql.ErrNoRows when
// nothing grants) so each caller keeps its own store error.

// The three lock*ActionGrant* statements are one query in three shapes, one
// per scope. Each shares the granting binding and its role (FOR SHARE OF rb, r)
// so neither can be ended, archived or edited before commit.

// grantRule says how a lock*ActionGrant* statement counts bindings beyond the
// action's definition. The zero value is a session or a token without a ceiling.
type grantRule struct {
	// ceiling is the token's academic unit ceiling; zero when there is none.
	ceiling model.AcademicUnitID
	// delegate means the user hands the action on rather than exercising it, so
	// under a ceiling each binding is clipped to the ceiling instead of the
	// resource having to lie within it.
	delegate bool
	// strictParent, for a protected action delegated below the institution,
	// refuses a binding at the target scope itself, so nobody widens authority at
	// their own level.
	strictParent bool
}

type actionGrantScopes struct {
	institution, academicUnits, class bool
}

// An unknown or relationship-only action has no grant scopes, so no binding
// grants it.
func grantScopesOf(action model.Action) actionGrantScopes {
	definition, known := model.DefinitionForAction(action)
	if !known || definition.RelationshipOnly {
		return actionGrantScopes{}
	}

	return actionGrantScopes{
		institution:   definition.SupportsRoleScope(model.RoleScopeInstitution),
		academicUnits: definition.SupportsRoleScope(model.RoleScopeAcademicUnit),
		class:         definition.SupportsRoleScope(model.RoleScopeClass),
	}
}

// Placeholders: user, instant, instant.
const effectiveRoleBindingSQL = `SELECT true
	FROM role_bindings rb
	JOIN roles r ON r.id = rb.role_id
		AND r.archived_at IS NULL
	WHERE rb.user_id = ?
		AND rb.archived_at IS NULL
		AND rb.start_at <= ?
		AND (rb.end_at IS NULL OR rb.end_at > ?)`

const shareRoleBindingSQL = `
	LIMIT 1
	FOR SHARE OF rb, r`

// academicUnitAncestorsCTE names "ancestors(id, parent_id, depth)": the bound
// unit at depth 0 when unarchived, and each unarchived unit above it.
// Placeholder: academic unit.
const academicUnitAncestorsCTE = `WITH RECURSIVE ancestors AS (
		SELECT id, parent_id, 0 depth
		FROM academic_units
		WHERE id = ?
			AND archived_at IS NULL
		UNION ALL
		SELECT au.id, au.parent_id, child.depth + 1
		FROM academic_units au
		JOIN ancestors child ON au.id = child.parent_id
		WHERE au.archived_at IS NULL
	)`

// classAncestorsCTE names "ancestors(id, depth)": the unit owning the bound
// class at depth 0, and each unit above it, walking up while the unit below is
// unarchived. Placeholder: class.
const classAncestorsCTE = `WITH RECURSIVE class_unit AS (
		SELECT p.academic_unit_id
		FROM classes c
		JOIN programme_levels pl ON pl.id = c.programme_level_id
		JOIN programmes p ON p.id = pl.programme_id
		WHERE c.id = ?
	),
	ancestors AS (
		SELECT academic_unit_id id, 0 depth
		FROM class_unit
		UNION ALL
		SELECT au.parent_id, child.depth + 1
		FROM academic_units au
		JOIN ancestors child ON au.id = child.id
		WHERE au.parent_id IS NOT NULL
			AND au.archived_at IS NULL
	)`

// tokenCeilingCTE must follow a CTE named "ancestors". It names
// "token_ceiling(unbounded, depth)": unbounded when there is no ceiling,
// otherwise the ceiling's depth among the ancestors, NULL when the resource is
// outside the ceiling. Placeholder: ceiling, NULL for none.
const tokenCeilingCTE = `,
	token_ceiling AS (
		SELECT bound.id IS NULL unbounded, a.depth
		FROM (SELECT CAST(? AS varchar) id) bound
		LEFT JOIN ancestors a ON a.id = bound.id
	)
	`

// withinTokenCeilingSQL is true with no ceiling, or when the ceiling sits at
// the bound depth or above (0 is the resource's own unit). Placeholder: depth.
const withinTokenCeilingSQL = `EXISTS (SELECT 1 FROM token_ceiling WHERE unbounded OR depth >= ?)`

// grantingAncestorsSQL selects the ancestors at which an academic unit binding
// counts: without the resource's own unit under strict parent; under a
// ceiling, all of them to act (when the resource is within it), or only those
// at or below it to delegate. Placeholders: strict parent, delegate.
const grantingAncestorsSQL = `SELECT a.id
				FROM ancestors a
				CROSS JOIN token_ceiling c
				WHERE (NOT ? OR a.depth > 0)
					AND (c.unbounded OR c.depth >= CASE WHEN ? THEN a.depth ELSE 0 END)`

// lockActionGrantAtInstitution returns sql.ErrNoRows when no institution
// binding grants the action at the instant. Under a ceiling nothing grants.
func lockActionGrantAtInstitution(
	ctx context.Context,
	tx *sqlxTxWrapper,
	userID model.UserID,
	action model.Action,
	institutionID string,
	at time.Time,
	rule grantRule,
) error {
	var granted bool
	return tx.Get(
		ctx,
		&granted,
		effectiveRoleBindingSQL+`
			AND rb.scope_type = 'institution'
			AND rb.scope_id = ?
			AND ? = ANY(r.permissions)
			AND ?
			AND ?`+shareRoleBindingSQL,
		userID.String(),
		at,
		at,
		institutionID,
		string(action),
		grantScopesOf(action).institution,
		rule.ceiling.IsZero(),
	)
}

// lockActionGrantAtAcademicUnit returns sql.ErrNoRows when no institution or
// unit binding at or above the unit (strictly above with rule.strictParent)
// grants the action at the instant.
func lockActionGrantAtAcademicUnit(
	ctx context.Context,
	tx *sqlxTxWrapper,
	userID model.UserID,
	action model.Action,
	academicUnitID string,
	at time.Time,
	rule grantRule,
) error {
	scopes := grantScopesOf(action)
	institutionDepth := 0
	if rule.strictParent {
		// Clipped to the ceiling, an institution binding stands at the ceiling,
		// which must then lie strictly above the unit.
		institutionDepth = 1
	}

	var granted bool
	return tx.Get(
		ctx,
		&granted,
		academicUnitAncestorsCTE+tokenCeilingCTE+effectiveRoleBindingSQL+`
			AND ? = ANY(r.permissions)
			AND (
				(
					rb.scope_type = 'institution'
					AND ?
					AND `+withinTokenCeilingSQL+`
				)
				OR (
					rb.scope_type = 'academic_unit'
					AND ?
					AND rb.scope_id IN (`+grantingAncestorsSQL+`)
				)
			)`+shareRoleBindingSQL,
		academicUnitID,
		nullableID(rule.ceiling.String()),
		userID.String(),
		at,
		at,
		string(action),
		scopes.institution,
		institutionDepth,
		scopes.academicUnits,
		rule.strictParent,
		rule.delegate,
	)
}

// lockActionGrantAtClass returns sql.ErrNoRows when no institution, class or
// owning-unit binding grants the action at the instant. strictParent refuses
// only the class binding itself.
func lockActionGrantAtClass(
	ctx context.Context,
	tx *sqlxTxWrapper,
	userID model.UserID,
	action model.Action,
	classID string,
	at time.Time,
	rule grantRule,
) error {
	scopes := grantScopesOf(action)

	var granted bool
	return tx.Get(
		ctx,
		&granted,
		classAncestorsCTE+tokenCeilingCTE+effectiveRoleBindingSQL+`
			AND ? = ANY(r.permissions)
			AND (
				(
					rb.scope_type = 'institution'
					AND ?
					AND `+withinTokenCeilingSQL+`
				)
				OR (
					rb.scope_type = 'class'
					AND rb.scope_id = ?
					AND ?
					AND NOT ?
					AND `+withinTokenCeilingSQL+`
				)
				OR (
					rb.scope_type = 'academic_unit'
					AND ?
					AND rb.scope_id IN (`+grantingAncestorsSQL+`)
				)
			)`+shareRoleBindingSQL,
		classID,
		nullableID(rule.ceiling.String()),
		userID.String(),
		at,
		at,
		string(action),
		scopes.institution,
		0,
		classID,
		scopes.class,
		rule.strictParent,
		0,
		scopes.academicUnits,
		// The target is the class, so no unit binding sits at it.
		false,
		rule.delegate,
	)
}

// Inviter authority for invitation issuance. Issuance checks several actions
// in a row, so these statements pick the granting binding deterministically
// (ORDER BY r.id, rb.id) and lock the role before the binding (FOR SHARE OF
// r, rb), the reverse of the statements above. Keep each family's lock order:
// the invitation fences were tested against it. Arguments are reused
// positionally ($N).

// shareOrderedRoleBindingSQL holds the first granting role, then its binding,
// shared until the transaction ends.
const shareOrderedRoleBindingSQL = `
	ORDER BY r.id, rb.id
	LIMIT 1
	FOR SHARE OF r, rb`

// lockInviterActionGrantAtInstitution returns sql.ErrNoRows when nothing
// grants.
func lockInviterActionGrantAtInstitution(
	ctx context.Context,
	tx *sqlxTxWrapper,
	inviterID model.UserID,
	action model.Action,
	institutionID string,
	at time.Time,
) error {
	var granted bool
	return tx.Get(
		ctx,
		&granted,
		`SELECT true
		FROM role_bindings rb
		JOIN roles r ON r.id = rb.role_id
			AND r.archived_at IS NULL
		WHERE rb.user_id = $1
			AND rb.archived_at IS NULL
			AND rb.start_at <= $2
			AND (rb.end_at IS NULL OR rb.end_at > $2)
			AND rb.scope_type = 'institution'
			AND rb.scope_id = $3
			AND $4 = ANY(r.permissions)
			AND $5`+shareOrderedRoleBindingSQL,
		inviterID.String(),
		model.TimeUTC(at),
		institutionID,
		string(action),
		grantScopesOf(action).institution,
	)
}

// lockInviterActionGrantAtAcademicUnit returns sql.ErrNoRows when nothing
// grants. The inherit flags and strictParent come from the action's
// definition.
func lockInviterActionGrantAtAcademicUnit(
	ctx context.Context,
	tx *sqlxTxWrapper,
	inviterID model.UserID,
	action model.Action,
	academicUnitID model.AcademicUnitID,
	at time.Time,
	inheritInstitution, inheritAcademicUnits, strictParent bool,
) error {
	var granted bool
	return tx.Get(
		ctx,
		&granted,
		`WITH RECURSIVE ancestors AS (
			SELECT id, parent_id, 0 depth
			FROM academic_units
			WHERE id = $1
				AND archived_at IS NULL
			UNION ALL
			SELECT au.id, au.parent_id, a.depth+1
			FROM academic_units au
			JOIN ancestors a ON au.id = a.parent_id
			WHERE au.archived_at IS NULL
		)
		SELECT true
		FROM role_bindings rb
		JOIN roles r ON r.id = rb.role_id
			AND r.archived_at IS NULL
		WHERE rb.user_id = $2
			AND rb.archived_at IS NULL
			AND rb.start_at <= $3
			AND (rb.end_at IS NULL OR rb.end_at > $3)
			AND $4 = ANY(r.permissions)
			AND (
				(rb.scope_type = 'institution' AND $5)
				OR (
					rb.scope_type = 'academic_unit'
					AND $6
					AND rb.scope_id IN (SELECT id FROM ancestors WHERE NOT $7 OR depth > 0)
				)
			)`+shareOrderedRoleBindingSQL,
		academicUnitID.String(),
		inviterID.String(),
		model.TimeUTC(at),
		string(action),
		inheritInstitution,
		inheritAcademicUnits,
		strictParent,
	)
}

// lockInviterActionGrantAtClass returns sql.ErrNoRows when nothing grants.
// Unlike lockActionGrantAtClass, a unit binding reaches the class only while
// the class, its programme level and its programme are unarchived.
func lockInviterActionGrantAtClass(
	ctx context.Context,
	tx *sqlxTxWrapper,
	inviterID model.UserID,
	action model.Action,
	classID model.ClassID,
	at time.Time,
) error {
	scopes := grantScopesOf(action)
	var granted struct {
		RoleID        string `db:"role_id"`
		RoleBindingID string `db:"role_binding_id"`
	}
	return tx.Get(
		ctx,
		&granted,
		`WITH RECURSIVE class_unit AS (
			SELECT p.academic_unit_id
			FROM classes c
			JOIN programme_levels pl ON pl.id = c.programme_level_id
				AND pl.archived_at IS NULL
			JOIN programmes p ON p.id = pl.programme_id
				AND p.archived_at IS NULL
			WHERE c.id = $1
				AND c.archived_at IS NULL
		),
		ancestors AS (
			SELECT academic_unit_id id
			FROM class_unit
			UNION ALL
			SELECT au.parent_id
			FROM academic_units au
			JOIN ancestors a ON au.id = a.id
			WHERE au.parent_id IS NOT NULL
				AND au.archived_at IS NULL
		)
		SELECT r.id role_id, rb.id role_binding_id
		FROM role_bindings rb
		JOIN roles r ON r.id = rb.role_id
			AND r.archived_at IS NULL
		WHERE rb.user_id = $2
			AND rb.archived_at IS NULL
			AND rb.start_at <= $3
			AND (rb.end_at IS NULL OR rb.end_at > $3)
			AND $4 = ANY(r.permissions)
			AND (
				(rb.scope_type = 'institution' AND $5)
				OR (rb.scope_type = 'class' AND rb.scope_id = $1 AND $6)
				OR (
					rb.scope_type = 'academic_unit'
					AND $7
					AND rb.scope_id IN (SELECT id FROM ancestors)
				)
			)`+shareOrderedRoleBindingSQL,
		classID.String(),
		inviterID.String(),
		model.TimeUTC(at),
		string(action),
		scopes.institution,
		scopes.class,
		scopes.academicUnits,
	)
}

// examManagementAuthoritySQL finds a binding giving the user management
// authority over one exam: the system administrator role's
// exam.manage.override at the institution, or exam.manage at the institution
// or at or above the exam's unit together with the exam manager relationship
// and a current membership of the exam's unit. It locks nothing; wrap it in
// EXISTS. unitAncestorIDs is a subquery; examID and academicUnitID are a
// column or a placeholder. Placeholders, in order: user, instant, instant,
// system administrator role name, exam.manage.override, exam.manage,
// [examID], user, [academicUnitID], user, instant, instant.
func examManagementAuthoritySQL(unitAncestorIDs, examID, academicUnitID string) string {
	return `SELECT 1
		FROM role_bindings binding
		JOIN roles role ON role.id = binding.role_id
		WHERE binding.user_id = ?
			AND binding.archived_at IS NULL
			AND binding.start_at <= ?
			AND (binding.end_at IS NULL OR binding.end_at > ?)
			AND role.archived_at IS NULL
			AND (
				(
					role.name = ?
					AND ? = ANY(role.permissions)
					AND binding.scope_type = 'institution'
				)
				OR (
					? = ANY(role.permissions)
					AND (
						binding.scope_type = 'institution'
						OR (
							binding.scope_type = 'academic_unit'
							AND binding.scope_id IN (` + unitAncestorIDs + `)
						)
					)
					AND EXISTS (
						SELECT 1
						FROM exam_managers manager
						WHERE manager.exam_id = ` + examID + `
							AND manager.user_id = ?
					)
					AND EXISTS (
						SELECT 1
						FROM academic_unit_members member
						WHERE member.academic_unit_id = ` + academicUnitID + `
							AND member.user_id = ?
							AND member.archived_at IS NULL
							AND member.start_at <= ?
							AND (member.end_at IS NULL OR member.end_at > ?)
					)
				)
			)`
}

// hasEffectiveExamManagementAuthority reads bindings, roles, exam managers and
// memberships without locking them. That is safe because a binding grant and
// an exam manager addition take the same candidate advisory lock first
// (guardRoleBindingExamCandidateConflict, lockExamManagerCandidateFence), and
// admission holds it when it calls this.
func hasEffectiveExamManagementAuthority(
	ctx context.Context,
	tx *sqlxTxWrapper,
	userID, examID, academicUnitID string,
	at time.Time,
) (bool, error) {
	var authoritative bool
	err := tx.Get(
		ctx,
		&authoritative,
		`WITH RECURSIVE ancestors AS (
			SELECT id, parent_id
			FROM academic_units
			WHERE id = ?
				AND archived_at IS NULL
			UNION ALL
			SELECT parent.id, parent.parent_id
			FROM academic_units parent
			JOIN ancestors child ON parent.id = child.parent_id
			WHERE parent.archived_at IS NULL
		)
		SELECT EXISTS (
			`+examManagementAuthoritySQL(`SELECT id FROM ancestors`, `?`, `?`)+`
		)`,
		academicUnitID,
		userID,
		at,
		at,
		model.SystemAdministratorRoleName,
		string(model.ActionExamManageOverride),
		string(model.ActionExamManage),
		examID,
		userID,
		academicUnitID,
		userID,
		at,
		at,
	)
	if err != nil {
		return false, fmt.Errorf("inspect effective Exam management authority: %w", err)
	}

	return authoritative, nil
}

// unitAncestorsByRootCTE names "unit_ancestors(root_id, id)": for every
// unarchived unit as root_id, the unit and each unarchived unit above it. It
// binds nothing.
const unitAncestorsByRootCTE = `unit_ancestors(root_id, id) AS (
		SELECT id, id
		FROM academic_units
		WHERE archived_at IS NULL
		UNION ALL
		SELECT child.root_id, parent.id
		FROM unit_ancestors child
		JOIN academic_units current ON current.id = child.id
		JOIN academic_units parent ON parent.id = current.parent_id
			AND parent.archived_at IS NULL
	)`

// candidateActivityManagementAuthoritySQL only reports the answer at statement
// time; admission decides it again under its locks.
func candidateActivityManagementAuthoritySQL() string {
	return examManagementAuthoritySQL(
		`
								SELECT id
								FROM unit_ancestors
								WHERE root_id = e.academic_unit_id
							`,
		`e.id`,
		`e.academic_unit_id`,
	)
}

// roleBindingGrantsExamManagementOverUnresolvedAttempt reports whether
// granting the binding would give its user management authority over an exam
// where they have a ready, active or suspended attempt. The binding does not
// exist yet, so its role, scope and end are bound as values; "now" is
// statement_timestamp(). It locks nothing: the caller holds the candidate's
// admission advisory lock, which admission takes before evaluating the same
// authority.
//
// Placeholders, in order: binding end, user, role, system administrator role
// name, exam.manage.override, scope type, exam.manage, scope type, scope
// type, scope ID, user, user.
func roleBindingGrantsExamManagementOverUnresolvedAttempt(
	ctx context.Context,
	tx *sqlxTxWrapper,
	candidate *model.RoleBinding,
) (bool, error) {
	var grants bool
	err := tx.Get(
		ctx,
		&grants,
		`WITH RECURSIVE input AS (
			SELECT ?::timestamptz AS end_at
		),
		unresolved AS (
			SELECT attempt.exam_id, exam.academic_unit_id
			FROM exam_attempts attempt
			JOIN exams exam ON exam.id = attempt.exam_id
			WHERE attempt.candidate_user_id = ?
				AND attempt.state IN ('ready', 'active', 'suspended')
		),
		covered_units(root_id, id) AS (
			SELECT academic_unit_id, academic_unit_id
			FROM unresolved
			UNION
			SELECT child.root_id, parent.parent_id
			FROM covered_units child
			JOIN academic_units parent ON parent.id = child.id
				AND parent.parent_id IS NOT NULL
				AND parent.archived_at IS NULL
		)
		SELECT EXISTS (
			SELECT 1
			FROM roles role
			CROSS JOIN input binding
			WHERE role.id = ?
				AND role.archived_at IS NULL
				AND (binding.end_at IS NULL OR binding.end_at > statement_timestamp())
				AND (
					(
						role.name = ?
						AND ? = ANY(role.permissions)
						AND ? = 'institution'
						AND EXISTS (SELECT 1 FROM unresolved)
					)
					OR (
						? = ANY(role.permissions)
						AND (? = 'institution' OR (? = 'academic_unit' AND ? IN (SELECT id FROM covered_units)))
						AND EXISTS (
							SELECT 1
							FROM unresolved attempt
							JOIN exam_managers manager ON manager.exam_id = attempt.exam_id
								AND manager.user_id = ?
							JOIN academic_unit_members member ON member.academic_unit_id = attempt.academic_unit_id
								AND member.user_id = ?
							WHERE member.archived_at IS NULL
								AND member.start_at <= statement_timestamp()
								AND (member.end_at IS NULL OR member.end_at > statement_timestamp())
						)
					)
				)
		)`,
		NullTimeFromOptional(candidate.EndsAt),
		candidate.UserID.String(),
		candidate.RoleID.String(),
		model.SystemAdministratorRoleName,
		string(model.ActionExamManageOverride),
		candidate.ScopeType,
		string(model.ActionExamManage),
		candidate.ScopeType,
		candidate.ScopeType,
		candidate.ScopeID,
		candidate.UserID.String(),
		candidate.UserID.String(),
	)

	return grants, err
}

// isActiveSystemAdministrator reads without locking. It is not the fence that
// keeps one system administrator able to sign in; that lives in
// system_administrator_authentication.go.
func isActiveSystemAdministrator(ctx context.Context, executor sqlxExecutor, userID string, at time.Time) (bool, error) {
	var found bool
	if err := executor.Get(ctx, &found, `SELECT EXISTS (
		SELECT 1 FROM role_bindings rb
		JOIN roles r ON r.id = rb.role_id
		JOIN users u ON u.id = rb.user_id
		WHERE rb.user_id = $1
			AND r.name = $2
			AND r.built_in = TRUE
			AND r.archived_at IS NULL
			AND rb.scope_type = 'institution'
			AND rb.archived_at IS NULL
			AND rb.start_at <= $3
			AND (rb.end_at IS NULL OR rb.end_at > $3)
			AND u.archived_at IS NULL
			AND u.disabled_at IS NULL
	)`, userID, model.SystemAdministratorRoleName, at); err != nil {
		return false, fmt.Errorf("check active system administrator: %w", err)
	}
	return found, nil
}

// The current user's product areas only steer navigation, so they are read
// outside a transaction at statement_timestamp(); every use case behind an
// area authorizes again.

// activeRoleBindingsCTE names "active_bindings": the bound user's bindings
// effective at statement_timestamp(). Placeholder: user.
const activeRoleBindingsCTE = `active_bindings AS (
		SELECT binding.scope_type, binding.scope_id, role.name, role.permissions
		FROM role_bindings binding
		JOIN roles role ON role.id = binding.role_id
		WHERE binding.user_id = ?
			AND binding.archived_at IS NULL
			AND role.archived_at IS NULL
			AND binding.start_at <= statement_timestamp()
			AND (binding.end_at IS NULL OR binding.end_at > statement_timestamp())
	)`

// managedExamsCTE names "managed_exams": exams the bound user manages while a
// current member of the exam's unit. Placeholders: user, user.
const managedExamsCTE = `managed_exams AS (
		SELECT exam.id, exam.academic_unit_id
		FROM exams exam
		JOIN exam_managers manager ON manager.exam_id = exam.id
			AND manager.user_id = ?
		JOIN academic_unit_members member ON member.academic_unit_id = exam.academic_unit_id
			AND member.user_id = ?
		WHERE exam.copying_until IS NULL
			AND member.archived_at IS NULL
			AND member.start_at <= statement_timestamp()
			AND (member.end_at IS NULL OR member.end_at > statement_timestamp())
	)`

// examManagementBindingSQL is examManagementAuthoritySQL for navigation,
// widened from exam.manage to every exam management action. It needs
// activeRoleBindingsCTE, unitAncestorsByRootCTE and managedExamsCTE.
// Placeholders: system administrator role name, exam.manage.override, exam
// management actions (text[]).
const examManagementBindingSQL = `(binding.name = ? AND ? = ANY(binding.permissions) AND binding.scope_type = 'institution')
		OR (
			binding.permissions && ?::text[]
			AND EXISTS (
				SELECT 1
				FROM managed_exams managed
				WHERE binding.scope_type = 'institution'
					OR (
						binding.scope_type = 'academic_unit'
						AND binding.scope_id IN (
							SELECT id
							FROM unit_ancestors
							WHERE root_id = managed.academic_unit_id
						)
					)
			)
		)`

// Exam manager fragments. Each binds the actor user and reads exam_managers
// without locking it, so the answer is as of the embedding statement, which
// locks the exam or attempt row.

// examManagerExistsSQL tests exam alias e.
const examManagerExistsSQL = `EXISTS (
			SELECT 1
			FROM exam_managers m
			WHERE m.exam_id = e.id
				AND m.user_id = ?
		)`

// attemptExamManagerExistsSQL tests the exam of attempt a.
const attemptExamManagerExistsSQL = `EXISTS (
			SELECT 1
			FROM exam_managers m
			WHERE m.exam_id = a.exam_id
				AND m.user_id = ?
		)`

// actorExamManagerExistsSQL is examManagerExistsSQL under the alias the
// authoring projections use beside their other exam_managers subqueries.
const actorExamManagerExistsSQL = `EXISTS (
			SELECT 1
			FROM exam_managers actor_manager
			WHERE actor_manager.exam_id = e.id
				AND actor_manager.user_id = ?
		)`

// lockExamManagerWithUnitMembership shares the exam manager and unit
// membership rows so neither can be removed or ended before commit. It returns
// sql.ErrNoRows when the relationship does not hold.
func lockExamManagerWithUnitMembership(
	ctx context.Context,
	tx *sqlxTxWrapper,
	examID model.ExamID,
	userID model.UserID,
	academicUnitID model.AcademicUnitID,
	at time.Time,
) error {
	var held bool
	return tx.Get(
		ctx,
		&held,
		`SELECT true
		FROM exam_managers m
		JOIN academic_unit_members u ON u.user_id = m.user_id
		WHERE m.exam_id = ?
			AND m.user_id = ?
			AND u.academic_unit_id = ?
			AND u.archived_at IS NULL
			AND u.start_at <= ?
			AND (u.end_at IS NULL OR u.end_at > ?)
		LIMIT 1
		FOR SHARE OF m, u`,
		examID.String(),
		userID.String(),
		academicUnitID.String(),
		at,
		at,
	)
}

// lockExamManager shares the exam manager row so it cannot be removed before
// commit, and returns sql.ErrNoRows otherwise. An override export that
// includes browser activity still requires the relationship.
func lockExamManager(ctx context.Context, tx *sqlxTxWrapper, examID model.ExamID, userID model.UserID) error {
	var held bool
	return tx.Get(
		ctx,
		&held,
		`SELECT true
		FROM exam_managers
		WHERE exam_id = ?
			AND user_id = ?
		FOR SHARE`,
		examID.String(),
		userID.String(),
	)
}
