// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package onboarding

import (
	"context"
	"slices"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/sudosylabs/proctor/server/app/access/accesstest"
	"github.com/sudosylabs/proctor/server/app/apperror"
	"github.com/sudosylabs/proctor/server/app/invocation"
	"github.com/sudosylabs/proctor/server/model"
)

func TestInvitationServiceIssuesTeacherPackageThroughDelegationCeiling(t *testing.T) {
	now := model.TimeFromMillis(1_800_000_000_000)
	unitID, roleID, inviterID := model.NewAcademicUnitID(), model.NewRoleID(), model.NewUserID()
	role := &model.Role{ID: roleID, Name: "teacher", DisplayName: "Teacher",
		Permissions: []string{string(model.ActionProgrammeManage), string(model.ActionAcademicUnitView), string(model.ActionExamManage)}}
	issue := func(authorization *accesstest.Fixture) (*invitationStoreFake, InvitationView, error) {
		persistence := &invitationStoreFake{}
		composer, _ := newTestMail(t, true)
		service := newInvitationServiceForTest(t, persistence, invitationAcademicUnitStoreFake{unit: &model.AcademicUnit{ID: unitID}},
			invitationRoleStoreFake{role: role}, authorization, composer, now)
		view, err := service.IssueTeacherAcademicUnitInvitation(context.Background(), sessionCaller(inviterID, now),
			IssueTeacherAcademicUnitInvitationCommand{TargetEmail: "teacher@example.edu", AcademicUnitID: unitID,
				RoleID: roleID, IntendedStartsAt: model.MillisFromTime(now.Add(time.Hour))})
		return persistence, view, err
	}
	// The inviter may delegate the Role's permissions only where it holds
	// them itself: at the target Academic Unit, not at another one.
	otherUnitID := model.NewAcademicUnitID()
	_, _, err := issue(delegationAccess(t, inviterID, []model.Action{model.ActionInvitationCreate, model.ActionAcademicUnitMembersManage},
		role, model.RoleScopeAcademicUnit, otherUnitID.String(), unitID, otherUnitID))
	require.True(t, apperror.Is(err, "authorization.denied"), "teacher issue beyond the delegation ceiling = %v", err)
	authorization := delegationAccess(t, inviterID, []model.Action{model.ActionInvitationCreate, model.ActionAcademicUnitMembersManage},
		role, model.RoleScopeAcademicUnit, unitID.String(), unitID)
	persistence, view, err := issue(authorization)
	require.NoError(t, err)
	require.Equal(t, unitID, view.AcademicUnitID, "teacher issue view/input = %#v", view)
	require.Equal(t, roleID, view.RoleID, "teacher issue view/input = %#v", view)
	require.NotNil(t, persistence.teacherIssued, "teacher issue view/input = %#v", view)
	require.Equal(t, model.InvitationLifetime, persistence.teacherIssued.Lifetime, "teacher issue view/input = %#v", view)
	require.True(t, slices.Equal(persistence.teacherIssued.Invitation.RoleActions, []string{string(model.ActionAcademicUnitView), string(model.ActionExamManage), string(model.ActionProgrammeManage)}), "teacher issue view/input = %#v", view)
	require.Equal(t, []model.Action{model.ActionInvitationCreate, model.ActionAcademicUnitMembersManage}, decidedActions(authorization), "teacher issue authorization")
}

// delegationAccess grants the inviter the issuing Actions Institution-wide
// and the Role's permissions only at the scope given, so issuing succeeds
// exactly when the delegation ceiling is checked at that scope.
func delegationAccess(t *testing.T, inviterID model.UserID, issuing []model.Action, role *model.Role,
	scopeType model.RoleScopeType, scopeID string, units ...model.AcademicUnitID,
) *accesstest.Fixture {
	t.Helper()
	permissions := make([]model.Action, 0, len(role.Permissions))
	for _, permission := range role.Permissions {
		permissions = append(permissions, model.Action(permission))
	}
	return permissiveAccess(t, accesstest.Options{Grants: []accesstest.Grant{
		{UserID: inviterID, Actions: issuing},
		{UserID: inviterID, Actions: permissions, ScopeType: scopeType, ScopeID: scopeID},
	}}, nil, units...)
}

