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
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/sudosylabs/proctor/server/app/apperror"
	"github.com/sudosylabs/proctor/server/app/identity"
	"github.com/sudosylabs/proctor/server/app/invocation"
	"github.com/sudosylabs/proctor/server/app/realtime/realtimetest"
	"github.com/sudosylabs/proctor/server/model"
	"github.com/sudosylabs/proctor/server/store"
)

func TestInvitationExternalIdentityAcceptancePreparesTheExactTerminalPackage(t *testing.T) {
	now := model.TimeFromMillis(1_800_000_000_000)
	classID, periodID, inviterID := model.NewClassID(), model.NewAcademicPeriodID(), model.NewUserID()
	invitation, err := model.NewStudentClassInvitation(model.StudentClassInvitationInput{ID: model.NewInvitationID(),
		TargetEmail: "invited@example.edu", ClassID: classID, AcademicPeriodID: periodID,
		IntendedStartsAt: now.Add(time.Hour), InviterUserID: inviterID, ScopeType: model.RoleScopeClass,
		ScopeID: classID.String(), ClaimHash: model.HashInvitationClaim(model.NewCredentialToken()), IssuedAt: now})
	require.NoError(t, err)
	persistence := &invitationStoreFake{invitation: invitation}
	composer := &mailFake{}
	service := newInvitationServiceForTest(t, persistence, invitationAcademicUnitStoreFake{}, invitationRoleStoreFake{},
		&authorizerFake{}, composer, now.Add(time.Minute))
	state := &model.ExternalLoginState{ID: model.NewExternalLoginStateID(), Provider: "campus",
		Purpose: model.ExternalAuthenticationPurposeInvitationAdmission, InvitationID: invitation.ID,
		ConsumedAt: model.OptionalTimeFrom(now)}
	assertion := &model.ExternalAuthenticationAssertion{ProviderID: "campus", Subject: "opaque-subject",
		Username: "provider-name", Email: " Provider.Mailbox@Example.EDU ", EmailVerified: true,
		DisplayName: "Provider Person"}
	capabilities := store.AccessDeploymentCapabilities{Providers: map[string]store.AccessProviderCapability{"campus": {}}}
	result, err := service.acceptExternalIdentity(context.Background(), state, assertion, capabilities,
		model.RequestMetadata{RequestID: "request-1", IPAddress: "192.0.2.10"}, "oidc")
	require.NoError(t, err)
	accepted := persistence.externalAccepted
	require.NotNil(t, accepted, "external Invitation package/result = %#v / %#v", accepted, result)
	require.Same(t, accepted.User, result.User, "external Invitation package/result = %#v / %#v", accepted, result)
	require.Equal(t, state.ID, accepted.ExternalStateID, "external Invitation package/result = %#v / %#v", accepted, result)
	require.Equal(t, "provider.mailbox@example.edu", accepted.ProviderEmail, "external Invitation package/result = %#v / %#v", accepted, result)
	require.Equal(t, invitation.TargetEmail, accepted.User.Email, "external Invitation package/result = %#v / %#v", accepted, result)
	require.True(t, accepted.User.EmailVerified, "external Invitation package/result = %#v / %#v", accepted, result)
	require.Equal(t, "campus", accepted.Identity.Provider, "external Invitation package/result = %#v / %#v", accepted, result)
	require.Equal(t, assertion.Subject, accepted.Identity.Subject, "external Invitation package/result = %#v / %#v", accepted, result)
	require.NotNil(t, accepted.Affiliation, "external Invitation package/result = %#v / %#v", accepted, result)
	require.Equal(t, model.AffiliationStudent, accepted.Affiliation.Kind, "external Invitation package/result = %#v / %#v", accepted, result)
	require.NotNil(t, accepted.ClassMember, "external Invitation package/result = %#v / %#v", accepted, result)
	require.Equal(t, classID, accepted.ClassMember.ClassID, "external Invitation package/result = %#v / %#v", accepted, result)
	require.Equal(t, periodID, accepted.ClassMember.AcademicPeriodID, "external Invitation package/result = %#v / %#v", accepted, result)
	require.Equal(t, accepted.User.ID, accepted.ClassMember.UserID, "external Invitation package/result = %#v / %#v", accepted, result)
	require.NotNil(t, accepted.Notice, "external Invitation package/result = %#v / %#v", accepted, result)
	require.Equal(t, accepted.User.ID, accepted.Notice.Delivery.TargetUserID, "external Invitation package/result = %#v / %#v", accepted, result)
	require.Equal(t, "oidc", accepted.AuditEvent.AuthMethod, "external Invitation package/result = %#v / %#v", accepted, result)
	require.Contains(t, string(accepted.AuditEvent.Parameters), `"provider":"campus"`, "external Invitation package/result = %#v / %#v", accepted, result)
	require.True(t, slices.Equal(accepted.RequiredActions, []model.Action{model.ActionInvitationCreate, model.ActionClassMembersManage}), "external Invitation package/result = %#v / %#v", accepted, result)
	require.Equal(t, model.JobTypeMailDeliver, composer.directJobType)
	composer.directErr = fmt.Errorf("mail preparation unavailable")
	assertion.Username = strings.Repeat("x", 1_000)
	result, err = service.acceptExternalIdentity(context.Background(), state, assertion, capabilities,
		model.RequestMetadata{RequestID: "request-2"}, "oidc")
	require.NoError(t, err, "existing-User candidate fallback result")
	require.NotNil(t, result, "existing-User candidate fallback result")
	accepted = persistence.externalAccepted
	require.NotNil(t, accepted)
	require.True(t, accepted.User.ID.IsValid())
	require.Nil(t, accepted.Settings)
	require.Nil(t, accepted.DefaultProfilePictureJob)
	require.Nil(t, accepted.Notice)
	require.NotNil(t, accepted.ClassMember)
	require.Equal(t, accepted.User.ID, accepted.ClassMember.UserID)
}

