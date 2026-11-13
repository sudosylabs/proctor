// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package model

import "slices"

// AuditSelfEvent is the stable, client-labeled name of one account-relevant
// audit fact in a User's own history. An action name alone is too coarse to
// label (session.manage covers revoking one, all, or the other Sessions), so
// each name is derived from the recorded action, the operation the writer put
// in its parameters, and, for sign-in only, the terminal status.
type AuditSelfEvent string

const (
	AuditSelfEventSignInSucceeded                 AuditSelfEvent = "sign_in.succeeded"
	AuditSelfEventSignInFailed                    AuditSelfEvent = "sign_in.failed"
	AuditSelfEventSignedOut                       AuditSelfEvent = "sign_out"
	AuditSelfEventDesktopSignedIn                 AuditSelfEvent = "desktop.signed_in"
	AuditSelfEventDesktopRegistrationRevoked      AuditSelfEvent = "desktop.registration_revoked"
	AuditSelfEventDesktopRegistrationRenamed      AuditSelfEvent = "desktop.registration_renamed"
	AuditSelfEventMFASetupStarted                 AuditSelfEvent = "mfa.setup_started"
	AuditSelfEventMFAEnabled                      AuditSelfEvent = "mfa.enabled"
	AuditSelfEventMFADisabled                     AuditSelfEvent = "mfa.disabled"
	AuditSelfEventMFARecoveryCodesRegenerated     AuditSelfEvent = "mfa.recovery_codes_regenerated"
	AuditSelfEventMFAResetByAdministrator         AuditSelfEvent = "mfa.reset_by_administrator"
	AuditSelfEventAccountRecoveredByAdministrator AuditSelfEvent = "account.recovered_by_administrator"
	AuditSelfEventPasswordEnrolled                AuditSelfEvent = "password.enrolled"
	AuditSelfEventPasswordChanged                 AuditSelfEvent = "password.changed"
	AuditSelfEventPasswordRemoved                 AuditSelfEvent = "password.removed"
	AuditSelfEventPasswordReset                   AuditSelfEvent = "password.reset"
	AuditSelfEventProviderUnlinked                AuditSelfEvent = "provider.unlinked"
	AuditSelfEventSessionRevoked                  AuditSelfEvent = "session.revoked"
	AuditSelfEventSessionsRevokedAll              AuditSelfEvent = "sessions.revoked_all"
	AuditSelfEventSessionsRevokedOthers           AuditSelfEvent = "sessions.revoked_others"
	AuditSelfEventSessionRenamed                  AuditSelfEvent = "session.renamed"
	AuditSelfEventTokenCreated                    AuditSelfEvent = "personal_access_token.created"
	AuditSelfEventTokenRevoked                    AuditSelfEvent = "personal_access_token.revoked"
	AuditSelfEventTokenEnabled                    AuditSelfEvent = "personal_access_token.enabled"
	AuditSelfEventTokenDisabled                   AuditSelfEvent = "personal_access_token.disabled"
	AuditSelfEventProfileChanged                  AuditSelfEvent = "profile.changed"
	AuditSelfEventProfilePictureChanged           AuditSelfEvent = "profile_picture.changed"
	AuditSelfEventProfilePictureRemoved           AuditSelfEvent = "profile_picture.removed"
	AuditSelfEventEmailChanged                    AuditSelfEvent = "email.changed"
	AuditSelfEventEmailVerified                   AuditSelfEvent = "email.verified"
	AuditSelfEventEmailChangeUndone               AuditSelfEvent = "email.change_undone"
	AuditSelfEventAccountDisabled                 AuditSelfEvent = "account.disabled"
	AuditSelfEventAccountEnabled                  AuditSelfEvent = "account.enabled"
	AuditSelfEventAffiliationStarted              AuditSelfEvent = "affiliation.started"
	AuditSelfEventAffiliationEnded                AuditSelfEvent = "affiliation.ended"
	AuditSelfEventInvitationAccepted              AuditSelfEvent = "invitation.accepted"
	AuditSelfEventRoleGranted                     AuditSelfEvent = "role.granted"
	AuditSelfEventRoleEnded                       AuditSelfEvent = "role.ended"
	AuditSelfEventAcademicUnitJoined              AuditSelfEvent = "academic_unit.joined"
	AuditSelfEventAcademicUnitLeft                AuditSelfEvent = "academic_unit.left"
	AuditSelfEventClassEnrolled                   AuditSelfEvent = "class.enrolled"
	AuditSelfEventClassTransferredIn              AuditSelfEvent = "class.transferred_in"
	AuditSelfEventClassTransferredOut             AuditSelfEvent = "class.transferred_out"
	AuditSelfEventClassUnenrolled                 AuditSelfEvent = "class.unenrolled"
)

