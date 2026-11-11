// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package storetest

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/sudosylabs/proctor/server/model"
	"github.com/sudosylabs/proctor/server/store"
)

// A command carrying a CommandAuthorization has its authority rechecked
// inside the Store transaction. These cases call the Store directly, without
// the application's decision in front, so the recheck alone must refuse what
// the application refuses: a Role Binding at a scope the Action's definition
// does not inherit from, and a Personal Access Token used outside its
// Academic Unit ceiling.

// testCommandAuthorityFollowsActionInheritance delegates Roles in Role
// Binding batch commands. user.manage inherits only Institution scope, and
// exam.manage accepts no Class resource, so an actor's Academic Unit or Class
// binding cannot hand either on (access.Authorizer.CanDelegateActionsAtScope).
func testCommandAuthorityFollowsActionInheritance(t *testing.T, ss store.Store) {
	ctx := context.Background()
	institution := saveInstitution(t, ctx, ss)
	suffix := model.NewID()
	parent := saveAcademicUnit(t, ctx, ss, institution.ID.String(), "", "inheritance-parent-"+suffix)
	child := saveAcademicUnit(t, ctx, ss, institution.ID.String(), parent.ID.String(), "inheritance-child-"+suffix)
	programme := saveProgramme(t, ctx, ss, child.ID.String(), "inheritance-programme-"+suffix)
	level := saveProgrammeLevel(t, ctx, ss, programme.ID.String(), "inheritance-level-"+suffix)
	period := saveAcademicPeriod(t, ctx, ss, institution.ID.String(), "inheritance-period-"+suffix, 1_800_000_000_000)
	class := saveClass(t, ctx, ss, level.ID.String(), period.ID.String(), "inheritance-class-"+suffix)
	actor := saveUser(t, ctx, ss)
	principal := commandAuthoritySessionPrincipal(t, ctx, ss, actor)
	at := model.NowUTC().Add(-time.Minute)

	saveRole := func(name string, actions ...model.Action) *model.Role {
		permissions := make([]string, 0, len(actions))
		for _, action := range actions {
			permissions = append(permissions, string(action))
		}
		role, err := SaveRole(ctx, ss, &model.Role{Name: name + "-" + suffix, DisplayName: name, Permissions: permissions})
		require.NoError(t, err)
		return role
	}
	bind := func(role *model.Role, scopeType model.RoleScopeType, scopeID string) {
		_, err := SaveRoleBinding(ctx, ss, &model.RoleBinding{UserID: actor.ID, RoleID: role.ID, ScopeType: scopeType,
			ScopeID: scopeID, StartsAt: at.Add(-time.Second)})
		require.NoError(t, err)
	}
	delegate := func(key string, role *model.Role, scopeType model.RoleScopeType, scopeID string) error {
		grantee := saveUser(t, ctx, ss)
		audit, err := ss.Audit().Save(ctx, &model.AuditEvent{Action: string(model.ActionRoleBindingManage),
			Resource: model.Resource{Type: model.ResourceInstitution, ID: institution.ID.String()}, ScopeType: scopeType, ScopeID: scopeID,
			Status: model.AuditStatusAttempt, NodeID: "test-node"})
		require.NoError(t, err)
		delegated := make([]model.Action, 0, len(role.Permissions))
		for _, permission := range role.Permissions {
			delegated = append(delegated, model.Action(permission))
		}
		command := invitationTestCommand(actor.ID, "role_binding.create", key+"-"+suffix, key)
		command.Authorization = &store.CommandAuthorization{Principal: principal, ScopeType: scopeType, ScopeID: scopeID,
			Actions:          []model.Action{model.ActionOnboardingBatchManage, model.ActionRoleBindingManage},
			DelegatedActions: delegated}
		_, err = ss.RoleBinding().Save(ctx, &store.RoleBindingCreation{
			Binding:               &model.RoleBinding{UserID: grantee.ID, RoleID: role.ID, ScopeType: scopeType, ScopeID: scopeID, StartsAt: at},
			ExpectedRoleUpdatedAt: role.UpdatedAt, ExpectedRolePermissions: append([]string(nil), role.Permissions...),
			AuditEventID: audit.ID, AuditAt: at, Command: command,
		})
		return err
	}

	// The actor administers Role Bindings from the parent unit and from the
	// Class, and also holds user.manage at the unit and exam.manage at the
	// Class, where neither Action can be granted.
	bind(saveRole("inheritance-unit-authority", model.ActionOnboardingBatchManage, model.ActionRoleBindingManage,
		model.ActionUserManage, model.ActionAcademicUnitView), model.RoleScopeAcademicUnit, parent.ID.String())
	bind(saveRole("inheritance-class-authority", model.ActionOnboardingBatchManage, model.ActionRoleBindingManage,
		model.ActionExamManage, model.ActionClassView), model.RoleScopeClass, class.ID.String())
	userManager := saveRole("inheritance-user-manager", model.ActionUserManage)
	examManager := saveRole("inheritance-exam-manager", model.ActionExamManage)
	unitViewer := saveRole("inheritance-unit-viewer", model.ActionAcademicUnitView)
	classViewer := saveRole("inheritance-class-viewer", model.ActionClassView)

	t.Run("AcademicUnitBindingDoesNotGrantInstitutionOnlyAction", func(t *testing.T) {
		requireCommandAuthorityRefused(t, delegate("unit-user-manage", userManager, model.RoleScopeAcademicUnit, child.ID.String()))
	})
	t.Run("AcademicUnitBindingAboveClassDoesNotGrantInstitutionOnlyAction", func(t *testing.T) {
		requireCommandAuthorityRefused(t, delegate("class-user-manage", userManager, model.RoleScopeClass, class.ID.String()))
	})
	t.Run("ClassBindingDoesNotGrantActionWithoutClassResource", func(t *testing.T) {
		requireCommandAuthorityRefused(t, delegate("class-exam-manage", examManager, model.RoleScopeClass, class.ID.String()))
	})
	t.Run("AcademicUnitBindingGrantsInheritingAction", func(t *testing.T) {
		require.NoError(t, delegate("unit-unit-view", unitViewer, model.RoleScopeAcademicUnit, child.ID.String()))
	})
	t.Run("ClassBindingGrantsClassAction", func(t *testing.T) {
		require.NoError(t, delegate("class-class-view", classViewer, model.RoleScopeClass, class.ID.String()))
	})
	t.Run("InstitutionBindingGrantsInstitutionOnlyAction", func(t *testing.T) {
		bind(saveRole("inheritance-institution-user-manager", model.ActionUserManage), model.RoleScopeInstitution, institution.ID.String())
		require.NoError(t, delegate("institution-unit-user-manage", userManager, model.RoleScopeAcademicUnit, child.ID.String()))
		require.NoError(t, delegate("institution-class-user-manage", userManager, model.RoleScopeClass, class.ID.String()))
	})
}