func TestInvitationServiceAcceptsAcademicUnitRoleForAuthenticatedExistingUserWithoutMail(t *testing.T) {
	now := model.TimeFromMillis(1_800_000_000_000)
	unitID, roleID, inviterID, userID := model.NewAcademicUnitID(), model.NewRoleID(), model.NewUserID(), model.NewUserID()
	raw := model.NewCredentialToken()
	invitation, err := model.NewScopedRoleInvitation(model.ScopedRoleInvitationInput{
		ID: model.NewInvitationID(), Purpose: model.InvitationPurposeAcademicUnitRole, TargetEmail: "other-mailbox@example.edu",
		AcademicUnitID: unitID, RoleID: roleID, RoleActions: []string{string(model.ActionAcademicUnitView)},
		IntendedStartsAt: now, InviterUserID: inviterID, ScopeType: model.RoleScopeAcademicUnit, ScopeID: unitID.String(),
		ClaimHash: model.HashInvitationClaim(raw), IssuedAt: now,
	})
	require.NoError(t, err)
	events := []string{}
	auditID := model.NewAuditEventID().String()
	auditor := &mutationAttemptAuditorFake{events: &events, beginID: auditID}
	persistence := &invitationStoreFake{invitation: invitation, events: &events}
	composer := &mailFake{}
	service := &Service{invitations: persistence, classes: invitationClassStoreFake{}, periods: invitationPeriodStoreFake{}, academicUnits: invitationAcademicUnitStoreFake{}, roles: invitationRoleStoreFake{}, authorizer: &authorizerFake{}, mail: composer, hasher: passwordHasherFake{}, auditor: auditor, attempts: testAttempts(t), rateLimit: testRateLimit, publisher: &realtimetest.Publisher{}, nodeID: "node-1", publicURL: "https://proctor.example.edu", recentAuthenticationTTL: 15 * time.Minute, newToken: model.NewCredentialToken, now: func() time.Time { return now.Add(time.Minute) }}
	principal := model.Principal{UserID: userID, SessionID: model.NewSessionID(), CredentialID: model.PrincipalCredentialID(model.NewID()),
		CredentialType: model.CredentialSessionAccess, AuthenticationMethod: "password", AuthenticationStrength: model.AuthenticationSingleFactor,
		ClientType: model.SessionClientWeb, AuthenticatedAt: now}
	result, err := service.AcceptAcademicUnitRoleInvitation(context.Background(), invocation.New(principal, model.RequestMetadata{RequestID: "request-1"}),
		AcceptAcademicUnitRoleInvitationCommand{Claim: raw, Source: "127.0.0.1"})
	require.NoError(t, err)
	accepted := persistence.scopedAccepted
	require.NotNil(t, accepted, "scoped Role acceptance/result = %#v / %#v", accepted, result)
	require.Equal(t, userID, accepted.UserID, "scoped Role acceptance/result = %#v / %#v", accepted, result)
	require.Equal(t, userID, accepted.RoleBinding.UserID, "scoped Role acceptance/result = %#v / %#v", accepted, result)
	require.Equal(t, roleID, accepted.RoleBinding.RoleID, "scoped Role acceptance/result = %#v / %#v", accepted, result)
	require.Equal(t, model.RoleScopeAcademicUnit, accepted.RoleBinding.ScopeType, "scoped Role acceptance/result = %#v / %#v", accepted, result)
	require.Equal(t, unitID.String(), accepted.RoleBinding.ScopeID, "scoped Role acceptance/result = %#v / %#v", accepted, result)
	require.Equal(t, invitation.ID, accepted.RoleBinding.OriginInvitationID, "scoped Role acceptance/result = %#v / %#v", accepted, result)
	require.Equal(t, model.AuditEventID(auditID), accepted.AuditEventID, "scoped Role acceptance/result = %#v / %#v", accepted, result)
	require.True(t, model.TimeUTC(now.Add(time.Minute)).Equal(accepted.AuditAt), "scoped Role acceptance/result = %#v / %#v", accepted, result)
	require.NotNil(t, result.User, "scoped Role acceptance/result = %#v / %#v", accepted, result)
	require.Equal(t, userID, result.User.ID, "scoped Role acceptance/result = %#v / %#v", accepted, result)
	require.NotNil(t, result.RoleBinding, "scoped Role acceptance/result = %#v / %#v", accepted, result)
	require.Empty(t, composer.directJobType, "scoped Role acceptance/result = %#v / %#v", accepted, result)
	require.True(t, slices.Equal(events, []string{"resolve", "begin-at-scope", "accept"}), "scoped Role audit attempt/events = %#v / %v", auditor.attempt, events)
	require.Equal(t, model.Action("invitation.accept"), auditor.attempt.Action, "scoped Role audit attempt/events = %#v / %v", auditor.attempt, events)
	require.Equal(t, model.ResourceAcademicUnit, auditor.attempt.Resource.Type, "scoped Role audit attempt/events = %#v / %v", auditor.attempt, events)
	require.Equal(t, unitID.String(), auditor.attempt.Resource.ID, "scoped Role audit attempt/events = %#v / %v", auditor.attempt, events)
	require.Equal(t, model.RoleScopeAcademicUnit, auditor.attempt.ScopeType, "scoped Role audit attempt/events = %#v / %v", auditor.attempt, events)
	require.Equal(t, unitID.String(), auditor.attempt.ScopeID, "scoped Role audit attempt/events = %#v / %v", auditor.attempt, events)
	require.Equal(t, "accept_scoped_role_invitation", auditor.attempt.Operation, "scoped Role audit attempt/events = %#v / %v", auditor.attempt, events)
	encoded, encodeErr := model.EncodeAuditData(auditor.attempt.Value)
	require.NoError(t, encodeErr, "scoped Role audit attempt leaked claim material")
	require.NotContains(t, string(encoded), raw, "scoped Role audit attempt leaked claim material")
	require.NotContains(t, string(encoded), invitation.ClaimHash, "scoped Role audit attempt leaked claim material")
}

