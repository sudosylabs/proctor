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
	"github.com/sudosylabs/proctor/server/app/invocation"
	"github.com/sudosylabs/proctor/server/app/paging"
	"github.com/sudosylabs/proctor/server/model"
	"github.com/sudosylabs/proctor/server/store"
)

// ListInvitations pages the Invitations visible through the caller's invitation.view
// scopes; the first page also carries their total.
func (s *Service) ListInvitations(
	ctx context.Context,
	inv invocation.Invocation,
	query ListInvitationsQuery,
) (InvitationAdministrationPage, error) {
	visibility, err := s.visibility(ctx, inv, model.ActionInvitationView)
	if err != nil {
		return InvitationAdministrationPage{}, err
	}
	if query.Limit == 0 {
		query.Limit = 50
	}

	targetEmail := strings.ToLower(strings.TrimSpace(query.TargetEmail))
	if query.Limit < 1 ||
		query.Limit > 200 ||
		(query.Purpose != "" && !query.Purpose.IsValid()) ||
		(query.State != "" && !query.State.IsValid()) ||
		(targetEmail != "" && !model.IsValidEmail(targetEmail)) ||
		(query.TargetID != "" && !model.IsValidOpaqueID(query.TargetID)) ||
		(query.BeforeID.IsValid() != !query.BeforeCreatedAt.IsZero()) {
		return InvitationAdministrationPage{}, apperror.New("invitation.query.invalid")
	}

	options := store.InvitationListOptions{
		Visibility:      visibility,
		Purpose:         query.Purpose,
		State:           query.State,
		TargetEmail:     targetEmail,
		TargetID:        strings.TrimSpace(query.TargetID),
		CreatedAfter:    query.CreatedAfter,
		CreatedBefore:   query.CreatedBefore,
		BeforeCreatedAt: query.BeforeCreatedAt,
		BeforeID:        query.BeforeID,
		Limit:           query.Limit,
	}
	page, err := s.invitations.List(ctx, options)
	if err != nil {
		return InvitationAdministrationPage{}, invitationStoreErrors.Translate(err)
	}

	result := InvitationAdministrationPage{Items: []InvitationAdministrationView{}, More: page.More}
	for _, item := range page.Items {
		result.Items = append(result.Items, invitationAdministrationView(item))
	}

	total, consistent, err := paging.CountFirst(!query.BeforeID.IsValid(), len(result.Items), func(maximum int) (int, error) {
		return s.invitations.Count(ctx, options, maximum)
	})
	if err != nil {
		return InvitationAdministrationPage{}, invitationStoreErrors.Translate(err)
	}
	if !consistent {
		return InvitationAdministrationPage{}, apperror.New("invitation.unavailable")
	}

	result.Total = total
	return result, nil
}

// countPendingInvitations counts the unexpired pending Invitations within the
// caller's invitation.view visibility, authorized and audited exactly as
// ListInvitations is, stopping at maximum.
func (s *Service) countPendingInvitations(ctx context.Context, inv invocation.Invocation, maximum int) (int, error) {
	visibility, err := s.visibility(ctx, inv, model.ActionInvitationView)
	if err != nil {
		return 0, err
	}

	total, err := s.invitations.Count(
		ctx,
		store.InvitationListOptions{Visibility: visibility, State: model.InvitationPending, Unexpired: true, Limit: 1},
		maximum,
	)
	if err != nil {
		return 0, invitationStoreErrors.Translate(err)
	}

	return total, nil
}

// GetInvitation reads one Invitation visible through the caller's invitation.view
// scopes.
func (s *Service) GetInvitation(
	ctx context.Context,
	inv invocation.Invocation,
	id model.InvitationID,
) (InvitationAdministrationView, error) {
	if !id.IsValid() {
		return InvitationAdministrationView{}, apperror.New("request.invalid").WithField("field", "invitation_id")
	}

	visibility, err := s.visibility(ctx, inv, model.ActionInvitationView)
	if err != nil {
		return InvitationAdministrationView{}, err
	}

	record, err := s.invitations.GetForAdministration(ctx, id, visibility)
	if err != nil {
		return InvitationAdministrationView{}, invitationStoreErrors.Translate(err)
	}

	return invitationAdministrationView(record), nil
}