// The operation strings writers record in audit parameters. They are part of
// this catalogue's key, so a writer that renames one must change it here too.
const (
	AuditOperationDisableAccount            = "disable_account"
	AuditOperationEnableAccount             = "enable_account"
	AuditOperationRenameDesktopRegistration = "rename"
	AuditOperationRenameOwnSession          = "rename_own_session"
)

// AuditSelfEventKey is one exact (action, operation, status) combination a
// self-history event name covers. Status is always terminal.
type AuditSelfEventKey struct {
	Action    string
	Operation string
	Status    AuditStatus
}

// auditSelfEventRule maps a recorded action and operation to a name. A zero
// status matches either terminal status; sign-in names bind the status
// because success and failure are different facts to the account holder.
type auditSelfEventRule struct {
	action    Action
	operation string
	status    AuditStatus
	event     AuditSelfEvent
}

// auditSelfEventRules is the closed catalogue. Rows matching no rule are not
// part of anyone's self history.
var auditSelfEventRules = []auditSelfEventRule{
	{ActionAuthenticationLogin, "", AuditStatusSuccess, AuditSelfEventSignInSucceeded},
	{ActionAuthenticationLogin, "", AuditStatusFail, AuditSelfEventSignInFailed},
	{ActionAuthenticationExternalLogin, "", AuditStatusSuccess, AuditSelfEventSignInSucceeded},
	{ActionAuthenticationExternalLogin, "", AuditStatusFail, AuditSelfEventSignInFailed},
	{ActionSessionManage, "logout", "", AuditSelfEventSignedOut},
	{ActionAuthenticationDesktopSessionIssue, "exchange", AuditStatusSuccess, AuditSelfEventDesktopSignedIn},
	{ActionDesktopRegistrationRevoke, "revoke", "", AuditSelfEventDesktopRegistrationRevoked},
	{ActionDesktopRegistrationRename, AuditOperationRenameDesktopRegistration, "", AuditSelfEventDesktopRegistrationRenamed},

	{ActionMFASetup, "", "", AuditSelfEventMFASetupStarted},
	{ActionMFAActivate, "", "", AuditSelfEventMFAEnabled},
	{ActionMFADisable, "", "", AuditSelfEventMFADisabled},
	{ActionMFARecoveryCodesRegenerate, "", "", AuditSelfEventMFARecoveryCodesRegenerated},
	{ActionUserMFAReset, "", "", AuditSelfEventMFAResetByAdministrator},
	{ActionAuthenticationAdministratorMFAReset, "", "", AuditSelfEventMFAResetByAdministrator},
	{ActionAuthenticationAdministratorRecovery, "", "", AuditSelfEventAccountRecoveredByAdministrator},

	{ActionExternalIdentityManage, "enroll_password", "", AuditSelfEventPasswordEnrolled},
	{ActionExternalIdentityManage, "change_password", "", AuditSelfEventPasswordChanged},
	{ActionExternalIdentityManage, "remove_password", "", AuditSelfEventPasswordRemoved},
	{ActionAuthenticationPasswordResetComplete, "", "", AuditSelfEventPasswordReset},
	{ActionExternalIdentityManage, "unlink_provider", "", AuditSelfEventProviderUnlinked},

	{ActionSessionManage, "revoke_own_session", "", AuditSelfEventSessionRevoked},
	{ActionSessionManage, "revoke_session", "", AuditSelfEventSessionRevoked},
	{ActionSessionManage, "revoke_own_sessions", "", AuditSelfEventSessionsRevokedAll},
	{ActionSessionManage, "revoke_sessions", "", AuditSelfEventSessionsRevokedAll},
	{ActionSessionManage, "revoke_other_sessions", "", AuditSelfEventSessionsRevokedOthers},
	{ActionSessionManage, AuditOperationRenameOwnSession, "", AuditSelfEventSessionRenamed},

	{ActionPersonalAccessTokenCreate, "", "", AuditSelfEventTokenCreated},
	{ActionPersonalAccessTokenRevoke, "", "", AuditSelfEventTokenRevoked},
	{ActionPersonalAccessTokenEnable, "", "", AuditSelfEventTokenEnabled},
	{ActionPersonalAccessTokenDisable, "", "", AuditSelfEventTokenDisabled},

	{ActionUserManage, "update_profile", "", AuditSelfEventProfileChanged},
	{ActionUserManage, "set_profile_picture", "", AuditSelfEventProfilePictureChanged},
	{ActionUserProfilePictureManage, "set_profile_picture", "", AuditSelfEventProfilePictureChanged},
	{ActionUserManage, "remove_profile_picture", "", AuditSelfEventProfilePictureRemoved},
	{ActionUserProfilePictureManage, "remove_profile_picture", "", AuditSelfEventProfilePictureRemoved},
	{ActionUserManage, "change_email", "", AuditSelfEventEmailChanged},
	{ActionUserManage, "verify_email_privileged", "", AuditSelfEventEmailVerified},
	{ActionAuthenticationEmailVerificationComplete, "", "", AuditSelfEventEmailVerified},
	{ActionAuthenticationEmailChangeUndo, "", "", AuditSelfEventEmailChangeUndone},
	{ActionUserManage, AuditOperationDisableAccount, "", AuditSelfEventAccountDisabled},
	{ActionUserManage, AuditOperationEnableAccount, "", AuditSelfEventAccountEnabled},
	{ActionUserManage, "create", "", AuditSelfEventAffiliationStarted},
	{ActionUserManage, "end", "", AuditSelfEventAffiliationEnded},

	{ActionInvitationAccept, "", "", AuditSelfEventInvitationAccepted},
	{ActionInvitationAccept, "accept_scoped_role_invitation", "", AuditSelfEventInvitationAccepted},
	{ActionRoleBindingManage, "create_binding", "", AuditSelfEventRoleGranted},
	{ActionRoleBindingManage, "end_binding", "", AuditSelfEventRoleEnded},
	{ActionAcademicUnitMembersManage, "create_member", "", AuditSelfEventAcademicUnitJoined},
	{ActionAcademicUnitMembersManage, "end_member", "", AuditSelfEventAcademicUnitLeft},
	{ActionClassMembersManage, "enroll", "", AuditSelfEventClassEnrolled},
	{ActionClassMembersManage, "transfer_in", "", AuditSelfEventClassTransferredIn},
	{ActionClassMembersManage, "transfer_out", "", AuditSelfEventClassTransferredOut},
	{ActionClassMembersManage, "end", "", AuditSelfEventClassUnenrolled},
}

