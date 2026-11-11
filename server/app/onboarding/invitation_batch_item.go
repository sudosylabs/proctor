// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package onboarding

import (
	"context"
	"crypto/sha256"
	"errors"
	"strings"

	"github.com/sudosylabs/proctor/server/app/apperror"
	"github.com/sudosylabs/proctor/server/app/invocation"
	"github.com/sudosylabs/proctor/server/model"
)

func validateInvitationBatchItem(operation InvitationBatchOperation, item InvitationBatchItemCommand) error {
	if !validInvitationBatchItemKey(item.IdempotencyKey) {
		return errors.New("invalid Invitation batch item key")
	}

	hasSuggestions := item.SuggestedUsername != "" ||
		item.SuggestedDisplayName != "" ||
		item.SuggestedFirstName != "" ||
		item.SuggestedLastName != "" ||
		item.SuggestedLocale != "" ||
		item.SuggestedTimezone != ""
	switch operation {
	case InvitationBatchStudentClassCreate:
		if item.InvitationID != "" || item.RoleID != "" || item.ExpectedRevision != 0 || strings.TrimSpace(item.TargetEmail) == "" {
			return errors.New("invalid student Invitation batch item")
		}
	case InvitationBatchTeacherAcademicUnitCreate:
		if item.InvitationID != "" ||
			item.ExpectedRevision != 0 ||
			strings.TrimSpace(item.TargetEmail) == "" ||
			strings.TrimSpace(item.RoleID.String()) == "" {
			return errors.New("invalid teacher Invitation batch item")
		}
	case InvitationBatchAcademicUnitRoleCreate, InvitationBatchInstitutionRoleCreate:
		if item.InvitationID != "" ||
			item.ExpectedRevision != 0 ||
			strings.TrimSpace(item.TargetEmail) == "" ||
			strings.TrimSpace(item.RoleID.String()) == "" ||
			hasSuggestions {
			return errors.New("invalid Role Invitation batch item")
		}
	case InvitationBatchResend, InvitationBatchRevoke:
		if strings.TrimSpace(item.InvitationID.String()) == "" ||
			item.ExpectedRevision < 1 ||
			item.TargetEmail != "" ||
			item.RoleID != "" ||
			item.IntendedStartsAt != 0 ||
			item.IntendedEndsAt != 0 ||
			hasSuggestions {
			return errors.New("invalid Invitation lifecycle batch item")
		}
	default:
		return errors.New("invalid Invitation batch operation")
	}

	return nil
}

func validInvitationBatchItemKey(value string) bool {
	if len(value) < 1 || len(value) > 128 {
		return false
	}

	for _, character := range value {
		if (character >= 'a' && character <= 'z') ||
			(character >= 'A' && character <= 'Z') ||
			(character >= '0' && character <= '9') ||
			character == '-' ||
			character == '.' ||
			character == '_' ||
			character == '~' {
			continue
		}

		return false
	}

	return true
}

func invitationBatchDuplicateKey(operation InvitationBatchOperation, item InvitationBatchItemCommand) string {
	mailbox := strings.ToLower(strings.TrimSpace(model.SanitizeUnicode(item.TargetEmail)))
	switch operation {
	case InvitationBatchStudentClassCreate:
		return mailbox
	case InvitationBatchTeacherAcademicUnitCreate, InvitationBatchAcademicUnitRoleCreate, InvitationBatchInstitutionRoleCreate:
		return mailbox + "\x00" + strings.TrimSpace(item.RoleID.String())
	case InvitationBatchResend, InvitationBatchRevoke:
		return strings.TrimSpace(item.InvitationID.String())
	default:
		return ""
	}
}

