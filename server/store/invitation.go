// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package store

import (
	"context"
	"crypto/sha256"
	"time"

	"github.com/sudosylabs/proctor/server/model"
)

// StudentClassInvitationIssue stores an invitation for someone with no
// account yet, with its mail delivery, and completes the audit.
type StudentClassInvitationIssue struct {
	Invitation   *model.Invitation
	Occurrence   *model.MailOccurrence
	Delivery     *model.MailDelivery
	DeliveryJob  *model.Job
	AuditEventID model.AuditEventID
	AuditAt      time.Time
}

// TeacherAcademicUnitInvitationIssue stores a teacher invitation with its
// mail delivery and completes the audit, atomically.
type TeacherAcademicUnitInvitationIssue struct {
	Invitation *model.Invitation
	// Lifetime is a duration, not a deadline: the store starts it from the
	// database clock so node clock skew cannot move the expiry.
	Lifetime     time.Duration
	Occurrence   *model.MailOccurrence
	Delivery     *model.MailDelivery
	DeliveryJob  *model.Job
	AuditEventID model.AuditEventID
	AuditAt      time.Time
}

// StudentClassInvitationAcceptance is the prepared input of one claim. The
// store finds an existing user by the invited email in the transaction and
// uses the prepared User and credential only when it must create the account
// or its local sign-in.
type StudentClassInvitationAcceptance struct {
	ClaimHash                string
	AcceptedAt               time.Time
	User                     *model.User
	Settings                 *model.UserSettingsDocument
	PasswordCredential       *model.PasswordCredential
	DefaultProfilePictureJob *model.Job
	Affiliation              *model.Affiliation
	ClassMember              *model.ClassMember
	Occurrence               *model.MailOccurrence
	Delivery                 *model.MailDelivery
	DeliveryJob              *model.Job
	AuditEvent               *model.AuditEvent
	RequiredActions          []model.Action
	BrowserTransaction       *BrowserInvitationTransactionProof
}

// StudentClassInvitationAcceptanceResult is the accepted account. Replayed
// means the claim was already accepted and nothing was repeated.
type StudentClassInvitationAcceptanceResult struct {
	Invitation  *model.Invitation
	User        *model.User
	Affiliation *model.Affiliation
	ClassMember *model.ClassMember
	Replayed    bool
}

// TeacherAcademicUnitInvitationAcceptance is the prepared input of one
// teacher claim. The store adds only the relationships that are missing.
type TeacherAcademicUnitInvitationAcceptance struct {
	ClaimHash                string
	AcceptedAt               time.Time
	User                     *model.User
	Settings                 *model.UserSettingsDocument
	PasswordCredential       *model.PasswordCredential
	DefaultProfilePictureJob *model.Job
	Affiliation              *model.Affiliation
	AcademicUnitMember       *model.AcademicUnitMember
	RoleBinding              *model.RoleBinding
	Occurrence               *model.MailOccurrence
	Delivery                 *model.MailDelivery
	DeliveryJob              *model.Job
	AuditEvent               *model.AuditEvent
	RequiredActions          []model.Action
	BrowserTransaction       *BrowserInvitationTransactionProof
}

type TeacherAcademicUnitInvitationAcceptanceResult struct {
	Invitation         *model.Invitation
	User               *model.User
	Affiliation        *model.Affiliation
	AcademicUnitMember *model.AcademicUnitMember
	RoleBinding        *model.RoleBinding
	Replayed           bool
}

// ScopedRoleInvitationIssue stores a role invitation for an existing user with
// its mail delivery and completes the audit, atomically.
type ScopedRoleInvitationIssue struct {
	Invitation   *model.Invitation
	Lifetime     time.Duration
	Occurrence   *model.MailOccurrence
	Delivery     *model.MailDelivery
	DeliveryJob  *model.Job
	AuditEventID model.AuditEventID
	AuditAt      time.Time
}

// ScopedRoleInvitationAcceptance consumes a role-only claim for the signed-in
// user and adds the binding if missing. It deliberately changes no profile and
// sends no mail.
type ScopedRoleInvitationAcceptance struct {
	ClaimHash   string
	UserID      model.UserID
	RoleBinding *model.RoleBinding
	// AuditEventID is the recorded audit attempt, completed with the
	// acceptance or its replay.
	AuditEventID       model.AuditEventID
	AuditAt            time.Time
	RequiredActions    []model.Action
	BrowserTransaction *BrowserInvitationTransactionProof
}

type ScopedRoleInvitationAcceptanceResult struct {
	Invitation  *model.Invitation
	User        *model.User
	RoleBinding *model.RoleBinding
	Replayed    bool
}

