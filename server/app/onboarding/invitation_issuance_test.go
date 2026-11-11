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
	"github.com/sudosylabs/proctor/server/app/apperror"
	"github.com/sudosylabs/proctor/server/app/invocation"
	"github.com/sudosylabs/proctor/server/app/realtime/realtimetest"
	"github.com/sudosylabs/proctor/server/model"
)

func TestInvitationServiceIssuesTeacherPackageThroughDelegationCeiling(t *testing.T) {
	now := model.TimeFromMillis(1_800_000_000_000)
	unitID, roleID, inviterID := model.NewAcademicUnitID(), model.NewRoleID(), model.NewUserID()
	role := &model.Role{ID: roleID, Name: "teacher", DisplayName: "Teacher",
		Permissions: []string{string(model.ActionProgrammeManage), string(model.ActionAcademicUnitView), string(model.ActionExamManage)}}
	persistence := &invitationStoreFake{}
	authorizer := &authorizerFake{}
	composer := &mailFake{}
	service := newInvitationServiceForTest(t, persistence, invitationAcademicUnitStoreFake{unit: &model.AcademicUnit{ID: unitID}},
		invitationRoleStoreFake{role: role}, authorizer, composer, now)
	view, err := service.IssueTeacherAcademicUnitInvitation(context.Background(),
		invocation.New(model.Principal{UserID: inviterID}, model.RequestMetadata{}),
		IssueTeacherAcademicUnitInvitationCommand{TargetEmail: "teacher@example.edu", AcademicUnitID: unitID,
			RoleID: roleID, IntendedStartsAt: model.MillisFromTime(now.Add(time.Hour))})
	require.NoError(t, err)
	require.Equal(t, unitID, view.AcademicUnitID, "teacher issue view/input = %#v", view)
	require.Equal(t, roleID, view.RoleID, "teacher issue view/input = %#v", view)
	require.NotNil(t, persistence.teacherIssued, "teacher issue view/input = %#v", view)
	require.Equal(t, model.InvitationLifetime, persistence.teacherIssued.Lifetime, "teacher issue view/input = %#v", view)
	require.True(t, slices.Equal(persistence.teacherIssued.Invitation.RoleActions, []string{string(model.ActionAcademicUnitView), string(model.ActionExamManage), string(model.ActionProgrammeManage)}), "teacher issue view/input = %#v", view)
	require.True(t, slices.Equal(authorizer.actions, []model.Action{model.ActionInvitationCreate, model.ActionAcademicUnitMembersManage}), "teacher issue authorization = %v / %v", authorizer.actions, authorizer.delegatedActions)
	require.True(t, slices.Equal(authorizer.delegatedActions, role.Permissions), "teacher issue authorization = %v / %v", authorizer.actions, authorizer.delegatedActions)
	require.Equal(t, model.RoleScopeAcademicUnit, authorizer.delegatedScopeType, "teacher issue authorization = %v / %v", authorizer.actions, authorizer.delegatedActions)
	require.Equal(t, unitID.String(), authorizer.delegatedScopeID, "teacher issue authorization = %v / %v", authorizer.actions, authorizer.delegatedActions)
}

