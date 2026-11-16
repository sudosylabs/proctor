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

// AuditListOptions filters an audit listing. Actions are alternatives (at most
// MaximumAuditListActions); an empty Kind keeps every kind. Filters only
// narrow Visibility, so an academic visibility still reads only its subtrees
// and AllowedActions.
type AuditListOptions struct {
	ActorID       model.UserID
	SubjectUserID model.UserID
	Actions       []string
	Kind          model.AuditKind
	Status        model.AuditStatus
	Resource      *model.Resource
	// Since is an inclusive and Until an exclusive bound on created_at; a
	// zero bound is open. When both are set Since precedes Until.
	Since time.Time
	Until time.Time
	// Ascending reads oldest first; the default is newest first.
	Ascending bool
	// BeforeCreatedAt and BeforeID are the previous page's last key, at
	// microsecond precision.
	BeforeCreatedAt time.Time
	BeforeID        model.AuditEventID
	Limit           int
	Visibility      AuditVisibilityScope
}

// MaximumAuditListActions bounds the actions one audit listing names.
const MaximumAuditListActions = 32

// AuditVisibilityScope limits which audit events a reader sees; the zero value
// sees nothing. InstitutionWide is operator visibility. AcademicInstitutionWide
// sees only events scoped to an academic unit or class, and like
// AcademicUnitRootIDs still requires AllowedActions.
type AuditVisibilityScope struct {
	InstitutionWide         bool
	AcademicInstitutionWide bool
	AcademicUnitRootIDs     []model.AcademicUnitID
	AllowedActions          []string
}

// AuditSelfRelation narrows a user's own history to events about their
// account (subject) or events they performed on anything else (actor). The
// zero value selects both.
type AuditSelfRelation string

const (
	AuditSelfRelationSubject AuditSelfRelation = "subject"
	AuditSelfRelationActor   AuditSelfRelation = "actor"
)

// MaximumAuditSelfEventKeys bounds AuditSelfListOptions.Events.
const MaximumAuditSelfEventKeys = 256

// AuditSelfListOptions pages a user's own completed audit history. Events is
// required and names the catalogue keys to match; unfinished attempts never
// match. Since is inclusive and Until exclusive; both are optional.
type AuditSelfListOptions struct {
	UserID   model.UserID
	Relation AuditSelfRelation
	Events   []model.AuditSelfEventKey
	Since    time.Time
	Until    time.Time
	// Ascending reads oldest first; the default is newest first.
	Ascending bool
	// BeforeCreatedAt and BeforeID are the previous page's last key, at
	// microsecond precision.
	BeforeCreatedAt time.Time
	BeforeID        model.AuditEventID
	Limit           int
}

// MaximumAuditSelfCount bounds CountSelf; a count at the bound means "at
// least".
const MaximumAuditSelfCount = MaximumListCount

// AuditSelfEntry is one event of a user's own history. It carries no
// parameters, prior state or result beyond the operation name and, for a role
// binding change, the role. ResourceRelated is true when the resource is the
// user, the institution, or a unit or class the user has or had a membership
// or binding at.
type AuditSelfEntry struct {
	ID              model.AuditEventID
	CreatedAt       time.Time
	ActorID         model.UserID
	SubjectUserID   model.UserID
	Action          string
	Operation       string
	Status          model.AuditStatus
	Resource        model.Resource
	ResourceRelated bool
	ClientType      string
	IPAddress       string
	UserAgent       string
	ErrorCode       string
	// RoleID is the role a role_binding.manage event granted or ended; zero
	// for any other action.
	RoleID model.RoleID
}

// MaximumAuditNames bounds the IDs of each kind one ListNames call takes: a
// 201-event page names an actor, a subject and a resource each.
const MaximumAuditNames = 3 * 201

// AuditNameOptions lists the records an audit page refers to, so the page
// reads every name in one call. Each name is read only inside the visibility
// the reader would need to see that record: users as the directory applies
// UserVisibility, units through AcademicUnitInstitutionWide or the root
// subtrees, classes as a class search applies ClassVisibility. A zero
// visibility names nothing. Roles have no visibility check, so the caller must
// ask only for roles the reader may already see. Archived and missing records
// are never named.
type AuditNameOptions struct {
	UserIDs                     []model.UserID
	UserVisibility              UserVisibilityScope
	AcademicUnitIDs             []model.AcademicUnitID
	AcademicUnitInstitutionWide bool
	AcademicUnitRootIDs         []model.AcademicUnitID
	ClassIDs                    []model.ClassID
	ClassVisibility             ClassVisibilityScope
	RoleIDs                     []model.RoleID
}

// AuditNames are the names ListNames found; a record outside its visibility,
// archived or missing is absent.
type AuditNames struct {
	Users         map[model.UserID]string
	AcademicUnits map[model.AcademicUnitID]string
	Classes       map[model.ClassID]string
	Roles         map[model.RoleID]AuditRoleName
}

// AuditRoleName carries Name as well as DisplayName because the built-in
// system administrator role is recognized by Name and shown localized.
type AuditRoleName struct {
	Name        string
	DisplayName string
}

// AuditStore persists security records. Events are append-only, except that
// Complete moves an attempt to success or failure once.
type AuditStore interface {
	Save(context.Context, *model.AuditEvent) (*model.AuditEvent, error)
	Get(context.Context, model.AuditEventID) (*model.AuditEvent, error)
	Complete(context.Context, *AuditCompletion) (*model.AuditEvent, error)
	List(context.Context, AuditListOptions) ([]*model.AuditEvent, error)
	// Count counts what List would return across every page, ignoring the
	// keyset and limit, capped at maximum (see MaximumListCount).
	Count(context.Context, AuditListOptions, int) (int, error)
	// ListSelf orders by (created_at, id). It unions the subject and actor
	// relations, each read through its own index.
	ListSelf(context.Context, AuditSelfListOptions) ([]*AuditSelfEntry, error)
	// CountSelf counts what ListSelf would return across every page, capped at
	// MaximumAuditSelfCount.
	CountSelf(context.Context, AuditSelfListOptions) (int, error)
	ListNames(context.Context, AuditNameOptions) (AuditNames, error)
	// FailAbandonedAttempts fails, with model.AuditErrorAttemptAbandoned, up
	// to Limit attempts older than Age on the database clock, oldest first.
	FailAbandonedAttempts(context.Context, *AuditAttemptAbandonment) (int64, error)
}

// AuditAttemptAbandonment bounds one pass over attempts whose outcome was
// never recorded, such as when a node died in between. Failing them gives
// retention a terminal row to work with.
type AuditAttemptAbandonment struct {
	Age   time.Duration
	Limit int
}

// MaximumAuditAttemptAbandonmentBatch bounds one abandonment pass.
const MaximumAuditAttemptAbandonmentBatch = 1000

// AuditCompletion is the input to AuditStore.Complete.
type AuditCompletion struct {
	ID        model.AuditEventID
	Status    model.AuditStatus
	ErrorCode string
	Result    []byte
	UpdateAt  time.Time
}