// testCommandAuthorityAppliesTokenCeiling runs Academic Unit membership and
// Affiliation batch commands under Personal Access Tokens. A token with an
// Academic Unit ceiling acts only on resources within that unit
// (access.personalAccessTokenAllows): never on the Institution, never on a
// unit outside it, whichever unit the User's binding is at.
func testCommandAuthorityAppliesTokenCeiling(t *testing.T, ss store.Store) {
	ctx := context.Background()
	institution := saveInstitution(t, ctx, ss)
	suffix := model.NewID()
	root := saveAcademicUnit(t, ctx, ss, institution.ID.String(), "", "ceiling-root-"+suffix)
	ceiling := saveAcademicUnit(t, ctx, ss, institution.ID.String(), root.ID.String(), "ceiling-"+suffix)
	inside := saveAcademicUnit(t, ctx, ss, institution.ID.String(), ceiling.ID.String(), "ceiling-inside-"+suffix)
	outside := saveAcademicUnit(t, ctx, ss, institution.ID.String(), root.ID.String(), "ceiling-outside-"+suffix)
	at := model.NowUTC().Add(-time.Minute)
	memberActions := []model.Action{model.ActionOnboardingBatchManage, model.ActionAcademicUnitMembersManage}
	affiliationActions := []model.Action{model.ActionOnboardingBatchManage, model.ActionUserManage}

	actorWith := func(name string, actions []model.Action, scopeType model.RoleScopeType, scopeID string) *model.User {
		actor, _ := saveLocalUser(t, ctx, ss)
		permissions := make([]string, 0, len(actions))
		for _, action := range actions {
			permissions = append(permissions, string(action))
		}
		role, err := SaveRole(ctx, ss, &model.Role{Name: name + "-" + model.NewID(), DisplayName: name, Permissions: permissions})
		require.NoError(t, err)
		_, err = SaveRoleBinding(ctx, ss, &model.RoleBinding{UserID: actor.ID, RoleID: role.ID, ScopeType: scopeType,
			ScopeID: scopeID, StartsAt: at.Add(-time.Second)})
		require.NoError(t, err)
		return actor
	}
	addMember := func(key string, principal model.Principal, unit *model.AcademicUnit) error {
		member := saveUser(t, ctx, ss)
		candidate := &model.AcademicUnitMember{AcademicUnitID: unit.ID, UserID: member.ID, StartsAt: at}
		candidate.PrepareCreate(model.NewAcademicUnitMemberID(), at)
		audit := saveAcademicUnitAuditAttempt(t, ctx, ss, unit.ID.String())
		command := invitationTestCommand(principal.UserID, "academic_unit_member.add", key+"-"+suffix, key)
		command.Authorization = &store.CommandAuthorization{Principal: principal, ScopeType: model.RoleScopeAcademicUnit,
			ScopeID: unit.ID.String(), Actions: memberActions}
		_, err := ss.AcademicUnitMember().Create(ctx, &store.AcademicUnitMemberCreation{Member: candidate,
			ExpectedRecipientRevision: member.Revision, AuditEventID: audit.ID, AuditAt: at, Command: command})
		return err
	}
	addAffiliation := func(key string, principal model.Principal) error {
		target := saveUser(t, ctx, ss)
		candidate := &model.Affiliation{UserID: target.ID, Kind: model.AffiliationStaff, StartsAt: at}
		candidate.PrepareCreate(model.NewAffiliationID(), at)
		audit := saveAffiliationAuditAttempt(t, ctx, ss, institution.ID.String(), target.ID.String())
		command := invitationTestCommand(principal.UserID, "affiliation.add", key+"-"+suffix, key)
		command.Authorization = &store.CommandAuthorization{Principal: principal, ScopeType: model.RoleScopeInstitution,
			ScopeID: institution.ID.String(), Actions: affiliationActions}
		_, err := ss.Affiliation().Create(ctx, &store.AffiliationCreation{Affiliation: candidate, AuditEventID: audit.ID,
			AuditAt: at, Command: command})
		return err
	}

	t.Run("BindingOutsideCeilingDoesNotReachOutside", func(t *testing.T) {
		actor := actorWith("ceiling-outside-member-manager", memberActions, model.RoleScopeAcademicUnit, outside.ID.String())
		token := commandAuthorityTokenPrincipal(t, ctx, ss, actor, memberActions, ceiling.ID)
		requireCommandAuthorityRefused(t, addMember("outside-binding-outside-unit", token, outside))
	})
	rootActor := actorWith("ceiling-root-member-manager", memberActions, model.RoleScopeAcademicUnit, root.ID.String())
	rootToken := commandAuthorityTokenPrincipal(t, ctx, ss, rootActor, memberActions, ceiling.ID)
	t.Run("BindingAboveCeilingDoesNotReachOutside", func(t *testing.T) {
		requireCommandAuthorityRefused(t, addMember("root-binding-outside-unit", rootToken, outside))
	})
	t.Run("BindingAboveCeilingReachesWithin", func(t *testing.T) {
		require.NoError(t, addMember("root-binding-inside-unit", rootToken, inside))
	})
	t.Run("BindingWithinCeilingReachesWithin", func(t *testing.T) {
		actor := actorWith("ceiling-member-manager", memberActions, model.RoleScopeAcademicUnit, ceiling.ID.String())
		token := commandAuthorityTokenPrincipal(t, ctx, ss, actor, memberActions, ceiling.ID)
		require.NoError(t, addMember("ceiling-binding-ceiling-unit", token, ceiling))
		require.NoError(t, addMember("ceiling-binding-inside-unit", token, inside))
	})
	t.Run("CeilingNeverReachesInstitution", func(t *testing.T) {
		actor := actorWith("ceiling-institution-administrator", affiliationActions, model.RoleScopeInstitution, institution.ID.String())
		bounded := commandAuthorityTokenPrincipal(t, ctx, ss, actor, affiliationActions, ceiling.ID)
		requireCommandAuthorityRefused(t, addAffiliation("bounded-token-institution", bounded))
		unbounded := commandAuthorityTokenPrincipal(t, ctx, ss, actor, affiliationActions, "")
		require.NoError(t, addAffiliation("unbounded-token-institution", unbounded), "token without a ceiling")
	})
}

