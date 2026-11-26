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

// MembershipOrder is the order of a membership page: by start (the zero
// value) or by the member's users.sort_name, each then by membership ID.
type MembershipOrder string

const (
	MembershipOrderStart MembershipOrder = "start"
	MembershipOrderName  MembershipOrder = "name"
)

// IsValid reports whether o is a known order; the zero value is the start
// order.
func (o MembershipOrder) IsValid() bool {
	return o == "" || o == MembershipOrderStart || o == MembershipOrderName
}

// MembershipPageKeyset is the shared order and continuation of a membership
// page. Descending reverses Order. A continuation sets AfterID, the previous
// page's last membership, with AfterStartAt (start order) or AfterUserID, its
// member, whose sort name the Store reads itself (name order); a page without
// a continuation leaves all three zero.
type MembershipPageKeyset struct {
	Order        MembershipOrder
	Descending   bool
	AfterStartAt time.Time
	AfterUserID  model.UserID
}

// AcademicUnitMemberPageOptions selects a bounded page in the keyset's order.
// A zero ActiveAt includes history; a non-zero instant selects [start, end).
// The cursor is exclusive and either entirely absent or entirely valid.
type AcademicUnitMemberPageOptions struct {
	AcademicUnitID model.AcademicUnitID
	ActiveAt       time.Time
	// Affiliations keeps only members holding a current Affiliation of one of
	// these kinds at AffiliationsAt, which a non-empty list requires.
	Affiliations   []model.AffiliationKind
	AffiliationsAt time.Time
	// ExcludeDisabledUsers leaves out members whose account is disabled, for a
	// caller who may not learn that an account is disabled.
	ExcludeDisabledUsers bool
	MembershipPageKeyset
	AfterID model.AcademicUnitMemberID
	Limit   int
}

type AcademicUnitMemberPage struct {
	Members []*model.AcademicUnitMember
	HasMore bool
}

// ClassMemberPageOptions selects a bounded page in the keyset's order.
// A zero ActiveAt includes history; a non-zero instant selects [start, end).
// The cursor is exclusive and either entirely absent or entirely valid.
type ClassMemberPageOptions struct {
	ClassID  model.ClassID
	ActiveAt time.Time
	// Query keeps members whose username, display name, first or last name
	// contains it, as the user directory matches; QueryMatchesEmail also
	// matches the email, only for a caller who may see emails.
	Query             string
	QueryMatchesEmail bool
	MembershipPageKeyset
	AfterID model.ClassMemberID
	Limit   int
}

type ClassMemberPage struct {
	Members []*model.ClassMember
	HasMore bool
}

// ClassMemberTransfer is the class a membership's end moves the student to,
// and when.
type ClassMemberTransfer struct {
	ClassMemberID model.ClassMemberID
	ToClassID     model.ClassID
	ToClassName   string
	At            time.Time
}

// ClassStudentCount is the number of current members of one class, capped at
// the maximum its read was given.
type ClassStudentCount struct {
	ClassID model.ClassID
	Count   int
}

// AcademicUnitMemberCount is the number of current members of one unit, and
// of those holding each current Affiliation kind, each capped at the maximum
// its read was given. The kinds overlap, so they don't sum to Members.
type AcademicUnitMemberCount struct {
	AcademicUnitID model.AcademicUnitID
	Members        int
	ByAffiliation  map[model.AffiliationKind]int
}

// AcademicUnitMemberCountOptions names one page of units, at most
// MaximumAcademicUnitCountUnits, counted at ActiveAt.
type AcademicUnitMemberCountOptions struct {
	AcademicUnitIDs []model.AcademicUnitID
	ActiveAt        time.Time
	Maximum         int
}

// MaximumAcademicUnitCountUnits is the most units one count accepts: one page.
const MaximumAcademicUnitCountUnits = 200

// CountedAffiliationKinds are the kinds a unit's member counts break down by.
var CountedAffiliationKinds = []model.AffiliationKind{
	model.AffiliationStudent, model.AffiliationStaff, model.AffiliationTeacher, model.AffiliationExternal,
}

// MaximumClassStudentCountClasses is the most classes one
// CountCurrentByClasses read accepts: one page.
const MaximumClassStudentCountClasses = 200

type AffiliationCreation struct {
	Affiliation  *model.Affiliation
	AuditEventID model.AuditEventID
	AuditAt      time.Time
	Command      *CommandIdempotency
	Replayed     bool
	NoOp         bool
}

type AffiliationEnd struct {
	ID               model.AffiliationID
	ExpectedRevision int64
	EndAt            time.Time
	AuditEventID     model.AuditEventID
	AuditAt          time.Time
	Command          *CommandIdempotency
	Replayed         bool
	NoOp             bool
}

// AffiliationStore persists institution affiliations; a user may hold several.
type AffiliationStore interface {
	Create(context.Context, *AffiliationCreation) (*model.Affiliation, error)
	End(context.Context, *AffiliationEnd) (*model.Affiliation, error)
	Get(context.Context, model.AffiliationID) (*model.Affiliation, error)
	ListByUser(context.Context, model.UserID) ([]*model.Affiliation, error)
	ListActiveByUser(ctx context.Context, userID model.UserID, at time.Time) ([]*model.Affiliation, error)
}

type AcademicUnitMemberCreation struct {
	Member *model.AcademicUnitMember
	// Recipient is the User the Notice was written for; the write refuses it
	// if anything the mail was written from has changed since.
	Recipient    *model.User
	Notice       *PreparedMail
	AuditEventID model.AuditEventID
	AuditAt      time.Time
	Command      *CommandIdempotency
	Replayed     bool
	NoOp         bool
}

