// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package onboarding

import (
	"context"
	"strings"

	"github.com/sudosylabs/proctor/server/app/access"
	"github.com/sudosylabs/proctor/server/app/apperror"
	"github.com/sudosylabs/proctor/server/app/identity"
	"github.com/sudosylabs/proctor/server/app/invocation"
	"github.com/sudosylabs/proctor/server/app/jobhandler"
	"github.com/sudosylabs/proctor/server/app/mail"
	"github.com/sudosylabs/proctor/server/app/user"
	"github.com/sudosylabs/proctor/server/model"
	"github.com/sudosylabs/proctor/server/store"
)

// AcceptStudentClassInvitation accepts a student Class Invitation by its raw claim,
// creating the local account and Class Membership it grants.
func (s *Service) AcceptStudentClassInvitation(ctx context.Context, invocation invocation.Invocation, command AcceptStudentClassInvitationCommand) (*InvitationAcceptanceView, error) {
	if err := s.checkInvitationAttempt(ctx, model.HashInvitationClaim(command.Claim), command.Source); err != nil {
		return nil, err
	}
	if !model.IsValidCredentialToken(command.Claim) {
		return nil, apperror.New("invitation.invalid")
	}
	claimHash := model.HashInvitationClaim(command.Claim)
	return s.acceptStudentClassByClaimHash(ctx, invocation, command, claimHash)
}

func (s *Service) acceptStudentClassByClaimHash(ctx context.Context, invocation invocation.Invocation, command AcceptStudentClassInvitationCommand, claimHash string) (*InvitationAcceptanceView, error) {
	if !model.IsValidTokenHash(claimHash) {
		return nil, apperror.New("invitation.invalid")
	}
	invitation, err := s.invitations.GetByClaimHash(ctx, claimHash)
	if err != nil {
		return nil, invalidInvitationError(err)
	}
	at := model.TimeUTC(s.now())
	timezone := command.Timezone
	if timezone == "" {
		timezone = invitation.Suggestions.Timezone
	}
	if err = identity.CheckNewPassword(command.Password, identity.PasswordIdentity{Username: command.Username, Email: invitation.TargetEmail,
		DisplayName: command.DisplayName, FirstName: command.FirstName, LastName: command.LastName}); err != nil {
		return nil, err
	}
	hash, err := s.hasher.Hash(ctx, command.Password)
	if err != nil {
		return nil, identity.PasswordHashError(err, "invitation.unavailable")
	}
	account, defaultJob, err := jobhandler.PrepareUserDefaultProfilePictureJob(&model.User{Username: command.Username, Email: invitation.TargetEmail,
		EmailVerified: true, DisplayName: command.DisplayName, FirstName: command.FirstName, LastName: command.LastName,
		Locale: command.Locale, Timezone: timezone}, at)
	if err != nil {
		return nil, apperror.New("invitation.user_invalid").Wrap(err)
	}
	credential := &model.PasswordCredential{UserID: account.ID, PasswordHash: hash}
	credential.PrepareCreate(model.NewPasswordCredentialID(), at)
	settings, err := user.PrepareInitialSettingsDocument(account)
	if err != nil {
		return nil, apperror.New("invitation.user_invalid").Wrap(err)
	}
	effectiveStart := invitation.EffectiveStartsAt(at)
	affiliation := &model.Affiliation{UserID: account.ID, Kind: model.AffiliationStudent, StartsAt: effectiveStart}
	affiliation.PrepareCreate(model.NewAffiliationID(), at)
	member := &model.ClassMember{ClassID: invitation.ClassID, AcademicPeriodID: invitation.AcademicPeriodID,
		UserID: account.ID, StartsAt: effectiveStart, EndsAt: invitation.IntendedEndsAt}
	member.PrepareCreate(model.NewClassMemberID(), at)
	prepared, err := s.mail.PrepareInvitationAccepted(mail.NoticePreparation{Recipient: account, At: at})
	if err != nil {
		return nil, apperror.New("invitation.mail_unavailable").Wrap(err)
	}
	metadata := invocation.RequestMetadata()
	event := &model.AuditEvent{ActorID: account.ID, SubjectUserID: account.ID, Action: string(model.ActionInvitationAccept),
		Resource: model.Resource{Type: model.ResourceClass, ID: invitation.ClassID.String()}, ScopeType: model.RoleScopeClass,
		ScopeID: invitation.ClassID.String(), Status: model.AuditStatusSuccess, RequestID: metadata.RequestID,
		NodeID: s.nodeID, ClientType: "web", AuthMethod: "invitation", IPAddress: metadata.IPAddress, UserAgent: metadata.UserAgent}
	result, err := s.invitations.AcceptStudentClass(ctx, &store.StudentClassInvitationAcceptance{ClaimHash: claimHash,
		AcceptedAt: at, User: account, Settings: settings, PasswordCredential: credential,
		DefaultProfilePictureJob: defaultJob, Affiliation: affiliation, ClassMember: member,
		Occurrence: prepared.Occurrence, Delivery: prepared.Delivery, DeliveryJob: prepared.Job, AuditEvent: event,
		RequiredActions:    []model.Action{model.ActionInvitationCreate, model.ActionClassMembersManage},
		BrowserTransaction: command.browserTransaction})
	if err != nil {
		return nil, invalidInvitationError(err)
	}
	if !result.Replayed && result.ClassMember != nil {
		if effectErr := s.membershipEffects.MembershipChanged(ctx, result.ClassMember.UserID,
			[]model.ClassID{result.ClassMember.ClassID}); effectErr != nil {
			s.membershipEffects.Report(ctx, "invitation.student_class_membership_changed", effectErr)
		}
	}
	return &InvitationAcceptanceView{Invitation: invitationView(result.Invitation), User: result.User,
		Affiliation: result.Affiliation, ClassMember: result.ClassMember, Replayed: result.Replayed}, nil
}

