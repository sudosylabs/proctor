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
	"time"

	"github.com/sudosylabs/proctor/server/app/access"
	"github.com/sudosylabs/proctor/server/app/apperror"
	"github.com/sudosylabs/proctor/server/app/idempotency"
	"github.com/sudosylabs/proctor/server/app/identity"
	"github.com/sudosylabs/proctor/server/app/internal/batchitem"
	"github.com/sudosylabs/proctor/server/app/invocation"
	"github.com/sudosylabs/proctor/server/model"
	"github.com/sudosylabs/proctor/server/store"
)

// IssueTeacherAcademicUnitInvitation invites a teacher into an Academic Unit with a
// delegable Role and mails the claim.
func (s *Service) IssueTeacherAcademicUnitInvitation(
	ctx context.Context,
	invocation invocation.Invocation,
	command IssueTeacherAcademicUnitInvitationCommand,
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
	if err = s.authorize(ctx, invocation, model.ActionInvitationCreate, resource); err != nil {
		return InvitationView{}, err
	}
	if err = s.authorize(ctx, invocation, model.ActionAcademicUnitMembersManage, resource); err != nil {
		return InvitationView{}, err
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
	if err = s.authorizeActionDelegation(ctx, invocation, role.Permissions, model.RoleScopeAcademicUnit, command.AcademicUnitID.String()); err != nil {
		return InvitationView{}, err
	}

	semantic := command
	semantic.IdempotencyKey = ""
	commandIdempotency, err := idempotency.Prepare(invocation, "invitation.teacher_academic_unit.issue", command.IdempotencyKey, semantic)
	if err != nil {
		return InvitationView{}, err
	}

	batchitem.BindOnboardingImport(commandIdempotency, command.onboardingImportID, command.onboardingImportRowNumber)
	attempt := access.Mutation{
		Invocation: invocation,
		Action:     model.ActionInvitationCreate,
		Resource:   resource,
		ScopeType:  model.RoleScopeAcademicUnit,
		ScopeID:    unit.ID.String(),
		Operation:  "issue_teacher_academic_unit",
		Value: map[string]any{
			"purpose":          model.InvitationPurposeTeacherAcademicUnit,
			"academic_unit_id": unit.ID.String(),
			"role_id":          role.ID.String(),
		},
	}
	if recovered, ok, recoverErr := s.recoverInvitationIssueOutcome(ctx, commandIdempotency, attempt); ok {
		return recovered, recoverErr
	}

	issuedAt := model.TimeUTC(s.now())
	startsAt := model.TimeFromMillis(command.IntendedStartsAt)
	if startsAt.IsZero() {
		startsAt = issuedAt
	}

	placeholderClaimHash := strings.Repeat("0", model.TokenHashLength)
	if command.onboardingImportID.IsValid() && !command.batchDuplicate {
		candidate, candidateErr := newTeacherAcademicUnitInvitationValue(
			invocation,
			command,
			unit.ID,
			role,
			startsAt,
			issuedAt,
			lifetime,
			placeholderClaimHash,
		)
		if candidateErr != nil {
			return InvitationView{}, candidateErr
		}
		if view, handled, noOpErr := s.resolveOnboardingInvitationNoOp(ctx, invocation, attempt, candidate, commandIdempotency); handled {
			return view, noOpErr
		}
	}

	if !s.mail.Enabled() {
		return InvitationView{}, apperror.New("invitation.mail_unavailable")
	}

	if command.batchDuplicate {
		candidate, candidateErr := newTeacherAcademicUnitInvitationValue(
			invocation,
			command,
			unit.ID,
			role,
			startsAt,
			issuedAt,
			lifetime,
			placeholderClaimHash,
		)
		if candidateErr != nil {
			return InvitationView{}, candidateErr
		}

		attempt.Value = candidate.Auditable()
		return s.recordDuplicateInvitationView(
			ctx,
			commandIdempotency,
			attempt,
			store.InvitationBatchDuplicate{Candidate: candidate},
			command.batchCanonicalKey,
		)
	}

	rawClaim := s.newToken()
	if !model.IsValidCredentialToken(rawClaim) {
		return InvitationView{}, apperror.New("invitation.unavailable")
	}

	invitation, err := newTeacherAcademicUnitInvitationValue(
		invocation,
		command,
		unit.ID,
		role,
		startsAt,
		issuedAt,
		lifetime,
		model.HashInvitationClaim(rawClaim),
	)
	if err != nil {
		return InvitationView{}, err
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
	created, err := access.RunAudited(
		ctx,
		s.auditor,
		attempt,
		func() time.Time { return issuedAt },
		func(ctx context.Context, reference access.MutationReference) (*store.InvitationCommandResult, error) {
			return s.storeTeacherAcademicUnitInvitation(ctx, commandIdempotency, invitation, prepared, reference)
		},
		invitationStoreErrors.Translate,
	)
	if err != nil {
		return InvitationView{}, err
	}

	return issuedInvitationView(created), nil
}

// newTeacherAcademicUnitInvitationValue builds the Teacher Academic Unit Invitation for the command with the given claim hash.
func newTeacherAcademicUnitInvitationValue(
	invocation invocation.Invocation,
	command IssueTeacherAcademicUnitInvitationCommand,
	unitID model.AcademicUnitID,
	role *model.Role,
	startsAt, issuedAt time.Time,
	lifetime time.Duration,
	claimHash string,
) (*model.Invitation, error) {
	value, err := model.NewTeacherAcademicUnitInvitation(model.TeacherAcademicUnitInvitationInput{
		ID:               model.NewInvitationID(),
		TargetEmail:      command.TargetEmail,
		AcademicUnitID:   unitID,
		RoleID:           role.ID,
		RoleActions:      role.Permissions,
		IntendedStartsAt: startsAt,
		IntendedEndsAt:   model.OptionalTimeFromMillis(command.IntendedEndsAt),
		Suggestions: model.InvitationProfileSuggestions{
			Username:    command.SuggestedUsername,
			DisplayName: command.SuggestedDisplayName,
			FirstName:   command.SuggestedFirstName,
			LastName:    command.SuggestedLastName,
			Locale:      command.SuggestedLocale,
			Timezone:    command.SuggestedTimezone,
		},
		InviterUserID: invocation.Principal().UserID,
		ScopeType:     model.RoleScopeAcademicUnit,
		ScopeID:       unitID.String(),
		ClaimHash:     claimHash,
		IssuedAt:      issuedAt,
		Lifetime:      lifetime,
	})
	if err != nil {
		return nil, apperror.DomainInvalid("invitation.invalid", err)
	}

	return value, nil
}

// storeTeacherAcademicUnitInvitation records the audited Teacher Academic Unit Invitation issue, idempotently when a
// key is present.
func (s *Service) storeTeacherAcademicUnitInvitation(
	ctx context.Context,
	commandIdempotency *store.CommandIdempotency,
	invitation *model.Invitation,
	prepared *store.PreparedMail,
	reference access.MutationReference,
) (*store.InvitationCommandResult, error) {
	input := &store.TeacherAcademicUnitInvitationIssue{
		Invitation:   invitation,
		Lifetime:     invitation.ExpiresAt.Sub(invitation.CreatedAt),
		Occurrence:   prepared.Occurrence,
		Delivery:     prepared.Delivery,
		DeliveryJob:  prepared.Job,
		AuditEventID: reference.ID,
		AuditAt:      reference.MutationAt,
	}
	if commandIdempotency != nil {
		value, storeErr := s.invitations.IssueTeacherAcademicUnitIdempotently(ctx, input, commandIdempotency)
		if storeErr == nil && value.Duplicate {
			return nil, apperror.New("onboarding_batch.duplicate")
		}

		return value, storeErr
	}

	value, storeErr := s.invitations.IssueTeacherAcademicUnit(ctx, input)
	return &store.InvitationCommandResult{Invitation: value}, storeErr
}