func TestInvitationServiceTerminalizesScopedRoleAcceptanceConflict(t *testing.T) {
	now := model.TimeFromMillis(1_800_000_000_000)
	unitID, roleID, inviterID, userID := model.NewAcademicUnitID(), model.NewRoleID(), model.NewUserID(), model.NewUserID()
	raw := model.NewCredentialToken()
	invitation, err := model.NewScopedRoleInvitation(model.ScopedRoleInvitationInput{
		ID: model.NewInvitationID(), Purpose: model.InvitationPurposeAcademicUnitRole, TargetEmail: "other-mailbox@example.edu",
		AcademicUnitID: unitID, RoleID: roleID, RoleActions: []string{string(model.ActionAcademicUnitView)}, IntendedStartsAt: now,
		InviterUserID: inviterID, ScopeType: model.RoleScopeAcademicUnit, ScopeID: unitID.String(),
		ClaimHash: model.HashInvitationClaim(raw), IssuedAt: now,
	})
	require.NoError(t, err)
	events := []string{}
	auditID := model.NewAuditEventID().String()
	auditor := &mutationAttemptAuditorFake{events: &events, beginID: auditID}
	persistence := &invitationStoreFake{invitation: invitation, events: &events,
		scopedAcceptErr: store.NewErrConflict("invitation", "invitation_role_binding_conflict", nil)}
	service := &Service{invitations: persistence, classes: invitationClassStoreFake{}, periods: invitationPeriodStoreFake{}, academicUnits: invitationAcademicUnitStoreFake{}, roles: invitationRoleStoreFake{}, authorizer: &authorizerFake{}, mail: &mailFake{}, hasher: passwordHasherFake{}, auditor: auditor, attempts: testAttempts(t), rateLimit: testRateLimit, publisher: &realtimetest.Publisher{}, nodeID: "node-1", publicURL: "https://proctor.example.edu", recentAuthenticationTTL: 15 * time.Minute, newToken: model.NewCredentialToken, now: func() time.Time { return now.Add(time.Minute) }}
	principal := model.Principal{UserID: userID, SessionID: model.NewSessionID(), CredentialID: model.PrincipalCredentialID(model.NewID()),
		CredentialType: model.CredentialSessionAccess, AuthenticationMethod: "password", AuthenticationStrength: model.AuthenticationSingleFactor,
		ClientType: model.SessionClientWeb, AuthenticatedAt: now}
	_, appErr := service.AcceptAcademicUnitRoleInvitation(context.Background(), invocation.New(principal, model.RequestMetadata{}),
		AcceptAcademicUnitRoleInvitationCommand{Claim: raw, Source: "127.0.0.1"})
	require.True(t, apperror.Is(appErr, "invitation.invalid"), "scoped Role conflict error/audit/events = %v / %#v / %v", appErr, auditor, events)
	require.Equal(t, auditID, auditor.failID, "scoped Role conflict error/audit/events = %v / %#v / %v", appErr, auditor, events)
	require.Equal(t, "invitation.invalid", auditor.failCode, "scoped Role conflict error/audit/events = %v / %#v / %v", appErr, auditor, events)
	require.True(t, slices.Equal(events, []string{"resolve", "begin-at-scope", "accept", "fail"}), "scoped Role conflict error/audit/events = %v / %#v / %v", appErr, auditor, events)
}