func TestInvitationServiceIssuesAcademicUnitRoleForExistingUser(t *testing.T) {
	now := model.TimeFromMillis(1_800_000_000_000)
	unitID, roleID, inviterID := model.NewAcademicUnitID(), model.NewRoleID(), model.NewUserID()
	role := &model.Role{ID: roleID, Name: "unit-reviewer", DisplayName: "Unit Reviewer",
		Permissions: []string{string(model.ActionProgrammeManage), string(model.ActionAcademicUnitView)}}
	persistence := &invitationStoreFake{}
	authorizer := &authorizerFake{}
	service := newInvitationServiceForTest(t, persistence, invitationAcademicUnitStoreFake{unit: &model.AcademicUnit{ID: unitID}},
		invitationRoleStoreFake{role: role}, authorizer, &mailFake{}, now)
	view, err := service.IssueAcademicUnitRoleInvitation(context.Background(),
		invocation.New(model.Principal{UserID: inviterID}, model.RequestMetadata{}),
		IssueAcademicUnitRoleInvitationCommand{TargetEmail: "existing@example.edu", AcademicUnitID: unitID,
			RoleID: roleID, IntendedStartsAt: model.MillisFromTime(now.Add(time.Hour))})
	require.NoError(t, err)
	require.Equal(t, model.InvitationPurposeAcademicUnitRole, view.Purpose, "academic-unit Role issue view/input = %#v", view)
	require.Equal(t, unitID, view.AcademicUnitID, "academic-unit Role issue view/input = %#v", view)
	require.Equal(t, roleID, view.RoleID, "academic-unit Role issue view/input = %#v", view)
	require.NotNil(t, persistence.scopedIssued, "academic-unit Role issue view/input = %#v", view)
	require.Equal(t, model.InvitationLifetime, persistence.scopedIssued.Lifetime, "academic-unit Role issue view/input = %#v", view)
	require.True(t, slices.Equal(authorizer.actions, []model.Action{model.ActionInvitationCreate, model.ActionRoleBindingManage}), "academic-unit Role issue authorization = %v / %v", authorizer.actions, authorizer.delegatedActions)
	require.True(t, slices.Equal(authorizer.delegatedActions, role.Permissions), "academic-unit Role issue authorization = %v / %v", authorizer.actions, authorizer.delegatedActions)
	require.Equal(t, model.RoleScopeAcademicUnit, authorizer.delegatedScopeType, "academic-unit Role issue authorization = %v / %v", authorizer.actions, authorizer.delegatedActions)
	require.Equal(t, unitID.String(), authorizer.delegatedScopeID, "academic-unit Role issue authorization = %v / %v", authorizer.actions, authorizer.delegatedActions)
}

func TestInvitationServiceInstitutionRoleRequiresStrongRecentInteractiveSession(t *testing.T) {
	now := model.TimeFromMillis(1_800_000_000_000)
	institutionID, roleID, inviterID := model.NewInstitutionID(), model.NewRoleID(), model.NewUserID()
	role := &model.Role{ID: roleID, Name: "institution-reviewer", DisplayName: "Institution Reviewer",
		Permissions: []string{string(model.ActionAuditView)}}
	persistence := &invitationStoreFake{}
	authorizer := &authorizerFake{}
	service := newInvitationServiceForTest(t, persistence, invitationAcademicUnitStoreFake{},
		invitationRoleStoreFake{role: role}, authorizer, &mailFake{}, now)
	principal := model.Principal{UserID: inviterID, SessionID: model.NewSessionID(), CredentialID: model.PrincipalCredentialID(model.NewID()),
		CredentialType: model.CredentialSessionAccess, AuthenticationMethod: "password", AuthenticationStrength: model.AuthenticationSingleFactor,
		ClientType: model.SessionClientWeb, AuthenticatedAt: now}
	command := IssueInstitutionRoleInvitationCommand{TargetEmail: "existing@example.edu", InstitutionID: institutionID, RoleID: roleID}
	_, err := service.IssueInstitutionRoleInvitation(context.Background(), invocation.New(principal, model.RequestMetadata{}), command)
	require.True(t, apperror.Is(err, "authentication.strong_required"), "weak IssueInstitutionRole() error = %v", err)
	principal.AuthenticationStrength = model.AuthenticationMultiFactor
	principal.MFACompletedAt = model.OptionalTimeFrom(now)
	view, err := service.IssueInstitutionRoleInvitation(context.Background(), invocation.New(principal, model.RequestMetadata{}), command)
	require.NoError(t, err)
	require.Equal(t, model.InvitationPurposeInstitutionRole, view.Purpose, "institution Role issue = %#v", view)
	require.Equal(t, roleID, view.RoleID, "institution Role issue = %#v", view)
	require.False(t, view.AcademicUnitID.IsValid(), "institution Role issue = %#v", view)
	require.NotNil(t, persistence.scopedIssued, "institution Role issue = %#v", view)
	require.Equal(t, model.RoleScopeInstitution, persistence.scopedIssued.Invitation.ScopeType, "institution Role issue = %#v", view)
	require.Equal(t, institutionID.String(), persistence.scopedIssued.Invitation.ScopeID, "institution Role issue = %#v", view)
	require.True(t, slices.Equal(authorizer.actions, []model.Action{model.ActionInvitationCreate, model.ActionRoleBindingManage}), "institution Role issue authorization = %v / %v", authorizer.actions, authorizer.delegatedActions)
	require.True(t, slices.Equal(authorizer.delegatedActions, role.Permissions), "institution Role issue authorization = %v / %v", authorizer.actions, authorizer.delegatedActions)
	require.Equal(t, model.RoleScopeInstitution, authorizer.delegatedScopeType, "institution Role issue authorization = %v / %v", authorizer.actions, authorizer.delegatedActions)
}

