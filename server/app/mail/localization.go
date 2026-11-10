// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package mail

import (
	"slices"

	"github.com/sudosylabs/proctor/server/localization"
	"github.com/sudosylabs/proctor/server/model"
)

const localizationOrigin = "app/mail"

// requiredCopyFields are present for every template.
var requiredCopyFields = []string{"subject", "preheader", "heading", "body", "why"}

// optionalCopyFields is the closed per-template set of additional copy. A
// listed field is required in English for that template; an unlisted field is
// never read.
var optionalCopyFields = map[model.MailTemplateKey][]string{
	model.MailTemplateAcademicUnitAssigned:                 {"at_label"},
	model.MailTemplateAcademicUnitAssignmentEnded:          {"at_label"},
	model.MailTemplateAcademicClassEnrolled:                {},
	model.MailTemplateAcademicClassEnrollmentEnded:         {},
	model.MailTemplateAcademicClassTransferred:             {},
	model.MailTemplateAccessAcademicUnitRoleInvitation:     {"action_label", "action_note", "aside_title", "aside_text", "expires_label"},
	model.MailTemplateAccessInstitutionRoleInvitation:      {"action_label", "action_note", "aside_title", "aside_text", "expires_label"},
	model.MailTemplateAccessInvitationAccepted:             {"aside_title", "aside_text", "address_label"},
	model.MailTemplateAccessInvitationRevoked:              {"at_label"},
	model.MailTemplateAccessStudentClassInvitation:         {"action_label", "action_note", "aside_title", "aside_text", "expires_label"},
	model.MailTemplateAccessTeacherAcademicUnitInvitation:  {"action_label", "action_note", "aside_title", "aside_text", "expires_label"},
	model.MailTemplateAuthorizationInstitutionRoleAssigned: {"aside_title", "aside_text", "at_label"},
	model.MailTemplateAuthorizationInstitutionRoleEnded:    {"at_label"},
	model.MailTemplateAuthorizationScopedRoleAssigned:      {"aside_title", "aside_text", "at_label"},
	model.MailTemplateAuthorizationScopedRoleEnded:         {"at_label"},
	model.MailTemplateExamManagerAdded:                     {},
	model.MailTemplateExamManagerRemoved:                   {},
	model.MailTemplateExamOwnershipTransferredFromYou:      {},
	model.MailTemplateExamOwnershipTransferredToYou:        {},
	model.MailTemplateExamResultReleased:                   {},
	model.MailTemplateExamRetentionScheduled:               {"aside_title", "aside_text"},
	model.MailTemplateExamSittingAssignmentRemoved:         {},
	model.MailTemplateExamSittingCanceled:                  {},
	model.MailTemplateExamSittingRescheduled:               {},
	model.MailTemplateExamSittingScheduled:                 {"aside_title", "aside_text"},
	model.MailTemplateExamSubmissionAutomaticallySealed:    {},
	model.MailTemplateExamSubmissionManagerEnded:           {},
	model.MailTemplateExamSubmissionReceived:               {"aside_title", "aside_text"},
	model.MailTemplateIdentityAccountDisabled:              {"address_label", "at_label"},
	model.MailTemplateIdentityAccountEnabled:               {"address_label", "at_label"},
	model.MailTemplateIdentityEmailChangeVerifyNew: {
		"action_label",
		"action_note",
		"aside_title",
		"aside_text",
		"address_label",
		"at_label",
	},
	model.MailTemplateIdentityEmailChangeWarningOld: {
		"action_label",
		"action_note",
		"aside_title",
		"aside_text",
		"address_label",
		"at_label",
	},
	model.MailTemplateIdentityEmailVerifiedByAdmin:        {"aside_title", "aside_text", "address_label", "at_label"},
	model.MailTemplateIdentityMFADisabled:                 {"aside_title", "aside_text", "address_label", "at_label"},
	model.MailTemplateIdentityMFAEnabled:                  {"aside_title", "aside_text", "address_label", "at_label"},
	model.MailTemplateIdentityMFARecoveryCodesRegenerated: {"aside_title", "aside_text", "address_label", "at_label"},
	model.MailTemplateIdentityMFAReset:                    {"aside_title", "aside_text", "address_label", "at_label"},
	model.MailTemplateIdentityPasswordChanged:             {"aside_title", "aside_text", "address_label", "at_label"},
	model.MailTemplateIdentityPasswordReset: {
		"action_label",
		"action_note",
		"aside_title",
		"aside_text",
		"address_label",
		"at_label",
	},
	model.MailTemplateIdentityPersonalAccessTokenCreated:  {"aside_title", "aside_text"},
	model.MailTemplateIdentityPersonalAccessTokenDisabled: {"aside_title", "aside_text"},
	model.MailTemplateIdentityPersonalAccessTokenEnabled:  {"aside_title", "aside_text"},
	model.MailTemplateIdentityPersonalAccessTokenRevoked:  {"aside_title", "aside_text"},
	model.MailTemplateIdentitySessionsRevokedByAdmin:      {"aside_title", "aside_text", "address_label", "at_label"},
	model.MailTemplateIdentityVerifyEmail:                 {"action_label", "action_note", "aside_title", "aside_text", "address_label"},
	model.MailTemplateSystemTest:                          {"address_label", "at_label"},
}