func TestInvitationServiceAcceptsTeacherPackageArtifacts(t *testing.T) {
	now := model.TimeFromMillis(1_800_000_000_000)
	unitID, roleID, inviterID := model.NewAcademicUnitID(), model.NewRoleID(), model.NewUserID()
	raw := model.NewCredentialToken()
	invitation, err := model.NewTeacherAcademicUnitInvitation(model.TeacherAcademicUnitInvitationInput{
		ID: model.NewInvitationID(), TargetEmail: "teacher@example.edu", AcademicUnitID: unitID, RoleID: roleID,
		RoleActions: []string{string(model.ActionAcademicUnitView)}, IntendedStartsAt: now,
		InviterUserID: inviterID, ScopeType: model.RoleScopeAcademicUnit, ScopeID: unitID.String(),
		ClaimHash: model.HashInvitationClaim(raw), IssuedAt: now,
	})
	require.NoError(t, err)
	persistence := &invitationStoreFake{invitation: invitation}
	service := newInvitationServiceForTest(t, persistence, invitationAcademicUnitStoreFake{}, invitationRoleStoreFake{},
		&authorizerFake{}, &mailFake{}, now.Add(time.Minute))
	result, err := service.AcceptTeacherAcademicUnitInvitation(context.Background(), invocation.Invocation{}, AcceptTeacherAcademicUnitInvitationCommand{
		Claim: raw, Password: "correct horse battery staple", Username: "teacher-one", Source: "127.0.0.1",
	})
	require.NoError(t, err)
	accepted := persistence.teacherAccepted
	require.NotNil(t, accepted, "teacher acceptance artifacts/result = %#v / %#v", accepted, result)
	require.Equal(t, model.AffiliationTeacher, accepted.Affiliation.Kind, "teacher acceptance artifacts/result = %#v / %#v", accepted, result)
	require.Equal(t, unitID, accepted.AcademicUnitMember.AcademicUnitID, "teacher acceptance artifacts/result = %#v / %#v", accepted, result)
	require.Equal(t, roleID, accepted.RoleBinding.RoleID, "teacher acceptance artifacts/result = %#v / %#v", accepted, result)
	require.Equal(t, model.RoleScopeAcademicUnit, accepted.RoleBinding.ScopeType, "teacher acceptance artifacts/result = %#v / %#v", accepted, result)
	require.Equal(t, unitID.String(), accepted.RoleBinding.ScopeID, "teacher acceptance artifacts/result = %#v / %#v", accepted, result)
	require.Equal(t, invitation.ID, accepted.RoleBinding.OriginInvitationID, "teacher acceptance artifacts/result = %#v / %#v", accepted, result)
	require.NotNil(t, result.AcademicUnitMember, "teacher acceptance artifacts/result = %#v / %#v", accepted, result)
	require.NotNil(t, result.RoleBinding, "teacher acceptance artifacts/result = %#v / %#v", accepted, result)
}

