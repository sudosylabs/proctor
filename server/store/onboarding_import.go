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
	// AcademicAdministrationPreviewMaximumRows bounds the Users one
	// academic administration filter preview freezes: the capped count of
	// every list (MaximumListCount).
	AcademicAdministrationPreviewMaximumRows = MaximumListCount
	// AcademicAdministrationPreviewLifetime is how long a ready academic
	// administration filter preview admits its commit, measured by
	// PostgreSQL from the instant the preview became ready.
	AcademicAdministrationPreviewLifetime = 15 * time.Minute
	// AcademicAdministrationPreviewNoChange is the preview code of a filter
	// preview row whose change already holds; a commit records the row as
	// no_op instead of executing it.
	AcademicAdministrationPreviewNoChange = "academic_administration_preview.no_change"
)

// OnboardingImport is the safe aggregate projection. Recipient data and the
// frozen row commands remain private to persistence and never enter Jobs,
// audit fields, logs, or administration list responses.
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
	// Filter is the operation and User directory filter of an academic
	// administration filter preview, and nil for every other mode.
	Filter *AcademicAdministrationFilter
	// PreviewExpiresAt is when a ready academic administration filter
	// preview stops admitting its commit; PostgreSQL sets it when the
	// preview becomes ready. It is absent for every other mode.
	PreviewExpiresAt model.OptionalTime
}

// AcademicAdministrationFilter is what an academic administration filter
// preview was asked for: one academic administration operation applied to
// every User the User directory filter matches, with the operation's own
// fields. The directory fields mean what the User directory query of the
// same name means, and the same visibility rules admit them.
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
	// RoleID is the Role a role_binding.create or role_binding.end applies.
	RoleID model.RoleID
	// AffiliationKind is the kind an affiliation.add or affiliation.end
	// applies.
	AffiliationKind model.AffiliationKind
	// StartAt and EndAt bound a created relationship.
	StartAt model.OptionalTime
	EndAt   model.OptionalTime
}

// OnboardingImportRow is the detailed seven-day preview/report row. Command
// contains private normalized recipient/package input and must never be
// returned directly by a transport.
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
	// BatchAuditEvent is the draft of the one batch-level audit entry of an
	// academic administration filter preview, required for that mode and
	// refused for every other. The first commit inserts and completes it
	// with the frozen counts in the same transaction; a replay inserts none,
	// so the batch is recorded exactly once.
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

// OnboardingImportVisibilityScope is the persistence-ready
// onboarding_batch.view authority of an administrative import listing: every
// import Institution-wide, the Academic Unit and Class imports under the named
// Academic Unit subtrees, and the imports scoped to the named Classes.
type OnboardingImportVisibilityScope struct {
	InstitutionWide     bool
	AcademicUnitRootIDs []model.AcademicUnitID
	ClassIDs            []model.ClassID
}

// OnboardingImportListOptions selects up to Limit (1..OnboardingImportListMaximum)
// unexpired administrative imports inside Visibility, newest first (oldest
// first when Ascending) by creation instant then identity. Student progression aggregates are never listed: they
// authorize through Class progression authority, not onboarding_batch.view.
// Academic administration filter previews are never listed either: their
// rows name Users and are read only by their creator through their own
// surface.
// An empty States matches every state; BeforeCreatedAt and BeforeID are the
// keyset of the previous page's last item, both set or both zero. An empty
// Visibility is invalid input.
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

// StudentProgressionVisibilityScope is the persistence-ready
// academic_progression.manage authority of a student progression listing:
// every Class Institution-wide, the Classes whose Programme lies under a named
// Academic Unit subtree, and the named Classes. A progression is visible only
// when both its source and its destination Class lie inside the scope, as the
// single read authorizes both.
type StudentProgressionVisibilityScope struct {
	InstitutionWide     bool
	AcademicUnitRootIDs []model.AcademicUnitID
	ClassIDs            []model.ClassID
}

// StudentProgressionListOptions selects up to Limit
// (1..OnboardingImportListMaximum) unexpired student progressions inside
// Visibility, newest first by creation instant then identity. An empty States
// matches every state, and each zero Period or Class filter matches every
// value. BeforeCreatedAt and BeforeID are the keyset of the previous page's
// last item, both set or both zero. An empty Visibility is invalid input.
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

// StudentProgressionSummary is one listed progression with the display names
// of its source and destination Classes, read with it.
type StudentProgressionSummary struct {
	Progression          *OnboardingImport
	SourceClassName      string
	DestinationClassName string
}

