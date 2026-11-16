// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package onboarding

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/sudosylabs/proctor/server/app/access/accesstest"
	"github.com/sudosylabs/proctor/server/model"
)

func TestInvitationReplacementReauthorizesAndBuildsANewImmutablePackage(t *testing.T) {
	now := model.TimeFromMillis(1_800_000_000_000)
	period := &model.AcademicPeriod{ID: model.NewAcademicPeriodID(), StartsAt: now.Add(24 * time.Hour), EndsAt: now.Add(180 * 24 * time.Hour)}
	currentClassID := model.NewClassID()
	class := &model.Class{ID: model.NewClassID(), AcademicPeriodID: period.ID}
	invitation, err := model.NewStudentClassInvitation(model.StudentClassInvitationInput{ID: model.NewInvitationID(),
		TargetEmail: "old@example.edu", ClassID: currentClassID, AcademicPeriodID: period.ID,
		IntendedStartsAt: period.StartsAt, InviterUserID: model.NewUserID(), ScopeType: model.RoleScopeClass,
		ScopeID: currentClassID.String(), ClaimHash: model.HashInvitationClaim(model.NewCredentialToken()), IssuedAt: now})
	require.NoError(t, err)
	persistence := &invitationStoreFake{invitation: invitation}
	actor := model.NewUserID()
	authorization := permissiveAccess(t, accesstest.Options{}, []model.UserID{actor})
	composer, rendered := newTestMail(t, true)
	service := newInvitationServiceForTest(t, persistence, invitationAcademicUnitStoreFake{}, invitationRoleStoreFake{}, authorization, composer, now.Add(time.Hour))
	service.classes, service.periods = invitationClassStoreFake{class: class}, invitationPeriodStoreFake{period: period}
	result, err := service.ReplaceInvitation(context.Background(), sessionCaller(actor, now),
		ReplaceInvitationCommand{ID: invitation.ID, ExpectedRevision: invitation.Revision,
			Purpose: string(model.InvitationPurposeStudentClass), TargetEmail: "new@example.edu", ClassID: class.ID,
			SuggestedUsername: "new-student"})
	require.NoError(t, err)
	require.NotNil(t, persistence.replaced, "Invitation replacement = %#v / %#v", result, persistence.replaced)
	require.NotEqual(t, invitation.ID, result.ID, "Invitation replacement = %#v / %#v", result, persistence.replaced)
	require.Equal(t, "new@example.edu", result.TargetEmail, "Invitation replacement = %#v / %#v", result, persistence.replaced)
	require.Equal(t, actor, persistence.replaced.Replacement.InviterUserID, "Invitation replacement = %#v / %#v", result, persistence.replaced)
	require.Equal(t, class.ID, persistence.replaced.Replacement.ClassID, "Invitation replacement = %#v / %#v", result, persistence.replaced)
	require.Equal(t, period.ID, persistence.replaced.Replacement.AcademicPeriodID, "Invitation replacement = %#v / %#v", result, persistence.replaced)
	require.NotEqual(t, invitation.ClaimHash, persistence.replaced.Replacement.ClaimHash, "Invitation replacement = %#v / %#v", result, persistence.replaced)
	require.Equal(t, invitation.Revision, persistence.replaced.ExpectedRevision, "Invitation replacement = %#v / %#v", result, persistence.replaced)
	attempts := authorization.Audits.Attempts()
	require.Len(t, attempts, 2)
	require.Equal(t, attempts[0].ID, persistence.replaced.CurrentAuditEventID, "Invitation replacement = %#v / %#v", result, persistence.replaced)
	require.Equal(t, attempts[1].ID, persistence.replaced.ReplacementAuditEventID, "Invitation replacement = %#v / %#v", result, persistence.replaced)
	require.Len(t, rendered.actionURLs(), 1)
	require.Contains(t, rendered.actionURLs()[0], "/join#token=", "Invitation replacement = %#v / %#v", result, persistence.replaced)
	wantActions := []model.Action{model.ActionInvitationManage, model.ActionInvitationCreate, model.ActionClassMembersManage}
	require.Equal(t, wantActions, decidedActions(authorization), "Invitation replacement authorization")
	require.Equal(t, currentClassID.String(), attempts[0].ScopeID)
	require.Equal(t, class.ID.String(), attempts[1].ScopeID)
	_, currentValue := auditParameters(t, attempts[0])
	_, replacementValue := auditParameters(t, attempts[1])
	require.NotContains(t, fmt.Sprint(currentValue), class.ID.String())
	require.NotContains(t, fmt.Sprint(replacementValue), currentClassID.String())
}
