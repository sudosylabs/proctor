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

const (
	OnboardingImportMaximumBytes   = 10 * 1024 * 1024
	OnboardingImportMaximumRows    = 50_000
	OnboardingImportMaximumColumns = 64
	OnboardingImportMaximumField   = 16 * 1024
	OnboardingImportPageSize       = 200
	// OnboardingImportListMaximum bounds one listing read: a 200-item page
	// plus the probe item that reports whether another page follows.
	OnboardingImportListMaximum = 201
	// AcademicAdministrationPreviewMaximumRows bounds the users one filter
	// preview freezes, matching the capped count of every list.
	AcademicAdministrationPreviewMaximumRows = MaximumListCount
	// AcademicAdministrationPreviewLifetime is how long a ready filter
	// preview admits its commit, measured by PostgreSQL from when it became
	// ready.
	AcademicAdministrationPreviewLifetime = 15 * time.Minute
	// AcademicAdministrationPreviewNoChange marks a filter preview row whose
	// change already holds; a commit records it as no_op instead of executing
	// it.
	AcademicAdministrationPreviewNoChange = "academic_administration_preview.no_change"
)

// OnboardingImport is an import without its rows. Recipient data and frozen
// row commands stay in the store and never enter jobs, audit, logs or list
// responses.
type OnboardingImport struct {
	ID                        model.OnboardingImportID
	Mode                      model.OnboardingImportMode
	State                     model.OnboardingImportState
	ScopeType                 model.RoleScopeType
	ScopeID                   string
	RoleID                    model.RoleID
	SourcePeriodID            model.AcademicPeriodID
	SourceClassID             model.ClassID
	DestinationPeriodID       model.AcademicPeriodID
	DestinationClassID        model.ClassID
	SourcePeriodRevision      int64
	SourceClassRevision       int64
	DestinationPeriodRevision int64
	DestinationClassRevision  int64
	EffectiveAt               time.Time
	ActorUserID               model.UserID
	PreviewDigest             string
	IgnoredHeaders            []string
	TotalRows                 int
	ValidRows                 int
	InvalidRows               int
	SucceededRows             int
	NoOpRows                  int
	FailedRows                int
	SkippedRows               int
	CommitPolicy              model.OnboardingImportCommitPolicy
	ParseJobID                model.JobID
	ExecutionJobID            model.JobID
	CreatedAt                 time.Time
	UpdatedAt                 time.Time
	ExpiresAt                 time.Time
	Revision                  int64
	FailureCode               string
	// FileName is the uploaded file's base name as the uploader gave it, or
	// empty when none was given; a student progression has none.
	FileName  string
	Principal model.Principal
	// Filter is nil for every mode but an academic administration filter
	// preview.
	Filter *AcademicAdministrationFilter
	// PreviewExpiresAt is when a ready filter preview stops admitting its
	// commit, set by PostgreSQL; absent for every other mode.
	PreviewExpiresAt model.OptionalTime
}

// AcademicAdministrationFilter is one academic administration operation
// applied to every user a directory filter matches. The directory fields mean
// what the same-named user directory query fields mean, under the same
// visibility rules.
type AcademicAdministrationFilter struct {
	Operation        string
	Query            string
	Affiliations     []model.AffiliationKind
	AcademicUnitID   model.AcademicUnitID
	RoleIDs          []model.RoleID
	State            string
	NeverSignedIn    bool
	LastActiveAfter  time.Time
	LastActiveBefore time.Time
	// RoleID is the role a role_binding.create or role_binding.end applies.
	RoleID model.RoleID
	// AffiliationKind is the kind an affiliation.add or affiliation.end
	// applies.
	AffiliationKind model.AffiliationKind
	// StartAt and EndAt bound a created relationship.
	StartAt model.OptionalTime
	EndAt   model.OptionalTime
}