// ExternalIdentityInvitationAcceptance links a proved provider subject and
// accepts the pending invitation atomically. ExternalStateID is the consumed
// redirect transaction. ProviderEmail is a hint only and never selects an
// account.
type ExternalIdentityInvitationAcceptance struct {
	ExternalStateID          model.ExternalLoginStateID
	Identity                 *model.ExternalIdentity
	ProviderEmail            string
	User                     *model.User
	Settings                 *model.UserSettingsDocument
	DefaultProfilePictureJob *model.Job
	Affiliation              *model.Affiliation
	ClassMember              *model.ClassMember
	AcademicUnitMember       *model.AcademicUnitMember
	RoleBinding              *model.RoleBinding
	Notice                   *PreparedMail
	AuditEvent               *model.AuditEvent
	Capabilities             AccessDeploymentCapabilities
	RequiredActions          []model.Action
}

type ExternalIdentityInvitationAcceptanceResult struct {
	Invitation         *model.Invitation
	Identity           *model.ExternalIdentity
	User               *model.User
	Affiliation        *model.Affiliation
	ClassMember        *model.ClassMember
	AcademicUnitMember *model.AcademicUnitMember
	RoleBinding        *model.RoleBinding
}

type InvitationMaintenanceResult struct {
	Expired int
	Purged  int
	More    bool
}

// InvitationVisibilityScope limits which invitations an administrator sees.
// AcademicUnitRootIDs cover whole subtrees; ClassIDs cover single classes.
type InvitationVisibilityScope struct {
	InstitutionWide     bool
	AcademicUnitRootIDs []model.AcademicUnitID
	ClassIDs            []model.ClassID
}

// InvitationDeliverySummary is the newest delivery of an invitation. It
// deliberately excludes ciphertext, rendered content, transport detail, the
// Job and the Message-ID.
type InvitationDeliverySummary struct {
	TemplateKey       model.MailTemplateKey
	State             model.MailDeliveryState
	MaskedRecipient   string
	CreatedAt         time.Time
	UpdatedAt         time.Time
	Deadline          time.Time
	AcceptedAt        model.OptionalTime
	PublicFailureCode string
}

type InvitationAdministrationRecord struct {
	Invitation *model.Invitation
	Delivery   *InvitationDeliverySummary
}

// InvitationCommandResult is the result of an issue command; Replayed means
// an earlier committed outcome was returned.
type InvitationCommandResult struct {
	Invitation *model.Invitation
	Replayed   bool
	Duplicate  bool
	NoOp       bool
}

// InvitationAdministrationCommandResult is InvitationCommandResult for resend
// and revoke.
type InvitationAdministrationCommandResult struct {
	Record    *InvitationAdministrationRecord
	Replayed  bool
	Duplicate bool
}

// InvitationBatchCommandResult is the retained outcome of a batch item found
// to be a duplicate before its command ran. It holds no secret.
type InvitationBatchCommandResult struct {
	InvitationID model.InvitationID
	Replayed     bool
	Duplicate    bool
}

type InvitationBatchDuplicate struct {
	Candidate            *model.Invitation
	LifecycleID          model.InvitationID
	ExpectedRevision     int64
	ActorUserID          model.UserID
	CanonicalOperation   string
	CanonicalKeyDigest   [sha256.Size]byte
	CanonicalFingerprint [sha256.Size]byte
	AuditEventID         model.AuditEventID
	AuditAt              time.Time
}

type InvitationListOptions struct {
	Visibility    InvitationVisibilityScope
	Purpose       model.InvitationPurpose
	State         model.InvitationState
	TargetEmail   string
	TargetID      string
	CreatedAfter  time.Time
	CreatedBefore time.Time
	// Unexpired compares expires_at with the database clock, so a pending
	// invitation maintenance has not expired yet is not shown as open.
	Unexpired       bool
	BeforeCreatedAt time.Time
	BeforeID        model.InvitationID
	Limit           int
}

type InvitationPage struct {
	Items []*InvitationAdministrationRecord
	More  bool
}

// InvitationResend rotates the claim and replaces every unsent delivery with
// one new one, atomically.
type InvitationResend struct {
	ID               model.InvitationID
	ExpectedRevision int64
	ClaimHash        string
	Occurrence       *model.MailOccurrence
	Delivery         *model.MailDelivery
	DeliveryJob      *model.Job
	ActorUserID      model.UserID
	AuditEventID     model.AuditEventID
	AuditAt          time.Time
}

