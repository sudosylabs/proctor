// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package onboarding

import (
	"context"
	"time"

	"github.com/sudosylabs/proctor/server/app/access"
	"github.com/sudosylabs/proctor/server/app/apperror"
	"github.com/sudosylabs/proctor/server/app/invocation"
	"github.com/sudosylabs/proctor/server/model"
	"github.com/sudosylabs/proctor/server/store"
)

// resolveOnboardingInvitationNoOp reports handled when the caller must
// return the view and error as they are.
func (s *Service) resolveOnboardingInvitationNoOp(
	ctx context.Context,
	inv invocation.Invocation,
	attempt access.Mutation,
	candidate *model.Invitation,
	commandIdempotency *store.CommandIdempotency,
) (view InvitationView, handled bool, err error) {
	_, noOp, noOpErr := s.invitations.ResolveOnboardingInvitationNoOp(ctx, candidate)
	if noOpErr != nil {
		return InvitationView{}, true, invitationStoreErrors.Translate(noOpErr)
	}

	if noOp {
		view, err = s.completeOnboardingInvitationNoOp(ctx, inv, attempt, candidate, commandIdempotency)
		return view, true, err
	}

	return InvitationView{}, false, nil
}

func issuedInvitationView(created *store.InvitationCommandResult) InvitationView {
	view := invitationView(created.Invitation)
	view.Replayed = created.Replayed
	view.NoOp = created.NoOp
	return view
}

func (s *Service) completeOnboardingInvitationNoOp(
	ctx context.Context,
	inv invocation.Invocation,
	attempt access.Mutation,
	candidate *model.Invitation,
	commandIdempotency *store.CommandIdempotency,
) (InvitationView, error) {
	if candidate == nil || commandIdempotency == nil || !commandIdempotency.OnboardingImportID.IsValid() {
		return InvitationView{}, apperror.New("invitation.unavailable")
	}

	attempt.Value = candidate.Auditable()
	attempt.At = candidate.CreatedAt
	created, err := access.RunAudited(
		ctx,
		s.auditor,
		attempt,
		func(ctx context.Context, reference access.MutationReference) (*store.InvitationCommandResult, error) {
			switch candidate.Purpose {
			case model.InvitationPurposeStudentClass:
				return s.invitations.IssueStudentClassIdempotently(ctx, &store.StudentClassInvitationIssue{
					Invitation:   candidate,
					AuditEventID: reference.ID,
					AuditAt:      reference.MutationAt,
				}, commandIdempotency)
			case model.InvitationPurposeTeacherAcademicUnit:
				return s.invitations.IssueTeacherAcademicUnitIdempotently(ctx, &store.TeacherAcademicUnitInvitationIssue{
					Invitation:   candidate,
					Lifetime:     model.InvitationLifetime,
					AuditEventID: reference.ID,
					AuditAt:      reference.MutationAt,
				}, commandIdempotency)
			case model.InvitationPurposeAcademicUnitRole, model.InvitationPurposeInstitutionRole:
				return s.invitations.IssueScopedRoleIdempotently(ctx, &store.ScopedRoleInvitationIssue{
					Invitation:   candidate,
					Lifetime:     model.InvitationLifetime,
					AuditEventID: reference.ID,
					AuditAt:      reference.MutationAt,
				}, commandIdempotency)
			default:
				return nil, store.NewErrInvalidInput("invitation", "purpose", candidate.Purpose)
			}
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

// invitationLifetime: nil selects fallback; a day count outside the model
// bounds is invitation.invalid on expires_in_days.
func invitationLifetime(days *int, fallback time.Duration) (time.Duration, error) {
	if days == nil {
		return fallback, nil
	}
	const day = 24 * time.Hour
	if *days < int(model.MinimumInvitationLifetime/day) || *days > int(model.MaximumInvitationLifetime/day) {
		return 0, apperror.Invalid("invitation.invalid", "expires_in_days")
	}
	return time.Duration(*days) * day, nil
}

// replacedInvitationLifetime rounds to whole days and clamps to the model
// bounds.
func replacedInvitationLifetime(current *model.Invitation) time.Duration {
	const day = 24 * time.Hour
	if current == nil {
		return model.InvitationLifetime
	}
	lifetime := current.ExpiresAt.Sub(current.CreatedAt).Round(day)
	return min(max(lifetime, model.MinimumInvitationLifetime), model.MaximumInvitationLifetime)
}
