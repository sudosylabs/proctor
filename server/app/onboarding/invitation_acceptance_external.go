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

	"github.com/sudosylabs/proctor/server/app/academic"
	"github.com/sudosylabs/proctor/server/app/identity"
	"github.com/sudosylabs/proctor/server/app/jobs"
	"github.com/sudosylabs/proctor/server/app/mail"
	"github.com/sudosylabs/proctor/server/app/user"
	"github.com/sudosylabs/proctor/server/model"
	"github.com/sudosylabs/proctor/server/store"
)

func (s *Service) acceptExternalIdentity(
	ctx context.Context,
	state *model.ExternalLoginState,
	assertion *model.ExternalAuthenticationAssertion,
	capabilities store.AccessDeploymentCapabilities,
	metadata model.RequestMetadata,
	authenticationMethod string,
) (*store.ExternalIdentityInvitationAcceptanceResult, error) {
	if state == nil ||
		state.Purpose != model.ExternalAuthenticationPurposeInvitationAdmission ||
		!state.ID.IsValid() ||
		!state.InvitationID.IsValid() ||
		!state.ConsumedAt.Valid ||
		assertion == nil ||
		assertion.ProviderID != state.Provider ||
		!assertion.EmailVerified {
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
	account, defaultJob, preparationErr := jobs.PrepareUserDefaultProfilePictureJob(userCandidate, at)
	var settings *model.UserSettingsDocument
	if preparationErr == nil {
		settings, preparationErr = user.PrepareInitialSettingsDocument(account)
	}

	if preparationErr != nil {
		userCandidate.PrepareCreate(model.NewUserID(), at)
		account, settings, defaultJob = userCandidate, nil, nil
	}

	externalIdentity := &model.ExternalIdentity{
		UserID:     account.ID,
		Provider:   state.Provider,
		Subject:    assertion.Subject,
		LastSeenAt: model.OptionalTimeFrom(at),
	}
	effectiveStart := invitation.EffectiveStartsAt(at)
	input := &store.ExternalIdentityInvitationAcceptance{
		ExternalStateID:          state.ID,
		Identity:                 externalIdentity,
		ProviderEmail:            providerEmail,
		User:                     account,
		Settings:                 settings,
		DefaultProfilePictureJob: defaultJob,
		Capabilities:             capabilities,
	}
	resource := model.Resource{}
	switch invitation.Purpose {
	case model.InvitationPurposeStudentClass:
		input.Affiliation = &model.Affiliation{UserID: account.ID, Kind: model.AffiliationStudent, StartsAt: effectiveStart}
		input.Affiliation.PrepareCreate(model.NewAffiliationID(), at)
		input.ClassMember = &model.ClassMember{
			ClassID:          invitation.ClassID,
			AcademicPeriodID: invitation.AcademicPeriodID,
			UserID:           account.ID,
			StartsAt:         effectiveStart,
			EndsAt:           invitation.IntendedEndsAt,
		}
		input.ClassMember.PrepareCreate(model.NewClassMemberID(), at)
		input.RequiredActions = []model.Action{model.ActionInvitationCreate, model.ActionClassMembersManage}
		resource = model.Resource{Type: model.ResourceClass, ID: invitation.ClassID.String()}
	case model.InvitationPurposeTeacherAcademicUnit:
		input.Affiliation = &model.Affiliation{UserID: account.ID, Kind: model.AffiliationTeacher, StartsAt: effectiveStart}
		input.Affiliation.PrepareCreate(model.NewAffiliationID(), at)
		input.AcademicUnitMember = &model.AcademicUnitMember{
			AcademicUnitID: invitation.AcademicUnitID,
			UserID:         account.ID,
			StartsAt:       effectiveStart,
			EndsAt:         invitation.IntendedEndsAt,
		}
		input.AcademicUnitMember.PrepareCreate(model.NewAcademicUnitMemberID(), at)
		input.RoleBinding = &model.RoleBinding{
			UserID:                     account.ID,
			RoleID:                     invitation.RoleID,
			OriginInvitationID:         invitation.ID,
			OriginAcademicUnitMemberID: input.AcademicUnitMember.ID,
			ScopeType:                  model.RoleScopeAcademicUnit,
			ScopeID:                    invitation.AcademicUnitID.String(),
			StartsAt:                   effectiveStart,
			EndsAt:                     invitation.IntendedEndsAt,
		}
		input.RoleBinding.PrepareCreate(model.NewRoleBindingID(), at)
		input.RequiredActions = []model.Action{model.ActionInvitationCreate, model.ActionAcademicUnitMembersManage}
		resource = model.Resource{Type: model.ResourceAcademicUnit, ID: invitation.AcademicUnitID.String()}
	case model.InvitationPurposeAcademicUnitRole, model.InvitationPurposeInstitutionRole:
		input.RoleBinding = &model.RoleBinding{
			UserID:             account.ID,
			RoleID:             invitation.RoleID,
			OriginInvitationID: invitation.ID,
			ScopeType:          invitation.ScopeType,
			ScopeID:            invitation.ScopeID,
			StartsAt:           effectiveStart,
			EndsAt:             invitation.IntendedEndsAt,
		}
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

	input.AuditEvent = &model.AuditEvent{
		ActorID:       account.ID,
		SubjectUserID: account.ID,
		Action:        string(model.ActionInvitationAccept),
		Resource:      resource,
		ScopeType:     invitation.ScopeType,
		ScopeID:       invitation.ScopeID,
		Status:        model.AuditStatusSuccess,
		RequestID:     metadata.RequestID,
		NodeID:        s.nodeID,
		ClientType:    "web",
		AuthMethod:    authenticationMethod,
		IPAddress:     metadata.IPAddress,
		UserAgent:     metadata.UserAgent,
		Parameters:    auditParameters,
	}
	result, err := s.invitations.AcceptExternalIdentity(ctx, input)
	if err != nil {
		return nil, err
	}
	if result.User != nil && (result.AcademicUnitMember != nil || result.RoleBinding != nil) {
		s.publisher.InvalidateAuthorization(ctx, result.User.ID)
	}

	if result.ClassMember != nil {
		if effectErr := academic.PublishClassMembershipChanged(
			ctx,
			s.publisher,
			s.sittings,
			result.ClassMember.UserID,
			[]model.ClassID{result.ClassMember.ClassID},
		); effectErr != nil {
			s.publisher.Report(ctx, "invitation.external_student_class_membership_changed", effectErr)
		}
	}

	return result, nil
}
