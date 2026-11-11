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

// This file holds the places the store itself evaluates authority: whether a
// User holds an Action through a Role Binding, holds the system administrator
// Role, or is an Exam Manager. The application decides authorization first
// (package access); the store repeats the decision only where it must hold
// under the transaction's row locks and against the instant the transaction
// decided at, so a Role, Role Binding, membership or Manager change cannot
// slip between the application's check and the commit. Change how authority
// works here and in package access together.
//
// Not here, because they decide something else: list and search filters that
// apply a scope the application already computed (the visibility predicates
// in the user, class, audit, Role Binding and Exam catalog stores), the fence
// that keeps a system administrator able to sign in
// (system_administrator_authentication.go), and checks that a relationship
// exists rather than that an actor may act (archive dependencies, Invitation
// satisfaction, mail relevance, retention notice recipients).
//
// The rules every evaluation shares:
//   - A Role Binding counts when it and its Role are unarchived and
//     start_at <= at < end_at (no end_at means open-ended).
//   - A Role grants an Action when its permissions array contains it.
//   - An Institution binding covers every resource; an Academic Unit binding
//     covers that unit and its unarchived descendants (walked upwards from
//     the resource as the "ancestors" of its unit); a Class binding covers
//     exactly that Class.
//   - Ordinary Exam management needs the exam.manage Action, the Exam Manager
//     relationship and a current membership of the Exam's Academic Unit; the
//     system administrator Role's exam.manage.override at the Institution
//     needs neither.
//
// The helpers return the executor's error unchanged (sql.ErrNoRows when no
// binding grants the Action) so each caller keeps its own Store error.

// Role Bindings that grant one Action, locked for the transaction.
//
// These five statements are one query in five shapes. Each selects one Role
// Binding of the User that is effective at the bound instant and whose Role
// grants the bound Action, and shares that binding and its Role (FOR SHARE OF
// rb, r) so neither can be ended, archived or edited before commit.

// effectiveRoleBindingSQL selects from the User's Role Bindings that are
// effective at the bound instant, joined to their unarchived Roles.
// Placeholders: User, instant, instant.
const effectiveRoleBindingSQL = `SELECT true
	FROM role_bindings rb
	JOIN roles r ON r.id = rb.role_id
		AND r.archived_at IS NULL
	WHERE rb.user_id = ?
		AND rb.archived_at IS NULL
		AND rb.start_at <= ?
		AND (rb.end_at IS NULL OR rb.end_at > ?)`

// shareRoleBindingSQL keeps one granting Role Binding and its Role shared
// until the transaction ends.
const shareRoleBindingSQL = `
	LIMIT 1
	FOR SHARE OF rb, r`

// academicUnitAncestorsCTE names "ancestors": the bound Academic Unit, when
// unarchived, and each unarchived unit above it. Placeholder: Academic Unit.
const academicUnitAncestorsCTE = `WITH RECURSIVE ancestors AS (
		SELECT id, parent_id
		FROM academic_units
		WHERE id = ?
			AND archived_at IS NULL
		UNION ALL
		SELECT au.id, au.parent_id
		FROM academic_units au
		JOIN ancestors child ON au.id = child.parent_id
		WHERE au.archived_at IS NULL
	)
	`

// classAncestorsCTE names "ancestors": the Academic Unit that owns the bound
// Class through its Programme Level and Programme, and each unit above it,
// walking up while the unit below is unarchived. Placeholder: Class.
const classAncestorsCTE = `WITH RECURSIVE class_unit AS (
		SELECT p.academic_unit_id
		FROM classes c
		JOIN programme_levels pl ON pl.id = c.programme_level_id
		JOIN programmes p ON p.id = pl.programme_id
		WHERE c.id = ?
	),
	ancestors AS (
		SELECT academic_unit_id id
		FROM class_unit
		UNION ALL
		SELECT au.parent_id
		FROM academic_units au
		JOIN ancestors child ON au.id = child.id
		WHERE au.parent_id IS NOT NULL
			AND au.archived_at IS NULL
	)
	`