// AcceptTeacherAcademicUnitInvitation accepts a teacher Academic Unit Invitation by its raw
// claim, creating the local account, membership and Role Binding it grants.
func (s *Service) AcceptTeacherAcademicUnitInvitation(ctx context.Context, invocation invocation.Invocation, command AcceptTeacherAcademicUnitInvitationCommand) (*InvitationAcceptanceView, error) {
	if err := s.checkInvitationAttempt(ctx, model.HashInvitationClaim(command.Claim), command.Source); err != nil {
		return nil, err
	}
	if !model.IsValidCredentialToken(command.Claim) {
		return nil, apperror.New("invitation.invalid")
	}
	claimHash := model.HashInvitationClaim(command.Claim)
	return s.acceptTeacherAcademicUnitByClaimHash(ctx, invocation, command, claimHash)
}

func (s *Service) acceptTeacherAcademicUnitByClaimHash(ctx context.Context, invocation invocation.Invocation, command AcceptTeacherAcademicUnitInvitationCommand, claimHash string) (*InvitationAcceptanceView, error) {
	if !model.IsValidTokenHash(claimHash) {
		return nil, apperror.New("invitation.invalid")
	}
	invitation, err := s.invitations.GetByClaimHash(ctx, claimHash)
	if err != nil || invitation.Purpose != model.InvitationPurposeTeacherAcademicUnit {
		return nil, invalidInvitationError(err)
	}
	at := model.TimeUTC(s.now())
	timezone := command.Timezone
	if timezone == "" {
		timezone = invitation.Suggestions.Timezone
	}
	if err = identity.CheckNewPassword(command.Password, identity.PasswordIdentity{Username: command.Username, Email: invitation.TargetEmail,
		DisplayName: command.DisplayName, FirstName: command.FirstName, LastName: command.LastName}); err != nil {
		return nil, err
	}
	hash, err := s.hasher.Hash(ctx, command.Password)
	if err != nil {
		return nil, identity.PasswordHashError(err, "invitation.unavailable")
	}
	account, defaultJob, err := jobhandler.PrepareUserDefaultProfilePictureJob(&model.User{Username: command.Username, Email: invitation.TargetEmail,
		EmailVerified: true, DisplayName: command.DisplayName, FirstName: command.FirstName, LastName: command.LastName,
		Locale: command.Locale, Timezone: timezone}, at)
	if err != nil {
		return nil, apperror.New("invitation.user_invalid").Wrap(err)
	}
	credential := &model.PasswordCredential{UserID: account.ID, PasswordHash: hash}
	credential.PrepareCreate(model.NewPasswordCredentialID(), at)
	settings, err := user.PrepareInitialSettingsDocument(account)
	if err != nil {
		return nil, apperror.New("invitation.user_invalid").Wrap(err)
	}
	effectiveStart := invitation.EffectiveStartsAt(at)
	affiliation := &model.Affiliation{UserID: account.ID, Kind: model.AffiliationTeacher, StartsAt: effectiveStart}
	affiliation.PrepareCreate(model.NewAffiliationID(), at)
	member := &model.AcademicUnitMember{AcademicUnitID: invitation.AcademicUnitID, UserID: account.ID, StartsAt: effectiveStart, EndsAt: invitation.IntendedEndsAt}
	member.PrepareCreate(model.NewAcademicUnitMemberID(), at)
	binding := &model.RoleBinding{UserID: account.ID, RoleID: invitation.RoleID, OriginInvitationID: invitation.ID,
		OriginAcademicUnitMemberID: member.ID,
		ScopeType:                  model.RoleScopeAcademicUnit, ScopeID: invitation.AcademicUnitID.String(), StartsAt: effectiveStart, EndsAt: invitation.IntendedEndsAt}
	binding.PrepareCreate(model.NewRoleBindingID(), at)
	prepared, err := s.mail.PrepareInvitationAccepted(mail.NoticePreparation{Recipient: account, At: at})
	if err != nil {
		return nil, apperror.New("invitation.mail_unavailable").Wrap(err)
	}
	metadata := invocation.RequestMetadata()
	event := &model.AuditEvent{ActorID: account.ID, SubjectUserID: account.ID, Action: string(model.ActionInvitationAccept),
		Resource: model.Resource{Type: model.ResourceAcademicUnit, ID: invitation.AcademicUnitID.String()}, ScopeType: model.RoleScopeAcademicUnit,
		ScopeID: invitation.AcademicUnitID.String(), Status: model.AuditStatusSuccess, RequestID: metadata.RequestID,
		NodeID: s.nodeID, ClientType: "web", AuthMethod: "invitation", IPAddress: metadata.IPAddress, UserAgent: metadata.UserAgent}
	result, err := s.invitations.AcceptTeacherAcademicUnit(ctx, &store.TeacherAcademicUnitInvitationAcceptance{ClaimHash: claimHash,
		AcceptedAt: at, User: account, Settings: settings, PasswordCredential: credential,
		DefaultProfilePictureJob: defaultJob, Affiliation: affiliation, AcademicUnitMember: member, RoleBinding: binding,
		Occurrence: prepared.Occurrence, Delivery: prepared.Delivery, DeliveryJob: prepared.Job, AuditEvent: event,
		RequiredActions:    []model.Action{model.ActionInvitationCreate, model.ActionAcademicUnitMembersManage},
		BrowserTransaction: command.browserTransaction})
	if err != nil {
		return nil, invalidInvitationError(err)
	}
	if !result.Replayed && result.User != nil {
		s.membershipEffects.InvalidateAuthorization(ctx, result.User.ID)
	}
	return &InvitationAcceptanceView{Invitation: invitationView(result.Invitation), User: result.User, Affiliation: result.Affiliation,
		AcademicUnitMember: result.AcademicUnitMember, RoleBinding: result.RoleBinding, Replayed: result.Replayed}, nil
}

