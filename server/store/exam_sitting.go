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

// ExamSittingSnapshot is a sitting without its class roster, revision
// content or private cancellation reason.
type ExamSittingSnapshot struct {
	Sitting        *model.ExamSitting
	AcademicUnitID model.AcademicUnitID
}

// ConsistentAndValid reports whether the snapshot carries a sitting that
// passes its own validation. It does not ask which sitting that is.
func (s *ExamSittingSnapshot) ConsistentAndValid() bool {
	return !(s == nil ||
		s.Sitting == nil ||
		s.Sitting.Validate() != nil)
}

// ConsistentAndValidWith reports whether the snapshot carries a sitting that
// passes its own validation, is the one asked for, and belongs to the given
// exam.
func (s *ExamSittingSnapshot) ConsistentAndValidWith(sittingID model.ExamSittingID, examID model.ExamID) bool {
	return !(s == nil ||
		s.Sitting == nil ||
		s.Sitting.Validate() != nil ||
		s.Sitting.ID != sittingID ||
		s.Sitting.ExamID != examID)
}

// ConsistentWithOwningClass reports whether the snapshot carries the sitting
// that was asked for and that sitting names its class. It does not validate
// the sitting or ask for its academic unit.
func (s *ExamSittingSnapshot) ConsistentWithOwningClass(sittingID model.ExamSittingID) bool {
	return !(s == nil ||
		s.Sitting == nil ||
		s.Sitting.ID != sittingID ||
		!s.Sitting.ClassID.IsValid())
}

// ConsistentWithOwningUnit reports whether the snapshot carries the sitting
// that was asked for and names the academic unit that owns it. It does not
// validate the sitting or ask for its class.
func (s *ExamSittingSnapshot) ConsistentWithOwningUnit(sittingID model.ExamSittingID) bool {
	return !(s == nil ||
		s.Sitting == nil ||
		s.Sitting.ID != sittingID ||
		!s.AcademicUnitID.IsValid())
}

// ExamSittingInvalidationTarget names a manager sitting board to invalidate
// after a class or exam change.
type ExamSittingInvalidationTarget struct {
	ExamID    model.ExamID
	SittingID model.ExamSittingID
}

// ExamSittingListOptions filters an exam's sittings. ClassID and States are
// optional. OverlapStartAt and OverlapEndAt are both zero or a nonempty
// half-open interval matched against the scheduled start and end. The keyset
// cursor is (BeforeScheduledStartAt, BeforeSittingID), both descending.
type ExamSittingListOptions struct {
	ExamID                 model.ExamID
	ClassID                model.ClassID
	States                 []model.ExamSittingState
	OverlapStartAt         time.Time
	OverlapEndAt           time.Time
	BeforeScheduledStartAt time.Time
	BeforeSittingID        model.ExamSittingID
	Limit                  int
}

// ExamSittingSchedule creates a sitting. The store rechecks, in the same
// transaction, the active exam, the current manager unless ManagerOverride is
// set, a published revision of the same exam, the active class lineage and
// containment in the academic period.
type ExamSittingSchedule struct {
	Sitting         *model.ExamSitting
	OpenJob         *model.Job
	DeadlineJob     *model.Job
	ActorUserID     model.UserID
	ManagerOverride bool
	AuditEventID    model.AuditEventID
	AuditAt         time.Time
	Mail            *ExamSittingMailFanout
}

// ExamSittingScheduleUpdate replaces a scheduled sitting's revision, class
// and times, fenced by ExpectedRevision. PostgreSQL's statement time stamps
// the change.
type ExamSittingScheduleUpdate struct {
	ExamID           model.ExamID
	SittingID        model.ExamSittingID
	ActorUserID      model.UserID
	ManagerOverride  bool
	ExpectedRevision int64
	ExamRevisionID   model.ExamRevisionID
	ClassID          model.ClassID
	ScheduledStartAt time.Time
	ScheduledEndAt   time.Time
	OpenJob          *model.Job
	DeadlineJob      *model.Job
	AuditEventID     model.AuditEventID
	AuditAt          time.Time
	Mail             *ExamSittingMailFanout
}