func TestInvitationServiceIssuesAcademicUnitRoleForExistingUser(t *testing.T) {
	now := model.TimeFromMillis(1_800_000_000_000)
	unitID, roleID, inviterID := model.NewAcademicUnitID(), model.NewRoleID(), model.NewUserID()
	role := &model.Role{ID: roleID, Name: "unit-reviewer", DisplayName: "Unit Reviewer",
		Permissions: []string{string(model.ActionProgrammeManage), string(model.ActionAcademicUnitView)}}
	issue := func(authorization *accesstest.Fixture) (*invitationStoreFake, InvitationView, error) {
		persistence := &invitationStoreFake{}
		composer, _ := newTestMail(t, true)
		service := newInvitationServiceForTest(t, persistence, invitationAcademicUnitStoreFake{unit: &model.AcademicUnit{ID: unitID}},
			invitationRoleStoreFake{role: role}, authorization, composer, now)
		view, err := service.IssueAcademicUnitRoleInvitation(context.Background(), sessionCaller(inviterID, now),
			IssueAcademicUnitRoleInvitationCommand{TargetEmail: "existing@example.edu", AcademicUnitID: unitID,
				RoleID: roleID, IntendedStartsAt: model.MillisFromTime(now.Add(time.Hour))})
		return persistence, view, err
	}
	otherUnitID := model.NewAcademicUnitID()
	_, _, err := issue(delegationAccess(t, inviterID, []model.Action{model.ActionInvitationCreate, model.ActionRoleBindingManage},
		role, model.RoleScopeAcademicUnit, otherUnitID.String(), unitID, otherUnitID))
	require.True(t, apperror.Is(err, "authorization.denied"), "academic-unit Role issue beyond the delegation ceiling = %v", err)
	authorization := delegationAccess(t, inviterID, []model.Action{model.ActionInvitationCreate, model.ActionRoleBindingManage},
		role, model.RoleScopeAcademicUnit, unitID.String(), unitID)
	persistence, view, err := issue(authorization)
	require.NoError(t, err)
	require.Equal(t, model.InvitationPurposeAcademicUnitRole, view.Purpose, "academic-unit Role issue view/input = %#v", view)
	require.Equal(t, unitID, view.AcademicUnitID, "academic-unit Role issue view/input = %#v", view)
	require.Equal(t, roleID, view.RoleID, "academic-unit Role issue view/input = %#v", view)
	require.NotNil(t, persistence.scopedIssued, "academic-unit Role issue view/input = %#v", view)
	require.Equal(t, model.InvitationLifetime, persistence.scopedIssued.Lifetime, "academic-unit Role issue view/input = %#v", view)
	require.Equal(t, []model.Action{model.ActionInvitationCreate, model.ActionRoleBindingManage}, decidedActions(authorization), "academic-unit Role issue authorization")
}

func TestInvitationServiceInstitutionRoleRequiresStrongRecentInteractiveSession(t *testing.T) {
	now := model.TimeFromMillis(1_800_000_000_000)
	institutionID, roleID, inviterID := model.NewInstitutionID(), model.NewRoleID(), model.NewUserID()
	role := &model.Role{ID: roleID, Name: "institution-reviewer", DisplayName: "Institution Reviewer",
		Permissions: []string{string(model.ActionAuditView)}}
	persistence := &invitationStoreFake{}
	// The inviter holds every Action but the Role's audit.view, so the
	// Institution Role is beyond its delegation ceiling until it is granted.
	ceiling := permissiveAccess(t, accesstest.Options{Grants: []accesstest.Grant{{UserID: inviterID, Actions: allActionsExcept(model.ActionAuditView)}}}, nil)
	composer, _ := newTestMail(t, true)
	service := newInvitationServiceForTest(t, persistence, invitationAcademicUnitStoreFake{},
		invitationRoleStoreFake{role: role}, ceiling, composer, now)
	principal := model.Principal{UserID: inviterID, SessionID: model.NewSessionID(), CredentialID: model.PrincipalCredentialID(model.NewID()),
		CredentialType: model.CredentialSessionAccess, AuthenticationMethod: "password", AuthenticationStrength: model.AuthenticationSingleFactor,
		ClientType: model.SessionClientWeb, AuthenticatedAt: now}
	command := IssueInstitutionRoleInvitationCommand{TargetEmail: "existing@example.edu", InstitutionID: institutionID, RoleID: roleID}
	_, err := service.IssueInstitutionRoleInvitation(context.Background(), invocation.New(principal, model.RequestMetadata{}), command)
	require.True(t, apperror.Is(err, "authentication.strong_required"), "weak IssueInstitutionRole() error = %v", err)
	principal.AuthenticationStrength = model.AuthenticationMultiFactor
	principal.MFACompletedAt = model.OptionalTimeFrom(now)
	_, err = service.IssueInstitutionRoleInvitation(context.Background(), invocation.New(principal, model.RequestMetadata{}), command)
	require.True(t, apperror.Is(err, "authorization.denied"), "institution Role issue beyond the delegation ceiling = %v", err)
	authorization := permissiveAccess(t, accesstest.Options{}, []model.UserID{inviterID})
	service.authorizer, service.auditor = authorization.Authorizer, authorization.Auditor.MutationAudit()
	view, err := service.IssueInstitutionRoleInvitation(context.Background(), invocation.New(principal, model.RequestMetadata{}), command)
	require.NoError(t, err)
	require.Equal(t, model.InvitationPurposeInstitutionRole, view.Purpose, "institution Role issue = %#v", view)
	require.Equal(t, roleID, view.RoleID, "institution Role issue = %#v", view)
	require.False(t, view.AcademicUnitID.IsValid(), "institution Role issue = %#v", view)
	require.NotNil(t, persistence.scopedIssued, "institution Role issue = %#v", view)
	require.Equal(t, model.RoleScopeInstitution, persistence.scopedIssued.Invitation.ScopeType, "institution Role issue = %#v", view)
	require.Equal(t, institutionID.String(), persistence.scopedIssued.Invitation.ScopeID, "institution Role issue = %#v", view)
	require.Equal(t, []model.Action{model.ActionInvitationCreate, model.ActionRoleBindingManage}, decidedActions(authorization), "institution Role issue authorization")
}