// AcceptAcademicUnitRoleInvitation accepts an Academic Unit Role Invitation by its raw claim
// for the caller.
func (s *Service) AcceptAcademicUnitRoleInvitation(ctx context.Context, invocation invocation.Invocation, command AcceptAcademicUnitRoleInvitationCommand) (*InvitationAcceptanceView, error) {
	return s.acceptScopedRole(ctx, invocation, command.Claim, command.Source, model.InvitationPurposeAcademicUnitRole)
}

// AcceptInstitutionRoleInvitation accepts an Institution Role Invitation by its raw claim
// for the caller.
func (s *Service) AcceptInstitutionRoleInvitation(ctx context.Context, invocation invocation.Invocation, command AcceptInstitutionRoleInvitationCommand) (*InvitationAcceptanceView, error) {
	return s.acceptScopedRole(ctx, invocation, command.Claim, command.Source, model.InvitationPurposeInstitutionRole)
}

func (s *Service) acceptExternalIdentity(ctx context.Context, state *model.ExternalLoginState,
	assertion *model.ExternalAuthenticationAssertion, capabilities store.AccessDeploymentCapabilities,
	metadata model.RequestMetadata, authenticationMethod string,
) (*store.ExternalIdentityInvitationAcceptanceResult, error) {
	if state == nil || state.Purpose != model.ExternalAuthenticationPurposeInvitationAdmission ||
		!state.ID.IsValid() || !state.InvitationID.IsValid() || !state.ConsumedAt.Valid || assertion == nil ||
		assertion.ProviderID != state.Provider || !assertion.EmailVerified {
		return nil, store.NewErrInvalidInput("invitation", "external_identity_acceptance", nil)
	}
	providerEmail := strings.ToLower(strings.TrimSpace(model.SanitizeUnicode(assertion.Email)))
	if !model.IsValidEmail(providerEmail) {
		return nil, store.NewErrInvalidInput("invitation", "provider_email", nil)
	}
	invitation, err := s.invitations.Get(ctx, state.InvitationID)
	if err != nil {
		return nil, err
	}
	at := model.TimeUTC(s.now())
	userCandidate := identity.ExternalUserCandidate(assertion)
	userCandidate.Email, userCandidate.EmailVerified = invitation.TargetEmail, true
	account, defaultJob, preparationErr := jobhandler.PrepareUserDefaultProfilePictureJob(userCandidate, at)
	var settings *model.UserSettingsDocument
	if preparationErr == nil {
		settings, preparationErr = user.PrepareInitialSettingsDocument(account)
	}
	if preparationErr != nil {
		userCandidate.PrepareCreate(model.NewUserID(), at)
		account, settings, defaultJob = userCandidate, nil, nil
	}
	identity := &model.ExternalIdentity{UserID: account.ID, Provider: state.Provider, Subject: assertion.Subject,
		LastSeenAt: model.OptionalTimeFrom(at)}
	effectiveStart := invitation.EffectiveStartsAt(at)
	input := &store.ExternalIdentityInvitationAcceptance{ExternalStateID: state.ID, Identity: identity,
		ProviderEmail: providerEmail, User: account, Settings: settings, DefaultProfilePictureJob: defaultJob,
		Capabilities: capabilities}
	resource := model.Resource{}
	switch invitation.Purpose {
	case model.InvitationPurposeStudentClass:
		input.Affiliation = &model.Affiliation{UserID: account.ID, Kind: model.AffiliationStudent, StartsAt: effectiveStart}
		input.Affiliation.PrepareCreate(model.NewAffiliationID(), at)
		input.ClassMember = &model.ClassMember{ClassID: invitation.ClassID, AcademicPeriodID: invitation.AcademicPeriodID,
			UserID: account.ID, StartsAt: effectiveStart, EndsAt: invitation.IntendedEndsAt}
		input.ClassMember.PrepareCreate(model.NewClassMemberID(), at)
		input.RequiredActions = []model.Action{model.ActionInvitationCreate, model.ActionClassMembersManage}
		resource = model.Resource{Type: model.ResourceClass, ID: invitation.ClassID.String()}
	case model.InvitationPurposeTeacherAcademicUnit:
		input.Affiliation = &model.Affiliation{UserID: account.ID, Kind: model.AffiliationTeacher, StartsAt: effectiveStart}
		input.Affiliation.PrepareCreate(model.NewAffiliationID(), at)
		input.AcademicUnitMember = &model.AcademicUnitMember{AcademicUnitID: invitation.AcademicUnitID,
			UserID: account.ID, StartsAt: effectiveStart, EndsAt: invitation.IntendedEndsAt}
		input.AcademicUnitMember.PrepareCreate(model.NewAcademicUnitMemberID(), at)
		input.RoleBinding = &model.RoleBinding{UserID: account.ID, RoleID: invitation.RoleID, OriginInvitationID: invitation.ID,
			OriginAcademicUnitMemberID: input.AcademicUnitMember.ID, ScopeType: model.RoleScopeAcademicUnit,
			ScopeID: invitation.AcademicUnitID.String(), StartsAt: effectiveStart, EndsAt: invitation.IntendedEndsAt}
		input.RoleBinding.PrepareCreate(model.NewRoleBindingID(), at)
		input.RequiredActions = []model.Action{model.ActionInvitationCreate, model.ActionAcademicUnitMembersManage}
		resource = model.Resource{Type: model.ResourceAcademicUnit, ID: invitation.AcademicUnitID.String()}
	case model.InvitationPurposeAcademicUnitRole, model.InvitationPurposeInstitutionRole:
		input.RoleBinding = &model.RoleBinding{UserID: account.ID, RoleID: invitation.RoleID, OriginInvitationID: invitation.ID,
			ScopeType: invitation.ScopeType, ScopeID: invitation.ScopeID, StartsAt: effectiveStart, EndsAt: invitation.IntendedEndsAt}
		input.RoleBinding.PrepareCreate(model.NewRoleBindingID(), at)
		input.RequiredActions = []model.Action{model.ActionInvitationCreate, model.ActionRoleBindingManage}
		resource = model.Resource{Type: model.ResourceAcademicUnit, ID: invitation.ScopeID}
		if invitation.Purpose == model.InvitationPurposeInstitutionRole {
			resource.Type = model.ResourceInstitution
		}
	default:
		return nil, store.NewErrInvalidInput("invitation", "purpose", nil)
	}
	if invitation.Purpose == model.InvitationPurposeStudentClass || invitation.Purpose == model.InvitationPurposeTeacherAcademicUnit {
		prepared, prepareErr := s.mail.PrepareInvitationAccepted(mail.NoticePreparation{Recipient: account, At: at})
		if prepareErr == nil {
			input.Notice = &store.PreparedMail{Occurrence: prepared.Occurrence, Delivery: prepared.Delivery, Job: prepared.Job}
		}
	}
	auditParameters, err := model.EncodeAuditData(map[string]string{"provider": state.Provider})
	if err != nil {
		return nil, err
	}
	input.AuditEvent = &model.AuditEvent{ActorID: account.ID, SubjectUserID: account.ID, Action: string(model.ActionInvitationAccept), Resource: resource,
		ScopeType: invitation.ScopeType, ScopeID: invitation.ScopeID, Status: model.AuditStatusSuccess,
		RequestID: metadata.RequestID, NodeID: s.nodeID, ClientType: "web", AuthMethod: authenticationMethod,
		IPAddress: metadata.IPAddress, UserAgent: metadata.UserAgent, Parameters: auditParameters}
	result, err := s.invitations.AcceptExternalIdentity(ctx, input)
	if err != nil {
		return nil, err
	}
	if result.User != nil && (result.AcademicUnitMember != nil || result.RoleBinding != nil) {
		s.membershipEffects.InvalidateAuthorization(ctx, result.User.ID)
	}
	if result.ClassMember != nil {
		if effectErr := s.membershipEffects.MembershipChanged(ctx, result.ClassMember.UserID,
			[]model.ClassID{result.ClassMember.ClassID}); effectErr != nil {
			s.membershipEffects.Report(ctx, "invitation.external_student_class_membership_changed", effectErr)
		}
	}
	return result, nil
}

