// ---------------------------------------------------------------------------------------------
// Copyright (c) 2026 Sudosy Labs. All rights reserved.
// Licensed under the GNU Affero General Public License, version 3 only.
// See LICENSE in the server module root for license information.
// SPDX-License-Identifier: AGPL-3.0-only
// ---------------------------------------------------------------------------------------------

package store

import (
	"context"
	"time"

	"github.com/sudosylabs/proctor/server/model"
)

// ExamAuthoringSnapshot is the authoring view of an exam. Managers are
// counted, never loaded as an unbounded set.
type ExamAuthoringSnapshot struct {
	Exam                *model.Exam
	Draft               *model.ExamDraft
	OwnerUserID         model.UserID
	ManagerCount        int
	ActorIsManager      bool
	Capacity            model.ExamCapacityPolicy
	ResourceCount       int
	HasStarterWorkspace bool
}

// Consistent reports whether both Exam and Draft are present.
func (s *ExamAuthoringSnapshot) Consistent() bool {
	return !(s == nil ||
		s.Exam == nil ||
		s.Draft == nil)
}

// ExamAccessSnapshot is what authorizing an exam operation needs, read before
// any draft content is loaded.
type ExamAccessSnapshot struct {
	Exam           *model.Exam
	ActorIsManager bool
}

// Consistent reports whether Exam is present, without asking which exam.
func (s *ExamAccessSnapshot) Consistent() bool {
	return !(s == nil ||
		s.Exam == nil)
}

// ConsistentWith reports whether Exam is the one asked for, in the given
// academic unit.
func (s *ExamAccessSnapshot) ConsistentWith(examID model.ExamID, academicUnitID model.AcademicUnitID) bool {
	return !(s == nil ||
		s.Exam == nil ||
		s.Exam.ID != examID ||
		s.Exam.AcademicUnitID != academicUnitID)
}

// ConsistentWithOwningUnit reports whether Exam is the one asked for and names
// an owning unit, for a caller with no unit to compare.
func (s *ExamAccessSnapshot) ConsistentWithOwningUnit(examID model.ExamID) bool {
	return !(s == nil ||
		s.Exam == nil ||
		s.Exam.ID != examID ||
		!s.Exam.AcademicUnitID.IsValid())
}

// ConsistentAndValidWith reports whether Exam is the one asked for and passes
// its own validation; it is the only check here that validates it.
func (s *ExamAccessSnapshot) ConsistentAndValidWith(examID model.ExamID) bool {
	return !(s == nil ||
		s.Exam == nil ||
		s.Exam.Validate() != nil ||
		s.Exam.ID != examID)
}

type ExamAuthoringCommandResult struct {
	Value    *ExamAuthoringSnapshot
	Replayed bool
}

// ConsistentWithDraft reports whether the result carries the updated draft. It
// does not require Exam, which a draft update does not read.
func (r *ExamAuthoringCommandResult) ConsistentWithDraft() bool {
	return !(r == nil ||
		r.Value == nil ||
		r.Value.Draft == nil)
}

type ExamArchiveFilter string

const (
	ExamArchiveActive   ExamArchiveFilter = "active"
	ExamArchiveArchived ExamArchiveFilter = "archived"
	ExamArchiveAll      ExamArchiveFilter = "all"
)

// ExamListVisibility is the result of role authorization for a listing.
// OrdinaryMembershipAt is the instant at which the store requires the actor's
// current unit membership and manager relationship. Override visibility
// requires neither and creates neither.
type ExamListVisibility struct {
	ActorUserID                 model.UserID
	OrdinaryMembershipAt        time.Time
	OrdinaryInstitutionWide     bool
	OrdinaryAcademicUnitRootIDs []model.AcademicUnitID
	OverrideInstitutionWide     bool
	OverrideAcademicUnitRootIDs []model.AcademicUnitID
}

type ExamListOptions struct {
	AcademicUnitID  model.AcademicUnitID
	Query           string
	ArchiveFilter   ExamArchiveFilter
	SittingStates   []model.ExamSittingState
	EndsAfter       time.Time
	StartsBefore    time.Time
	BeforeUpdatedAt time.Time
	BeforeExamID    model.ExamID
	// HasSittings keeps only exams that have, or with false have not, any
	// Sitting. Nil keeps both.
	HasSittings *bool
	Limit       int
	Visibility  ExamListVisibility
}

// ExamSummary is the catalog view of an exam; authored text, policy,
// resources and starter workspace are deliberately absent.
type ExamSummary struct {
	ID                      model.ExamID
	AcademicUnitID          model.AcademicUnitID
	AcademicUnitDisplayName string
	CreatorUserID           model.UserID
	OwnerUserID             model.UserID
	Title                   string
	UpdatedAt               time.Time
	ArchivedAt              model.OptionalTime
	Revision                int64
	ManagerCount            int
	SittingCount            int
	MatchingSittingCount    int
	SittingSummary          *ExamCatalogSittingSummary
}

