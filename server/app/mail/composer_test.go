// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package mail

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/sudosylabs/proctor/server/model"
)

func TestDirectMailPreparerFreezesEncryptedCredentialPayload(t *testing.T) {
	at := time.Date(2026, 8, 17, 12, 0, 0, 0, time.UTC)
	principal := mailTestPrincipal(at)
	user := mailTestUser(principal, at)
	actionURL := "https://proctor.example.test/account/verify-email#token=credential-secret"
	content := FrozenContent{Subject: "Verify", Text: "Use " + actionURL, HTML: "<a href=\"" + actionURL + "\">Verify</a>"}
	preparer, err := newDirectMailPreparer(mailRendererFake{content: content}, &mailSenderFake{enabled: true, from: Address{Name: "Proctor", Address: "no-reply@example.test"}}, mailTestSealer(t), nil)
	require.NoError(t, err)
	prepared, err := preparer.PrepareEmailVerification(AccountTokenPreparation{Recipient: user,
		OccurrenceID: model.NewMailOccurrenceID(), ActionURL: actionURL, At: at, Deadline: at.Add(time.Hour)})
	require.NoError(t, err)
	require.Equal(t, model.JobTypeMailDeliverCredential, prepared.Job.Type)
	require.Equal(t, model.MailDeliveryQueued, prepared.Delivery.State)
	require.Equal(t, model.MailTemplateIdentityVerifyEmail, prepared.Delivery.TemplateKey)
	require.NotEmpty(t, prepared.Delivery.EncryptedPayload)
	require.NotContains(t, string(prepared.Delivery.EncryptedPayload), "credential-secret")
	require.NotContains(t, string(prepared.Delivery.EncryptedPayload), user.Email)
	opened, err := OpenDelivery(mailTestSealer(t), prepared.Delivery)
	require.NoError(t, err, "opened payload = %#v", opened)
	require.Equal(t, content.Text, opened.Text, "opened payload = %#v", opened)
	require.Equal(t, user.Email, opened.To.Address, "opened payload = %#v", opened)
}

func TestRelationshipMailComposerSupportsCompleteCatalog(t *testing.T) {
	at := time.Date(2026, 8, 21, 12, 0, 0, 0, time.UTC)
	user := mailTestUser(mailTestPrincipal(at), at)
	preparer, err := newDirectMailPreparer(
		mailRendererFake{content: FrozenContent{Subject: "Relationship changed", Text: "Relationship changed", HTML: "<p>Relationship changed</p>"}},
		&mailSenderFake{enabled: true, from: Address{Name: "Proctor", Address: "no-reply@example.test"}},
		mailTestSealer(t),
		nil,
	)
	require.NoError(t, err)
	keys := []model.MailTemplateKey{
		model.MailTemplateAcademicUnitAssigned,
		model.MailTemplateAcademicUnitAssignmentEnded,
		model.MailTemplateAuthorizationScopedRoleAssigned,
		model.MailTemplateAuthorizationScopedRoleEnded,
		model.MailTemplateAuthorizationInstitutionRoleAssigned,
		model.MailTemplateAuthorizationInstitutionRoleEnded,
	}
	for _, key := range keys {
		key := key
		t.Run(string(key), func(t *testing.T) {
			prepared, prepareErr := preparer.PrepareRelationshipTransition(RelationshipTransitionPreparation{
				Recipient: user, OccurrenceID: model.NewMailOccurrenceID(), TemplateKey: key, ActionAt: at,
			})
			require.NoError(t, prepareErr)
			require.Equal(t, key, prepared.Occurrence.TemplateKey)
			require.Equal(t, model.MailOccurrenceAcademicAdministration, prepared.Occurrence.Kind)
			require.Equal(t, key, prepared.Delivery.TemplateKey)
			require.Equal(t, model.MailDeliveryQueued, prepared.Delivery.State)
			require.Equal(t, model.JobTypeMailDeliver, prepared.Job.Type)
		})
	}
}

