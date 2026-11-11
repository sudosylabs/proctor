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
	"github.com/sudosylabs/proctor/server/app/administration"
	"github.com/sudosylabs/proctor/server/app/apperror"
	"github.com/sudosylabs/proctor/server/app/identity"
	"github.com/sudosylabs/proctor/server/app/internal/batchitem"
	"github.com/sudosylabs/proctor/server/app/invocation"
	"github.com/sudosylabs/proctor/server/model"
	"github.com/sudosylabs/proctor/server/store"
)

func (s *Service) runAcademicAdministrationBatchItem(
	ctx context.Context,
	inv invocation.Invocation,
	batch RunAcademicAdministrationBatchCommand,
	item AcademicAdministrationBatchItemCommand,
	itemKey string,
	metadata *store.CommandBatch,
	retained bool,
) (string, InvitationBatchItemStatus, error) {
	authority := academicAdministrationBatchAuthority(inv, batch)
	if model.UserID(strings.TrimSpace(item.UserID.String())).IsValid() {
		authority.RecipientUserID = model.UserID(strings.TrimSpace(item.UserID.String()))
	}
	if batch.Operation == AcademicAdministrationClassTransfer || batch.Operation == AcademicAdministrationClassEnd {
		authority.ClassMemberID = model.ClassMemberID(strings.TrimSpace(item.RelationshipID))
	}

	switch batch.Operation {
	case AcademicAdministrationAffiliationAdd:
		var replayed bool
		value, err := s.academic.CreateAffiliation(ctx, inv, academic.CreateAffiliationCommand{
			UserID:         item.UserID,
			Kind:           item.AffiliationKind,
			StartAt:        item.StartAt,
			EndAt:          item.EndAt,
			IdempotencyKey: itemKey,
			Batch: batchitem.New(batchitem.Fields{
				Replayed:                  &replayed,
				Authorization:             authority,
				Metadata:                  metadata,
				RetainedOutcome:           retained,
				OnboardingImportID:        batch.onboardingImportID,
				OnboardingImportRowNumber: batch.onboardingImportRowNumber,
			}),
		})
		return administrationItemOutcome(value, replayed, metadata, err)
	case AcademicAdministrationAffiliationEnd:
		var replayed bool
		value, err := s.academic.EndAffiliation(ctx, inv, academic.EndAffiliationCommand{
			ID:             model.AffiliationID(item.RelationshipID),
			IdempotencyKey: itemKey,
			BatchScopeType: batch.ScopeType,
			BatchScopeID:   batch.ScopeID,
			Batch: batchitem.New(batchitem.Fields{
				Replayed:                  &replayed,
				Authorization:             authority,
				Metadata:                  metadata,
				RetainedOutcome:           retained,
				OnboardingImportID:        batch.onboardingImportID,
				OnboardingImportRowNumber: batch.onboardingImportRowNumber,
			}),
		})
		return administrationItemOutcome(value, replayed, metadata, err)
	case AcademicAdministrationAcademicUnitMemberAdd:
		var replayed bool
		value, err := s.academic.CreateAcademicUnitMember(ctx, inv, academic.CreateAcademicUnitMemberCommand{
			AcademicUnitID: model.AcademicUnitID(batch.ScopeID),
			UserID:         item.UserID,
			StartAt:        item.StartAt,
			IdempotencyKey: itemKey,
			Batch: batchitem.New(batchitem.Fields{
				Replayed:                  &replayed,
				Authorization:             authority,
				Metadata:                  metadata,
				RetainedOutcome:           retained,
				OnboardingImportID:        batch.onboardingImportID,
				OnboardingImportRowNumber: batch.onboardingImportRowNumber,
			}),
		})
		return administrationItemOutcome(value, replayed, metadata, err)
	case AcademicAdministrationAcademicUnitMemberEnd:
		var replayed bool
		value, err := s.academic.EndAcademicUnitMember(ctx, inv, academic.EndAcademicUnitMemberCommand{
			ID:             model.AcademicUnitMemberID(item.RelationshipID),
			IdempotencyKey: itemKey,
			BatchScopeID:   batch.ScopeID,
			Batch: batchitem.New(batchitem.Fields{
				Replayed:                  &replayed,
				Authorization:             authority,
				Metadata:                  metadata,
				RetainedOutcome:           retained,
				OnboardingImportID:        batch.onboardingImportID,
				OnboardingImportRowNumber: batch.onboardingImportRowNumber,
			}),
		})
		return administrationItemOutcome(value, replayed, metadata, err)
	case AcademicAdministrationClassEnroll, AcademicAdministrationClassTransfer:
		var replayed bool
		value, err := s.academic.EnrollClassMember(ctx, inv, academic.EnrollClassMemberCommand{
			ClassID:            model.ClassID(batch.ScopeID),
			UserID:             item.UserID,
			StartAt:            item.StartAt,
			EndAt:              item.EndAt,
			ExpectedPreviousID: model.ClassMemberID(item.RelationshipID),
			RequireTransfer:    batch.Operation == AcademicAdministrationClassTransfer,
			IdempotencyKey:     itemKey,
			Batch: batchitem.New(batchitem.Fields{
				Replayed:                      &replayed,
				Authorization:                 authority,
				Metadata:                      metadata,
				RetainedOutcome:               retained,
				StudentProgression:            batch.progression,
				ProgressionSourceAuditID:      batch.progressionSourceAuditID,
				ProgressionDestinationAuditID: batch.progressionDestinationAuditID,
				OnboardingImportID:            batch.onboardingImportID,
				OnboardingImportRowNumber:     batch.onboardingImportRowNumber,
			}),
		})
		if err != nil || value == nil || value.Membership == nil {
			return "", "", err
		}
		if metadata.Duplicate {
			return "", "", apperror.New("onboarding_batch.duplicate")
		}

		status := InvitationBatchItemSucceeded
		if replayed {
			status = InvitationBatchItemNoOp
		}

		return value.Membership.ID.String(), status, nil
	case AcademicAdministrationClassEnd:
		var replayed bool
		value, err := s.academic.EndClassMember(ctx, inv, academic.EndClassMemberCommand{
			ID:             model.ClassMemberID(item.RelationshipID),
			IdempotencyKey: itemKey,
			BatchScopeID:   batch.ScopeID,
			Batch: batchitem.New(batchitem.Fields{
				Replayed:                  &replayed,
				Authorization:             authority,
				Metadata:                  metadata,
				RetainedOutcome:           retained,
				OnboardingImportID:        batch.onboardingImportID,
				OnboardingImportRowNumber: batch.onboardingImportRowNumber,
			}),
		})
		return administrationItemOutcome(value, replayed, metadata, err)
	case AcademicAdministrationRoleBindingCreate:
		var replayed bool
		value, err := s.administration.CreateRoleBinding(ctx, inv, administration.CreateRoleBindingCommand{
			UserID:         item.UserID,
			RoleID:         item.RoleID,
			ScopeType:      batch.ScopeType,
			ScopeID:        batch.ScopeID,
			StartAt:        item.StartAt,
			EndAt:          item.EndAt,
			IdempotencyKey: itemKey,
			Batch: batchitem.New(batchitem.Fields{
				Replayed:                  &replayed,
				Authorization:             authority,
				Metadata:                  metadata,
				RetainedOutcome:           retained,
				OnboardingImportID:        batch.onboardingImportID,
				OnboardingImportRowNumber: batch.onboardingImportRowNumber,
			}),
		})
		return administrationItemOutcome(value, replayed, metadata, err)
	case AcademicAdministrationRoleBindingEnd:
		var replayed bool
		value, err := s.administration.EndRoleBinding(ctx, inv, administration.EndRoleBindingCommand{
			ID:             model.RoleBindingID(item.RelationshipID),
			IdempotencyKey: itemKey,
			BatchScopeType: batch.ScopeType,
			BatchScopeID:   batch.ScopeID,
			Batch: batchitem.New(batchitem.Fields{
				Replayed:                  &replayed,
				Authorization:             authority,
				Metadata:                  metadata,
				RetainedOutcome:           retained,
				OnboardingImportID:        batch.onboardingImportID,
				OnboardingImportRowNumber: batch.onboardingImportRowNumber,
			}),
		})
		return administrationItemOutcome(value, replayed, metadata, err)
	case AcademicAdministrationUserEnable, AcademicAdministrationUserDisable:
		var replayed bool
		value, err := s.identity.SetUserEnabled(ctx, inv, identity.SetUserEnabledCommand{
			ID:             item.UserID,
			Enabled:        batch.Operation == AcademicAdministrationUserEnable,
			IdempotencyKey: itemKey,
			Batch: batchitem.New(batchitem.Fields{
				Replayed:                  &replayed,
				Authorization:             authority,
				Metadata:                  metadata,
				RetainedOutcome:           retained,
				OnboardingImportID:        batch.onboardingImportID,
				OnboardingImportRowNumber: batch.onboardingImportRowNumber,
			}),
		})
		return administrationItemOutcome(value, replayed, metadata, err)
	case AcademicAdministrationUserSessionsRevoke:
		var replayed bool
		err := s.identity.RevokeUserSessions(ctx, inv, identity.RevokeUserSessionsCommand{
			UserID:         item.UserID,
			IdempotencyKey: itemKey,
			Batch: batchitem.New(batchitem.Fields{
				Replayed:                  &replayed,
				Authorization:             authority,
				Metadata:                  metadata,
				RetainedOutcome:           retained,
				OnboardingImportID:        batch.onboardingImportID,
				OnboardingImportRowNumber: batch.onboardingImportRowNumber,
			}),
		})
		if err != nil {
			return "", "", err
		}
		if metadata.Duplicate {
			return "", "", apperror.New("onboarding_batch.duplicate")
		}
		if replayed {
			return item.UserID.String(), InvitationBatchItemNoOp, nil
		}

		return item.UserID.String(), InvitationBatchItemSucceeded, nil
	default:
		return "", "", apperror.New("request.invalid")
	}
}

