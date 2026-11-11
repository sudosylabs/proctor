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
	"errors"
	"time"

	"github.com/lib/pq"

	"github.com/sudosylabs/proctor/server/model"
	"github.com/sudosylabs/proctor/server/store"
)

func validCommandAuthorization(authority *store.CommandAuthorization) bool {
	if authority == nil {
		return true
	}

	if authority.Principal.Validate() != nil ||
		!authority.ScopeType.IsValid() ||
		!model.IsValidOpaqueID(authority.ScopeID) ||
		len(authority.Actions) == 0 {
		return false
	}

	if !authority.ClassMemberID.IsZero() && !authority.ClassMemberID.IsValid() ||
		!authority.RecipientUserID.IsZero() && !authority.RecipientUserID.IsValid() {
		return false
	}

	for _, action := range append(append([]model.Action(nil), authority.Actions...), authority.DelegatedActions...) {
		if _, known := model.DefinitionForAction(action); !known {
			return false
		}
	}

	return true
}

func lockCommandAuthorization(ctx context.Context, tx *sqlxTxWrapper, authority *store.CommandAuthorization) error {
	if authority == nil {
		return nil
	}

	at, err := requireCurrentPrincipalCredential(ctx, tx, authority.Principal)
	if err != nil {
		return err
	}

	recipientUserID := authority.RecipientUserID
	if authority.ClassMemberID.IsValid() {
		var resolvedUserID string
		if err = tx.Get(
			ctx,
			&resolvedUserID,
			`SELECT user_id FROM class_members WHERE id = ? AND archived_at IS NULL`,
			authority.ClassMemberID.String(),
		); err != nil {
			return translateError("class_member", authority.ClassMemberID.String(), err)
		}

		resolved, parseErr := model.ParseUserID(resolvedUserID)
		if parseErr != nil {
			return invalidPersistedState("class_member", "user_id", parseErr)
		}
		if recipientUserID.IsValid() && recipientUserID != resolved {
			return store.NewErrConflict("class_member", "user", nil)
		}

		recipientUserID = resolved
	}

	if recipientUserID.IsValid() && recipientUserID != authority.Principal.UserID {
		var lockedUserID string
		if err = tx.Get(
			ctx,
			&lockedUserID,
			`SELECT id FROM users WHERE id = ? AND archived_at IS NULL FOR SHARE`,
			recipientUserID.String(),
		); err != nil {
			return translateError("user", recipientUserID.String(), err)
		}
	}

	needsHierarchy := authority.ScopeType == model.RoleScopeAcademicUnit ||
		authority.ScopeType == model.RoleScopeClass ||
		authority.ClassMemberID.IsValid()
	if needsHierarchy {
		if err = lockAcademicUnitHierarchy(ctx, tx); err != nil {
			return err
		}
	}

	if authority.ClassMemberID.IsValid() {
		var relationship struct {
			ClassID string `db:"class_id"`
			UserID  string `db:"user_id"`
		}
		if err = tx.Get(
			ctx,
			&relationship,
			`SELECT class_id, user_id FROM class_members WHERE id = ? AND archived_at IS NULL FOR SHARE`,
			authority.ClassMemberID.String(),
		); err != nil {
			return translateError("class_member", authority.ClassMemberID.String(), err)
		}

		if relationship.UserID != recipientUserID.String() {
			return store.NewErrConflict("class_member", "user", nil)
		}

		if err = requirePrincipalActionAtScope(
			ctx,
			tx,
			authority.Principal,
			model.ActionClassMembersManage,
			model.RoleScopeClass,
			relationship.ClassID,
			at,
			false,
		); err != nil {
			return err
		}
	}

	for _, action := range authority.Actions {
		if err = requirePrincipalActionAtScope(
			ctx,
			tx,
			authority.Principal,
			action,
			authority.ScopeType,
			authority.ScopeID,
			at,
			false,
		); err != nil {
			return err
		}
	}

	for _, action := range authority.DelegatedActions {
		strictParent := teacherInvitationProtectedDelegationAction(action) && authority.ScopeType != model.RoleScopeInstitution
		if err = requirePrincipalActionAtScope(
			ctx,
			tx,
			authority.Principal,
			action,
			authority.ScopeType,
			authority.ScopeID,
			at,
			strictParent,
		); err != nil {
			return err
		}
	}

	return nil
}

