// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package model

import "sort"

// Event-only audit actions are outcomes, not permissions: authorization never
// accepts them and nothing can grant them. Every permission action is also an
// audit action, because checked decisions record the action checked.
const (
	ActionAuthenticationLogin                     Action = "authentication.login"
	ActionAuthenticationExternalLogin             Action = "authentication.external_login"
	ActionAuthenticationExternalProvision         Action = "authentication.external_provision"
	ActionAuthenticationPublicRegistration        Action = "authentication.public_registration"
	ActionAuthenticationReauthenticate            Action = "authentication.reauthenticate"
	ActionAuthenticationDesktopAuthorization      Action = "authentication.desktop_authorization"
	ActionAuthenticationDesktopSessionIssue       Action = "authentication.desktop_session_issue"
	ActionAuthenticationEmailVerificationRequest  Action = "authentication.email_verification.request"
	ActionAuthenticationEmailVerificationComplete Action = "authentication.email_verification.complete"
	ActionAuthenticationPasswordResetRequest      Action = "authentication.password_reset.request"
	ActionAuthenticationPasswordResetComplete     Action = "authentication.password_reset.complete"
	ActionAuthenticationEmailChangeUndo           Action = "authentication.email_change.undo"
	ActionAuthenticationAdministratorRecovery     Action = "authentication.administrator_recovery"
	ActionAuthenticationAdministratorMFAReset     Action = "authentication.administrator_mfa_reset"
	ActionInstallationBootstrap                   Action = "installation.bootstrap"
	ActionRoleSystemAdminReconcile                Action = "role.system_admin.reconcile"
	ActionInvitationAccept                        Action = "invitation.accept"
	ActionUserSearch                              Action = "user.search"
	ActionUserSettingsReplace                     Action = "user.settings.replace"
	ActionDesktopRegistrationRename               Action = "desktop_registration.rename"
	ActionPersonalAccessTokenCreate               Action = "personal_access_token.create"
	ActionPersonalAccessTokenEnable               Action = "personal_access_token.enable"
	ActionPersonalAccessTokenDisable              Action = "personal_access_token.disable"
	ActionPersonalAccessTokenRevoke               Action = "personal_access_token.revoke"
	ActionMFASetup                                Action = "mfa.setup"
	ActionMFAActivate                             Action = "mfa.activate"
	ActionMFAChallenge                            Action = "mfa.challenge"
	ActionMFARecoveryCodesRegenerate              Action = "mfa.recovery_codes.regenerate"
	ActionMFADisable                              Action = "mfa.disable"
)

// eventAuditActionDefinition holds the audit-listing traits of one event-only
// action; AcademicAudit means the same as on ActionDefinition.
type eventAuditActionDefinition struct {
	AcademicAudit bool
}

var eventAuditActions = map[Action]eventAuditActionDefinition{
	ActionAuthenticationLogin:                     {},
	ActionAuthenticationExternalLogin:             {},
	ActionAuthenticationExternalProvision:         {},
	ActionAuthenticationPublicRegistration:        {},
	ActionAuthenticationReauthenticate:            {},
	ActionAuthenticationDesktopAuthorization:      {},
	ActionAuthenticationDesktopSessionIssue:       {},
	ActionAuthenticationEmailVerificationRequest:  {},
	ActionAuthenticationEmailVerificationComplete: {},
	ActionAuthenticationPasswordResetRequest:      {},
	ActionAuthenticationPasswordResetComplete:     {},
	ActionAuthenticationEmailChangeUndo:           {},
	ActionAuthenticationAdministratorRecovery:     {},
	ActionAuthenticationAdministratorMFAReset:     {},
	ActionInstallationBootstrap:                   {},
	ActionRoleSystemAdminReconcile:                {},
	ActionInvitationAccept:                        {},
	ActionUserSearch:                              {AcademicAudit: true},
	ActionUserSettingsReplace:                     {},
	ActionDesktopRegistrationRename:               {},
	ActionPersonalAccessTokenCreate:               {},
	ActionPersonalAccessTokenEnable:               {},
	ActionPersonalAccessTokenDisable:              {},
	ActionPersonalAccessTokenRevoke:               {},
	ActionMFASetup:                                {},
	ActionMFAActivate:                             {},
	ActionMFAChallenge:                            {},
	ActionMFARecoveryCodesRegenerate:              {},
	ActionMFADisable:                              {},
}

// AuditActions returns every declared audit action in stable order.
func AuditActions() []string {
	actions := make([]string, 0, len(actionDefinitions)+len(eventAuditActions))
	for action := range actionDefinitions {
		actions = append(actions, string(action))
	}
	for action := range eventAuditActions {
		actions = append(actions, string(action))
	}
	sort.Strings(actions)
	return actions
}

// AcademicAuditActions returns, in stable order, the actions declared with
// AcademicAudit: the only ones academic audit visibility may list.
func AcademicAuditActions() []string {
	actions := make([]string, 0, 32)
	for action, definition := range actionDefinitions {
		if definition.AcademicAudit {
			actions = append(actions, string(action))
		}
	}
	for action, definition := range eventAuditActions {
		if definition.AcademicAudit {
			actions = append(actions, string(action))
		}
	}
	sort.Strings(actions)
	return actions
}
