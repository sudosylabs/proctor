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
	"github.com/sudosylabs/proctor/server/app/idempotency"
	"github.com/sudosylabs/proctor/server/app/identity"
	"github.com/sudosylabs/proctor/server/app/internal/batchitem"
	"github.com/sudosylabs/proctor/server/app/invocation"
	"github.com/sudosylabs/proctor/server/model"
	"github.com/sudosylabs/proctor/server/store"
)

// IssueAcademicUnitRoleInvitation invites a holder of a delegable Role at an Academic
// Unit and mails the claim.
func (s *Service) IssueAcademicUnitRoleInvitation(
	ctx context.Context,
	inv invocation.Invocation,
	command IssueAcademicUnitRoleInvitationCommand,
) (InvitationView, error) {
	if !command.AcademicUnitID.IsValid() {
		return InvitationView{}, apperror.Invalid("request.invalid", "academic_unit_id")
	}

	roleID, err := model.ParseRoleID(strings.TrimSpace(command.RoleID.String()))
	if err != nil {
		return InvitationView{}, apperror.New("request.invalid").WithField("field", "role_id").Wrap(err)
	}

	lifetime, err := invitationLifetime(command.ExpiresInDays, model.InvitationLifetime)
	if err != nil {
		return InvitationView{}, err
	}

	resource := model.Resource{Type: model.ResourceAcademicUnit, ID: command.AcademicUnitID.String()}
	for _, action := range []model.Action{model.ActionInvitationCreate, model.ActionRoleBindingManage} {
		if err = s.authorize(ctx, inv, action, resource); err != nil {
			return InvitationView{}, err
		}
	}

	unit, err := s.academicUnits.Get(ctx, command.AcademicUnitID)
	if err != nil || unit.ID != command.AcademicUnitID || unit.IsArchived() {
		return InvitationView{}, invitationStoreErrors.Translate(err)
	}

	role, err := s.roles.Get(ctx, roleID)
	if err != nil || role.ID != roleID || role.IsArchived() {
		return InvitationView{}, invitationStoreErrors.Translate(err)
	}
	if err = validateInvitationDelegableRole(role, model.RoleScopeAcademicUnit); err != nil {
		return InvitationView{}, err
	}
	if err = s.authorizeActionDelegation(ctx, inv, role.Permissions, model.RoleScopeAcademicUnit, command.AcademicUnitID.String()); err != nil {
		return InvitationView{}, err
	}

	semantic := command
	semantic.IdempotencyKey = ""
	commandIdempotency, err := idempotency.Prepare(inv, "invitation.academic_unit_role.issue", command.IdempotencyKey, semantic)
	if err != nil {
		return InvitationView{}, err
	}

	batchitem.BindOnboardingImport(commandIdempotency, command.onboardingImportID, command.onboardingImportRowNumber)
	return s.issueAuthorizedScopedRole(ctx, inv, authorizedScopedRoleInvitationIssue{
		targetEmail:               command.TargetEmail,
		purpose:                   model.InvitationPurposeAcademicUnitRole,
		resource:                  resource,
		scopeType:                 model.RoleScopeAcademicUnit,
		scopeID:                   unit.ID.String(),
		operation:                 "issue_academic_unit_role",
		academicUnitID:            unit.ID,
		role:                      role,
		intendedStartsAt:          command.IntendedStartsAt,
		intendedEndsAt:            command.IntendedEndsAt,
		lifetime:                  lifetime,
		idempotency:               commandIdempotency,
		batchDuplicate:            command.batchDuplicate,
		batchCanonicalKey:         command.batchCanonicalKey,
		onboardingImportID:        command.onboardingImportID,
		onboardingImportRowNumber: command.onboardingImportRowNumber,
	})
}

