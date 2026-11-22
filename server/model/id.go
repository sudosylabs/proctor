// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package model

import (
	"encoding/json"
	"fmt"
)

//go:generate go run ./internal/idgen -source id.go -output id_gen.go

// Each entity has its own ID type. They share one opaque 26-character
// z-base-32 form but do not convert implicitly; the zero value is invalid.
// The constructors, parsers and codecs are generated from internal/idgen.

// InstitutionID identifies the singleton installation institution.
type InstitutionID string

// AccessPolicyID identifies the installation's revisioned access policy.
type AccessPolicyID string

// AcademicUnitID identifies a hierarchical academic unit.
type AcademicUnitID string

// ProgrammeID identifies a programme of study.
type ProgrammeID string

// ProgrammeLevelID identifies a curriculum stage within a programme.
type ProgrammeLevelID string

// ClassID identifies a time-bound class roster.
type ClassID string

// AcademicPeriodID identifies an academic enrollment period.
type AcademicPeriodID string

// ExamID identifies the stable authoring identity of an examination.
type ExamID string

// ExamRevisionID identifies one immutable published examination revision.
type ExamRevisionID string

// ExamSittingID identifies one scheduled delivery of a revision to one class.
type ExamSittingID string

// ExamResourceID identifies one supporting file attached to a draft.
type ExamResourceID string

// ExamCorrectionResourceStageID identifies one pending resource upload for a
// single live correction.
type ExamCorrectionResourceStageID string

// ExamAttemptID identifies one candidate's stable work identity for a sitting.
type ExamAttemptID string

// ExamAttemptWorkspaceID identifies the private workspace of one attempt.
type ExamAttemptWorkspaceID string

// SubmissionID identifies the single immutable submission sealed from one
// attempt.
type SubmissionID string

// RetentionHoldID identifies one manually released preservation hold.
type RetentionHoldID string

// RetentionPreviewID identifies one revision-bound retention preview.
type RetentionPreviewID string

// RetentionRetirementID identifies one irreversible category retirement receipt.
type RetentionRetirementID string

// ExamExportID identifies one finite examination archive.
type ExamExportID string

// AttemptParticipationID identifies one bounded continuity-credential lease.
type AttemptParticipationID string

// AttemptConnectionID identifies one transport connection admitted to a
// participation.
type AttemptConnectionID string

// AssistantProfileID identifies one immutable assistant profile.
type AssistantProfileID string

// AssistantConversationID identifies an attempt's assistant conversation.
type AssistantConversationID string

// AssistantTurnID identifies one assistant conversation turn.
type AssistantTurnID string

// AssistantCallID identifies one tool call within an assistant turn.
type AssistantCallID string

// EnvironmentProfileID identifies one immutable environment profile.
type EnvironmentProfileID string

// FocusLossSignalID identifies one accepted focus loss claim and its outcome.
type FocusLossSignalID string

// IntegrityEvidenceID identifies one retained integrity observation episode.
type IntegrityEvidenceID string

// IntegrityFlagID identifies one manager-reviewable integrity concern.
type IntegrityFlagID string

// IntegrityReviewDecisionID identifies a manager's current decision on a flag.
type IntegrityReviewDecisionID string

// SubmissionReviewID identifies the single integrity review of one submission.
type SubmissionReviewID string

// IntegrityDiscrepancyID identifies one record received after integrity
// collection ended.
type IntegrityDiscrepancyID string

// AttemptSuspensionID identifies one reversible suspension episode.
type AttemptSuspensionID string

// ScreenClipID identifies one requested range of a candidate's recorded screen.
type ScreenClipID string

// AttemptWorkspaceEntryID identifies one logical path in an attempt workspace.
type AttemptWorkspaceEntryID string

// AttemptWorkspaceObjectID identifies one immutable content object in an
// attempt workspace.
type AttemptWorkspaceObjectID string

// StarterWorkspaceEntryID identifies one logical file or directory in a
// draft's starter workspace.
type StarterWorkspaceEntryID string

// StarterWorkspaceObjectID identifies one staged or current starter workspace
// file object.
type StarterWorkspaceObjectID string

// UserID identifies a user account.
type UserID string

// SessionID identifies a server-side session.
type SessionID string

// DesktopRegistrationID identifies one user's DPoP key registration.
type DesktopRegistrationID string