func academicAdministrationBatchAuthority(
	inv invocation.Invocation,
	batch RunAcademicAdministrationBatchCommand,
) *store.CommandAuthorization {
	var action model.Action
	switch batch.Operation {
	case AcademicAdministrationAffiliationAdd,
		AcademicAdministrationAffiliationEnd,
		AcademicAdministrationUserEnable,
		AcademicAdministrationUserDisable:
		action = model.ActionUserManage
	case AcademicAdministrationAcademicUnitMemberAdd, AcademicAdministrationAcademicUnitMemberEnd:
		action = model.ActionAcademicUnitMembersManage
	case AcademicAdministrationClassEnroll, AcademicAdministrationClassEnd, AcademicAdministrationClassTransfer:
		action = model.ActionClassMembersManage
	case AcademicAdministrationRoleBindingCreate, AcademicAdministrationRoleBindingEnd:
		action = model.ActionRoleBindingManage
	case AcademicAdministrationUserSessionsRevoke:
		action = model.ActionSessionManage
	}

	actions := []model.Action{model.ActionOnboardingBatchManage, action}
	if batch.progression {
		actions = []model.Action{model.ActionAcademicProgressionManage, action}
	}

	return &store.CommandAuthorization{
		Principal: inv.Principal(),
		ScopeType: batch.ScopeType,
		ScopeID:   batch.ScopeID,
		Actions:   actions,
	}
}

func administrationItemOutcome(
	value any,
	replayed bool,
	metadata *store.CommandBatch,
	err error,
) (string, InvitationBatchItemStatus, error) {
	if err != nil {
		return "", "", err
	}
	if metadata != nil && metadata.Duplicate {
		return "", "", apperror.New("onboarding_batch.duplicate")
	}

	var id string
	switch typed := value.(type) {
	case *model.Affiliation:
		if typed != nil {
			id = typed.ID.String()
		}
	case *model.AcademicUnitMember:
		if typed != nil {
			id = typed.ID.String()
		}
	case *model.ClassMember:
		if typed != nil {
			id = typed.ID.String()
		}
	case *model.RoleBinding:
		if typed != nil {
			id = typed.ID.String()
		}
	case *model.User:
		if typed != nil {
			id = typed.ID.String()
		}
	}

	if id == "" {
		return "", "", apperror.New("administration.unavailable")
	}

	status := InvitationBatchItemSucceeded
	if replayed {
		status = InvitationBatchItemNoOp
	}

	return id, status, nil
}