// OnboardingImportRow is one preview and report row, kept seven days. It
// holds private recipient data and must never be returned as is by a
// transport.
type OnboardingImportRow struct {
	ImportID                        model.OnboardingImportID
	RowNumber                       int
	Reference                       string
	Operation                       string
	ScopeType                       model.RoleScopeType
	ScopeID                         string
	TargetRevision                  int64
	RoleID                          model.RoleID
	RoleRevision                    int64
	Email                           string
	UserID                          model.UserID
	RelationshipID                  string
	RelationshipRevision            int64
	DestinationRelationshipID       string
	DestinationRelationshipRevision int64
	AffiliationKind                 model.AffiliationKind
	Username                        string
	DisplayName                     string
	FirstName                       string
	LastName                        string
	Locale                          string
	Timezone                        string
	StartsAt                        model.OptionalTime
	EndsAt                          model.OptionalTime
	PreviewStatus                   model.OnboardingImportRowStatus
	PreviewCode                     string
	Status                          model.OnboardingImportRowStatus
	PublicCode                      string
	InvitationID                    model.InvitationID
	ResourceID                      string
	UpdatedAt                       time.Time
}

type OnboardingImportCreation struct {
	Import             *OnboardingImport
	ParseJob           *model.Job
	AuditEventID       model.AuditEventID
	SourceAuditEventID model.AuditEventID
	AuditAt            time.Time
}

type OnboardingImportPreviewCompletion struct {
	ID               model.OnboardingImportID
	ExpectedRevision int64
	Digest           string
	IgnoredHeaders   []string
	Rows             []OnboardingImportRow
	At               time.Time
}

type OnboardingImportCommit struct {
	ID                 model.OnboardingImportID
	ActorUserID        model.UserID
	Principal          model.Principal
	ExpectedRevision   int64
	PreviewDigest      string
	Policy             model.OnboardingImportCommitPolicy
	IdempotencyKey     [sha256.Size]byte
	ExecutionJob       *model.Job
	At                 time.Time
	AuditEventID       model.AuditEventID
	SourceAuditEventID model.AuditEventID
	AuditAt            time.Time
	// BatchAuditEvent is required for a filter preview and refused for
	// every other mode. The first commit inserts and completes it with the
	// frozen counts; a replay inserts none, so the batch is audited exactly
	// once.
	BatchAuditEvent *model.AuditEvent
}

type OnboardingImportCancellation struct {
	ID                 model.OnboardingImportID
	ActorUserID        model.UserID
	Principal          model.Principal
	At                 time.Time
	AuditEventID       model.AuditEventID
	SourceAuditEventID model.AuditEventID
	AuditAt            time.Time
}

type OnboardingImportRowCompletion struct {
	ID           model.OnboardingImportID
	RowNumber    int
	Status       model.OnboardingImportRowStatus
	PublicCode   string
	InvitationID model.InvitationID
	ResourceID   string
	At           time.Time
}

// OnboardingImportVisibilityScope is the onboarding_batch.view authority of
// an import listing: every import institution-wide, the unit and class
// imports under the named academic unit subtrees, and the imports of the
// named classes.
type OnboardingImportVisibilityScope struct {
	InstitutionWide     bool
	AcademicUnitRootIDs []model.AcademicUnitID
	ClassIDs            []model.ClassID
}

// OnboardingImportListOptions lists unexpired imports inside Visibility,
// newest first by creation time then ID. Limit is
// 1..OnboardingImportListMaximum. Student progressions are never listed (they
// authorize through class progression authority), nor are filter previews
// (their rows name users and only the creator reads them). An empty States
// matches every state; BeforeCreatedAt and BeforeID are both set or both
// zero. An empty Visibility is invalid input.
type OnboardingImportListOptions struct {
	Visibility OnboardingImportVisibilityScope
	States     []model.OnboardingImportState
	// Ascending reads oldest first instead; BeforeCreatedAt and BeforeID then
	// continue past the previous page's last import toward newer ones.
	Ascending       bool
	BeforeCreatedAt time.Time
	BeforeID        model.OnboardingImportID
	Limit           int
}