func TestInvitationServiceRejectsInertTeacherRoleAction(t *testing.T) {
	now := model.TimeFromMillis(1_800_000_000_000)
	unitID, roleID := model.NewAcademicUnitID(), model.NewRoleID()
	persistence := &invitationStoreFake{}
	service := newInvitationServiceForTest(t, persistence, invitationAcademicUnitStoreFake{unit: &model.AcademicUnit{ID: unitID}},
		invitationRoleStoreFake{role: &model.Role{ID: roleID, Name: "inert", DisplayName: "Inert", Permissions: []string{string(model.ActionRoleManage)}}},
		&authorizerFake{}, &mailFake{}, now)
	_, err := service.IssueTeacherAcademicUnitInvitation(context.Background(), invocation.New(model.Principal{UserID: model.NewUserID()}, model.RequestMetadata{}),
		IssueTeacherAcademicUnitInvitationCommand{TargetEmail: "teacher@example.edu", AcademicUnitID: unitID, RoleID: roleID})
	require.True(t, apperror.Is(err, "invitation.role_not_delegable"), "IssueTeacherAcademicUnit() = %v", err)
	require.Nil(t, persistence.teacherIssued, "IssueTeacherAcademicUnit() = %v", err)
}

func TestInvitationIssueAuthorizesBeforeInspectingMailCapability(t *testing.T) {
	now := model.TimeFromMillis(1_800_000_000_000)
	periodID, classID := model.NewAcademicPeriodID(), model.NewClassID()
	authorizer := &authorizerFake{err: apperror.New("authorization.denied")}
	composer := &mailFake{disabled: true}
	service := &Service{invitations: &invitationStoreFake{}, classes: invitationClassStoreFake{class: &model.Class{ID: classID, AcademicPeriodID: periodID}}, periods: invitationPeriodStoreFake{period: &model.AcademicPeriod{ID: periodID, StartsAt: now, EndsAt: now.Add(24 * time.Hour)}}, academicUnits: invitationAcademicUnitStoreFake{}, roles: invitationRoleStoreFake{}, authorizer: authorizer, mail: composer, hasher: passwordHasherFake{}, auditor: &mutationAttemptAuditorFake{}, attempts: testAttempts(t), rateLimit: testRateLimit, publisher: &realtimetest.Publisher{}, nodeID: "node-1", publicURL: "https://proctor.example.edu", recentAuthenticationTTL: 15 * time.Minute, newToken: model.NewCredentialToken, now: func() time.Time { return now }}
	_, appErr := service.IssueStudentClassInvitation(context.Background(), invocation.New(model.Principal{UserID: model.NewUserID()}, model.RequestMetadata{}),
		IssueStudentClassInvitationCommand{ClassID: classID, TargetEmail: "student@example.edu"})
	require.True(t, apperror.Is(appErr, "authorization.denied"), "IssueStudentClass() error/actions = %v", appErr)
	require.Len(t, authorizer.actions, 1, "IssueStudentClass() error/actions = %v", appErr)
	require.Equal(t, model.ActionInvitationCreate, authorizer.actions[0], "IssueStudentClass() error/actions = %v", appErr)
}