func requireCurrentPrincipalCredential(ctx context.Context, tx *sqlxTxWrapper, principal model.Principal) (time.Time, error) {
	if principal.Validate() != nil {
		return time.Time{}, store.NewErrInvalidInput("authorization", "principal", nil)
	}

	at, err := jobDatabaseNow(ctx, tx)
	if err != nil {
		return time.Time{}, err
	}

	var active bool
	if err = tx.Get(
		ctx,
		&active,
		`SELECT true FROM users WHERE id = ? AND archived_at IS NULL AND disabled_at IS NULL FOR SHARE`,
		principal.UserID.String(),
	); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return time.Time{}, store.NewErrConflict("authorization", "authority", nil)
		}
		return time.Time{}, err
	}

	state, stateErr := getMFARecoveryState(ctx, tx, principal.UserID)
	if stateErr != nil {
		return time.Time{}, stateErr
	}

	if state.ReenrollmentRequired ||
		(principal.CredentialType == model.CredentialSessionAccess && principal.AuthenticationGeneration != state.Generation) {
		return time.Time{}, store.NewErrConflict("authorization", "credential", nil)
	}

	switch principal.CredentialType {
	case model.CredentialSessionAccess:
		err = tx.Get(
			ctx,
			&active,
			`SELECT true
			FROM sessions s
			JOIN session_credentials c ON c.session_id = s.id
			WHERE s.id = ?
				AND s.user_id = ?
				AND s.archived_at IS NULL
				AND s.revoked_at IS NULL
				AND s.expires_at > ?
				AND s.idle_expires_at > ?
				AND s.authentication_generation = ?
				AND NOT s.mfa_recovery_required
				AND NOT s.mfa_enrollment_required
				AND c.id = ?
				AND c.kind = 'access'
				AND c.archived_at IS NULL
				AND c.revoked_at IS NULL
				AND c.expires_at > ?
			FOR SHARE OF s, c`,
			principal.SessionID.String(),
			principal.UserID.String(),
			at,
			at,
			state.Generation,
			principal.CredentialID.String(),
			at,
		)
	case model.CredentialPersonalAccessToken:
		err = tx.Get(
			ctx,
			&active,
			`SELECT true
			FROM personal_access_tokens
			WHERE id = ?
				AND user_id = ?
				AND archived_at IS NULL
				AND disabled_at IS NULL
				AND revoked_at IS NULL
				AND expires_at > ?
				AND scopes = ?
				AND academic_unit_id IS NOT DISTINCT FROM ?
			FOR SHARE`,
			principal.CredentialID.String(),
			principal.UserID.String(),
			at,
			pq.Array(principal.CredentialScopes),
			nullableID(principal.AcademicUnitID.String()),
		)
	default:
		return time.Time{}, store.NewErrConflict("authorization", "credential", nil)
	}

	if errors.Is(err, sql.ErrNoRows) {
		return time.Time{}, store.NewErrConflict("authorization", "credential", nil)
	}
	if err != nil {
		return time.Time{}, err
	}

	return at, nil
}

func requirePrincipalActionAtScope(
	ctx context.Context,
	tx *sqlxTxWrapper,
	principal model.Principal,
	action model.Action,
	scopeType model.RoleScopeType,
	scopeID string,
	at time.Time,
	strictParent bool,
) error {
	if principal.CredentialType == model.CredentialPersonalAccessToken {
		allowed := false
		for _, scope := range principal.CredentialScopes {
			if scope == string(action) {
				allowed = true
				break
			}
		}

		if !allowed {
			return store.NewErrConflict("authorization", "credential_scope", nil)
		}
	}

	return requireUserActionAtScopeWithDelegation(ctx, tx, principal.UserID, action, scopeType, scopeID, at, strictParent)
}

func requireUserActionAtScopeWithDelegation(
	ctx context.Context,
	tx *sqlxTxWrapper,
	userID model.UserID,
	action model.Action,
	scopeType model.RoleScopeType,
	scopeID string,
	at time.Time,
	strictParent bool,
) error {
	if !userID.IsValid() || !scopeType.IsValid() || !model.IsValidOpaqueID(scopeID) || at.IsZero() {
		return store.NewErrInvalidInput("authorization", "scope", nil)
	}

	var err error
	switch scopeType {
	case model.RoleScopeInstitution:
		err = lockActionGrantAtInstitution(ctx, tx, userID, action, scopeID, at)
	case model.RoleScopeAcademicUnit:
		err = lockDelegableActionGrantAtAcademicUnit(ctx, tx, userID, action, scopeID, at, strictParent)
	case model.RoleScopeClass:
		err = lockDelegableActionGrantAtClass(ctx, tx, userID, action, scopeID, at, strictParent)
	default:
		return store.NewErrInvalidInput("authorization", "scope_type", nil)
	}

	if errors.Is(err, sql.ErrNoRows) {
		return store.NewErrConflict("authorization", "authority", nil)
	}

	return err
}