// SessionCredentialID identifies a hashed access or refresh credential.
type SessionCredentialID string

// RoleID identifies a role.
type RoleID string

// RoleBindingID identifies a scoped role assignment.
type RoleBindingID string

// AffiliationID identifies a user's institutional affiliation.
type AffiliationID string

// AcademicUnitMemberID identifies an academic unit membership.
type AcademicUnitMemberID string

// ClassMemberID identifies a student's class enrollment.
type ClassMemberID string

// ExternalIdentityID identifies a linked external-provider subject.
type ExternalIdentityID string

// PasswordCredentialID identifies a local password credential row.
type PasswordCredentialID string

// PersonalAccessTokenID identifies a personal access token.
type PersonalAccessTokenID string

// PersonalAccessTokenPreparationID identifies one prepared token mutation
// awaiting its outcome.
type PersonalAccessTokenPreparationID string

// UserTokenID identifies a purpose-specific user token.
type UserTokenID string

// InvitationID identifies an onboarding invitation, which exists before its
// user does.
type InvitationID string

// MFACredentialID identifies an encrypted MFA credential.
type MFACredentialID string

// MFARecoveryCodeID identifies a hashed MFA recovery code.
type MFARecoveryCodeID string

// AuditEventID identifies a durable audit event.
type AuditEventID string

// MailOccurrenceID identifies one immutable logical transactional notification.
type MailOccurrenceID string

// MailDeliveryID identifies one frozen single-recipient delivery.
type MailDeliveryID string

// DomainEventID identifies one committed domain event awaiting delivery.
type DomainEventID string

// ExternalLoginStateID identifies a one-use external login transaction.
type ExternalLoginStateID string

// ExternalSecondFactorStateID identifies one paused authentication.
type ExternalSecondFactorStateID string

// BrowserAuthenticationTransactionID identifies one purpose-bound browser authentication.
type BrowserAuthenticationTransactionID string

// FileEntryID identifies the stable logical identity of a managed file.
type FileEntryID string

// FileRevisionID identifies one immutable version of a managed file.
type FileRevisionID string

// FileRenditionID identifies one immutable representation of a file revision.
type FileRenditionID string

// UploadLeaseID identifies a bounded reservation for publishing a file revision.
type UploadLeaseID string

// AdministratorRecoveryRecordID identifies one administrator recovery record.
type AdministratorRecoveryRecordID string

type idLike interface {
	~string
}

func parseID[T idLike](value, field string) (T, error) {
	var zero T
	if !IsValidOpaqueID(value) {
		return zero, invalidModelError("parseID", "id", "", fmt.Sprintf("invalid %s %q", field, value), "")
	}
	return T(value), nil
}

func marshalID[T idLike](id T) ([]byte, error) {
	value := string(id)
	if value != "" && !IsValidOpaqueID(value) {
		return nil, invalidModelError("marshalID", "id", "", fmt.Sprintf("cannot marshal invalid id %q", value), "")
	}
	return []byte(value), nil
}

func unmarshalID[T idLike](target *T, data []byte, parse func(string) (T, error)) error {
	if target == nil {
		return invalidModelError("unmarshalID", "id", "", "id unmarshal target is nil", "")
	}
	if len(data) == 0 {
		*target = T("")
		return nil
	}
	parsed, err := parse(string(data))
	if err != nil {
		return err
	}
	*target = parsed
	return nil
}

// Typed IDs marshal as JSON strings, not objects.
var (
	_ json.Marshaler   = InstitutionID("")
	_ json.Unmarshaler = (*InstitutionID)(nil)
)

func marshalIDJSON[T idLike](id T) ([]byte, error) {
	value := string(id)
	if value != "" && !IsValidOpaqueID(value) {
		return nil, invalidModelError("marshalIDJSON", "id", "", fmt.Sprintf("cannot marshal invalid id %q", value), "")
	}
	return json.Marshal(value)
}

func unmarshalIDJSON[T idLike](target *T, data []byte, parse func(string) (T, error)) error {
	if target == nil {
		return invalidModelError("unmarshalIDJSON", "id", "", "id unmarshal target is nil", "")
	}
	if string(data) == "null" {
		*target = T("")
		return nil
	}
	var raw string
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	if raw == "" {
		*target = T("")
		return nil
	}
	parsed, err := parse(raw)
	if err != nil {
		return err
	}
	*target = parsed
	return nil
}