type OnboardingImportPage struct {
	Rows []OnboardingImportRow
	More bool
}

// AcademicAdministrationPreviewClass is the preview class of one filter
// preview row: it will change, its change already holds, or it is skipped.
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

// AcademicAdministrationPreviewGroupCountOptions selects the rows of one
// academic administration filter preview a group count counts. A nil
// Visibility counts every frozen row, as the batch-level audit entry records
// them; a set Visibility counts only the rows ListAcademicAdministrationPreviewRows
// shows through it, so a reader's summary never counts a User the reader no
// longer sees.
type AcademicAdministrationPreviewGroupCountOptions struct {
	ImportID   model.OnboardingImportID
	Visibility *UserVisibilityScope
}

// AcademicAdministrationPreviewRowListOptions selects up to Limit (1..201)
// rows of one academic administration filter preview in row order after
// AfterRowNumber, keeping only rows of Class (and of Reason when set, for
// the skipped class) whose User lies inside Visibility. Visibility is the
// reader's User directory visibility, evaluated at its ActiveAt as the
// directory evaluates it, with disabled Users included only for an
// InstitutionWide visibility; an empty Visibility matches no row.
type AcademicAdministrationPreviewRowListOptions struct {
	ImportID       model.OnboardingImportID
	Class          AcademicAdministrationPreviewClass
	Reason         string
	AfterRowNumber int
	Limit          int
	Visibility     UserVisibilityScope
}

// AcademicAdministrationPreviewRow is one frozen filter preview row with the
// public directory identity of its User and the row's execution outcome.
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

// OnboardingImportStore owns seven-day administrative import and progression
// aggregates, frozen private rows, execution fences, and retention.
type OnboardingImportStore interface {
	CreateOnboardingImport(context.Context, *OnboardingImportCreation) (*OnboardingImport, error)
	GetOnboardingImport(context.Context, model.OnboardingImportID) (*OnboardingImport, error)
	ListOnboardingImports(context.Context, OnboardingImportListOptions) ([]*OnboardingImport, error)
	// CountOpenOnboardingImports counts the unexpired administrative imports,
	// every mode but student progression and the academic administration
	// filter preview, that are not yet in a terminal
	// state (uploading, parsing, preview_ready, or executing) and whose
	// scope lies within the visibility. It stops at maximum (see
	// MaximumListCount).
	CountOpenOnboardingImports(context.Context, OnboardingImportVisibilityScope, int) (int, error)
	ListStudentProgressions(context.Context, StudentProgressionListOptions) ([]*StudentProgressionSummary, error)
	// CountStudentProgressions counts the progressions ListStudentProgressions
	// would return across every page for the same filters and visibility,
	// ignoring the keyset and limit, and stops at maximum (see
	// MaximumListCount).
	CountStudentProgressions(context.Context, StudentProgressionListOptions, int) (int, error)
	ListStudentProgressionRoster(context.Context, model.ClassID, time.Time, int) ([]*model.ClassMember, error)
	// CompleteOnboardingImportPreview freezes the rows of a parsing import.
	// For an academic administration filter preview it also refuses more
	// than AcademicAdministrationPreviewMaximumRows rows or a row outside
	// the preview's operation and scope, and sets PreviewExpiresAt to
	// AcademicAdministrationPreviewLifetime after the PostgreSQL instant the
	// preview became ready.
	CompleteOnboardingImportPreview(context.Context, *OnboardingImportPreviewCompletion) (*OnboardingImport, error)
	// CommitOnboardingImport queues the one execution Job of a ready
	// preview. An academic administration filter preview is refused with a
	// conflict on preview_expired once PostgreSQL time reaches its
	// PreviewExpiresAt, and its first commit inserts BatchAuditEvent.
	CommitOnboardingImport(context.Context, *OnboardingImportCommit) (*OnboardingImport, error)
	ListOnboardingImportRows(context.Context, model.OnboardingImportID, int, int) (*OnboardingImportPage, error)
	// CountAcademicAdministrationPreviewGroups counts the rows of one
	// academic administration filter preview by class and reason, ordered
	// by class then reason (see AcademicAdministrationPreviewGroupCountOptions).
	CountAcademicAdministrationPreviewGroups(
		context.Context,
		AcademicAdministrationPreviewGroupCountOptions,
	) ([]AcademicAdministrationPreviewGroup, error)
	// ListAcademicAdministrationPreviewRows reads one page of a filter
	// preview's rows whose User the reader may see (see
	// AcademicAdministrationPreviewRowListOptions).
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
