// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package model

// AuditKind says whether an audit action records looking at something or
// changing something, so an audit reader can ask for changes only.
type AuditKind string

// A read only looks, including an export that copies records out. A change
// mutates state, authenticates someone, issues or revokes a credential, or
// sends mail.
const (
	AuditKindRead   AuditKind = "read"
	AuditKindChange AuditKind = "change"
)

// IsValid reports whether k is AuditKindRead or AuditKindChange.
func (k AuditKind) IsValid() bool {
	return k == AuditKindRead || k == AuditKindChange
}

// A test pins auditActionKinds to AuditActions, so a new action cannot be
// listed before it is classified.
var auditActionKinds = map[Action]AuditKind{
	// Reads.
	ActionAcademicAuditView:              AuditKindRead,
	ActionAcademicPeriodView:             AuditKindRead,
	ActionAcademicUnitMembersView:        AuditKindRead,
	ActionAcademicUnitView:               AuditKindRead,
	ActionAccessPolicyView:               AuditKindRead,
	ActionAuditView:                      AuditKindRead,
	ActionClassMembersView:               AuditKindRead,
	ActionClassView:                      AuditKindRead,
	ActionExamAttemptBrowserActivityView: AuditKindRead,
	// An export writes nothing back, so it is a read.
	ActionExamRecordsExport:         AuditKindRead,
	ActionExamRecordsExportOverride: AuditKindRead,
	ActionExamSittingView:           AuditKindRead,
	ActionExamSittingViewOverride:   AuditKindRead,
	ActionExamView:                  AuditKindRead,
	ActionExamViewOverride:          AuditKindRead,
	ActionInvitationView:            AuditKindRead,
	ActionJobView:                   AuditKindRead,
	ActionMailView:                  AuditKindRead,
	ActionOnboardingBatchView:       AuditKindRead,
	ActionProgrammeLevelView:        AuditKindRead,
	ActionProgrammeView:             AuditKindRead,
	ActionRetentionPolicyView:       AuditKindRead,
	ActionRoleBindingView:           AuditKindRead,
	ActionRoleView:                  AuditKindRead,
	ActionSessionView:               AuditKindRead,
	ActionSubmissionView:            AuditKindRead,
	ActionSubmissionViewOverride:    AuditKindRead,
	ActionUserSearch:                AuditKindRead,
	ActionUserView:                  AuditKindRead,

	// Changes to academic structure, exams, records and policy.
	ActionAcademicPeriodManage:             AuditKindChange,
	ActionAcademicProgressionManage:        AuditKindChange,
	ActionAcademicUnitManage:               AuditKindChange,
	ActionAcademicUnitMembersManage:        AuditKindChange,
	ActionAccessPolicyManage:               AuditKindChange,
	ActionClassManage:                      AuditKindChange,
	ActionClassMembersManage:               AuditKindChange,
	ActionDesktopCompatibilityPolicyManage: AuditKindChange,
	ActionExamCreate:                       AuditKindChange,
	ActionExamCreateOverride:               AuditKindChange,
	ActionExamManage:                       AuditKindChange,
	ActionExamManageOverride:               AuditKindChange,
	ActionExamPublish:                      AuditKindChange,
	ActionExamPublishOverride:              AuditKindChange,
	ActionExamRecordsComplete:              AuditKindChange,
	ActionExamRecordsCompleteOverride:      AuditKindChange,
	ActionExamRecordsHold:                  AuditKindChange,
	ActionExamRecordsHoldOverride:          AuditKindChange,
	ActionExamSittingCreate:                AuditKindChange,
	ActionExamSittingCreateOverride:        AuditKindChange,
	ActionExamSittingManage:                AuditKindChange,
	ActionExamSittingManageOverride:        AuditKindChange,
	// Taking part in a sitting starts and submits attempts.
	ActionExamSittingParticipate:    AuditKindChange,
	ActionInstallationBootstrap:     AuditKindChange,
	ActionInstitutionManage:         AuditKindChange,
	ActionInvitationAccept:          AuditKindChange,
	ActionInvitationCreate:          AuditKindChange,
	ActionInvitationManage:          AuditKindChange,
	ActionJobManage:                 AuditKindChange,
	ActionMailManage:                AuditKindChange,
	ActionMailRekey:                 AuditKindChange,
	ActionOnboardingBatchManage:     AuditKindChange,
	ActionPluginManage:              AuditKindChange,
	ActionEnvironmentProfileManage:  AuditKindChange,
	ActionProgrammeLevelManage:      AuditKindChange,
	ActionProgrammeManage:           AuditKindChange,
	ActionRetentionCleanupManage:    AuditKindChange,
	ActionRetentionHoldRelease:      AuditKindChange,
	ActionRetentionPolicyManage:     AuditKindChange,
	ActionRoleBindingManage:         AuditKindChange,
	ActionRoleManage:                AuditKindChange,
	ActionRoleSystemAdminReconcile:  AuditKindChange,
	ActionSubmissionRelease:         AuditKindChange,
	ActionSubmissionReleaseOverride: AuditKindChange,
	// A review records a decision about a submission.
	ActionSubmissionReview:         AuditKindChange,
	ActionSubmissionReviewOverride: AuditKindChange,
	ActionUserManage:               AuditKindChange,
	ActionUserProfilePictureManage: AuditKindChange,
	ActionUserSettingsReplace:      AuditKindChange,

	// Authentication is a change even when it fails: success issues or raises a
	// session and failure counts against the account. Verification and reset
	// requests issue a token and send mail.
	ActionAuthenticationAdministratorMFAReset:     AuditKindChange,
	ActionAuthenticationAdministratorRecovery:     AuditKindChange,
	ActionAuthenticationDesktopAuthorization:      AuditKindChange,
	ActionAuthenticationDesktopSessionIssue:       AuditKindChange,
	ActionAuthenticationEmailChangeUndo:           AuditKindChange,
	ActionAuthenticationEmailVerificationComplete: AuditKindChange,
	ActionAuthenticationEmailVerificationRequest:  AuditKindChange,
	ActionAuthenticationExternalLogin:             AuditKindChange,
	ActionAuthenticationExternalProvision:         AuditKindChange,
	ActionAuthenticationLogin:                     AuditKindChange,
	ActionAuthenticationPasswordResetComplete:     AuditKindChange,
	ActionAuthenticationPasswordResetRequest:      AuditKindChange,
	ActionAuthenticationPublicRegistration:        AuditKindChange,
	ActionAuthenticationReauthenticate:            AuditKindChange,
	ActionMFAChallenge:                            AuditKindChange,

	// Credentials, sessions and devices issued, changed or revoked.
	ActionDesktopRegistrationRename:  AuditKindChange,
	ActionDesktopRegistrationRevoke:  AuditKindChange,
	ActionExternalIdentityManage:     AuditKindChange,
	ActionMFAActivate:                AuditKindChange,
	ActionMFADisable:                 AuditKindChange,
	ActionMFARecoveryCodesRegenerate: AuditKindChange,
	ActionMFARekey:                   AuditKindChange,
	ActionMFASetup:                   AuditKindChange,
	ActionPersonalAccessTokenCreate:  AuditKindChange,
	ActionPersonalAccessTokenDisable: AuditKindChange,
	ActionPersonalAccessTokenEnable:  AuditKindChange,
	ActionPersonalAccessTokenRevoke:  AuditKindChange,
	ActionSessionManage:              AuditKindChange,
	ActionUserMFAReset:               AuditKindChange,
}

// AuditActionKind classifies one audit action; false means it is not an audit
// action.
func AuditActionKind(action string) (AuditKind, bool) {
	kind, known := auditActionKinds[Action(action)]
	return kind, known
}

// AuditActionsOfKind returns, in stable order, every audit action of kind;
// an unknown kind has none.
func AuditActionsOfKind(kind AuditKind) []string {
	var actions []string
	for _, action := range AuditActions() {
		if actionKind, known := AuditActionKind(action); known && actionKind == kind {
			actions = append(actions, action)
		}
	}
	return actions
}