// ExamActionCountOptions asks for the action counts of one page of exams.
// SittingVisibility is where the viewer holds exam.sitting.view and
// ReviewVisibility where they hold submission.review, as ExamManagerScopes
// reports them. The viewer is never counted as their own candidate.
type ExamActionCountOptions struct {
	ExamIDs           []model.ExamID
	ViewerUserID      model.UserID
	SittingVisibility ExamListVisibility
	ReviewVisibility  ExamListVisibility
	Maximum           int
}

// ExamActionCounts is what waits for a manager on one exam. NeedsAttention
// counts candidates of its open and paused sittings who need a manager;
// PendingReview counts its sealed Submissions with no finalized review. Each
// is nil where the viewer's visibility does not reach the exam, and stops at
// the asked maximum.
type ExamActionCounts struct {
	NeedsAttention *int
	PendingReview  *int
}

type ExamCatalogSittingSummary struct {
	ID               model.ExamSittingID
	ExamRevisionID   model.ExamRevisionID
	ClassID          model.ClassID
	ClassDisplayName string
	ScheduledStartAt time.Time
	ScheduledEndAt   time.Time
	State            model.ExamSittingState
	Revision         int64
}

// ExamManagerSummary is who granted a manager relationship and when, never
// the user's profile.
type ExamManagerSummary struct {
	Manager   model.ExamManager
	IsCreator bool
	IsOwner   bool
}

type ExamManagerListOptions struct {
	ExamID          model.ExamID
	BeforeGrantedAt time.Time
	BeforeUserID    model.UserID
	Limit           int
}

// ExamManagerMutation is the input of AddManager, RemoveManager and
// TransferOwner.
type ExamManagerMutation struct {
	ExamID           model.ExamID
	ActorUserID      model.UserID
	TargetUserID     model.UserID
	ManagerOverride  bool
	ExpectedRevision int64
	ChangedAt        time.Time
	AuditEventID     model.AuditEventID
	AuditAt          time.Time
	Notices          []ExamManagerMail
}

// ExamManagerMail is one notice committed with a manager change. A transfer
// carries exactly two; an addition or removal exactly one.
type ExamManagerMail struct {
	Occurrence *model.MailOccurrence
	Delivery   *model.MailDelivery
	Job        *model.Job
}

type ExamManagerCommandResult struct {
	Exam     *model.Exam
	Manager  *model.ExamManager
	Replayed bool
}

type ExamArchive struct {
	ExamID           model.ExamID
	ActorUserID      model.UserID
	ManagerOverride  bool
	ExpectedRevision int64
	ArchivedAt       time.Time
	AuditEventID     model.AuditEventID
	AuditAt          time.Time
}

type ExamArchiveCommandResult struct {
	Value    *model.Exam
	Replayed bool
}

// ExamAuthoringCreation is a new exam with its draft and the audit attempt
// the creation completes.
type ExamAuthoringCreation struct {
	Exam         *model.Exam
	Draft        *model.ExamDraft
	Manager      *model.ExamManager
	AuditEventID model.AuditEventID
	AuditAt      time.Time
}

// ExamDraftTextUpdate changes only the draft's text fields.
type ExamDraftTextUpdate struct {
	ExamID               model.ExamID
	ActorUserID          model.UserID
	ManagerOverride      bool
	ExpectedRevision     int64
	Title                *string
	InstructionsMarkdown *string
	UpdatedAt            time.Time
	AuditEventID         model.AuditEventID
	AuditAt              time.Time
}

// ExamDraftFocusLossUpdate replaces only the focus loss rule. The store
// rebuilds and validates the whole policy so a caller cannot supply or weaken
// the connection loss rule.
type ExamDraftFocusLossUpdate struct {
	ExamID           model.ExamID
	ActorUserID      model.UserID
	ManagerOverride  bool
	ExpectedRevision int64
	FocusLoss        model.FocusLossPolicy
	UpdatedAt        time.Time
	AuditEventID     model.AuditEventID
	AuditAt          time.Time
}

// ExamDraftExecutionUpdate replaces only the execution policy. An enabled
// policy must name an unretired profile that allows its terminals, or the
// update conflicts with environment_profile_unusable.
type ExamDraftExecutionUpdate struct {
	ExamID           model.ExamID
	ActorUserID      model.UserID
	ManagerOverride  bool
	ExpectedRevision int64
	Execution        model.ExecutionPolicy
	UpdatedAt        time.Time
	AuditEventID     model.AuditEventID
	AuditAt          time.Time
}

// ExamDraftAssistantUpdate replaces only the assistant policy. An enabled
// policy must name an unretired profile that allows its terminals, or the
// update conflicts with assistant_profile_unusable.
type ExamDraftAssistantUpdate struct {
	ExamID           model.ExamID
	ActorUserID      model.UserID
	ManagerOverride  bool
	ExpectedRevision int64
	Assistant        model.AssistantPolicy
	UpdatedAt        time.Time
	AuditEventID     model.AuditEventID
	AuditAt          time.Time
}