// commonCopyFields are shared by every template under mail.common.
var commonCopyFields = []string{
	"greeting",
	"greeting_anonymous",
	"institution_fallback",
	"support",
	"support_fallback",
	"sent_by",
	"action_fallback",
	"action_fallback_link",
	"kind.security",
	"kind.account",
	"kind.invitation",
	"kind.classes",
	"kind.access",
	"kind.exams",
	"kind.system",
	"label.exam",
	"label.class",
	"label.previous_class",
	"label.from_class",
	"label.to_class",
	"label.when",
	"label.was_scheduled",
	"label.enrolled_from",
	"label.enrolled_until",
	"label.no_end",
	"label.your_role",
	"label.since",
	"label.token",
	"label.access",
	"label.expires",
	"label.released",
	"label.submitted",
	"label.ended",
	"label.sealed",
	"label.receipt_code",
	"label.pat_created",
	"label.pat_disabled",
	"label.pat_enabled",
	"label.pat_revoked",
	"label.new_address",
	"label.deletion_date",
	"value.institution_scope",
	"value.academic_unit_scope",
	"value.manager",
	"value.owner",
	"value.no_longer_manager",
	"value.duration_hours",
	"value.duration_minutes",
	"value.duration_join",
}

// copyVariables declares the exact placeholders each English message uses.
// Catalog validation rejects any drift between this list and en.json.
var copyVariables = map[string][]string{
	"mail.common.greeting":                                    {"Name"},
	"mail.common.support":                                     {"Address"},
	"mail.common.support_fallback":                            {"Institution"},
	"mail.common.sent_by":                                     {"Institution"},
	"mail.common.value.duration_hours":                        {"PluralCount"},
	"mail.common.value.duration_minutes":                      {"PluralCount"},
	"mail.common.value.duration_join":                         {"Hours", "Minutes"},
	"mail.academic.academic_unit_assigned.why":                {"Institution"},
	"mail.academic.academic_unit_assignment_ended.why":        {"Institution"},
	"mail.academic.class_enrolled.subject":                    {"Class"},
	"mail.academic.class_enrolled.why":                        {"Institution"},
	"mail.academic.class_enrollment_ended.subject":            {"Class"},
	"mail.academic.class_enrollment_ended.why":                {"Institution"},
	"mail.academic.class_transferred.subject":                 {"Class"},
	"mail.academic.class_transferred.preheader":               {"PreviousClass"},
	"mail.academic.class_transferred.why":                     {"Institution"},
	"mail.access.academic_unit_role_invitation.subject":       {"Institution"},
	"mail.access.academic_unit_role_invitation.body":          {"Institution"},
	"mail.access.academic_unit_role_invitation.why":           {"Institution"},
	"mail.access.institution_role_invitation.subject":         {"Institution"},
	"mail.access.institution_role_invitation.body":            {"Institution"},
	"mail.access.institution_role_invitation.why":             {"Institution"},
	"mail.access.invitation_accepted.subject":                 {"Institution"},
	"mail.access.invitation_accepted.why":                     {"Institution"},
	"mail.access.invitation_revoked.body":                     {"Institution"},
	"mail.access.invitation_revoked.why":                      {"Institution"},
	"mail.access.student_class_invitation.subject":            {"Institution"},
	"mail.access.student_class_invitation.body":               {"Institution"},
	"mail.access.student_class_invitation.why":                {"Institution"},
	"mail.access.teacher_academic_unit_invitation.subject":    {"Institution"},
	"mail.access.teacher_academic_unit_invitation.body":       {"Institution"},
	"mail.access.teacher_academic_unit_invitation.why":        {"Institution"},
	"mail.authorization.institution_role_assigned.preheader":  {"Institution"},
	"mail.authorization.institution_role_assigned.body":       {"Institution"},
	"mail.authorization.institution_role_assigned.why":        {"Institution"},
	"mail.authorization.institution_role_ended.why":           {"Institution"},
	"mail.authorization.scoped_role_assigned.preheader":       {"Institution"},
	"mail.authorization.scoped_role_assigned.why":             {"Institution"},
	"mail.authorization.scoped_role_ended.why":                {"Institution"},
	"mail.exam.manager_added.subject":                         {"ExamTitle"},
	"mail.exam.manager_added.why":                             {"Institution"},
	"mail.exam.manager_removed.subject":                       {"ExamTitle"},
	"mail.exam.manager_removed.why":                           {"Institution"},
	"mail.exam.ownership_transferred_from_you.subject":        {"ExamTitle"},
	"mail.exam.ownership_transferred_from_you.why":            {"Institution"},
	"mail.exam.ownership_transferred_to_you.subject":          {"ExamTitle"},
	"mail.exam.ownership_transferred_to_you.why":              {"Institution"},
	"mail.exam.result_released.subject":                       {"ExamTitle"},
	"mail.exam.result_released.why":                           {"Institution"},
	"mail.exam.retention_scheduled.subject":                   {"Day"},
	"mail.exam.retention_scheduled.preheader":                 {"Institution"},
	"mail.exam.retention_scheduled.body":                      {"Institution"},
	"mail.exam.retention_scheduled.why":                       {"Institution"},
	"mail.exam.sitting_assignment_removed.subject":            {"ExamTitle"},
	"mail.exam.sitting_assignment_removed.why":                {"Institution"},
	"mail.exam.sitting_canceled.subject":                      {"Day", "ExamTitle"},
	"mail.exam.sitting_canceled.why":                          {"Institution"},
	"mail.exam.sitting_rescheduled.subject":                   {"ExamTitle", "When"},
	"mail.exam.sitting_rescheduled.why":                       {"Institution"},
	"mail.exam.sitting_scheduled.subject":                     {"ExamTitle", "When"},
	"mail.exam.sitting_scheduled.preheader":                   {"Class"},
	"mail.exam.sitting_scheduled.why":                         {"Institution"},
	"mail.exam.submission_automatically_sealed.subject":       {"ExamTitle"},
	"mail.exam.submission_automatically_sealed.why":           {"Institution"},
	"mail.exam.submission_manager_ended.subject":              {"ExamTitle"},
	"mail.exam.submission_manager_ended.why":                  {"Institution"},
	"mail.exam.submission_received.subject":                   {"ExamTitle"},
	"mail.exam.submission_received.why":                       {"Institution"},
	"mail.identity.account_disabled.body":                     {"Institution"},
	"mail.identity.email_change_verify_new.body":              {"Institution"},
	"mail.identity.email_change_verify_new.action_note":       {"Deadline"},
	"mail.identity.email_change_verify_new.why":               {"Institution"},
	"mail.identity.email_change_warning_old.preheader":        {"Deadline"},
	"mail.identity.email_change_warning_old.action_note":      {"Deadline"},
	"mail.identity.mfa_enabled.aside_text":                    {"Institution"},
	"mail.identity.mfa_recovery_codes_regenerated.aside_text": {"Institution"},
	"mail.identity.mfa_reset.body":                            {"Institution"},
	"mail.identity.mfa_reset.aside_text":                      {"Institution"},
	"mail.identity.password_reset.action_note":                {"Deadline"},
	"mail.identity.personal_access_token_created.preheader":   {"Token"},
	"mail.identity.personal_access_token_disabled.preheader":  {"Token"},
	"mail.identity.personal_access_token_enabled.preheader":   {"Token"},
	"mail.identity.personal_access_token_revoked.preheader":   {"Token"},
	"mail.identity.verify_email.body":                         {"Institution"},
	"mail.identity.verify_email.action_note":                  {"Deadline"},
	"mail.identity.verify_email.why":                          {"Institution"},
	"mail.system.mail_test.body":                              {"Institution"},
}