// ResendInvitation issues a fresh claim for a pending Invitation visible through the
// caller's invitation.manage scopes and mails it.
func (s *Service) ResendInvitation(
	ctx context.Context,
	inv invocation.Invocation,
	command ResendInvitationCommand,
) (InvitationAdministrationView, error) {
	id := command.ID
	if !id.IsValid() || command.ExpectedRevision < 1 {
		return InvitationAdministrationView{}, apperror.New("request.invalid").WithField("field", "invitation_id")
	}

	visibility, err := s.visibility(ctx, inv, model.ActionInvitationManage)
	if err != nil {
		return InvitationAdministrationView{}, err
	}

	current, err := s.invitations.GetForAdministration(ctx, id, visibility)
	if err != nil {
		return InvitationAdministrationView{}, invitationStoreErrors.Translate(err)
	}

	if command.BatchScopeID != "" {
		if current.Invitation == nil ||
			current.Invitation.ScopeType != command.BatchScopeType ||
			current.Invitation.ScopeID != command.BatchScopeID {
			return InvitationAdministrationView{}, apperror.New("resource.not_found")
		}

		if invitationPurposeRequiresInteractiveBatch(current.Invitation.Purpose) {
			if err = access.RequireStrongRecentSession(inv.Principal(), s.now(), s.recentAuthenticationTTL); err != nil {
				return InvitationAdministrationView{}, err
			}
		}
	}

	commandIdempotency, err := idempotency.Prepare(
		inv,
		"invitation.resend",
		command.IdempotencyKey,
		invitationLifecycleIdempotencySemantic{ID: id.String(), Revision: command.ExpectedRevision},
	)
	if err != nil {
		return InvitationAdministrationView{}, err
	}

	attempt := access.Mutation{
		Invocation: inv,
		Action:     model.ActionInvitationManage,
		Resource:   invitationResource(current.Invitation),
		ScopeType:  current.Invitation.ScopeType,
		ScopeID:    current.Invitation.ScopeID,
		Operation:  "resend",
		Value:      current.Invitation.Auditable(),
	}
	if recovered, ok, recoverErr := s.recoverInvitationAdministrationOutcome(ctx, commandIdempotency, attempt); ok {
		return recovered, recoverErr
	}

	if command.batchDuplicate {
		if !s.mail.Enabled() {
			return InvitationAdministrationView{}, apperror.New("invitation.mail_unavailable")
		}

		view, duplicateErr := s.recordDuplicateInvitationView(
			ctx,
			commandIdempotency,
			attempt,
			store.InvitationBatchDuplicate{
				LifecycleID:          id,
				ExpectedRevision:     command.ExpectedRevision,
				CanonicalFingerprint: command.batchCanonicalFingerprint,
			},
			command.batchCanonicalKey,
		)
		return InvitationAdministrationView{InvitationView: view}, duplicateErr
	}

	result, err := access.RunAudited(
		ctx,
		s.auditor,
		attempt,
		s.now,
		func(ctx context.Context, reference access.MutationReference) (*store.InvitationAdministrationCommandResult, error) {
			if commandIdempotency != nil {
				replayed, replayErr := s.invitations.ReplayAdministration(
					ctx,
					&store.InvitationReplay{AuditEventID: reference.ID, AuditAt: reference.MutationAt},
					commandIdempotency,
				)
				if replayErr == nil {
					if replayed.Duplicate {
						return nil, apperror.New("onboarding_batch.duplicate")
					}
					return replayed, nil
				}

				if !store.IsNotFound(replayErr) {
					return nil, replayErr
				}
			}

			if !s.mail.Enabled() {
				return nil, apperror.New("invitation.mail_unavailable")
			}

			at := model.TimeUTC(reference.MutationAt)
			rawClaim := s.newToken()
			if !model.IsValidCredentialToken(rawClaim) {
				return nil, apperror.New("invitation.unavailable")
			}

			actionURL, linkErr := identity.AccountCredentialLink(s.publicURL, "/join", rawClaim)
			if linkErr != nil {
				return nil, apperror.New("invitation.unavailable").Wrap(linkErr)
			}

			prepared, prepareErr := s.mail.PrepareInvitationResend(current.Invitation, actionURL, inv.Principal().UserID, at)
			if prepareErr != nil {
				return nil, apperror.New("invitation.mail_unavailable").Wrap(prepareErr)
			}

			input := &store.InvitationResend{
				ID:               id,
				ExpectedRevision: command.ExpectedRevision,
				ClaimHash:        model.HashInvitationClaim(rawClaim),
				Occurrence:       prepared.Occurrence,
				Delivery:         prepared.Delivery,
				DeliveryJob:      prepared.Job,
				ActorUserID:      inv.Principal().UserID,
				AuditEventID:     reference.ID,
				AuditAt:          reference.MutationAt,
			}
			if commandIdempotency != nil {
				value, storeErr := s.invitations.ResendIdempotently(ctx, input, commandIdempotency)
				if storeErr == nil && value.Duplicate {
					return nil, apperror.New("onboarding_batch.duplicate")
				}

				return value, storeErr
			}

			record, storeErr := s.invitations.Resend(ctx, input)
			return &store.InvitationAdministrationCommandResult{Record: record}, storeErr
		},
		invitationStoreErrors.Translate,
	)
	if err != nil {
		return InvitationAdministrationView{}, err
	}

	view := invitationAdministrationView(result.Record)
	view.Replayed = result.Replayed
	return view, nil
}

