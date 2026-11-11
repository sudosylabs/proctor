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
	"github.com/sudosylabs/proctor/server/app/invocation"
	"github.com/sudosylabs/proctor/server/model"
)

func TestInvitationAdministrationUsesAuthorizedSafeLifecycleCommands(t *testing.T) {
	now := model.TimeFromMillis(1_800_000_000_000)
	classID := model.NewClassID()
	invitation, err := model.NewStudentClassInvitation(model.StudentClassInvitationInput{ID: model.NewInvitationID(),
		TargetEmail: "student@example.edu", ClassID: classID, AcademicPeriodID: model.NewAcademicPeriodID(),
		IntendedStartsAt: now, InviterUserID: model.NewUserID(), ScopeType: model.RoleScopeClass,
		ScopeID: classID.String(), ClaimHash: model.HashInvitationClaim(model.NewCredentialToken()), IssuedAt: now})
	require.NoError(t, err)
	persistence := &invitationStoreFake{invitation: invitation}
	authorizer := &authorizerFake{}
	composer := &mailFake{}
	service := newInvitationServiceForTest(t, persistence, invitationAcademicUnitStoreFake{}, invitationRoleStoreFake{}, authorizer, composer, now.Add(time.Hour))
	inv := invocation.New(model.Principal{UserID: model.NewUserID()}, model.RequestMetadata{})

	page, err := service.ListInvitations(context.Background(), inv, ListInvitationsQuery{TargetEmail: "STUDENT@example.edu", Limit: 25})
	require.NoError(t, err)
	require.Len(t, page.Items, 1, "Invitation list = %#v / %#v", page, persistence.listOptions)
	require.Equal(t, invitation.TargetEmail, page.Items[0].TargetEmail, "Invitation list = %#v / %#v", page, persistence.listOptions)
	require.Equal(t, invitation.TargetEmail, persistence.listOptions.TargetEmail, "Invitation list = %#v / %#v", page, persistence.listOptions)
	require.Equal(t, 25, persistence.listOptions.Limit, "Invitation list = %#v / %#v", page, persistence.listOptions)

	resent, err := service.ResendInvitation(context.Background(), inv, ResendInvitationCommand{ID: invitation.ID, ExpectedRevision: invitation.Revision})
	require.NoError(t, err)
	require.NotNil(t, persistence.resent, "Invitation resend = %#v / %#v", resent, persistence.resent)
	require.NotEqual(t, invitation.ClaimHash, persistence.resent.ClaimHash, "Invitation resend = %#v / %#v", resent, persistence.resent)
	require.NotContains(t, resent.String(), persistence.resent.ClaimHash, "Invitation resend = %#v / %#v", resent, persistence.resent)
	require.Contains(t, composer.issueURL, "/join#token=", "Invitation resend = %#v / %#v", resent, persistence.resent)

	revoked, err := service.RevokeInvitation(context.Background(), inv, RevokeInvitationCommand{ID: invitation.ID, ExpectedRevision: invitation.Revision})
	require.NoError(t, err)
	require.NotNil(t, persistence.revoked, "Invitation revoke = %#v", revoked)
	require.Equal(t, model.InvitationRevoked, revoked.State, "Invitation revoke = %#v", revoked)
	require.NotNil(t, persistence.revoked.RevocationNotice, "Invitation revoke = %#v", revoked)
	require.Equal(t, model.MailTemplateAccessInvitationRevoked, persistence.revoked.RevocationNotice.Delivery.TemplateKey, "Invitation revoke = %#v", revoked)
	require.True(t, slices.Equal(authorizer.actions, []model.Action{model.ActionInvitationView, model.ActionInvitationManage, model.ActionInvitationManage}), "Invitation administration authorization = %v", authorizer.actions)
}
