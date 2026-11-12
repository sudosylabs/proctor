// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package onboarding

import (
	"context"

	"github.com/sudosylabs/proctor/server/app/academic"
	"github.com/sudosylabs/proctor/server/app/access"
	"github.com/sudosylabs/proctor/server/app/apperror"
	"github.com/sudosylabs/proctor/server/app/identity"
	"github.com/sudosylabs/proctor/server/app/invocation"
	"github.com/sudosylabs/proctor/server/app/jobs"
	"github.com/sudosylabs/proctor/server/app/mail"
	"github.com/sudosylabs/proctor/server/app/user"
	"github.com/sudosylabs/proctor/server/model"
	"github.com/sudosylabs/proctor/server/store"
)

// AcceptStudentClassInvitation accepts a student Class Invitation by its raw claim,
// creating the local account and Class Membership it grants.
func (s *Service) AcceptStudentClassInvitation(
	ctx context.Context,
	inv invocation.Invocation,
	command AcceptStudentClassInvitationCommand,
) (*InvitationAcceptanceView, error) {
	if err := s.checkInvitationAttempt(ctx, model.HashInvitationClaim(command.Claim), command.Source); err != nil {
		return nil, err
	}
	if !model.IsValidCredentialToken(command.Claim) {
		return nil, apperror.New("invitation.invalid")
	}

	claimHash := model.HashInvitationClaim(command.Claim)
	return s.acceptStudentClassByClaimHash(ctx, inv, command, claimHash)
}

func (s *Service) acceptStudentClassByClaimHash(
	ctx context.Context,
	inv invocation.Invocation,
	command AcceptStudentClassInvitationCommand,
	claimHash string,
) (*InvitationAcceptanceView, error) {
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

	if err = identity.CheckNewPassword(command.Password, identity.PasswordIdentity{
		Username:    command.Username,
		Email:       invitation.TargetEmail,
		DisplayName: command.DisplayName,
		FirstName:   command.FirstName,
		LastName:    command.LastName,
	}); err != nil {
		return nil, err
	}

	hash, err := s.hasher.Hash(ctx, command.Password)
	if err != nil {
		return nil, identity.PasswordHashError(err, "invitation.unavailable")
	}

	account, defaultJob, err := jobs.PrepareUserDefaultProfilePictureJob(
		&model.User{
			Username:      command.Username,
			Email:         invitation.TargetEmail,
			EmailVerified: true,
			DisplayName:   command.DisplayName,
			FirstName:     command.FirstName,
			LastName:      command.LastName,
			Locale:        command.Locale,
			Timezone:      timezone,
		},
		at,
	)
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
	member := &model.ClassMember{
		ClassID:          invitation.ClassID,
		AcademicPeriodID: invitation.AcademicPeriodID,
		UserID:           account.ID,
		StartsAt:         effectiveStart,
		EndsAt:           invitation.IntendedEndsAt,
	}
	member.PrepareCreate(model.NewClassMemberID(), at)
	prepared, err := s.mail.PrepareInvitationAccepted(mail.NoticePreparation{Recipient: account, At: at})
	if err != nil {
		return nil, apperror.New("invitation.mail_unavailable").Wrap(err)
	}

	metadata := inv.RequestMetadata()
	event := &model.AuditEvent{
		ActorID:       account.ID,
		SubjectUserID: account.ID,
		Action:        string(model.ActionInvitationAccept),
		Resource:      model.Resource{Type: model.ResourceClass, ID: invitation.ClassID.String()},
		ScopeType:     model.RoleScopeClass,
		ScopeID:       invitation.ClassID.String(),
		Status:        model.AuditStatusSuccess,
		RequestID:     metadata.RequestID,
		NodeID:        s.nodeID,
		ClientType:    "web",
		AuthMethod:    "invitation",
		IPAddress:     metadata.IPAddress,
		UserAgent:     metadata.UserAgent,
	}
	result, err := s.invitations.AcceptStudentClass(ctx, &store.StudentClassInvitationAcceptance{
		ClaimHash:                claimHash,
		AcceptedAt:               at,
		User:                     account,
		Settings:                 settings,
		PasswordCredential:       credential,
		DefaultProfilePictureJob: defaultJob,
		Affiliation:              affiliation,
		ClassMember:              member,
		Occurrence:               prepared.Occurrence,
		Delivery:                 prepared.Delivery,
		DeliveryJob:              prepared.Job,
		AuditEvent:               event,
		RequiredActions:          []model.Action{model.ActionInvitationCreate, model.ActionClassMembersManage},
		BrowserTransaction:       command.browserTransaction,
	})
	if err != nil {
		return nil, invalidInvitationError(err)
	}

	if !result.Replayed {
		s.publishInvitationChanged(ctx, result.Invitation, "invitation.changed")
	}
	if !result.Replayed && result.ClassMember != nil {
		if effectErr := academic.PublishClassMembershipChanged(
			ctx,
			s.publisher,
			s.sittings,
			result.ClassMember.UserID,
			[]model.ClassID{result.ClassMember.ClassID},
		); effectErr != nil {
			s.publisher.Report(ctx, "invitation.student_class_membership_changed", effectErr)
		}
	}

	return &InvitationAcceptanceView{
		Invitation:  invitationView(result.Invitation),
		User:        result.User,
		Affiliation: result.Affiliation,
		ClassMember: result.ClassMember,
		Replayed:    result.Replayed,
	}, nil
}