func TestInvitationAcceptanceCommitsWithTerminalNoticeWhenMailIsDisabled(t *testing.T) {
	now := model.TimeFromMillis(1_800_000_000_000)
	periodID, classID, inviterID := model.NewAcademicPeriodID(), model.NewClassID(), model.NewUserID()
	raw := model.NewCredentialToken()
	invitation, err := model.NewStudentClassInvitation(model.StudentClassInvitationInput{ID: model.NewInvitationID(), TargetEmail: "student@example.edu",
		ClassID: classID, AcademicPeriodID: periodID, IntendedStartsAt: now, InviterUserID: inviterID,
		ScopeType: model.RoleScopeClass, ScopeID: classID.String(), ClaimHash: model.HashInvitationClaim(raw), IssuedAt: now})
	require.NoError(t, err)
	persistence := &invitationStoreFake{invitation: invitation}
	composer := &mailFake{disabled: true}
	service := &Service{invitations: persistence, classes: invitationClassStoreFake{}, periods: invitationPeriodStoreFake{}, academicUnits: invitationAcademicUnitStoreFake{}, roles: invitationRoleStoreFake{}, authorizer: &authorizerFake{}, mail: composer, hasher: passwordHasherFake{}, auditor: &mutationAttemptAuditorFake{}, attempts: testAttempts(t), rateLimit: testRateLimit, publisher: &realtimetest.Publisher{}, nodeID: "node-1", publicURL: "https://proctor.example.edu", recentAuthenticationTTL: 15 * time.Minute, newToken: model.NewCredentialToken, now: func() time.Time { return now.Add(time.Minute) }}
	_, appErr := service.AcceptStudentClassInvitation(context.Background(), invocation.Invocation{}, AcceptStudentClassInvitationCommand{Claim: raw,
		Password: "correct horse battery staple", Username: "student-one", Source: "127.0.0.1"})
	require.NoError(t, appErr)
	require.Equal(t, model.JobTypeMailDeliver, composer.directJobType, "disabled acceptance mail = %#v / %#v", persistence.accepted.Delivery, persistence.accepted.DeliveryJob)
	require.Equal(t, model.MailDeliverySuppressed, persistence.accepted.Delivery.State, "disabled acceptance mail = %#v / %#v", persistence.accepted.Delivery, persistence.accepted.DeliveryJob)
	require.Equal(t, model.MailDeliveryDisabledCode, persistence.accepted.Delivery.PublicFailureCode, "disabled acceptance mail = %#v / %#v", persistence.accepted.Delivery, persistence.accepted.DeliveryJob)
	require.Empty(t, persistence.accepted.Delivery.EncryptedPayload, "disabled acceptance mail = %#v / %#v", persistence.accepted.Delivery, persistence.accepted.DeliveryJob)
	require.Equal(t, model.JobStatusCanceled, persistence.accepted.DeliveryJob.Status, "disabled acceptance mail = %#v / %#v", persistence.accepted.Delivery, persistence.accepted.DeliveryJob)
}