// lockActionGrantAtInstitution decides whether the User holds the Action on
// the Institution at the instant at: a Role Binding scoped to exactly that
// Institution whose Role grants the Action. It shares the granting Role
// Binding and Role rows. It returns sql.ErrNoRows when nothing grants.
//
// It is the in-transaction counterpart of access.Authorizer.Authorize for an
// Institution resource.
func lockActionGrantAtInstitution(
	ctx context.Context,
	tx *sqlxTxWrapper,
	userID model.UserID,
	action model.Action,
	institutionID string,
	at time.Time,
) error {
	var granted bool
	return tx.Get(
		ctx,
		&granted,
		effectiveRoleBindingSQL+`
			AND rb.scope_type = 'institution'
			AND rb.scope_id = ?
			AND ? = ANY(r.permissions)`+shareRoleBindingSQL,
		userID.String(),
		at,
		at,
		institutionID,
		string(action),
	)
}

// lockActionGrantAtAcademicUnit decides whether the User holds the Action on
// the Academic Unit at the instant at: an Institution Role Binding, or a
// binding at the unit or any unit above it, whose Role grants the Action. It
// reads the unit's ancestry and shares the granting Role Binding and Role
// rows. It returns sql.ErrNoRows when nothing grants.
//
// It is the in-transaction counterpart of access.Authorizer.Authorize for an
// Academic Unit resource.
func lockActionGrantAtAcademicUnit(
	ctx context.Context,
	tx *sqlxTxWrapper,
	userID model.UserID,
	action model.Action,
	academicUnitID string,
	at time.Time,
) error {
	var granted bool
	return tx.Get(
		ctx,
		&granted,
		academicUnitAncestorsCTE+effectiveRoleBindingSQL+`
			AND ? = ANY(r.permissions)
			AND (
				rb.scope_type = 'institution'
				OR (
					rb.scope_type = 'academic_unit'
					AND rb.scope_id IN (SELECT id FROM ancestors)
				)
			)`+shareRoleBindingSQL,
		academicUnitID,
		userID.String(),
		at,
		at,
		string(action),
	)
}

// lockDelegableActionGrantAtAcademicUnit is lockActionGrantAtAcademicUnit for
// an Action the User hands on to someone else. With strictParent, a binding
// at the unit itself does not count: the grant must come from the Institution
// or a unit above, so nobody widens authority at their own level.
//
// It is the in-transaction counterpart of
// access.Authorizer.CanDelegateActionsAtScope, whose strict-parent rule covers
// the protected delegation Actions.
func lockDelegableActionGrantAtAcademicUnit(
	ctx context.Context,
	tx *sqlxTxWrapper,
	userID model.UserID,
	action model.Action,
	academicUnitID string,
	at time.Time,
	strictParent bool,
) error {
	var granted bool
	return tx.Get(
		ctx,
		&granted,
		academicUnitAncestorsCTE+effectiveRoleBindingSQL+`
			AND ? = ANY(r.permissions)
			AND (
				rb.scope_type = 'institution'
				OR (
					rb.scope_type = 'academic_unit'
					AND rb.scope_id IN (SELECT id FROM ancestors)
					AND (NOT ? OR rb.scope_id <> ?)
				)
			)`+shareRoleBindingSQL,
		academicUnitID,
		userID.String(),
		at,
		at,
		string(action),
		strictParent,
		academicUnitID,
	)
}

// lockActionGrantAtClass decides whether the User holds the Action on the
// Class at the instant at: an Institution Role Binding, a binding at exactly
// that Class, or a binding at the Academic Unit that owns the Class or any
// unit above it, whose Role grants the Action. It reads the Class lineage and
// shares the granting Role Binding and Role rows. It returns sql.ErrNoRows
// when nothing grants.
//
// It is the in-transaction counterpart of access.Authorizer.Authorize for a
// Class resource.
func lockActionGrantAtClass(
	ctx context.Context,
	tx *sqlxTxWrapper,
	userID model.UserID,
	action model.Action,
	classID string,
	at time.Time,
) error {
	var granted bool
	return tx.Get(
		ctx,
		&granted,
		classAncestorsCTE+effectiveRoleBindingSQL+`
			AND ? = ANY(r.permissions)
			AND (
				rb.scope_type = 'institution'
				OR (rb.scope_type = 'class' AND rb.scope_id = ?)
				OR (
					rb.scope_type = 'academic_unit'
					AND rb.scope_id IN (SELECT id FROM ancestors)
				)
			)`+shareRoleBindingSQL,
		classID,
		userID.String(),
		at,
		at,
		string(action),
		classID,
	)
}

