// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package onboarding

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/sudosylabs/proctor/server/app/apperror"
	"github.com/sudosylabs/proctor/server/app/invocation"
	"github.com/sudosylabs/proctor/server/model"
)

const invitationTestDay = 24 * time.Hour

func invitationDays(days int) *int { return &days }

func TestInvitationIssueUsesTheChosenLifetime(t *testing.T) {
	t.Parallel()
	now := model.TimeFromMillis(1_800_000_000_000)
	for name, test := range map[string]struct {
		days *int
		want time.Duration
	}{
		"default":     {want: model.InvitationLifetime},
		"one day":     {days: invitationDays(1), want: invitationTestDay},
		"thirty days": {days: invitationDays(30), want: 30 * invitationTestDay},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			periodID, classID := model.NewAcademicPeriodID(), model.NewClassID()
			persistence := &invitationStoreFake{}
			service := newInvitationServiceForTest(t, persistence, invitationAcademicUnitStoreFake{}, invitationRoleStoreFake{},
				&invitationAuthorizerFake{}, &invitationMailPreparerFake{}, now)
			service.deps.Classes = invitationClassStoreFake{class: &model.Class{ID: classID, AcademicPeriodID: periodID}}
			service.deps.Periods = invitationPeriodStoreFake{period: &model.AcademicPeriod{ID: periodID, StartsAt: now.Add(time.Hour), EndsAt: now.Add(90 * invitationTestDay)}}
			_, err := service.IssueStudentClassInvitation(context.Background(), invocation.New(model.Principal{UserID: model.NewUserID()}, model.RequestMetadata{}),
				IssueStudentClassInvitationCommand{TargetEmail: "student@example.edu", ClassID: classID, ExpiresInDays: test.days})
			require.NoError(t, err)
			require.NotNil(t, persistence.issued)
			require.Equal(t, test.want, persistence.issued.Invitation.ExpiresAt.Sub(persistence.issued.Invitation.CreatedAt))

			unitID, roleID := model.NewAcademicUnitID(), model.NewRoleID()
			role := &model.Role{ID: roleID, Name: "unit-reviewer", DisplayName: "Unit Reviewer",
				Permissions: []string{string(model.ActionProgrammeManage), string(model.ActionAcademicUnitView)}}
			service = newInvitationServiceForTest(t, persistence, invitationAcademicUnitStoreFake{unit: &model.AcademicUnit{ID: unitID}},
				invitationRoleStoreFake{role: role}, &invitationAuthorizerFake{}, &invitationMailPreparerFake{}, now)
			_, err = service.IssueTeacherAcademicUnitInvitation(context.Background(), invocation.New(model.Principal{UserID: model.NewUserID()}, model.RequestMetadata{}),
				IssueTeacherAcademicUnitInvitationCommand{TargetEmail: "teacher@example.edu", AcademicUnitID: unitID, RoleID: roleID, ExpiresInDays: test.days})
			require.NoError(t, err)
			require.NotNil(t, persistence.teacherIssued)
			require.Equal(t, test.want, persistence.teacherIssued.Lifetime)
			require.Equal(t, test.want, persistence.teacherIssued.Invitation.ExpiresAt.Sub(persistence.teacherIssued.Invitation.CreatedAt))
			_, err = service.IssueAcademicUnitRoleInvitation(context.Background(), invocation.New(model.Principal{UserID: model.NewUserID()}, model.RequestMetadata{}),
				IssueAcademicUnitRoleInvitationCommand{TargetEmail: "reviewer@example.edu", AcademicUnitID: unitID, RoleID: roleID, ExpiresInDays: test.days})
			require.NoError(t, err)
			require.NotNil(t, persistence.scopedIssued)
			require.Equal(t, test.want, persistence.scopedIssued.Lifetime)
			require.Equal(t, test.want, persistence.scopedIssued.Invitation.ExpiresAt.Sub(persistence.scopedIssued.Invitation.CreatedAt))
		})
	}
}