func TestInvitationServiceRejectsInertTeacherRoleAction(t *testing.T) {
	now := model.TimeFromMillis(1_800_000_000_000)
	unitID, roleID, inviterID := model.NewAcademicUnitID(), model.NewRoleID(), model.NewUserID()
	persistence := &invitationStoreFake{}
	service := newInvitationServiceForTest(t, persistence, invitationAcademicUnitStoreFake{unit: &model.AcademicUnit{ID: unitID}},
		invitationRoleStoreFake{role: &model.Role{ID: roleID, Name: "inert", DisplayName: "Inert", Permissions: []string{string(model.ActionRoleManage)}}},
		permissiveAccess(t, accesstest.Options{}, []model.UserID{inviterID}, unitID), newTestComposer(t), now)
	_, err := service.IssueTeacherAcademicUnitInvitation(context.Background(), sessionCaller(inviterID, now),
		IssueTeacherAcademicUnitInvitationCommand{TargetEmail: "teacher@example.edu", AcademicUnitID: unitID, RoleID: roleID})
	require.True(t, apperror.Is(err, "invitation.role_not_delegable"), "IssueTeacherAcademicUnit() = %v", err)
	require.Nil(t, persistence.teacherIssued, "IssueTeacherAcademicUnit() = %v", err)
}

func TestInvitationIssueAuthorizesBeforeInspectingMailCapability(t *testing.T) {
	now := model.TimeFromMillis(1_800_000_000_000)
	periodID, classID := model.NewAcademicPeriodID(), model.NewClassID()
	// The caller holds no grant, so the first decision refuses.
	authorization := permissiveAccess(t, accesstest.Options{}, nil)
	composer, _ := newTestMail(t, false)
	service := newInvitationServiceForTest(t, &invitationStoreFake{}, invitationAcademicUnitStoreFake{}, invitationRoleStoreFake{}, authorization, composer, now)
	service.classes = invitationClassStoreFake{class: &model.Class{ID: classID, AcademicPeriodID: periodID}}
	service.periods = invitationPeriodStoreFake{period: &model.AcademicPeriod{ID: periodID, StartsAt: now, EndsAt: now.Add(24 * time.Hour)}}
	_, appErr := service.IssueStudentClassInvitation(context.Background(), sessionCaller(model.NewUserID(), now),
		IssueStudentClassInvitationCommand{ClassID: classID, TargetEmail: "student@example.edu"})
	require.True(t, apperror.Is(appErr, "authorization.denied"), "IssueStudentClass() error/actions = %v", appErr)
	require.Equal(t, []model.Action{model.ActionInvitationCreate}, decidedActions(authorization), "IssueStudentClass() error/actions = %v", appErr)
}