// IsValid reports whether the name belongs to the closed catalogue.
func (e AuditSelfEvent) IsValid() bool {
	for _, rule := range auditSelfEventRules {
		if rule.event == e {
			return true
		}
	}
	return false
}

// AuditSelfEventFor names a terminal audit fact, or reports that it is not
// part of self history.
func AuditSelfEventFor(action, operation string, status AuditStatus) (AuditSelfEvent, bool) {
	if status != AuditStatusSuccess && status != AuditStatusFail {
		return "", false
	}

	for _, rule := range auditSelfEventRules {
		if string(rule.action) == action &&
			rule.operation == operation &&
			(rule.status == "" || rule.status == status) {
			return rule.event, true
		}
	}

	return "", false
}

// AuditSelfEventKeys expands event names into the exact keys persistence
// matches. Empty events select the whole catalogue; a non-empty status keeps
// only keys with that terminal status. Unknown names select nothing.
func AuditSelfEventKeys(events []AuditSelfEvent, status AuditStatus) []AuditSelfEventKey {
	keys := make([]AuditSelfEventKey, 0, len(auditSelfEventRules)*2)
	for _, rule := range auditSelfEventRules {
		if len(events) > 0 && !slices.Contains(events, rule.event) {
			continue
		}
		for _, terminal := range []AuditStatus{AuditStatusSuccess, AuditStatusFail} {
			if (rule.status != "" && rule.status != terminal) || (status != "" && status != terminal) {
				continue
			}
			keys = append(keys, AuditSelfEventKey{Action: string(rule.action), Operation: rule.operation, Status: terminal})
		}
	}
	return keys
}

// singularAuditSelfEvents are the events whose rows never follow one another
// in one actor's history, so a history never folds them into a run. Each
// needs a change in between before it can happen again and is never done to
// many people at once: two-step verification is turned on only by the User
// for themself, an email change is undone once through its own link, offline
// administrator recovery restores one account at a time, and signing out the
// other Sessions leaves none to sign out until the User signs in again.
var singularAuditSelfEvents = []AuditSelfEvent{
	AuditSelfEventMFAEnabled,
	AuditSelfEventEmailChangeUndone,
	AuditSelfEventAccountRecoveredByAdministrator,
	AuditSelfEventSessionsRevokedOthers,
}

// Repeats reports whether consecutive rows of this event may be presented as
// one run, such as an administrator's change to many people's profiles or a
// User's repeated sign-ins.
func (e AuditSelfEvent) Repeats() bool {
	return e.IsValid() && !slices.Contains(singularAuditSelfEvents, e)
}