// IssueInstitutionRoleInvitation invites a holder of a delegable Role at the Institution
// and mails the claim.
func (s *Service) IssueInstitutionRoleInvitation(
	ctx context.Context,
	inv invocation.Invocation,
	command IssueInstitutionRoleInvitationCommand,
) (InvitationView, error) {
	if err := access.RequireStrongRecentSession(inv.Principal(), s.now(), s.recentAuthenticationTTL); err != nil {
		return InvitationView{}, err
	}

	if !command.InstitutionID.IsValid() {
		return InvitationView{}, apperror.Invalid("request.invalid", "institution_id")
	}

	roleID, err := model.ParseRoleID(strings.TrimSpace(command.RoleID.String()))
	if err != nil {
		return InvitationView{}, apperror.New("request.invalid").WithField("field", "role_id").Wrap(err)
	}

	lifetime, err := invitationLifetime(command.ExpiresInDays, model.InvitationLifetime)
	if err != nil {
		return InvitationView{}, err
	}

	resource := model.Resource{Type: model.ResourceInstitution, ID: command.InstitutionID.String()}
	for _, action := range []model.Action{model.ActionInvitationCreate, model.ActionRoleBindingManage} {
		if err = s.authorize(ctx, inv, action, resource); err != nil {
			return InvitationView{}, err
		}
	}

	role, err := s.roles.Get(ctx, roleID)
	if err != nil || role.ID != roleID || role.IsArchived() {
		return InvitationView{}, invitationStoreErrors.Translate(err)
	}
	if err = validateInvitationDelegableRole(role, model.RoleScopeInstitution); err != nil {
		return InvitationView{}, err
	}
	if err = s.authorizeActionDelegation(ctx, inv, role.Permissions, model.RoleScopeInstitution, command.InstitutionID.String()); err != nil {
		return InvitationView{}, err
	}

	semantic := command
	semantic.IdempotencyKey = ""
	commandIdempotency, err := idempotency.Prepare(inv, "invitation.institution_role.issue", command.IdempotencyKey, semantic)
	if err != nil {
		return InvitationView{}, err
	}

	batchitem.BindOnboardingImport(commandIdempotency, command.onboardingImportID, command.onboardingImportRowNumber)
	return s.issueAuthorizedScopedRole(ctx, inv, authorizedScopedRoleInvitationIssue{
		targetEmail:               command.TargetEmail,
		purpose:                   model.InvitationPurposeInstitutionRole,
		resource:                  resource,
		scopeType:                 model.RoleScopeInstitution,
		scopeID:                   command.InstitutionID.String(),
		operation:                 "issue_institution_role",
		role:                      role,
		intendedStartsAt:          command.IntendedStartsAt,
		intendedEndsAt:            command.IntendedEndsAt,
		lifetime:                  lifetime,
		idempotency:               commandIdempotency,
		batchDuplicate:            command.batchDuplicate,
		batchCanonicalKey:         command.batchCanonicalKey,
		onboardingImportID:        command.onboardingImportID,
		onboardingImportRowNumber: command.onboardingImportRowNumber,
	})
}