// ExamSittingManagerTransition is the revision-fenced command for Pause,
// Resume and EarlyClose. PrivateReason is kept only in private action
// provenance, never in the snapshot, audit, command outcome or realtime events.
type ExamSittingManagerTransition struct {
	ExamID           model.ExamID
	SittingID        model.ExamSittingID
	ActorUserID      model.UserID
	ManagerOverride  bool
	ExpectedRevision int64
	FinalizeJob      *model.Job
	PrivateReason    string
	ChangedAt        time.Time
	AuditEventID     model.AuditEventID
	AuditAt          time.Time
}

type ExamSittingExtension struct {
	ExamID           model.ExamID
	SittingID        model.ExamSittingID
	ActorUserID      model.UserID
	ManagerOverride  bool
	ExpectedRevision int64
	ScheduledEndAt   time.Time
	DeadlineJob      *model.Job
	PrivateReason    string
	ChangedAt        time.Time
	AuditEventID     model.AuditEventID
	AuditAt          time.Time
}

type ExamSittingLifecycleTransitionCode string

const (
	ExamSittingTransitionOpened                   ExamSittingLifecycleTransitionCode = "scheduled_start_reached"
	ExamSittingTransitionManagerPaused            ExamSittingLifecycleTransitionCode = "manager_paused"
	ExamSittingTransitionManagerResumed           ExamSittingLifecycleTransitionCode = "manager_resumed"
	ExamSittingTransitionManagerExtended          ExamSittingLifecycleTransitionCode = "manager_extended"
	ExamSittingTransitionManagerClosed            ExamSittingLifecycleTransitionCode = "manager_closed"
	ExamSittingTransitionAcademicStructureInvalid ExamSittingLifecycleTransitionCode = "academic_structure_invalid"
	ExamSittingTransitionScheduleElapsed          ExamSittingLifecycleTransitionCode = "schedule_elapsed"
	ExamSittingTransitionScheduledEndReached      ExamSittingLifecycleTransitionCode = "scheduled_end_reached"
	ExamSittingTransitionClosedNoAttempts         ExamSittingLifecycleTransitionCode = "closed_no_attempts"
	ExamSittingTransitionSealingCompleted         ExamSittingLifecycleTransitionCode = "sealing_completed"
)

type ExamSittingLifecycleResult struct {
	Value      *ExamSittingSnapshot
	Transition ExamSittingLifecycleTransitionCode
	Changed    bool
	Replayed   bool
}

type ExamSittingLifecycleDueOptions struct {
	AfterDueAt     time.Time
	AfterSittingID model.ExamSittingID
	Limit          int
}

type ExamSittingLifecycleDue struct {
	Value *ExamSittingSnapshot
	DueAt time.Time
}

type ExamSittingDueAdvance struct {
	SittingID    model.ExamSittingID
	FinalizeJob  *model.Job
	AuditEventID model.AuditEventID
	AuditAt      time.Time
}

// ExamSittingFinishSealing closes the sitting only when every attempt is
// terminal and has a sealed submission. It is safe after every pass;
// unfinished work leaves the sitting closing.
type ExamSittingFinishSealing struct {
	SittingID    model.ExamSittingID
	AuditEventID model.AuditEventID
	AuditAt      time.Time
}

type ExamSittingNoShowListOptions struct {
	SittingID            model.ExamSittingID
	AfterCandidateUserID model.UserID
	Limit                int
}

// ExamSittingNoShow is a class member at the sitting's actual OpenedAt who
// has no attempt.
type ExamSittingNoShow struct {
	CandidateUserID model.UserID
	Username        string
	DisplayName     string
	// ClassMemberSince is when the membership the candidate held at OpenedAt
	// began.
	ClassMemberSince time.Time
}

// ExamSittingCancellation cancels a scheduled sitting. PrivateReason is
// manager-only and stored privately; it must never enter audit fields. The
// returned sitting carries only model.ExamSittingReasonManagerCanceled.
// PostgreSQL's statement time is the cancellation time.
type ExamSittingCancellation struct {
	ExamID           model.ExamID
	SittingID        model.ExamSittingID
	ActorUserID      model.UserID
	ManagerOverride  bool
	ExpectedRevision int64
	PrivateReason    string
	AuditEventID     model.AuditEventID
	AuditAt          time.Time
	Mail             *ExamSittingMailFanout
}