// AcceptTeacherAcademicUnitInvitation accepts a teacher Academic Unit Invitation by its raw
// claim, creating the local account, membership and Role Binding it grants.
func (s *Service) AcceptTeacherAcademicUnitInvitation(
	ctx context.Context,
	inv invocation.Invocation,
	command AcceptTeacherAcademicUnitInvitationCommand,
) (*InvitationAcceptanceView, error) {
	if err := s.checkInvitationAttempt(ctx, model.HashInvitationClaim(command.Claim), command.Source); err != nil {
		return nil, err
	}
	if !model.IsValidCredentialToken(command.Claim) {
		return nil, apperror.New("invitation.invalid")
	}

	claimHash := model.HashInvitationClaim(command.Claim)
	return s.acceptTeacherAcademicUnitByClaimHash(ctx, inv, command, claimHash)
}

func (s *Service) acceptTeacherAcademicUnitByClaimHash(
	ctx context.Context,
	inv invocation.Invocation,
	command AcceptTeacherAcademicUnitInvitationCommand,
	claimHash string,
) (*InvitationAcceptanceView, error) {
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

	if err = identity.CheckNewPassword(command.Password, identity.PasswordIdentity{
		Username:    command.Username,
		Email:       invitation.TargetEmail,
		DisplayName: command.DisplayName,
		FirstName:   command.FirstName,
		LastName:    command.LastName,
	}); err != nil {
		return nil, err
	}

	hash, err := s.hasher.Hash(ctx, command.Password)
	if err != nil {
		return nil, identity.PasswordHashError(err, "invitation.unavailable")
	}

	account, defaultJob, err := jobs.PrepareUserDefaultProfilePictureJob(
		&model.User{
			Username:      command.Username,
			Email:         invitation.TargetEmail,
			EmailVerified: true,
			DisplayName:   command.DisplayName,
			FirstName:     command.FirstName,
			LastName:      command.LastName,
			Locale:        command.Locale,
			Timezone:      timezone,
		},
		at,
	)
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
	member := &model.AcademicUnitMember{
		AcademicUnitID: invitation.AcademicUnitID,
		UserID:         account.ID,
		StartsAt:       effectiveStart,
		EndsAt:         invitation.IntendedEndsAt,
	}
	member.PrepareCreate(model.NewAcademicUnitMemberID(), at)
	binding := &model.RoleBinding{
		UserID:                     account.ID,
		RoleID:                     invitation.RoleID,
		OriginInvitationID:         invitation.ID,
		OriginAcademicUnitMemberID: member.ID,
		ScopeType:                  model.RoleScopeAcademicUnit,
		ScopeID:                    invitation.AcademicUnitID.String(),
		StartsAt:                   effectiveStart,
		EndsAt:                     invitation.IntendedEndsAt,
	}
	binding.PrepareCreate(model.NewRoleBindingID(), at)
	prepared, err := s.mail.PrepareInvitationAccepted(mail.NoticePreparation{Recipient: account, At: at})
	if err != nil {
		return nil, apperror.New("invitation.mail_unavailable").Wrap(err)
	}

	metadata := inv.RequestMetadata()
	event := &model.AuditEvent{
		ActorID:       account.ID,
		SubjectUserID: account.ID,
		Action:        string(model.ActionInvitationAccept),
		Resource:      model.Resource{Type: model.ResourceAcademicUnit, ID: invitation.AcademicUnitID.String()},
		ScopeType:     model.RoleScopeAcademicUnit,
		ScopeID:       invitation.AcademicUnitID.String(),
		Status:        model.AuditStatusSuccess,
		RequestID:     metadata.RequestID,
		NodeID:        s.nodeID,
		ClientType:    "web",
		AuthMethod:    "invitation",
		IPAddress:     metadata.IPAddress,
		UserAgent:     metadata.UserAgent,
	}
	result, err := s.invitations.AcceptTeacherAcademicUnit(ctx, &store.TeacherAcademicUnitInvitationAcceptance{
		ClaimHash:                claimHash,
		AcceptedAt:               at,
		User:                     account,
		Settings:                 settings,
		PasswordCredential:       credential,
		DefaultProfilePictureJob: defaultJob,
		Affiliation:              affiliation,
		AcademicUnitMember:       member,
		RoleBinding:              binding,
		Occurrence:               prepared.Occurrence,
		Delivery:                 prepared.Delivery,
		DeliveryJob:              prepared.Job,
		AuditEvent:               event,
		RequiredActions:          []model.Action{model.ActionInvitationCreate, model.ActionAcademicUnitMembersManage},
		BrowserTransaction:       command.browserTransaction,
	})
	if err != nil {
		return nil, invalidInvitationError(err)
	}
	if !result.Replayed {
		s.publishInvitationChanged(ctx, result.Invitation, "invitation.changed")
	}
	if !result.Replayed && result.User != nil {
		s.publisher.InvalidateAuthorization(ctx, result.User.ID)
	}

	return &InvitationAcceptanceView{
		Invitation:         invitationView(result.Invitation),
		User:               result.User,
		Affiliation:        result.Affiliation,
		AcademicUnitMember: result.AcademicUnitMember,
		RoleBinding:        result.RoleBinding,
		Replayed:           result.Replayed,
	}, nil
}