func (s *Service) runInvitationBatchItem(
	ctx context.Context,
	invocation invocation.Invocation,
	batch RunInvitationBatchCommand,
	item InvitationBatchItemCommand,
	itemKey string,
	duplicate bool,
	canonicalKey string,
	canonicalFingerprint [sha256.Size]byte,
) (model.InvitationID, InvitationBatchItemStatus, error) {
	switch batch.Operation {
	case InvitationBatchStudentClassCreate:
		view, err := s.IssueStudentClassInvitation(ctx, invocation, IssueStudentClassInvitationCommand{
			TargetEmail:               item.TargetEmail,
			ClassID:                   model.ClassID(batch.ScopeID),
			IntendedStartsAt:          item.IntendedStartsAt,
			IntendedEndsAt:            item.IntendedEndsAt,
			SuggestedUsername:         item.SuggestedUsername,
			SuggestedDisplayName:      item.SuggestedDisplayName,
			SuggestedFirstName:        item.SuggestedFirstName,
			SuggestedLastName:         item.SuggestedLastName,
			SuggestedLocale:           item.SuggestedLocale,
			SuggestedTimezone:         item.SuggestedTimezone,
			IdempotencyKey:            itemKey,
			batchDuplicate:            duplicate,
			batchCanonicalKey:         canonicalKey,
			onboardingImportID:        batch.onboardingImportID,
			onboardingImportRowNumber: batch.onboardingImportRowNumber,
		})
		return invitationBatchViewOutcome(view, err)
	case InvitationBatchTeacherAcademicUnitCreate:
		view, err := s.IssueTeacherAcademicUnitInvitation(ctx, invocation, IssueTeacherAcademicUnitInvitationCommand{
			TargetEmail:               item.TargetEmail,
			AcademicUnitID:            model.AcademicUnitID(batch.ScopeID),
			RoleID:                    item.RoleID,
			IntendedStartsAt:          item.IntendedStartsAt,
			IntendedEndsAt:            item.IntendedEndsAt,
			SuggestedUsername:         item.SuggestedUsername,
			SuggestedDisplayName:      item.SuggestedDisplayName,
			SuggestedFirstName:        item.SuggestedFirstName,
			SuggestedLastName:         item.SuggestedLastName,
			SuggestedLocale:           item.SuggestedLocale,
			SuggestedTimezone:         item.SuggestedTimezone,
			IdempotencyKey:            itemKey,
			batchDuplicate:            duplicate,
			batchCanonicalKey:         canonicalKey,
			onboardingImportID:        batch.onboardingImportID,
			onboardingImportRowNumber: batch.onboardingImportRowNumber,
		})
		return invitationBatchViewOutcome(view, err)
	case InvitationBatchAcademicUnitRoleCreate:
		view, err := s.IssueAcademicUnitRoleInvitation(ctx, invocation, IssueAcademicUnitRoleInvitationCommand{
			TargetEmail:               item.TargetEmail,
			AcademicUnitID:            model.AcademicUnitID(batch.ScopeID),
			RoleID:                    item.RoleID,
			IntendedStartsAt:          item.IntendedStartsAt,
			IntendedEndsAt:            item.IntendedEndsAt,
			IdempotencyKey:            itemKey,
			batchDuplicate:            duplicate,
			batchCanonicalKey:         canonicalKey,
			onboardingImportID:        batch.onboardingImportID,
			onboardingImportRowNumber: batch.onboardingImportRowNumber,
		})
		return invitationBatchViewOutcome(view, err)
	case InvitationBatchInstitutionRoleCreate:
		view, err := s.IssueInstitutionRoleInvitation(ctx, invocation, IssueInstitutionRoleInvitationCommand{
			TargetEmail:               item.TargetEmail,
			InstitutionID:             model.InstitutionID(batch.ScopeID),
			RoleID:                    item.RoleID,
			IntendedStartsAt:          item.IntendedStartsAt,
			IntendedEndsAt:            item.IntendedEndsAt,
			IdempotencyKey:            itemKey,
			batchDuplicate:            duplicate,
			batchCanonicalKey:         canonicalKey,
			onboardingImportID:        batch.onboardingImportID,
			onboardingImportRowNumber: batch.onboardingImportRowNumber,
		})
		return invitationBatchViewOutcome(view, err)
	case InvitationBatchResend:
		view, err := s.ResendInvitation(ctx, invocation, ResendInvitationCommand{
			ID:                        model.InvitationID(strings.TrimSpace(item.InvitationID.String())),
			ExpectedRevision:          item.ExpectedRevision,
			IdempotencyKey:            itemKey,
			BatchScopeType:            batch.ScopeType,
			BatchScopeID:              batch.ScopeID,
			batchDuplicate:            duplicate,
			batchCanonicalKey:         canonicalKey,
			batchCanonicalFingerprint: canonicalFingerprint,
		})
		return invitationBatchAdministrationOutcome(view, err)
	case InvitationBatchRevoke:
		view, err := s.RevokeInvitation(ctx, invocation, RevokeInvitationCommand{
			ID:                        model.InvitationID(strings.TrimSpace(item.InvitationID.String())),
			ExpectedRevision:          item.ExpectedRevision,
			IdempotencyKey:            itemKey,
			BatchScopeType:            batch.ScopeType,
			BatchScopeID:              batch.ScopeID,
			batchDuplicate:            duplicate,
			batchCanonicalKey:         canonicalKey,
			batchCanonicalFingerprint: canonicalFingerprint,
		})
		return invitationBatchAdministrationOutcome(view, err)
	default:
		return "", "", apperror.New("request.invalid")
	}
}

func invitationBatchViewOutcome(view InvitationView, err error) (model.InvitationID, InvitationBatchItemStatus, error) {
	if err != nil {
		return "", "", err
	}
	if view.NoOp || view.Replayed {
		return view.ID, InvitationBatchItemNoOp, nil
	}
	return view.ID, InvitationBatchItemSucceeded, nil
}

func invitationBatchAdministrationOutcome(
	view InvitationAdministrationView,
	err error,
) (model.InvitationID, InvitationBatchItemStatus, error) {
	return invitationBatchViewOutcome(view.InvitationView, err)
}