// lockDelegableActionGrantAtClass is lockActionGrantAtClass for an Action the
// User hands on to someone else. With strictParent, a binding at the Class
// itself does not count: the grant must come from the Institution or an
// Academic Unit above the Class.
//
// It is the in-transaction counterpart of
// access.Authorizer.CanDelegateActionsAtScope.
func lockDelegableActionGrantAtClass(
	ctx context.Context,
	tx *sqlxTxWrapper,
	userID model.UserID,
	action model.Action,
	classID string,
	at time.Time,
	strictParent bool,
) error {
	var granted bool
	return tx.Get(
		ctx,
		&granted,
		classAncestorsCTE+effectiveRoleBindingSQL+`
			AND ? = ANY(r.permissions)
			AND (
				rb.scope_type = 'institution'
				OR (rb.scope_type = 'class' AND rb.scope_id = ? AND NOT ?)
				OR (
					rb.scope_type = 'academic_unit'
					AND rb.scope_id IN (SELECT id FROM ancestors)
				)
			)`+shareRoleBindingSQL,
		classID,
		userID.String(),
		at,
		at,
		string(action),
		classID,
		strictParent,
	)
}

// Inviter authority for Invitation issuance.
//
// Issuance checks several Actions in a row for the same inviter, so these
// statements pick the granting binding deterministically (ORDER BY r.id,
// rb.id) and share the Role before the Role Binding (FOR SHARE OF r, rb),
// the reverse of the statements above. Keep each family's lock order: the
// Invitation fences were tested against this one. They reuse arguments
// positionally ($N).

// shareOrderedRoleBindingSQL keeps the first granting Role, then its Role
// Binding, shared until the transaction ends.
const shareOrderedRoleBindingSQL = `
	ORDER BY r.id, rb.id
	LIMIT 1
	FOR SHARE OF r, rb`

// lockInviterActionGrantAtInstitution decides whether the inviter holds the
// Action on the Institution at the instant at, through a Role Binding scoped
// to exactly that Institution. It shares the granting Role and Role Binding
// rows. It returns sql.ErrNoRows when nothing grants.
//
// It is the in-transaction counterpart of
// access.Authorizer.CanDelegateActionsAtScope at Institution scope, where no
// strict-parent rule applies.
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
			AND $4 = ANY(r.permissions)`+shareOrderedRoleBindingSQL,
		inviterID.String(),
		model.TimeUTC(at),
		institutionID,
		string(action),
	)
}

// lockInviterActionGrantAtAcademicUnit decides whether the inviter holds the
// Action on the Academic Unit at the instant at, counting only the binding
// scopes the Action inherits from: an Institution binding with
// inheritInstitution, and a binding at the unit or a unit above it with
// inheritAcademicUnits (strictly above with strictParent). It reads the
// unit's ancestry with each unit's depth and shares the granting Role and
// Role Binding rows. It returns sql.ErrNoRows when nothing grants.
//
// It is the in-transaction counterpart of
// access.Authorizer.CanDelegateActionsAtScope: the two inherit flags are the
// Action definition's InheritInstitutionScope and InheritAcademicUnitScopes,
// and strictParent its protected-delegation rule.
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

// lockInviterActionGrantAtClass decides whether the inviter holds the Action
// on the Class at the instant at: an Institution Role Binding, a binding at
// exactly that Class, or a binding at the Academic Unit that owns the Class
// or any unit above it. Unlike lockActionGrantAtClass, the Class, its
// Programme Level and its Programme must be unarchived for a unit binding to
// reach the Class. It shares the granting Role and Role Binding rows. It
// returns sql.ErrNoRows when nothing grants.
//
// It is the in-transaction counterpart of access.Authorizer.Authorize for a
// Class resource.
func lockInviterActionGrantAtClass(
	ctx context.Context,
	tx *sqlxTxWrapper,
	inviterID model.UserID,
	action model.Action,
	classID model.ClassID,
	at time.Time,
) error {
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
				rb.scope_type = 'institution'
				OR (rb.scope_type = 'class' AND rb.scope_id = $1)
				OR (
					rb.scope_type = 'academic_unit'
					AND rb.scope_id IN (SELECT id FROM ancestors)
				)
			)`+shareOrderedRoleBindingSQL,
		classID.String(),
		inviterID.String(),
		model.TimeUTC(at),
		string(action),
	)
}