// requireCommandAuthorityRefused requires the error the recheck returns when
// no current Role Binding grants an Action.
func requireCommandAuthorityRefused(t *testing.T, err error) {
	t.Helper()
	var conflict *store.ErrConflict
	require.ErrorAs(t, err, &conflict)
	require.Equal(t, store.NewErrConflict("authorization", "authority", nil), conflict)
}

func commandAuthoritySessionPrincipal(t *testing.T, ctx context.Context, ss store.Store, user *model.User) model.Principal {
	t.Helper()
	session, credentials, _ := saveSession(t, ctx, ss, user.ID.String(), 10)
	return model.Principal{UserID: user.ID, SessionID: session.ID, CredentialID: model.PrincipalCredentialID(credentials[0].ID),
		CredentialType: model.CredentialSessionAccess, AuthenticationMethod: "password", AuthenticationStrength: model.AuthenticationSingleFactor,
		ClientType: session.ClientType, AuthenticatedAt: session.AuthenticatedAt}
}

// commandAuthorityTokenPrincipal creates a Personal Access Token listing the
// Actions, with the Academic Unit ceiling unless it is empty, and returns the
// principal authentication would build from it.
func commandAuthorityTokenPrincipal(
	t *testing.T,
	ctx context.Context,
	ss store.Store,
	user *model.User,
	actions []model.Action,
	ceiling model.AcademicUnitID,
) model.Principal {
	t.Helper()
	token := newPersonalAccessToken(user.ID.String(), model.NewCredentialToken())
	token.Scopes = make([]string, 0, len(actions))
	for _, action := range actions {
		token.Scopes = append(token.Scopes, string(action))
	}
	token.AcademicUnitID = ceiling
	token, err := createPersonalAccessTokenWithNotice(t, ctx, ss, token, 10)
	require.NoError(t, err)
	return model.Principal{UserID: user.ID, CredentialID: model.PrincipalCredentialID(token.ID),
		CredentialType: model.CredentialPersonalAccessToken, AuthenticationMethod: "personal_access_token",
		ClientType: model.SessionClientCLI, CredentialScopes: append([]string(nil), token.Scopes...), AcademicUnitID: token.AcademicUnitID}
}