type ExamSittingMailChangeKind string

const (
	ExamSittingMailScheduled   ExamSittingMailChangeKind = "scheduled"
	ExamSittingMailRescheduled ExamSittingMailChangeKind = "rescheduled"
	ExamSittingMailCanceled    ExamSittingMailChangeKind = "canceled"
	ExamSittingMailReconciled  ExamSittingMailChangeKind = "reconciled"
)

// ExamSittingMailFanout is the mail a sitting transition commits. The store
// rebases every timestamp on one PostgreSQL clock sample. Bundle is nil only
// when mail is disabled.
type ExamSittingMailFanout struct {
	Occurrence       *model.MailOccurrence
	Bundle           *model.MailFanoutBundle
	ExpansionJob     *model.Job
	ChangeKind       ExamSittingMailChangeKind
	DeliveryLifetime time.Duration
}

type ExamSittingMailFanoutSnapshot struct {
	Occurrence      *model.MailOccurrence
	Bundle          *model.MailFanoutBundle
	SittingID       model.ExamSittingID
	SittingRevision int64
	PriorClassID    model.ClassID
	ChangeKind      ExamSittingMailChangeKind
	Deadline        time.Time
	CompletedAt     model.OptionalTime
}

type ExamSittingMailRecipientPageRequest struct {
	OccurrenceID model.MailOccurrenceID
	AfterUserID  model.UserID
	Limit        int
}

type ExamSittingMailRecipient struct {
	User        *model.User
	TemplateKey model.MailTemplateKey
}

type ExamSittingMailRecipientPage struct {
	Fanout     *ExamSittingMailFanoutSnapshot
	Recipients []ExamSittingMailRecipient
	More       bool
}

type ExamSittingMailRecipientCommit struct {
	OccurrenceID    model.MailOccurrenceID
	SittingRevision int64
	Recipient       *model.User
	Delivery        *model.MailDelivery
	DeliveryJob     *model.Job
}

type ExamSittingMailRecipientResult struct {
	Delivery   *model.MailDelivery
	Inserted   bool
	Suppressed bool
}

type ExamSittingMailExpansionCompletion struct {
	OccurrenceID model.MailOccurrenceID
}

// ExamSittingMailMaintenanceResult reports one pass over fan-outs that can
// no longer make progress.
type ExamSittingMailMaintenanceResult struct {
	FanoutsTerminalized  int
	DeliveriesSuppressed int
	More                 bool
}

type ExamSittingMailReconciliationOptions struct {
	AfterScheduledStartAt time.Time
	AfterSittingID        model.ExamSittingID
	Limit                 int
}

type ExamSittingMailReconciliationCandidate struct {
	Sitting     *model.ExamSitting
	ActorUserID model.UserID
}

type ExamSittingMailReconciliation struct {
	SittingID        model.ExamSittingID
	ExpectedRevision int64
	ActorUserID      model.UserID
	Mail             *ExamSittingMailFanout
}

type ExamSittingCommandResult struct {
	Value    *ExamSittingSnapshot
	Replayed bool
}