func TestDirectMailPreparerSelectsTeacherInvitationTemplate(t *testing.T) {
	at := time.Date(2026, 8, 17, 12, 0, 0, 0, time.UTC)
	unitID := model.NewAcademicUnitID()
	invitation, err := model.NewTeacherAcademicUnitInvitation(model.TeacherAcademicUnitInvitationInput{
		ID: model.NewInvitationID(), TargetEmail: "teacher@example.test", AcademicUnitID: unitID,
		RoleID: model.NewRoleID(), RoleActions: []string{string(model.ActionAcademicUnitView)}, IntendedStartsAt: at,
		InviterUserID: model.NewUserID(), ScopeType: model.RoleScopeAcademicUnit, ScopeID: unitID.String(),
		ClaimHash: model.HashInvitationClaim(model.NewCredentialToken()), IssuedAt: at,
	})
	require.NoError(t, err)
	preparer, err := newDirectMailPreparer(mailRendererFake{content: FrozenContent{Subject: "Invite", Text: "Invite", HTML: "<p>Invite</p>"}},
		&mailSenderFake{enabled: true, from: Address{Address: "no-reply@example.test"}}, mailTestSealer(t), nil)
	require.NoError(t, err)
	prepared, err := preparer.PrepareInvitation(invitation, "https://proctor.example.test/join#token=secret")
	require.NoError(t, err)
	require.Equal(t, model.MailTemplateAccessTeacherAcademicUnitInvitation, prepared.Delivery.TemplateKey)
	require.Equal(t, model.MailTemplateAccessTeacherAcademicUnitInvitation, prepared.Occurrence.TemplateKey)
}

func TestDirectMailPreparerSelectsScopedRoleInvitationTemplates(t *testing.T) {
	at := time.Date(2026, 8, 20, 12, 0, 0, 0, time.UTC)
	unitID, institutionID := model.NewAcademicUnitID(), model.NewInstitutionID()
	for _, test := range []struct {
		name       string
		purpose    model.InvitationPurpose
		unitID     model.AcademicUnitID
		scopeType  model.RoleScopeType
		scopeID    string
		template   model.MailTemplateKey
		permission model.Action
	}{
		{name: "academic unit", purpose: model.InvitationPurposeAcademicUnitRole, unitID: unitID,
			scopeType: model.RoleScopeAcademicUnit, scopeID: unitID.String(), template: model.MailTemplateAccessAcademicUnitRoleInvitation,
			permission: model.ActionAcademicAuditView},
		{name: "institution", purpose: model.InvitationPurposeInstitutionRole,
			scopeType: model.RoleScopeInstitution, scopeID: institutionID.String(), template: model.MailTemplateAccessInstitutionRoleInvitation,
			permission: model.ActionAuditView},
	} {
		t.Run(test.name, func(t *testing.T) {
			invitation, err := model.NewScopedRoleInvitation(model.ScopedRoleInvitationInput{ID: model.NewInvitationID(), Purpose: test.purpose,
				TargetEmail: "existing@example.test", AcademicUnitID: test.unitID, RoleID: model.NewRoleID(), RoleActions: []string{string(test.permission)},
				IntendedStartsAt: at, InviterUserID: model.NewUserID(), ScopeType: test.scopeType, ScopeID: test.scopeID,
				ClaimHash: model.HashInvitationClaim(model.NewCredentialToken()), IssuedAt: at})
			require.NoError(t, err)
			preparer, err := newDirectMailPreparer(mailRendererFake{content: FrozenContent{Subject: "Invite", Text: "Invite", HTML: "<p>Invite</p>"}},
				&mailSenderFake{enabled: true, from: Address{Address: "no-reply@example.test"}}, mailTestSealer(t), nil)
			require.NoError(t, err)
			prepared, err := preparer.PrepareInvitation(invitation, "https://proctor.example.test/join#token=secret")
			require.NoError(t, err)
			require.Equal(t, test.template, prepared.Delivery.TemplateKey)
			require.Equal(t, test.template, prepared.Occurrence.TemplateKey)
		})
	}
}

func TestDirectMailPreparerCreatesTerminalSuppressedIntentWhenDisabled(t *testing.T) {
	at := time.Date(2026, 8, 17, 12, 0, 0, 0, time.UTC)
	principal := mailTestPrincipal(at)
	preparer, err := newDirectMailPreparer(mailRendererFake{}, &mailSenderFake{enabled: false}, nil, nil)
	require.NoError(t, err)
	prepared, err := preparer.PreparePasswordChanged(NoticePreparation{Recipient: mailTestUser(principal, at), At: at})
	require.NoError(t, err)
	require.Equal(t, model.MailDeliverySuppressed, prepared.Delivery.State)
	require.Equal(t, model.MailDeliveryDisabledCode, prepared.Delivery.PublicFailureCode)
	require.Empty(t, prepared.Delivery.EncryptedPayload)
	require.Equal(t, model.JobStatusCanceled, prepared.Job.Status)
}