// pluralCopy lists messages that select a plural form by PluralCount.
var pluralCopy = []string{
	"mail.common.value.duration_hours",
	"mail.common.value.duration_minutes",
}

func copyDefinition(id string) localization.Definition {
	value := localization.Definition{ID: id, Origin: localizationOrigin, Variables: copyVariables[id]}
	if slices.Contains(pluralCopy, id) {
		value.PluralVariable = "PluralCount"
	}
	return value
}

// LocalizationDefinitions returns the closed set of copy consumed by the mail
// renderer. The IDs are derived from the same typed template registry used at
// runtime, so adding a template cannot silently escape catalog validation.
func LocalizationDefinitions() []localization.Definition {
	definitions := make([]localization.Definition, 0, len(model.AllMailTemplateKeys())*8+len(commonCopyFields))
	for _, field := range commonCopyFields {
		definitions = append(definitions, copyDefinition("mail.common."+field))
	}
	for _, key := range model.AllMailTemplateKeys() {
		for _, field := range localizationFields(key) {
			definitions = append(definitions, copyDefinition(localizationID(key, field)))
		}
	}
	return definitions
}

func localizationID(key model.MailTemplateKey, field string) string {
	return "mail." + string(key) + "." + field
}

func localizationFields(key model.MailTemplateKey) []string {
	return append(slices.Clone(requiredCopyFields), optionalCopyFields[key]...)
}

func hasCopyField(key model.MailTemplateKey, field string) bool {
	return slices.Contains(optionalCopyFields[key], field)
}
