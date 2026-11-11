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

// IssueStudentClassInvitation invites a Student into a Class and mails the claim.
func (s *Service) IssueStudentClassInvitation(
	ctx context.Context,
	inv invocation.Invocation,
	command IssueStudentClassInvitationCommand,
) (InvitationView, error) {
	if !command.ClassID.IsValid() {
		return InvitationView{}, apperror.Invalid("request.invalid", "class_id")
	}

	lifetime, err := invitationLifetime(command.ExpiresInDays, model.InvitationLifetime)
	if err != nil {
		return InvitationView{}, err
	}

	resource := model.Resource{Type: model.ResourceClass, ID: command.ClassID.String()}
	if err = s.authorize(ctx, inv, model.ActionInvitationCreate, resource); err != nil {
		return InvitationView{}, err
	}
	if err = s.authorize(ctx, inv, model.ActionClassMembersManage, resource); err != nil {
		return InvitationView{}, err
	}

	class, err := s.classes.Get(ctx, command.ClassID)
	if err != nil {
		return InvitationView{}, invitationStoreErrors.Translate(err)
	}

	period, err := s.periods.Get(ctx, class.AcademicPeriodID)
	if err != nil {
		return InvitationView{}, invitationStoreErrors.Translate(err)
	}
	if class.ID != command.ClassID || period.ID != class.AcademicPeriodID {
		return InvitationView{}, apperror.New("invitation.class_period_conflict")
	}

	semantic := command
	semantic.IdempotencyKey = ""
	commandIdempotency, err := idempotency.Prepare(inv, "invitation.student_class.issue", command.IdempotencyKey, semantic)
	if err != nil {
		return InvitationView{}, err
	}

	batchitem.BindOnboardingImport(commandIdempotency, command.onboardingImportID, command.onboardingImportRowNumber)
	attempt := access.Mutation{
		Invocation: inv,
		Action:     model.ActionInvitationCreate,
		Resource:   resource,
		ScopeType:  model.RoleScopeClass,
		ScopeID:    class.ID.String(),
		Operation:  "issue_student_class",
		Value: map[string]any{
			"purpose":            model.InvitationPurposeStudentClass,
			"class_id":           class.ID.String(),
			"academic_period_id": period.ID.String(),
		},
	}
	if recovered, ok, recoverErr := s.recoverInvitationIssueOutcome(ctx, commandIdempotency, attempt); ok {
		return recovered, recoverErr
	}

	issuedAt := model.TimeUTC(s.now())
	startsAt := model.TimeFromMillis(command.IntendedStartsAt)
	if startsAt.IsZero() {
		startsAt = period.StartsAt
	}

	placeholderClaimHash := strings.Repeat("0", model.TokenHashLength)
	if command.onboardingImportID.IsValid() && !command.batchDuplicate {
		candidate, candidateErr := newStudentClassInvitationValue(
			inv,
			command,
			class.ID,
			period.ID,
			startsAt,
			issuedAt,
			lifetime,
			placeholderClaimHash,
		)
		if candidateErr != nil {
			return InvitationView{}, candidateErr
		}
		if view, handled, noOpErr := s.resolveOnboardingInvitationNoOp(ctx, inv, attempt, candidate, commandIdempotency); handled {
			return view, noOpErr
		}
	}

	if !s.mail.Enabled() {
		return InvitationView{}, apperror.New("invitation.mail_unavailable")
	}

	if command.batchDuplicate {
		candidate, candidateErr := newStudentClassInvitationValue(
			inv,
			command,
			class.ID,
			period.ID,
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

	invitation, err := newStudentClassInvitationValue(
		inv,
		command,
		class.ID,
		period.ID,
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
			return s.storeStudentClassInvitation(ctx, commandIdempotency, invitation, prepared, reference)
		},
		invitationStoreErrors.Translate,
	)
	if err != nil {
		return InvitationView{}, err
	}

	return issuedInvitationView(created), nil
}

// newStudentClassInvitationValue builds the Student Class Invitation for the command with the given claim hash.
func newStudentClassInvitationValue(
	inv invocation.Invocation,
	command IssueStudentClassInvitationCommand,
	classID model.ClassID,
	periodID model.AcademicPeriodID,
	startsAt, issuedAt time.Time,
	lifetime time.Duration,
	claimHash string,
) (*model.Invitation, error) {
	value, err := model.NewStudentClassInvitation(model.StudentClassInvitationInput{
		ID:               model.NewInvitationID(),
		TargetEmail:      command.TargetEmail,
		ClassID:          classID,
		AcademicPeriodID: periodID,
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
		InviterUserID: inv.Principal().UserID,
		ScopeType:     model.RoleScopeClass,
		ScopeID:       classID.String(),
		ClaimHash:     claimHash,
		IssuedAt:      issuedAt,
		Lifetime:      lifetime,
	})
	if err != nil {
		return nil, apperror.DomainInvalid("invitation.invalid", err)
	}

	return value, nil
}

// storeStudentClassInvitation records the audited Student Class Invitation issue, idempotently when a key is present.
func (s *Service) storeStudentClassInvitation(
	ctx context.Context,
	commandIdempotency *store.CommandIdempotency,
	invitation *model.Invitation,
	prepared *store.PreparedMail,
	reference access.MutationReference,
) (*store.InvitationCommandResult, error) {
	input := &store.StudentClassInvitationIssue{
		Invitation:   invitation,
		Occurrence:   prepared.Occurrence,
		Delivery:     prepared.Delivery,
		DeliveryJob:  prepared.Job,
		AuditEventID: reference.ID,
		AuditAt:      reference.MutationAt,
	}
	if commandIdempotency != nil {
		value, storeErr := s.invitations.IssueStudentClassIdempotently(ctx, input, commandIdempotency)
		if storeErr == nil && value.Duplicate {
			return nil, apperror.New("onboarding_batch.duplicate")
		}

		return value, storeErr
	}

	value, storeErr := s.invitations.IssueStudentClass(ctx, input)
	return &store.InvitationCommandResult{Invitation: value}, storeErr
}