// ExamSittingStore lists and schedules sittings. Every fresh mutation locks
// and rechecks the active exam, the manager unless override is set, a sealed
// revision of the same exam, the active class lineage in the exam's academic
// unit, and containment in the class's academic period. The transition, audit
// and idempotent outcome commit together. An exact replay returns the
// committed snapshot before any stale, state, archive or relationship check.
type ExamSittingStore interface {
	// Resolve maps a sitting to its exam for authorization.
	Resolve(context.Context, model.ExamSittingID) (*ExamSittingSnapshot, error)
	Get(context.Context, model.ExamID, model.ExamSittingID) (*ExamSittingSnapshot, error)
	// List returns snapshots in descending (ScheduledStartAt, SittingID)
	// order. Limit is at most 201: a page of 200 plus one look-ahead row.
	List(context.Context, ExamSittingListOptions) ([]ExamSittingSnapshot, error)
	// ListInvalidationTargetsByClass orders by sitting ID after the exclusive
	// cursor; the limit is 1..201.
	ListInvalidationTargetsByClass(context.Context, model.ClassID, model.ExamSittingID, int) ([]ExamSittingInvalidationTarget, error)
	// ListInvalidationTargetsByExam orders by sitting ID after the exclusive
	// cursor; the limit is 1..201.
	ListInvalidationTargetsByExam(context.Context, model.ExamID, model.ExamSittingID, int) ([]ExamSittingInvalidationTarget, error)
	// ListCandidateInvalidationTargetsBySitting returns every user whose
	// candidate activity can show the sitting, including former class members
	// and candidates with an attempt. Results are distinct, ordered by user ID
	// after the exclusive cursor; the limit is 1..201.
	ListCandidateInvalidationTargetsBySitting(context.Context, model.ExamSittingID, model.UserID, int) ([]model.UserID, error)
	ListLifecycleDue(context.Context, ExamSittingLifecycleDueOptions) ([]ExamSittingLifecycleDue, error)
	Schedule(context.Context, *ExamSittingSchedule, *CommandIdempotency) (*ExamSittingCommandResult, error)
	UpdateSchedule(context.Context, *ExamSittingScheduleUpdate, *CommandIdempotency) (*ExamSittingCommandResult, error)
	Cancel(context.Context, *ExamSittingCancellation, *CommandIdempotency) (*ExamSittingCommandResult, error)
	Pause(context.Context, *ExamSittingManagerTransition, *CommandIdempotency) (*ExamSittingLifecycleResult, error)
	Resume(context.Context, *ExamSittingManagerTransition, *CommandIdempotency) (*ExamSittingLifecycleResult, error)
	Extend(context.Context, *ExamSittingExtension, *CommandIdempotency) (*ExamSittingLifecycleResult, error)
	EarlyClose(context.Context, *ExamSittingManagerTransition, *CommandIdempotency) (*ExamSittingLifecycleResult, error)
	AdvanceDue(context.Context, *ExamSittingDueAdvance) (*ExamSittingLifecycleResult, error)
	FinishSealing(context.Context, *ExamSittingFinishSealing) (*ExamSittingLifecycleResult, error)
	ListNoShows(context.Context, ExamSittingNoShowListOptions) ([]ExamSittingNoShow, error)
	// ListCandidateBoardTargets returns the sittings, scheduled or running,
	// whose board lists the user now: as a current class member or through
	// an Attempt. Ordered by sitting ID after the exclusive cursor; the limit
	// is 1..201.
	ListCandidateBoardTargets(context.Context, model.UserID, model.ExamSittingID, int) ([]ExamSittingInvalidationTarget, error)
	// CountCandidates counts each sitting's candidates, the set its board
	// lists, in one query, stopping at MaximumListCount. A sitting it cannot
	// see is absent from the result.
	CountCandidates(context.Context, []model.ExamSittingID) (map[model.ExamSittingID]int, error)
	// ListHistory returns up to limit (at most ExamSittingHistoryMaximum)
	// manager actions with their private reasons, newest first.
	ListHistory(context.Context, model.ExamSittingID, int) ([]ExamSittingHistoryEntry, error)
	// ListCorrections returns the sitting's live corrections in order, with
	// their acknowledgement counts.
	ListCorrections(context.Context, model.ExamSittingID) ([]ExamSittingCorrectionSummary, error)
	GetMailFanout(context.Context, model.MailOccurrenceID) (*ExamSittingMailFanoutSnapshot, error)
	ListMailRecipients(context.Context, ExamSittingMailRecipientPageRequest) (*ExamSittingMailRecipientPage, error)
	CommitMailRecipient(context.Context, *ExamSittingMailRecipientCommit) (*ExamSittingMailRecipientResult, error)
	CompleteMailExpansion(context.Context, *ExamSittingMailExpansionCompletion) (*ExamSittingMailFanoutSnapshot, error)
	MaintainMailExpansions(context.Context, int) (*ExamSittingMailMaintenanceResult, error)
	ListMailReconciliationDue(context.Context, ExamSittingMailReconciliationOptions) ([]ExamSittingMailReconciliationCandidate, error)
	ReconcileMail(context.Context, *ExamSittingMailReconciliation) (*ExamSittingMailFanoutSnapshot, error)
}