// RevokeInvitation revokes a pending Invitation visible through the caller's
// invitation.manage scopes and mails the revocation notice.
func (s *Service) RevokeInvitation(
	ctx context.Context,
	inv invocation.Invocation,
	command RevokeInvitationCommand,
) (InvitationAdministrationView, error) {
	id := command.ID
	if !id.IsValid() || command.ExpectedRevision < 1 {
		return InvitationAdministrationView{}, apperror.New("request.invalid").WithField("field", "invitation_id")
	}

	visibility, err := s.visibility(ctx, inv, model.ActionInvitationManage)
	if err != nil {
		return InvitationAdministrationView{}, err
	}

	current, err := s.invitations.GetForAdministration(ctx, id, visibility)
	if err != nil {
		return InvitationAdministrationView{}, invitationStoreErrors.Translate(err)
	}

	if command.BatchScopeID != "" {
		if current.Invitation == nil ||
			current.Invitation.ScopeType != command.BatchScopeType ||
			current.Invitation.ScopeID != command.BatchScopeID {
			return InvitationAdministrationView{}, apperror.New("resource.not_found")
		}

		if invitationPurposeRequiresInteractiveBatch(current.Invitation.Purpose) {
			if err = access.RequireStrongRecentSession(inv.Principal(), s.now(), s.recentAuthenticationTTL); err != nil {
				return InvitationAdministrationView{}, err
			}
		}
	}

	commandIdempotency, err := idempotency.Prepare(
		inv,
		"invitation.revoke",
		command.IdempotencyKey,
		invitationLifecycleIdempotencySemantic{ID: id.String(), Revision: command.ExpectedRevision},
	)
	if err != nil {
		return InvitationAdministrationView{}, err
	}

	attempt := access.Mutation{
		Invocation: inv,
		Action:     model.ActionInvitationManage,
		Resource:   invitationResource(current.Invitation),
		ScopeType:  current.Invitation.ScopeType,
		ScopeID:    current.Invitation.ScopeID,
		Operation:  "revoke",
		Value:      current.Invitation.Auditable(),
	}
	if recovered, ok, recoverErr := s.recoverInvitationAdministrationOutcome(ctx, commandIdempotency, attempt); ok {
		return recovered, recoverErr
	}

	if command.batchDuplicate {
		view, duplicateErr := s.recordDuplicateInvitationView(
			ctx,
			commandIdempotency,
			attempt,
			store.InvitationBatchDuplicate{
				LifecycleID:          id,
				ExpectedRevision:     command.ExpectedRevision,
				CanonicalFingerprint: command.batchCanonicalFingerprint,
			},
			command.batchCanonicalKey,
		)
		return InvitationAdministrationView{InvitationView: view}, duplicateErr
	}

	result, err := access.RunAudited(
		ctx,
		s.auditor,
		attempt,
		s.now,
		func(ctx context.Context, reference access.MutationReference) (*store.InvitationAdministrationCommandResult, error) {
			if commandIdempotency != nil {
				replayed, replayErr := s.invitations.ReplayAdministration(
					ctx,
					&store.InvitationReplay{AuditEventID: reference.ID, AuditAt: reference.MutationAt},
					commandIdempotency,
				)
				if replayErr == nil {
					if replayed.Duplicate {
						return nil, apperror.New("onboarding_batch.duplicate")
					}
					return replayed, nil
				}

				if !store.IsNotFound(replayErr) {
					return nil, replayErr
				}
			}

			at := model.TimeUTC(reference.MutationAt)
			prepared, prepareErr := s.mail.PrepareInvitationRevocation(current.Invitation, inv.Principal().UserID, at)
			if prepareErr != nil {
				return nil, apperror.New("invitation.mail_unavailable").Wrap(prepareErr)
			}

			input := &store.InvitationRevocation{
				ID:               id,
				ExpectedRevision: command.ExpectedRevision,
				ActorUserID:      inv.Principal().UserID,
				RevocationNotice: &store.PreparedMail{
					Occurrence: prepared.Occurrence,
					Delivery:   prepared.Delivery,
					Job:        prepared.Job,
				},
				AuditEventID: reference.ID,
				AuditAt:      reference.MutationAt,
			}
			if commandIdempotency != nil {
				value, storeErr := s.invitations.RevokeIdempotently(ctx, input, commandIdempotency)
				if storeErr == nil && value.Duplicate {
					return nil, apperror.New("onboarding_batch.duplicate")
				}

				return value, storeErr
			}

			record, storeErr := s.invitations.Revoke(ctx, input)
			return &store.InvitationAdministrationCommandResult{Record: record}, storeErr
		},
		invitationStoreErrors.Translate,
	)
	if err != nil {
		return InvitationAdministrationView{}, err
	}

	view := invitationAdministrationView(result.Record)
	view.Replayed = result.Replayed
	return view, nil
}