// ExamDraftNativePolicyUpdate replaces the native rules, keeping the focus
// and connection loss rules.
type ExamDraftNativePolicyUpdate struct {
	ExamID           model.ExamID
	ActorUserID      model.UserID
	ManagerOverride  bool
	ExpectedRevision int64
	NativePolicy     model.NativeSecurityPolicy
	UpdatedAt        time.Time
	AuditEventID     model.AuditEventID
	AuditAt          time.Time
}

// ExamDraftBrowserPolicyUpdate replaces the whole browser policy; rules are
// never changed one by one.
type ExamDraftBrowserPolicyUpdate struct {
	// InstitutionOrigin comes from installation configuration, never from the
	// request.
	InstitutionOrigin string
	ExamID            model.ExamID
	ActorUserID       model.UserID
	ManagerOverride   bool
	ExpectedRevision  int64
	Policy            model.BrowserPolicy
	UpdatedAt         time.Time
	AuditEventID      model.AuditEventID
	AuditAt           time.Time
}

// ExamAuthoringStore persists exams and their drafts. Create commits the exam,
// its draft, the creator as manager and the audit together. Draft updates
// recheck the manager relationship unless overridden, reject archived, stale
// or no-op drafts, and commit only their own fields with the revision and
// audit. Replays return the committed result without repeating the change.
type ExamAuthoringStore interface {
	Create(context.Context, *ExamAuthoringCreation, *CommandIdempotency) (*ExamAuthoringCommandResult, error)
	// ReserveCopy commits an invisible copy reservation; CommitCopy makes it
	// visible with its Resources and Starter Workspace in one transaction. A
	// replay of CommitCopy returns the copy first committed, and the replay's
	// own reservation lapses.
	ReserveCopy(context.Context, *ExamCopyReservation) error
	CommitCopy(context.Context, *ExamCopyCommit, *CommandIdempotency) (*ExamAuthoringCommandResult, error)
	UpdateDraftText(context.Context, *ExamDraftTextUpdate, *CommandIdempotency) (*ExamAuthoringCommandResult, error)
	UpdateDraftFocusLoss(context.Context, *ExamDraftFocusLossUpdate, *CommandIdempotency) (*ExamAuthoringCommandResult, error)
	UpdateDraftExecution(context.Context, *ExamDraftExecutionUpdate, *CommandIdempotency) (*ExamAuthoringCommandResult, error)
	UpdateDraftAssistant(context.Context, *ExamDraftAssistantUpdate, *CommandIdempotency) (*ExamAuthoringCommandResult, error)
	UpdateDraftNativePolicy(context.Context, *ExamDraftNativePolicyUpdate, *CommandIdempotency) (*ExamAuthoringCommandResult, error)
	UpdateDraftBrowserPolicy(context.Context, *ExamDraftBrowserPolicyUpdate, *CommandIdempotency) (*ExamAuthoringCommandResult, error)
	// List orders by (UpdatedAt, ExamID) descending. Unit, archive state, role
	// scope, membership and manager checks, manager counts and draft titles all
	// belong in the one bounded query, so no unrestricted set is ever read.
	// Override scope is a separate branch that needs no relationship.
	List(context.Context, ExamListOptions) ([]ExamSummary, error)
	// Count counts what List pages through, ignoring the cursor and limit,
	// stopping at maximum (1..MaximumListCount).
	Count(context.Context, ExamListOptions, int) (int, error)
	// CountExamActions reads the action counts of one page of exams, one
	// grouped query per count.
	CountExamActions(context.Context, ExamActionCountOptions) (map[model.ExamID]ExamActionCounts, error)
	// Archive resolves a replay first. Otherwise it locks the exam, rechecks
	// the manager relationship unless overridden, rejects archived or stale
	// state, and records the archive time without deleting rows. Of concurrent
	// new commands one commits and the rest get a conflict.
	Archive(context.Context, *ExamArchive, *CommandIdempotency) (*ExamArchiveCommandResult, error)
	// ListManagers orders by (GrantedAt, UserID) descending.
	ListManagers(context.Context, ExamManagerListOptions) ([]ExamManagerSummary, error)
	// AddManager, RemoveManager and TransferOwner lock the exam and recheck its
	// state, revision, the actor's relationship unless overridden, and the
	// target's eligibility. A deferred constraint also keeps the owner a
	// manager.
	AddManager(context.Context, *ExamManagerMutation, *CommandIdempotency) (*ExamManagerCommandResult, error)
	RemoveManager(context.Context, *ExamManagerMutation, *CommandIdempotency) (*ExamManagerCommandResult, error)
	TransferOwner(context.Context, *ExamManagerMutation, *CommandIdempotency) (*ExamManagerCommandResult, error)
	Access(context.Context, model.ExamID, model.UserID) (*ExamAccessSnapshot, error)
	Get(context.Context, model.ExamID, model.UserID) (*ExamAuthoringSnapshot, error)
	Resolve(context.Context, model.ExamID) (*model.Exam, error)
}
