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

// The audit kinds. A read only looks: a view, a search, a list, or an export
// that copies records out without changing them. A change mutates state,
// authenticates someone, issues or revokes a credential, or sends mail.
const (
	AuditKindRead   AuditKind = "read"
	AuditKindChange AuditKind = "change"
)

// IsValid reports whether the kind belongs to the closed vocabulary.
func (k AuditKind) IsValid() bool {
	return k == AuditKindRead || k == AuditKindChange
}

// auditActionKinds classifies every audit action, permission and event-only
// alike. A test pins it to AuditActions, so a new action cannot be listed
// before it is classified.
var auditActionKinds = map[Action]AuditKind{
	// Reads: views, searches and exports that copy records out.
	ActionAcademicAuditView:              AuditKindRead,
	ActionAcademicPeriodView:             AuditKindRead,
	ActionAcademicUnitMembersView:        AuditKindRead,
	ActionAcademicUnitView:               AuditKindRead,
	ActionAccessPolicyView:               AuditKindRead,
	ActionAuditView:                      AuditKindRead,
	ActionClassMembersView:               AuditKindRead,
	ActionClassView:                      AuditKindRead,
	ActionExamAttemptBrowserActivityView: AuditKindRead,
	// An export writes nothing back; it is a read of the records it copies.
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

	// Changes to academic structure, Exams, records and policy.
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
	// Taking part in a Sitting starts and submits Attempts.
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
	// A review records a decision about a Submission.
	ActionSubmissionReview:         AuditKindChange,
	ActionSubmissionReviewOverride: AuditKindChange,
	ActionUserManage:               AuditKindChange,
	ActionUserProfilePictureManage: AuditKindChange,
	ActionUserSettingsReplace:      AuditKindChange,

	// Authentication is a change even when it fails: a sign-in, a second
	// factor, or a reauthentication issues or raises a Session, and a failure
	// counts against the account. Requests for a verification or reset link
	// issue a token and send mail.
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

	// Credentials, Sessions and devices issued, changed or revoked.
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

// AuditActionKind classifies one audit action, or reports that the action is
// not in the audit action catalog.
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