type AcademicUnitMemberEnd struct {
	ID               model.AcademicUnitMemberID
	ExpectedRevision int64
	// Recipient is the User the Notice was written for; the write refuses it
	// if anything the mail was written from has changed since.
	Recipient    *model.User
	Notice       *PreparedMail
	EndAt        time.Time
	AuditEventID model.AuditEventID
	AuditAt      time.Time
	Command      *CommandIdempotency
	Replayed     bool
	NoOp         bool
}

// AcademicUnitMemberStore persists academic unit membership, which grants no
// role.
type AcademicUnitMemberStore interface {
	Create(context.Context, *AcademicUnitMemberCreation) (*model.AcademicUnitMember, error)
	End(context.Context, *AcademicUnitMemberEnd) (*model.AcademicUnitMember, error)
	Get(context.Context, model.AcademicUnitMemberID) (*model.AcademicUnitMember, error)
	ListByUser(context.Context, model.UserID) ([]*model.AcademicUnitMember, error)
	ListByAcademicUnit(ctx context.Context, academicUnitID model.AcademicUnitID, at time.Time) ([]*model.AcademicUnitMember, error)
	ListPageByAcademicUnit(context.Context, AcademicUnitMemberPageOptions) (*AcademicUnitMemberPage, error)
	// CountPageByAcademicUnit counts the memberships ListPageByAcademicUnit
	// would return across every page for the same unit and ActiveAt,
	// ignoring the keyset, order, and limit, and stops at maximum (see
	// MaximumListCount).
	CountPageByAcademicUnit(context.Context, AcademicUnitMemberPageOptions, int) (int, error)
	// CountCurrentByAcademicUnits counts each listed unit's current members in
	// one read; every listed unit has an entry, zero when it has no member.
	CountCurrentByAcademicUnits(context.Context, AcademicUnitMemberCountOptions) ([]AcademicUnitMemberCount, error)
	// ListActiveByUser compares at UTC microsecond precision over [start, end)
	// and excludes archived memberships.
	ListActiveByUser(context.Context, model.UserID, time.Time) ([]*model.AcademicUnitMember, error)
}

type ClassEnrollmentResult struct {
	Membership *model.ClassMember
	Previous   *model.ClassMember
}

// ClassMemberEnrollment is the input of an enrollment or transfer. A transfer
// ends the previous membership and creates Member in the same transaction.
type ClassMemberEnrollment struct {
	Member             *model.ClassMember
	ExpectedPreviousID model.ClassMemberID
	// Recipient is the User the Notice was written for; the write refuses it
	// if anything the mail was written from has changed since.
	Recipient                          *model.User
	StudentProgression                 bool
	ProgressionSourceAuditEventID      model.AuditEventID
	ProgressionDestinationAuditEventID model.AuditEventID
	Notice                             *PreparedMail
	AuditEventID                       model.AuditEventID
	PreviousAuditEventID               model.AuditEventID
	AuditAt                            time.Time
	Command                            *CommandIdempotency
	Replayed                           bool
	NoOp                               bool
}

type ClassMemberEnd struct {
	ID               model.ClassMemberID
	ExpectedRevision int64
	// Recipient is the User the Notice was written for; the write refuses it
	// if anything the mail was written from has changed since.
	Recipient    *model.User
	EndAt        time.Time
	Notice       *PreparedMail
	AuditEventID model.AuditEventID
	AuditAt      time.Time
	Command      *CommandIdempotency
	Replayed     bool
	NoOp         bool
}

// ClassMemberStore persists student enrollment and its history.
type ClassMemberStore interface {
	Enroll(context.Context, *ClassMemberEnrollment) (*ClassEnrollmentResult, error)
	End(context.Context, *ClassMemberEnd) (*model.ClassMember, error)
	Get(context.Context, model.ClassMemberID) (*model.ClassMember, error)
	ListByUser(context.Context, model.UserID) ([]*model.ClassMember, error)
	ListByClass(ctx context.Context, classID model.ClassID, at time.Time) ([]*model.ClassMember, error)
	ListPageByClass(context.Context, ClassMemberPageOptions) (*ClassMemberPage, error)
	// CountPageByClass counts the memberships ListPageByClass would return
	// across every page for the same class and ActiveAt, ignoring the keyset,
	// order, and limit, and stops at maximum (see MaximumListCount).
	CountPageByClass(context.Context, ClassMemberPageOptions, int) (int, error)
	// CountCurrentByClasses counts each listed class's memberships current at
	// the instant, each capped at maximum (see MaximumListCount). At most
	// MaximumClassStudentCountClasses distinct classes are read; a class with
	// no current member has no entry.
	CountCurrentByClasses(ctx context.Context, classIDs []model.ClassID, at time.Time, maximum int) ([]ClassStudentCount, error)
	// ListTransfers finds, for each listed membership that has an end, the
	// membership of the same student in another class starting exactly at
	// that end: the transfer that ends it. A membership without one has no
	// entry. At most MaximumClassStudentCountClasses memberships are read.
	ListTransfers(ctx context.Context, memberIDs []model.ClassMemberID, visibility ClassVisibilityScope) ([]ClassMemberTransfer, error)
	ListActiveByUser(ctx context.Context, userID model.UserID, at time.Time) ([]*model.ClassMember, error)
}

// ClassMemberSeedEnd is the input of the SQL store's SeedClassEnrollmentEnd, a test fixture write.
type ClassMemberSeedEnd struct {
	ID               model.ClassMemberID
	ExpectedRevision int64
	EndAt            time.Time
}
