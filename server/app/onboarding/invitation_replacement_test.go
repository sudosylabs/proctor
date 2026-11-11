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
	"slices"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/sudosylabs/proctor/server/app/invocation"
	"github.com/sudosylabs/proctor/server/app/realtime/realtimetest"
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
	authorizer := &authorizerFake{}
	composer := &mailFake{}
	events := []string{}
	auditIDs := []string{model.NewAuditEventID().String(), model.NewAuditEventID().String()}
	auditor := &mutationAttemptAuditorFake{events: &events, beginIDs: auditIDs}
	service := &Service{invitations: persistence, classes: invitationClassStoreFake{class: class}, periods: invitationPeriodStoreFake{period: period}, academicUnits: invitationAcademicUnitStoreFake{}, roles: invitationRoleStoreFake{}, authorizer: authorizer, mail: composer, hasher: passwordHasherFake{}, auditor: auditor, attempts: testAttempts(t), rateLimit: testRateLimit, publisher: &realtimetest.Publisher{}, nodeID: "node-1", publicURL: "https://proctor.example.edu", recentAuthenticationTTL: 15 * time.Minute, newToken: model.NewCredentialToken, now: func() time.Time { return now.Add(time.Hour) }}
	actor := model.NewUserID()
	result, err := service.ReplaceInvitation(context.Background(), invocation.New(model.Principal{UserID: actor}, model.RequestMetadata{}),
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
	require.Equal(t, model.AuditEventID(auditIDs[0]), persistence.replaced.CurrentAuditEventID, "Invitation replacement = %#v / %#v", result, persistence.replaced)
	require.Equal(t, model.AuditEventID(auditIDs[1]), persistence.replaced.ReplacementAuditEventID, "Invitation replacement = %#v / %#v", result, persistence.replaced)
	require.Contains(t, composer.issueURL, "/join#token=", "Invitation replacement = %#v / %#v", result, persistence.replaced)
	wantActions := []model.Action{model.ActionInvitationManage, model.ActionInvitationCreate, model.ActionClassMembersManage}
	require.True(t, slices.Equal(authorizer.actions, wantActions), "Invitation replacement authorization = %v, want %v", authorizer.actions, wantActions)
	require.Len(t, auditor.attempts, 2)
	require.Equal(t, currentClassID.String(), auditor.attempts[0].ScopeID)
	require.Equal(t, class.ID.String(), auditor.attempts[1].ScopeID)
	require.NotContains(t, fmt.Sprint(auditor.attempts[0].Value), class.ID.String())
	require.NotContains(t, fmt.Sprint(auditor.attempts[1].Value), currentClassID.String())
}