// AcceptAcademicUnitRoleInvitation accepts an Academic Unit Role Invitation by its raw claim
// for the caller.
func (s *Service) AcceptAcademicUnitRoleInvitation(
	ctx context.Context,
	inv invocation.Invocation,
	command AcceptAcademicUnitRoleInvitationCommand,
) (*InvitationAcceptanceView, error) {
	return s.acceptScopedRole(ctx, inv, command.Claim, command.Source, model.InvitationPurposeAcademicUnitRole)
}

// AcceptInstitutionRoleInvitation accepts an Institution Role Invitation by its raw claim
// for the caller.
func (s *Service) AcceptInstitutionRoleInvitation(
	ctx context.Context,
	inv invocation.Invocation,
	command AcceptInstitutionRoleInvitationCommand,
) (*InvitationAcceptanceView, error) {
	return s.acceptScopedRole(ctx, inv, command.Claim, command.Source, model.InvitationPurposeInstitutionRole)
}

func (s *Service) acceptScopedRole(
	ctx context.Context,
	inv invocation.Invocation,
	claim, source string,
	purpose model.InvitationPurpose,
) (*InvitationAcceptanceView, error) {
	if err := s.checkInvitationAttempt(ctx, model.HashInvitationClaim(claim), source); err != nil {
		return nil, err
	}

	principal := inv.Principal()
	if principal.Validate() != nil || principal.CredentialType != model.CredentialSessionAccess {
		return nil, access.InvalidTokenAppError()
	}
	if !model.IsValidCredentialToken(claim) {
		return nil, apperror.New("invitation.invalid")
	}

	claimHash := model.HashInvitationClaim(claim)
	return s.acceptScopedRoleByClaimHash(ctx, inv, claimHash, purpose, nil)
}

