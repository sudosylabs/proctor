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

// MembershipOrder names the order of an Academic Unit or Class membership
// page: by the membership's start (the zero value) or by the member's
// presented name (users.sort_name), each then by membership ID.
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
	MembershipPageKeyset
	AfterID model.ClassMemberID
	Limit   int
}

type ClassMemberPage struct {
	Members []*model.ClassMember
	HasMore bool
}

// ClassStudentCount is the number of current members of one Class, stopped
// at the bound its read was given.
type ClassStudentCount struct {
	ClassID model.ClassID
	Count   int
}

// MaximumClassStudentCountClasses bounds one CountCurrentByClasses read: a
// page of Classes.
const MaximumClassStudentCountClasses = 200

// AffiliationStore persists non-exclusive institution relationships.
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

type AffiliationStore interface {
	Create(context.Context, *AffiliationCreation) (*model.Affiliation, error)
	End(context.Context, *AffiliationEnd) (*model.Affiliation, error)
	Save(context.Context, *model.Affiliation) (*model.Affiliation, error)
	Get(context.Context, model.AffiliationID) (*model.Affiliation, error)
	ListByUser(context.Context, model.UserID) ([]*model.Affiliation, error)
	ListActiveByUser(ctx context.Context, userID model.UserID, at time.Time) ([]*model.Affiliation, error)
}

// AcademicUnitMemberStore persists organizational membership without roles.
type AcademicUnitMemberCreation struct {
	Member                    *model.AcademicUnitMember
	ExpectedRecipientRevision int64
	Notice                    *PreparedMail
	AuditEventID              model.AuditEventID
	AuditAt                   time.Time
	Command                   *CommandIdempotency
	Replayed                  bool
	NoOp                      bool
}

type AcademicUnitMemberEnd struct {
	ID                        model.AcademicUnitMemberID
	ExpectedRevision          int64
	ExpectedRecipientRevision int64
	Notice                    *PreparedMail
	EndAt                     time.Time
	AuditEventID              model.AuditEventID
	AuditAt                   time.Time
	Command                   *CommandIdempotency
	Replayed                  bool
	NoOp                      bool
}

type AcademicUnitMemberStore interface {
	Create(context.Context, *AcademicUnitMemberCreation) (*model.AcademicUnitMember, error)
	End(context.Context, *AcademicUnitMemberEnd) (*model.AcademicUnitMember, error)
	Save(context.Context, *model.AcademicUnitMember) (*model.AcademicUnitMember, error)
	Get(context.Context, model.AcademicUnitMemberID) (*model.AcademicUnitMember, error)
	ListByUser(context.Context, model.UserID) ([]*model.AcademicUnitMember, error)
	ListByAcademicUnit(ctx context.Context, academicUnitID model.AcademicUnitID, at time.Time) ([]*model.AcademicUnitMember, error)
	ListPageByAcademicUnit(context.Context, AcademicUnitMemberPageOptions) (*AcademicUnitMemberPage, error)
	// CountPageByAcademicUnit counts the memberships ListPageByAcademicUnit
	// would return across every page for the same unit and ActiveAt,
	// ignoring the keyset, order, and limit, and stops at maximum (see
	// MaximumListCount).
	CountPageByAcademicUnit(context.Context, AcademicUnitMemberPageOptions, int) (int, error)
	// ListActiveByUser uses UTC microsecond precision and the interval [start, end).
	// Archived memberships are excluded; no active memberships is an empty list.
	ListActiveByUser(context.Context, model.UserID, time.Time) ([]*model.AcademicUnitMember, error)
}

type ClassEnrollmentResult struct {
	Membership *model.ClassMember
	Previous   *model.ClassMember
}

// ClassMemberEnrollment is the complete durable input for an enrollment or
// transfer and its already-persisted mutation audit. A transfer closes the
// previous membership and creates Member in the same transaction.
type ClassMemberEnrollment struct {
	Member                             *model.ClassMember
	ExpectedPreviousID                 model.ClassMemberID
	ExpectedRecipientRevision          int64
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
	ID                        model.ClassMemberID
	ExpectedRevision          int64
	ExpectedRecipientRevision int64
	EndAt                     time.Time
	Notice                    *PreparedMail
	AuditEventID              model.AuditEventID
	AuditAt                   time.Time
	Command                   *CommandIdempotency
	Replayed                  bool
	NoOp                      bool
}

// ClassMemberStore owns transactional student enrollment and history.
type ClassMemberStore interface {
	Enroll(context.Context, *ClassMemberEnrollment) (*ClassEnrollmentResult, error)
	End(context.Context, *ClassMemberEnd) (*model.ClassMember, error)
	Get(context.Context, model.ClassMemberID) (*model.ClassMember, error)
	ListByUser(context.Context, model.UserID) ([]*model.ClassMember, error)
	ListByClass(ctx context.Context, classID model.ClassID, at time.Time) ([]*model.ClassMember, error)
	ListPageByClass(context.Context, ClassMemberPageOptions) (*ClassMemberPage, error)
	// CountPageByClass counts the memberships ListPageByClass would return
	// across every page for the same Class and ActiveAt, ignoring the keyset,
	// order, and limit, and stops at maximum (see MaximumListCount).
	CountPageByClass(context.Context, ClassMemberPageOptions, int) (int, error)
	// CountCurrentByClasses counts the memberships of each listed Class
	// current at the instant, each stopped at maximum (see MaximumListCount),
	// in one statement. At most MaximumClassStudentCountClasses distinct
	// Classes are read; a Class with no current member has no entry.
	CountCurrentByClasses(ctx context.Context, classIDs []model.ClassID, at time.Time, maximum int) ([]ClassStudentCount, error)
	ListActiveByUser(ctx context.Context, userID model.UserID, at time.Time) ([]*model.ClassMember, error)
	// SeedEnroll and SeedEnd are unaudited fixture writes for conformance and
	// integration tests; production code never calls a Seed operation.
	SeedEnroll(context.Context, *model.ClassMember) (*ClassEnrollmentResult, error)
	SeedEnd(context.Context, *ClassMemberSeedEnd) (*model.ClassMember, error)
}

// ClassMemberSeedEnd is the input to ClassMemberStore.SeedEnd.
type ClassMemberSeedEnd struct {
	ID               model.ClassMemberID
	ExpectedRevision int64
	EndAt            time.Time
}