// Exam management authority.

// examManagementAuthoritySQL is the subquery that finds a Role Binding giving
// a User management authority over one Exam: either the system administrator
// Role's exam.manage.override at the Institution, or exam.manage at the
// Institution or at the Exam's Academic Unit or a unit above it, together
// with the Exam Manager relationship and a current membership of the Exam's
// Academic Unit. It reads and locks nothing by itself; wrap it in EXISTS.
//
// The caller says how the statement names the resource: unitAncestorIDs is a
// subquery returning the Exam's Academic Unit and the units above it, examID
// and academicUnitID are a column or a placeholder. Placeholders, in order:
// User, instant, instant, system administrator Role name,
// exam.manage.override, exam.manage, [examID], User, [academicUnitID], User,
// instant, instant.
//
// It is the counterpart of manageraccess.SelectAction (ordinary Manager or
// override) followed by access.Authorizer.Authorize of the selected Action
// on the Exam. Attempt admission uses it to refuse a candidate who manages
// the Exam.
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

// hasEffectiveExamManagementAuthority decides whether the User has management
// authority over the Exam, owned by the given Academic Unit, at the instant
// at (see examManagementAuthoritySQL). Attempt admission calls it with the
// admission decision time read from PostgreSQL, after the candidate User, the
// admission advisory lock and the Exam are locked. The statement itself reads
// Role Bindings, Roles, Exam Managers and unit memberships without locking
// them; a Role Binding grant and an Exam Manager addition take the same
// candidate advisory lock first (guardRoleBindingExamCandidateConflict,
// lockExamManagerCandidateFence).
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
// unarchived Academic Unit as root_id, the unit itself and each unarchived
// unit above it. A statement that evaluates authority over many Exams at once
// looks a unit's ancestry up by root_id. It binds nothing.
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

// candidateActivityManagementAuthoritySQL is examManagementAuthoritySQL for
// the Candidate activity projection, which evaluates it for each listed
// Sitting's Exam e against unit_ancestors (unitAncestorsByRootCTE) at the
// statement's server time. The projection only reports the answer; admission
// decides it again under its locks with hasEffectiveExamManagementAuthority.
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

// roleBindingGrantsExamManagementOverUnresolvedAttempt decides whether
// granting the candidate Role Binding would give its User management
// authority over an Exam in which that User has an unresolved Attempt (ready,
// active or suspended). It is examManagementAuthoritySQL turned round: the
// binding does not exist yet, so its Role, scope and end are bound as values,
// and "now" is PostgreSQL statement_timestamp(). A binding that has already
// ended confers nothing. It reads the Role, the User's unresolved Attempts,
// their Exams' unit ancestry, Exam Managers and unit memberships and locks
// none of them; the caller holds the candidate's admission advisory lock,
// which Attempt admission takes before it evaluates the same authority.
//
// Placeholders, in order: binding end, User, Role, system administrator Role
// name, exam.manage.override, scope type, exam.manage, scope type, scope
// type, scope ID, User, User.
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

// The system administrator Role.

// isActiveSystemAdministrator decides whether the User holds the built-in
// system administrator Role at the instant at: an effective Institution Role
// Binding of that unarchived Role, for a User who is neither archived nor
// disabled. It reads the rows without locking them. Callers use it at their
// decision time either to recheck an actor (retention policy and cleanup,
// hold release, Desktop compatibility policy, MFA reset) or to recognise a
// target User as a system administrator (disabling, administrator MFA
// reset). For an actor it stands beside access.Authorizer.Authorize, which
// evaluated the Action through the same Institution Role Binding.
//
// The fence that keeps one system administrator able to sign in is a
// different question and stays in system_administrator_authentication.go.
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