// InvitationRevocation revokes a pending invitation. RevocationNotice is sent
// only if an earlier delivery was accepted by SMTP.
type InvitationRevocation struct {
	ID               model.InvitationID
	ExpectedRevision int64
	ActorUserID      model.UserID
	RevocationNotice *PreparedMail
	AuditEventID     model.AuditEventID
	AuditAt          time.Time
}

// InvitationReplacement supersedes CurrentID and inserts Replacement with its
// delivery in one transaction.
type InvitationReplacement struct {
	CurrentID               model.InvitationID
	ExpectedRevision        int64
	Replacement             *model.Invitation
	Lifetime                time.Duration
	Occurrence              *model.MailOccurrence
	Delivery                *model.MailDelivery
	DeliveryJob             *model.Job
	ActorUserID             model.UserID
	CurrentAuditEventID     model.AuditEventID
	ReplacementAuditEventID model.AuditEventID
	AuditAt                 time.Time
}

// InvitationReplay is the audit attempt a replay completes after the caller
// has authorized it again.
type InvitationReplay struct {
	AuditEventID model.AuditEventID
	AuditAt      time.Time
}

// InvitationStore persists invitations from issue to acceptance. Only claim
// hashes cross it, never raw claims.
type InvitationStore interface {
	IssueStudentClass(context.Context, *StudentClassInvitationIssue) (*model.Invitation, error)
	IssueStudentClassIdempotently(context.Context, *StudentClassInvitationIssue, *CommandIdempotency) (*InvitationCommandResult, error)
	IssueTeacherAcademicUnit(context.Context, *TeacherAcademicUnitInvitationIssue) (*model.Invitation, error)
	IssueTeacherAcademicUnitIdempotently(
		context.Context,
		*TeacherAcademicUnitInvitationIssue,
		*CommandIdempotency,
	) (*InvitationCommandResult, error)
	IssueScopedRole(context.Context, *ScopedRoleInvitationIssue) (*model.Invitation, error)
	IssueScopedRoleIdempotently(context.Context, *ScopedRoleInvitationIssue, *CommandIdempotency) (*InvitationCommandResult, error)
	ResolveOnboardingInvitationNoOp(context.Context, *model.Invitation) (*model.Invitation, bool, error)
	FindCommandOutcome(context.Context, *CommandIdempotency) (*InvitationCommandResult, error)
	ReplayIssue(context.Context, *InvitationReplay, *CommandIdempotency) (*InvitationCommandResult, error)
	ReplayAdministration(context.Context, *InvitationReplay, *CommandIdempotency) (*InvitationAdministrationCommandResult, error)
	RecordBatchDuplicate(context.Context, *InvitationBatchDuplicate, *CommandIdempotency) (*InvitationBatchCommandResult, error)
	Get(context.Context, model.InvitationID) (*model.Invitation, error)
	GetByClaimHash(context.Context, string) (*model.Invitation, error)
	AcceptStudentClass(context.Context, *StudentClassInvitationAcceptance) (*StudentClassInvitationAcceptanceResult, error)
	AcceptTeacherAcademicUnit(
		context.Context,
		*TeacherAcademicUnitInvitationAcceptance,
	) (*TeacherAcademicUnitInvitationAcceptanceResult, error)
	AcceptScopedRole(context.Context, *ScopedRoleInvitationAcceptance) (*ScopedRoleInvitationAcceptanceResult, error)
	AcceptExternalIdentity(context.Context, *ExternalIdentityInvitationAcceptance) (*ExternalIdentityInvitationAcceptanceResult, error)
	List(context.Context, InvitationListOptions) (*InvitationPage, error)
	// Count counts what List would return across every page, ignoring the
	// keyset and limit, capped at maximum (see MaximumListCount).
	Count(context.Context, InvitationListOptions, int) (int, error)
	GetForAdministration(context.Context, model.InvitationID, InvitationVisibilityScope) (*InvitationAdministrationRecord, error)
	Resend(context.Context, *InvitationResend) (*InvitationAdministrationRecord, error)
	ResendIdempotently(context.Context, *InvitationResend, *CommandIdempotency) (*InvitationAdministrationCommandResult, error)
	Revoke(context.Context, *InvitationRevocation) (*InvitationAdministrationRecord, error)
	RevokeIdempotently(context.Context, *InvitationRevocation, *CommandIdempotency) (*InvitationAdministrationCommandResult, error)
	Replace(context.Context, *InvitationReplacement) (*InvitationAdministrationRecord, error)
	Maintain(context.Context, int) (*InvitationMaintenanceResult, error)
}
