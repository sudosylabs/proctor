// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package mail

import (
	"context"
	"errors"

	"github.com/sudosylabs/proctor/server/model"
)

// DeliveryIsRelevant reports whether delivery should still be sent. The
// delivery Jobs ask before each attempt and the operator retry asks before it
// requeues.
func DeliveryIsRelevant(ctx context.Context, delivery *model.MailDelivery) (bool, error) {
	relevance, err := evaluateDeliveryRelevance(ctx, delivery)
	return relevance == deliveryRelevant, err
}

type deliveryRelevance uint8

const (
	deliveryRelevant deliveryRelevance = iota + 1
	deliveryObsolete
)

// evaluateDeliveryRelevance is the closed typed policy seam used before
// automatic and operator attempts. Each future template family must add an
// explicit authoritative check rather than defaulting to send.
func evaluateDeliveryRelevance(_ context.Context, delivery *model.MailDelivery) (deliveryRelevance, error) {
	if delivery == nil || delivery.Validate() != nil {
		return 0, errors.New("mail delivery relevance target is invalid")
	}

	switch delivery.TemplateKey {
	case model.MailTemplateSystemTest,
		model.MailTemplateIdentityVerifyEmail,
		model.MailTemplateIdentityPasswordReset,
		model.MailTemplateIdentityPasswordChanged,
		model.MailTemplateIdentityEmailChangeWarningOld,
		model.MailTemplateIdentityEmailChangeVerifyNew,
		model.MailTemplateIdentityEmailVerifiedByAdmin,
		model.MailTemplateIdentityAccountDisabled,
		model.MailTemplateIdentityAccountEnabled,
		model.MailTemplateIdentitySessionsRevokedByAdmin,
		model.MailTemplateIdentityMFAReset,
		model.MailTemplateIdentityMFAEnabled,
		model.MailTemplateIdentityMFADisabled,
		model.MailTemplateIdentityMFARecoveryCodesRegenerated,
		model.MailTemplateIdentityPersonalAccessTokenCreated,
		model.MailTemplateIdentityPersonalAccessTokenEnabled,
		model.MailTemplateIdentityPersonalAccessTokenDisabled,
		model.MailTemplateIdentityPersonalAccessTokenRevoked,
		model.MailTemplateAccessStudentClassInvitation,
		model.MailTemplateAccessTeacherAcademicUnitInvitation,
		model.MailTemplateAccessAcademicUnitRoleInvitation,
		model.MailTemplateAccessInstitutionRoleInvitation,
		model.MailTemplateAccessInvitationAccepted,
		model.MailTemplateAccessInvitationRevoked,
		model.MailTemplateAcademicClassEnrolled,
		model.MailTemplateAcademicClassEnrollmentEnded,
		model.MailTemplateAcademicClassTransferred,
		model.MailTemplateAcademicUnitAssigned,
		model.MailTemplateAcademicUnitAssignmentEnded,
		model.MailTemplateAuthorizationScopedRoleAssigned,
		model.MailTemplateAuthorizationScopedRoleEnded,
		model.MailTemplateAuthorizationInstitutionRoleAssigned,
		model.MailTemplateAuthorizationInstitutionRoleEnded,
		model.MailTemplateExamSittingScheduled,
		model.MailTemplateExamSittingRescheduled,
		model.MailTemplateExamSittingCanceled,
		model.MailTemplateExamSittingAssignmentRemoved,
		model.MailTemplateExamManagerAdded,
		model.MailTemplateExamManagerRemoved,
		model.MailTemplateExamOwnershipTransferredToYou,
		model.MailTemplateExamOwnershipTransferredFromYou,
		model.MailTemplateExamRetentionScheduled,
		model.MailTemplateExamSubmissionReceived,
		model.MailTemplateExamSubmissionManagerEnded,
		model.MailTemplateExamSubmissionAutomaticallySealed,
		model.MailTemplateExamResultReleased:
		// Purpose-bound credential deliveries and retention grace notices
		// receive their authoritative multi-node relevance fence in
		// MailStore.StartDelivery. The remaining immutable notifications are
		// relevant until their deadline.
		return deliveryRelevant, nil
	default:
		return 0, errors.New("mail delivery relevance policy is unavailable")
	}
}