// StudentProgressionVisibilityScope is the academic_progression.manage
// authority of a progression listing: every class institution-wide, the
// classes whose programme lies under a named academic unit subtree, and the
// named classes. A progression is visible only when both its source and
// destination class are inside the scope, since one read authorizes both.
type StudentProgressionVisibilityScope struct {
	InstitutionWide     bool
	AcademicUnitRootIDs []model.AcademicUnitID
	ClassIDs            []model.ClassID
}

// StudentProgressionListOptions lists unexpired progressions inside
// Visibility, newest first by creation time then ID. Limit is
// 1..OnboardingImportListMaximum. Empty or zero filters match everything;
// BeforeCreatedAt and BeforeID are both set or both zero. An empty Visibility
// is invalid input.
type StudentProgressionListOptions struct {
	Visibility          StudentProgressionVisibilityScope
	States              []model.OnboardingImportState
	SourcePeriodID      model.AcademicPeriodID
	SourceClassID       model.ClassID
	DestinationPeriodID model.AcademicPeriodID
	DestinationClassID  model.ClassID
	BeforeCreatedAt     time.Time
	BeforeID            model.OnboardingImportID
	Limit               int
}

// StudentProgressionSummary is a progression with its source and
// destination class names.
type StudentProgressionSummary struct {
	Progression          *OnboardingImport
	SourceClassName      string
	DestinationClassName string
}

type OnboardingImportPage struct {
	Rows []OnboardingImportRow
	More bool
}

// AcademicAdministrationPreviewClass says whether a filter preview row will
// change, already holds, or is skipped.
type AcademicAdministrationPreviewClass string

const (
	AcademicAdministrationPreviewWillChange AcademicAdministrationPreviewClass = "will_change"
	AcademicAdministrationPreviewNoOp       AcademicAdministrationPreviewClass = "no_op"
	AcademicAdministrationPreviewSkipped    AcademicAdministrationPreviewClass = "skipped"
)

// IsValid reports whether c is a known preview class.
func (c AcademicAdministrationPreviewClass) IsValid() bool {
	return c == AcademicAdministrationPreviewWillChange || c == AcademicAdministrationPreviewNoOp || c == AcademicAdministrationPreviewSkipped
}

// AcademicAdministrationPreviewGroup counts the rows of one preview class
// and reason. Reason is the skip reason of a skipped group and empty for
// the other classes.
type AcademicAdministrationPreviewGroup struct {
	Class  AcademicAdministrationPreviewClass
	Reason string
	Rows   int
}

// AcademicAdministrationPreviewGroupCountOptions selects the rows to count.
// A nil Visibility counts every frozen row, as the batch audit records them;
// a set one counts only the rows ListAcademicAdministrationPreviewRows shows
// through it, so a summary never counts a user the reader no longer sees.
type AcademicAdministrationPreviewGroupCountOptions struct {
	ImportID   model.OnboardingImportID
	Visibility *UserVisibilityScope
}

// AcademicAdministrationPreviewRowListOptions pages a filter preview's rows
// of one Class (and Reason, when set) in row order. Limit is 1..201. Only
// rows whose user is inside the reader's directory Visibility are kept,
// evaluated at its ActiveAt; disabled users appear only for an
// institution-wide visibility, and an empty Visibility matches no row.
type AcademicAdministrationPreviewRowListOptions struct {
	ImportID       model.OnboardingImportID
	Class          AcademicAdministrationPreviewClass
	Reason         string
	AfterRowNumber int
	Limit          int
	Visibility     UserVisibilityScope
}

// AcademicAdministrationPreviewRow is a frozen filter preview row with its
// user's public directory identity and its execution outcome.
type AcademicAdministrationPreviewRow struct {
	RowNumber   int
	UserID      model.UserID
	Username    string
	DisplayName string
	Class       AcademicAdministrationPreviewClass
	Reason      string
	Status      model.OnboardingImportRowStatus
	PublicCode  string
	ResourceID  string
}

