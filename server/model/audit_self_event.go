// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package model

import "slices"

// AuditSelfEvent is the stable, client-labeled name of one fact in a user's
// own account history. An action alone is too coarse (session.manage covers
// revoking one, all or the other sessions), so a name comes from the action,
// the writer's recorded operation and, for sign-in only, the final status.
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

// Operation strings writers record in audit parameters. They are part of the
// catalogue's key, so renaming one in a writer means changing it here too.
const (
	AuditOperationDisableAccount            = "disable_account"
	AuditOperationEnableAccount             = "enable_account"
	AuditOperationRenameDesktopRegistration = "rename"
	AuditOperationRenameOwnSession          = "rename_own_session"
)

// AuditSelfEventKey is one exact (action, operation, status) combination a
// self-history name covers; Status is always final.
type AuditSelfEventKey struct {
	Action    string
	Operation string
	Status    AuditStatus
}

// A zero status matches either final status. Sign-in rules bind the status
// because success and failure are different facts to the account holder.
type auditSelfEventRule struct {
	action    Action
	operation string
	status    AuditStatus
	event     AuditSelfEvent
}

// Rows matching no rule are not part of anyone's self history.
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

// IsValid reports whether e is a name in the catalogue.
func (e AuditSelfEvent) IsValid() bool {
	for _, rule := range auditSelfEventRules {
		if rule.event == e {
			return true
		}
	}
	return false
}

// AuditSelfEventFor names a final audit fact; false means it is not part of
// self history.
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

// AuditSelfEventKeys expands names into the exact keys persistence matches. No
// names selects the whole catalogue, a non-empty status keeps only keys with
// it, and unknown names select nothing.
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

// singularAuditSelfEvents never repeat back to back in one actor's history, so
// they are never folded into a run: each needs a change in between and is
// never done to many people at once. For example, signing out the other
// sessions leaves none to sign out until the user signs in again.
var singularAuditSelfEvents = []AuditSelfEvent{
	AuditSelfEventMFAEnabled,
	AuditSelfEventEmailChangeUndone,
	AuditSelfEventAccountRecoveredByAdministrator,
	AuditSelfEventSessionsRevokedOthers,
}

// Repeats reports whether consecutive rows of this event may be shown as one
// run, such as a user's repeated sign-ins.
func (e AuditSelfEvent) Repeats() bool {
	return e.IsValid() && !slices.Contains(singularAuditSelfEvents, e)
}