func (s *Service) acceptScopedRoleByClaimHash(
	ctx context.Context,
	inv invocation.Invocation,
	claimHash string,
	purpose model.InvitationPurpose,
	browserTransaction *store.BrowserInvitationTransactionProof,
) (*InvitationAcceptanceView, error) {
	principal := inv.Principal()
	if principal.Validate() != nil || principal.CredentialType != model.CredentialSessionAccess || !model.IsValidTokenHash(claimHash) {
		return nil, access.InvalidTokenAppError()
	}

	invitation, err := s.invitations.GetByClaimHash(ctx, claimHash)
	if err != nil || invitation.Purpose != purpose {
		return nil, invalidInvitationError(err)
	}

	at := model.TimeUTC(s.now())
	binding := &model.RoleBinding{
		UserID:             principal.UserID,
		RoleID:             invitation.RoleID,
		OriginInvitationID: invitation.ID,
		ScopeType:          invitation.ScopeType,
		ScopeID:            invitation.ScopeID,
		StartsAt:           invitation.EffectiveStartsAt(at),
		EndsAt:             invitation.IntendedEndsAt,
	}
	binding.PrepareCreate(model.NewRoleBindingID(), at)
	if err = binding.Validate(); err != nil {
		return nil, apperror.New("invitation.invalid").Wrap(err)
	}

	resourceType := model.ResourceAcademicUnit
	if purpose == model.InvitationPurposeInstitutionRole {
		resourceType = model.ResourceInstitution
	}

	result, err := access.RunAudited(ctx, s.auditor, access.Mutation{
		Invocation: inv,
		Action:     model.ActionInvitationAccept,
		Resource:   model.Resource{Type: resourceType, ID: invitation.ScopeID},
		ScopeType:  invitation.ScopeType,
		ScopeID:    invitation.ScopeID,
		Operation:  "accept_scoped_role_invitation",
		Value:      invitation.Auditable(),
		Subject:    principal.UserID,
		At:         s.now(),
	}, func(ctx context.Context, attempt access.MutationReference) (*store.ScopedRoleInvitationAcceptanceResult, error) {
		return s.invitations.AcceptScopedRole(ctx, &store.ScopedRoleInvitationAcceptance{
			ClaimHash:          claimHash,
			UserID:             principal.UserID,
			RoleBinding:        binding,
			AuditEventID:       attempt.ID,
			AuditAt:            attempt.MutationAt,
			RequiredActions:    []model.Action{model.ActionInvitationCreate, model.ActionRoleBindingManage},
			BrowserTransaction: browserTransaction,
		})
	}, invalidInvitationError)
	if err != nil {
		return nil, err
	}
	if !result.Replayed {
		s.publishInvitationChanged(ctx, result.Invitation, "invitation.changed")
	}
	if !result.Replayed && result.User != nil {
		s.publisher.InvalidateAuthorization(ctx, result.User.ID)
	}

	return &InvitationAcceptanceView{
		Invitation:  invitationView(result.Invitation),
		User:        result.User,
		RoleBinding: result.RoleBinding,
		Replayed:    result.Replayed,
	}, nil
}