// OnboardingImportStore keeps imports and progressions, with their private
// rows, for seven days.
type OnboardingImportStore interface {
	CreateOnboardingImport(context.Context, *OnboardingImportCreation) (*OnboardingImport, error)
	GetOnboardingImport(context.Context, model.OnboardingImportID) (*OnboardingImport, error)
	ListOnboardingImports(context.Context, OnboardingImportListOptions) ([]*OnboardingImport, error)
	// CountOpenOnboardingImports counts the unexpired, non-terminal imports
	// within the visibility, excluding progressions and filter previews. It
	// stops at maximum (see MaximumListCount).
	CountOpenOnboardingImports(context.Context, OnboardingImportVisibilityScope, int) (int, error)
	ListStudentProgressions(context.Context, StudentProgressionListOptions) ([]*StudentProgressionSummary, error)
	// CountStudentProgressions counts every page ListStudentProgressions
	// would return, ignoring the keyset and limit, and stops at maximum (see
	// MaximumListCount).
	CountStudentProgressions(context.Context, StudentProgressionListOptions, int) (int, error)
	ListStudentProgressionRoster(context.Context, model.ClassID, time.Time, int) ([]*model.ClassMember, error)
	// CompleteOnboardingImportPreview freezes a parsing import's rows. For a
	// filter preview it refuses more than
	// AcademicAdministrationPreviewMaximumRows rows or a row outside the
	// preview's operation and scope, and sets PreviewExpiresAt from
	// PostgreSQL time.
	CompleteOnboardingImportPreview(context.Context, *OnboardingImportPreviewCompletion) (*OnboardingImport, error)
	// CommitOnboardingImport queues the execution job of a ready preview. A
	// filter preview conflicts with preview_expired once PostgreSQL time
	// reaches PreviewExpiresAt.
	CommitOnboardingImport(context.Context, *OnboardingImportCommit) (*OnboardingImport, error)
	ListOnboardingImportRows(context.Context, model.OnboardingImportID, int, int) (*OnboardingImportPage, error)
	// CountAcademicAdministrationPreviewGroups orders by class then reason.
	CountAcademicAdministrationPreviewGroups(
		context.Context,
		AcademicAdministrationPreviewGroupCountOptions,
	) ([]AcademicAdministrationPreviewGroup, error)
	ListAcademicAdministrationPreviewRows(
		context.Context,
		AcademicAdministrationPreviewRowListOptions,
	) ([]AcademicAdministrationPreviewRow, error)
	CompleteOnboardingImportRow(context.Context, *OnboardingImportRowCompletion) (*OnboardingImport, error)
	FinishOnboardingImport(context.Context, *OnboardingImportFinish) (*OnboardingImport, error)
	CancelOnboardingImport(context.Context, *OnboardingImportCancellation) (*OnboardingImport, error)
	FailOnboardingImport(context.Context, *OnboardingImportFailure) (*OnboardingImport, error)
	ListExpiredOnboardingImports(context.Context, int, time.Time) ([]model.OnboardingImportID, error)
	PurgeOnboardingImport(context.Context, *OnboardingImportPurge) (bool, error)
}

// OnboardingImportFailure is the input to OnboardingImportStore.FailOnboardingImport.
type OnboardingImportFailure struct {
	ID   model.OnboardingImportID
	Code string
	At   time.Time
}

// OnboardingImportFinish is the input to OnboardingImportStore.FinishOnboardingImport.
type OnboardingImportFinish struct {
	ID model.OnboardingImportID
	At time.Time
}

// OnboardingImportPurge is the input to OnboardingImportStore.PurgeOnboardingImport.
type OnboardingImportPurge struct {
	ID model.OnboardingImportID
	At time.Time
}