func (s *Service) acceptScopedRole(ctx context.Context, invocation invocation.Invocation, claim, source string, purpose model.InvitationPurpose) (*InvitationAcceptanceView, error) {
	if err := s.checkInvitationAttempt(ctx, model.HashInvitationClaim(claim), source); err != nil {
		return nil, err
	}
	principal := invocation.Principal()
	if principal.Validate() != nil || principal.CredentialType != model.CredentialSessionAccess {
		return nil, access.InvalidTokenAppError()
	}
	if !model.IsValidCredentialToken(claim) {
		return nil, apperror.New("invitation.invalid")
	}
	claimHash := model.HashInvitationClaim(claim)
	return s.acceptScopedRoleByClaimHash(ctx, invocation, claimHash, purpose, nil)
}

func (s *Service) acceptScopedRoleByClaimHash(
	ctx context.Context,
	invocation invocation.Invocation,
	claimHash string,
	purpose model.InvitationPurpose,
	browserTransaction *store.BrowserInvitationTransactionProof,
) (*InvitationAcceptanceView, error) {
	principal := invocation.Principal()
	if principal.Validate() != nil || principal.CredentialType != model.CredentialSessionAccess || !model.IsValidTokenHash(claimHash) {
		return nil, access.InvalidTokenAppError()
	}
	invitation, err := s.invitations.GetByClaimHash(ctx, claimHash)
	if err != nil || invitation.Purpose != purpose {
		return nil, invalidInvitationError(err)
	}
	at := model.TimeUTC(s.now())
	binding := &model.RoleBinding{UserID: principal.UserID, RoleID: invitation.RoleID, OriginInvitationID: invitation.ID,
		ScopeType: invitation.ScopeType, ScopeID: invitation.ScopeID, StartsAt: invitation.EffectiveStartsAt(at), EndsAt: invitation.IntendedEndsAt}
	binding.PrepareCreate(model.NewRoleBindingID(), at)
	if err = binding.Validate(); err != nil {
		return nil, apperror.New("invitation.invalid").Wrap(err)
	}
	resourceType := model.ResourceAcademicUnit
	if purpose == model.InvitationPurposeInstitutionRole {
		resourceType = model.ResourceInstitution
	}
	result, err := access.RunAudited(ctx, s.auditor, access.Mutation{
		Invocation: invocation, Action: model.ActionInvitationAccept,
		Resource:  model.Resource{Type: resourceType, ID: invitation.ScopeID},
		ScopeType: invitation.ScopeType, ScopeID: invitation.ScopeID,
		Operation: "accept_scoped_role_invitation", Value: invitation.Auditable(), Subject: principal.UserID,
	}, s.now, func(ctx context.Context, attempt access.MutationReference) (*store.ScopedRoleInvitationAcceptanceResult, error) {
		return s.invitations.AcceptScopedRole(ctx, &store.ScopedRoleInvitationAcceptance{ClaimHash: claimHash,
			UserID: principal.UserID, RoleBinding: binding, AuditEventID: attempt.ID, AuditAt: attempt.MutationAt,
			RequiredActions:    []model.Action{model.ActionInvitationCreate, model.ActionRoleBindingManage},
			BrowserTransaction: browserTransaction})
	}, invalidInvitationError)
	if err != nil {
		return nil, err
	}
	if !result.Replayed && result.User != nil {
		s.membershipEffects.InvalidateAuthorization(ctx, result.User.ID)
	}
	return &InvitationAcceptanceView{Invitation: invitationView(result.Invitation), User: result.User,
		RoleBinding: result.RoleBinding, Replayed: result.Replayed}, nil
}