func TestInvitationIssueRejectsALifetimeOutsideOneThroughThirtyDays(t *testing.T) {
	t.Parallel()
	now := model.TimeFromMillis(1_800_000_000_000)
	for name, days := range map[string]int{"zero": 0, "negative": -1, "thirty-one": 31} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			unitID, roleID := model.NewAcademicUnitID(), model.NewRoleID()
			role := &model.Role{ID: roleID, Name: "unit-reviewer", DisplayName: "Unit Reviewer", Permissions: []string{string(model.ActionAcademicUnitView)}}
			persistence := &invitationStoreFake{}
			authorizer := &invitationAuthorizerFake{}
			service := newInvitationServiceForTest(t, persistence, invitationAcademicUnitStoreFake{unit: &model.AcademicUnit{ID: unitID}},
				invitationRoleStoreFake{role: role}, authorizer, &invitationMailPreparerFake{}, now)
			call := invocation.New(model.Principal{UserID: model.NewUserID()}, model.RequestMetadata{})
			_, err := service.IssueStudentClassInvitation(context.Background(), call,
				IssueStudentClassInvitationCommand{TargetEmail: "student@example.edu", ClassID: model.NewClassID(), ExpiresInDays: invitationDays(days)})
			require.True(t, apperror.Is(err, "invitation.invalid"), "student error = %v", err)
			_, err = service.IssueTeacherAcademicUnitInvitation(context.Background(), call,
				IssueTeacherAcademicUnitInvitationCommand{TargetEmail: "teacher@example.edu", AcademicUnitID: unitID, RoleID: roleID, ExpiresInDays: invitationDays(days)})
			require.True(t, apperror.Is(err, "invitation.invalid"), "teacher error = %v", err)
			_, err = service.IssueAcademicUnitRoleInvitation(context.Background(), call,
				IssueAcademicUnitRoleInvitationCommand{TargetEmail: "reviewer@example.edu", AcademicUnitID: unitID, RoleID: roleID, ExpiresInDays: invitationDays(days)})
			require.True(t, apperror.Is(err, "invitation.invalid"), "academic unit role error = %v", err)
			_, err = service.IssueInstitutionRoleInvitation(context.Background(), call,
				IssueInstitutionRoleInvitationCommand{TargetEmail: "admin@example.edu", InstitutionID: model.NewInstitutionID(), RoleID: roleID, ExpiresInDays: invitationDays(days)})
			require.Error(t, err)
			require.Empty(t, authorizer.actions, "an invalid lifetime is refused before any authorization")
			require.Nil(t, persistence.issued)
			require.Nil(t, persistence.teacherIssued)
			require.Nil(t, persistence.scopedIssued)
		})
	}
}

func TestInvitationReplacementKeepsTheReplacedLifetimeByDefault(t *testing.T) {
	t.Parallel()
	now := model.TimeFromMillis(1_800_000_000_000)
	for name, test := range map[string]struct {
		current time.Duration
		days    *int
		want    time.Duration
		code    string
	}{
		"keeps three days":      {current: 3 * invitationTestDay, want: 3 * invitationTestDay},
		"keeps thirty days":     {current: 30 * invitationTestDay, want: 30 * invitationTestDay},
		"explicit ten days":     {current: 3 * invitationTestDay, days: invitationDays(10), want: 10 * invitationTestDay},
		"rejects zero days":     {current: 3 * invitationTestDay, days: invitationDays(0), code: "invitation.invalid"},
		"rejects 31 days":       {current: 3 * invitationTestDay, days: invitationDays(31), code: "invitation.invalid"},
		"rounds to whole days":  {current: 2*invitationTestDay + 13*time.Hour, want: 3 * invitationTestDay},
		"default stays bounded": {current: invitationTestDay, want: invitationTestDay},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			period := &model.AcademicPeriod{ID: model.NewAcademicPeriodID(), StartsAt: now.Add(invitationTestDay), EndsAt: now.Add(180 * invitationTestDay)}
			currentClassID := model.NewClassID()
			class := &model.Class{ID: model.NewClassID(), AcademicPeriodID: period.ID}
			current, err := model.NewStudentClassInvitation(model.StudentClassInvitationInput{ID: model.NewInvitationID(),
				TargetEmail: "old@example.edu", ClassID: currentClassID, AcademicPeriodID: period.ID,
				IntendedStartsAt: period.StartsAt, InviterUserID: model.NewUserID(), ScopeType: model.RoleScopeClass,
				ScopeID: currentClassID.String(), ClaimHash: model.HashInvitationClaim(model.NewCredentialToken()), IssuedAt: now, Lifetime: test.current})
			require.NoError(t, err)
			persistence := &invitationStoreFake{invitation: current}
			service := newInvitationServiceForTest(t, persistence, invitationAcademicUnitStoreFake{}, invitationRoleStoreFake{},
				&invitationAuthorizerFake{}, &invitationMailPreparerFake{}, now.Add(time.Hour))
			service.deps.Classes = invitationClassStoreFake{class: class}
			service.deps.Periods = invitationPeriodStoreFake{period: period}
			_, err = service.ReplaceInvitation(context.Background(), invocation.New(model.Principal{UserID: model.NewUserID()}, model.RequestMetadata{}),
				ReplaceInvitationCommand{ID: current.ID, ExpectedRevision: current.Revision, Purpose: string(model.InvitationPurposeStudentClass),
					TargetEmail: "new@example.edu", ClassID: class.ID, ExpiresInDays: test.days})
			if test.code != "" {
				require.True(t, apperror.Is(err, test.code), "error = %v", err)
				require.Nil(t, persistence.replaced)
				return
			}
			require.NoError(t, err)
			require.Equal(t, test.want, persistence.replaced.Lifetime)
			require.Equal(t, test.want, persistence.replaced.Replacement.ExpiresAt.Sub(persistence.replaced.Replacement.CreatedAt))
		})
	}
}