// The current User's product areas.
//
// The current-User context reports which product areas a User's access opens
// and which scopes they can navigate to manage. It is a read outside any
// transaction, evaluated at PostgreSQL statement_timestamp(), and it only
// steers navigation: every use case behind an area authorizes again.

// activeRoleBindingsCTE names "active_bindings": the bound User's Role
// Bindings effective at statement_timestamp(), each with its Role's name and
// permissions. Placeholder: User.
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

// managedExamsCTE names "managed_exams": the Exams the bound User manages
// while currently a member of the Exam's Academic Unit, at
// statement_timestamp(). Placeholders: User, User.
const managedExamsCTE = `managed_exams AS (
		SELECT exam.id, exam.academic_unit_id
		FROM exams exam
		JOIN exam_managers manager ON manager.exam_id = exam.id
			AND manager.user_id = ?
		JOIN academic_unit_members member ON member.academic_unit_id = exam.academic_unit_id
			AND member.user_id = ?
		WHERE member.archived_at IS NULL
			AND member.start_at <= statement_timestamp()
			AND (member.end_at IS NULL OR member.end_at > statement_timestamp())
	)`

// examManagementBindingSQL is true for an active_bindings row "binding" that
// opens Exam management: the system administrator Role's exam.manage.override
// at the Institution, or any of the bound Exam management Actions at the
// Institution or at a unit at or above one of managed_exams. It needs
// activeRoleBindingsCTE, unitAncestorsByRootCTE and managedExamsCTE.
// Placeholders: system administrator Role name, exam.manage.override, Exam
// management Actions (text[]).
//
// It is the navigation counterpart of examManagementAuthoritySQL, widened
// from exam.manage to every Exam management Action.
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

// The Exam Manager relationship.
//
// A mutation of an Exam, its Draft, Resources, Starter Workspace, Sittings or
// Attempts reads whether its actor is an Exam Manager in the same statement
// that locks the Exam (or Attempt) row, then refuses a non-Manager unless the
// application authorized the explicit override Action. The three fragments
// below are that one test under the aliases of the statements that embed it;
// each binds the actor User and reads exam_managers without locking it, so
// the answer is as of the embedding statement. The application decides the
// same thing with manageraccess.SelectAction.

// examManagerExistsSQL is true when the bound User is a Manager of Exam e.
const examManagerExistsSQL = `EXISTS (
			SELECT 1
			FROM exam_managers m
			WHERE m.exam_id = e.id
				AND m.user_id = ?
		)`

// attemptExamManagerExistsSQL is true when the bound User is a Manager of
// the Exam of Attempt a.
const attemptExamManagerExistsSQL = `EXISTS (
			SELECT 1
			FROM exam_managers m
			WHERE m.exam_id = a.exam_id
				AND m.user_id = ?
		)`

// actorExamManagerExistsSQL is examManagerExistsSQL under the alias the Exam
// authoring projections use beside their other exam_managers subqueries.
const actorExamManagerExistsSQL = `EXISTS (
			SELECT 1
			FROM exam_managers actor_manager
			WHERE actor_manager.exam_id = e.id
				AND actor_manager.user_id = ?
		)`

// lockExamManagerWithUnitMembership decides whether the User is, at the
// instant at, a Manager of the Exam and a current member of the given
// Academic Unit (the Exam's), which the ordinary Exam records Actions require
// on top of the Role Binding. It shares the Exam Manager and unit membership
// rows so neither can be removed or ended before commit. It returns
// sql.ErrNoRows when the relationship does not hold.
//
// It is the in-transaction counterpart of the ordinary branch of
// manageraccess.SelectAction.
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

// lockExamManager decides whether the User is a Manager of the Exam and
// shares that Exam Manager row so it cannot be removed before commit. It
// returns sql.ErrNoRows when the User is not a Manager. An override export
// that includes browser activity still requires the Manager relationship.
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