func (s *Service) issueAuthorizedScopedRole(
	ctx context.Context,
	inv invocation.Invocation,
	issue authorizedScopedRoleInvitationIssue,
) (InvitationView, error) {
	attempt := access.Mutation{
		Invocation: inv,
		Action:     model.ActionInvitationCreate,
		Resource:   issue.resource,
		ScopeType:  issue.scopeType,
		ScopeID:    issue.scopeID,
		Operation:  issue.operation,
		Value: map[string]any{
			"purpose":          issue.purpose,
			"academic_unit_id": issue.academicUnitID.String(),
			"role_id":          issue.role.ID.String(),
		},
	}
	if recovered, ok, recoverErr := s.recoverInvitationIssueOutcome(ctx, issue.idempotency, attempt); ok {
		return recovered, recoverErr
	}

	issuedAt := model.TimeUTC(s.now())
	startsAt := model.TimeFromMillis(issue.intendedStartsAt)
	if startsAt.IsZero() {
		startsAt = issuedAt
	}

	if issue.onboardingImportID.IsValid() && !issue.batchDuplicate {
		candidate, candidateErr := model.NewScopedRoleInvitation(model.ScopedRoleInvitationInput{
			ID:               model.NewInvitationID(),
			Purpose:          issue.purpose,
			TargetEmail:      issue.targetEmail,
			AcademicUnitID:   issue.academicUnitID,
			RoleID:           issue.role.ID,
			RoleActions:      issue.role.Permissions,
			IntendedStartsAt: startsAt,
			IntendedEndsAt:   model.OptionalTimeFromMillis(issue.intendedEndsAt),
			InviterUserID:    inv.Principal().UserID,
			ScopeType:        issue.scopeType,
			ScopeID:          issue.scopeID,
			ClaimHash:        strings.Repeat("0", model.TokenHashLength),
			IssuedAt:         issuedAt,
			Lifetime:         issue.lifetime,
		})
		if candidateErr != nil {
			return InvitationView{}, apperror.DomainInvalid("invitation.invalid", candidateErr)
		}

		_, noOp, noOpErr := s.invitations.ResolveOnboardingInvitationNoOp(ctx, candidate)
		if noOpErr != nil {
			return InvitationView{}, invitationStoreErrors.Translate(noOpErr)
		}
		if noOp {
			return s.completeOnboardingInvitationNoOp(ctx, inv, attempt, candidate, issue.idempotency)
		}
	}

	if !s.mail.Enabled() {
		return InvitationView{}, apperror.New("invitation.mail_unavailable")
	}

	if issue.batchDuplicate {
		candidate, candidateErr := model.NewScopedRoleInvitation(model.ScopedRoleInvitationInput{
			ID:               model.NewInvitationID(),
			Purpose:          issue.purpose,
			TargetEmail:      issue.targetEmail,
			AcademicUnitID:   issue.academicUnitID,
			RoleID:           issue.role.ID,
			RoleActions:      issue.role.Permissions,
			IntendedStartsAt: startsAt,
			IntendedEndsAt:   model.OptionalTimeFromMillis(issue.intendedEndsAt),
			InviterUserID:    inv.Principal().UserID,
			ScopeType:        issue.scopeType,
			ScopeID:          issue.scopeID,
			ClaimHash:        strings.Repeat("0", model.TokenHashLength),
			IssuedAt:         issuedAt,
			Lifetime:         issue.lifetime,
		})
		if candidateErr != nil {
			return InvitationView{}, apperror.DomainInvalid("invitation.invalid", candidateErr)
		}

		attempt.Value = candidate.Auditable()
		return s.recordDuplicateInvitationView(
			ctx,
			issue.idempotency,
			attempt,
			store.InvitationBatchDuplicate{Candidate: candidate},
			issue.batchCanonicalKey,
		)
	}

	rawClaim := s.newToken()
	if !model.IsValidCredentialToken(rawClaim) {
		return InvitationView{}, apperror.New("invitation.unavailable")
	}

	invitation, err := model.NewScopedRoleInvitation(model.ScopedRoleInvitationInput{
		ID:               model.NewInvitationID(),
		Purpose:          issue.purpose,
		TargetEmail:      issue.targetEmail,
		AcademicUnitID:   issue.academicUnitID,
		RoleID:           issue.role.ID,
		RoleActions:      issue.role.Permissions,
		IntendedStartsAt: startsAt,
		IntendedEndsAt:   model.OptionalTimeFromMillis(issue.intendedEndsAt),
		InviterUserID:    inv.Principal().UserID,
		ScopeType:        issue.scopeType,
		ScopeID:          issue.scopeID,
		ClaimHash:        model.HashInvitationClaim(rawClaim),
		IssuedAt:         issuedAt,
		Lifetime:         issue.lifetime,
	})
	if err != nil {
		return InvitationView{}, apperror.DomainInvalid("invitation.invalid", err)
	}

	actionURL, err := identity.AccountCredentialLink(s.publicURL, "/join", rawClaim)
	if err != nil {
		return InvitationView{}, apperror.New("invitation.unavailable").Wrap(err)
	}

	prepared, err := s.mail.PrepareInvitation(invitation, actionURL)
	if err != nil {
		return InvitationView{}, apperror.New("invitation.mail_unavailable").Wrap(err)
	}

	attempt.Value = invitation.Auditable()
	attempt.At = issuedAt
	created, err := access.RunAudited(
		ctx,
		s.auditor,
		attempt,
		func(ctx context.Context, reference access.MutationReference) (*store.InvitationCommandResult, error) {
			input := &store.ScopedRoleInvitationIssue{
				Invitation:   invitation,
				Lifetime:     issue.lifetime,
				Occurrence:   prepared.Occurrence,
				Delivery:     prepared.Delivery,
				DeliveryJob:  prepared.Job,
				AuditEventID: reference.ID,
				AuditAt:      reference.MutationAt,
			}
			if issue.idempotency != nil {
				value, storeErr := s.invitations.IssueScopedRoleIdempotently(ctx, input, issue.idempotency)
				if storeErr == nil && value.Duplicate {
					return nil, apperror.New("onboarding_batch.duplicate")
				}

				return value, storeErr
			}

			value, storeErr := s.invitations.IssueScopedRole(ctx, input)
			return &store.InvitationCommandResult{Invitation: value}, storeErr
		},
		invitationStoreErrors.Translate,
	)
	if err != nil {
		return InvitationView{}, err
	}

	view := invitationView(created.Invitation)
	view.Replayed = created.Replayed
	view.NoOp = created.NoOp
	return view, nil
}

func validateInvitationDelegableRole(role *model.Role, scopeType model.RoleScopeType) error {
	if role == nil || role.IsArchived() || role.BuiltIn || role.Name == model.SystemAdministratorRoleName || len(role.Permissions) == 0 {
		return apperror.New("invitation.role_not_delegable")
	}
	for _, action := range role.Permissions {
		definition, ok := model.DefinitionForAction(model.Action(action))
		if !ok || definition.RelationshipOnly || !definition.SupportsRoleScope(scopeType) {
			return apperror.New("invitation.role_not_delegable")
		}
	}
	return nil
}
