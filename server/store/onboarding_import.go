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
	Principal                 model.Principal
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
// unexpired administrative imports inside Visibility, newest first by creation
// instant then identity. Student progression aggregates are never listed: they
// authorize through Class progression authority, not onboarding_batch.view.
// An empty States matches every state; BeforeCreatedAt and BeforeID are the
// keyset of the previous page's last item, both set or both zero. An empty
// Visibility is invalid input.
type OnboardingImportListOptions struct {
	Visibility      OnboardingImportVisibilityScope
	States          []model.OnboardingImportState
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

// OnboardingImportStore owns seven-day administrative import and progression
// aggregates, frozen private rows, execution fences, and retention.
type OnboardingImportStore interface {
	CreateOnboardingImport(context.Context, *OnboardingImportCreation) (*OnboardingImport, error)
	GetOnboardingImport(context.Context, model.OnboardingImportID) (*OnboardingImport, error)
	ListOnboardingImports(context.Context, OnboardingImportListOptions) ([]*OnboardingImport, error)
	// CountOpenOnboardingImports counts the unexpired administrative imports,
	// every mode but student progression, that are not yet in a terminal
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
	CompleteOnboardingImportPreview(context.Context, *OnboardingImportPreviewCompletion) (*OnboardingImport, error)
	CommitOnboardingImport(context.Context, *OnboardingImportCommit) (*OnboardingImport, error)
	ListOnboardingImportRows(context.Context, model.OnboardingImportID, int, int) (*OnboardingImportPage, error)
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