func TestInvitationPasswordWorkFailuresStopBeforeAcceptance(t *testing.T) {
	for _, purpose := range []model.InvitationPurpose{model.InvitationPurposeStudentClass, model.InvitationPurposeTeacherAcademicUnit} {
		t.Run(string(purpose), func(t *testing.T) {
			testPasswordHashFailures(t, "invitation.unavailable", func(t *testing.T, ctx context.Context, hasher testPasswordHasher) error {
				raw := model.NewCredentialToken()
				invitation := &model.Invitation{Purpose: purpose, ClaimHash: model.HashInvitationClaim(raw), TargetEmail: "invited@example.edu"}
				persistence := &invitationStoreFake{invitation: invitation}
				composer := &mailFake{}
				service := newInvitationServiceForTest(t, persistence, invitationAcademicUnitStoreFake{}, invitationRoleStoreFake{},
					&authorizerFake{}, composer, time.Now())
				service.hasher = hasher
				var result *InvitationAcceptanceView
				var err error
				if purpose == model.InvitationPurposeStudentClass {
					result, err = service.AcceptStudentClassInvitation(ctx, invocation.Invocation{}, AcceptStudentClassInvitationCommand{
						Claim: raw, Username: "invited", Password: "correct horse battery staple", Source: "192.0.2.36",
					})
				} else {
					result, err = service.AcceptTeacherAcademicUnitInvitation(ctx, invocation.Invocation{}, AcceptTeacherAcademicUnitInvitationCommand{
						Claim: raw, Username: "invited", Password: "correct horse battery staple", Source: "192.0.2.36",
					})
				}
				require.Nil(t, result, "failed password work accepted an Invitation or prepared mail")
				require.Nil(t, persistence.accepted, "failed password work accepted an Invitation or prepared mail")
				require.Nil(t, persistence.teacherAccepted, "failed password work accepted an Invitation or prepared mail")
				require.Empty(t, composer.directJobType, "failed password work accepted an Invitation or prepared mail")
				return err
			})
		})
	}
}

func TestInvitationAttemptAccountingLimitsClaimAndSourceBeforeValidation(t *testing.T) {
	now := time.Now()
	cache := newExpiringAuthenticationAttemptCache(func() time.Time { return now })
	accounting := mustAuthenticationAttemptAccounting(t, cache)
	limiter := &Service{attempts: accounting, rateLimit: identity.LoginRateLimitPolicy{
		Window: time.Minute, MaximumAttempts: 1, MaximumSourceAttempts: 2,
	}}
	claim := model.HashInvitationClaim("malformed")
	err := limiter.checkInvitationAttempt(context.Background(), claim, "127.0.0.1")
	require.NoError(t, err)
	// The identity area owns the key layout; derive each expected identity
	// counter key by counting the same claim under one qualifier alone.
	identityKey := func(qualifier string) string {
		probe := newExpiringAuthenticationAttemptCache(func() time.Time { return now })
		_, probeErr := mustAuthenticationAttemptAccounting(t, probe).Account(context.Background(), identity.AttemptPurposeInvitation,
			qualifier, time.Minute, identity.IdentityAttemptLimit(claim, 1))
		require.NoError(t, probeErr)
		keys := probe.snapshot()
		require.Len(t, keys, 1)
		for key := range keys {
			require.True(t, strings.HasPrefix(key, "authentication/attempts/invitation/identity/"), "identity counter key %q", key)
			return key
		}
		return ""
	}
	expectedKey := identityKey(invitationAcceptanceAttemptQualifier)
	legacyKey := identityKey("student-class-accept")
	entries := cache.snapshot()
	_, ok := entries[expectedKey]
	require.True(t, ok, "purpose-neutral Invitation acceptance counter was not recorded: %#v", entries)
	_, ok = entries[legacyKey]
	require.False(t, ok, "teacher-capable Invitation accounting used legacy student qualifier: %#v", entries)
	err = limiter.checkInvitationAttempt(context.Background(), claim, "127.0.0.1")
	failure, ok := apperror.As(err)
	require.True(t, ok)
	require.Equal(t, "authentication.rate_limited", failure.Code())
}
