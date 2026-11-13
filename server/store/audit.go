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

// AuditListOptions narrows an audit listing inside Visibility. ActorID,
// SubjectUserID, Status and Resource each match exactly; Actions are
// alternatives, at most MaximumAuditListActions of them; Kind keeps the
// actions of that model.AuditKind, or every action when empty. Every filter
// narrows the visibility and the others, so an academic visibility still reads
// only its subtrees and AllowedActions catalog.
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
	// microsecond precision. The page continues past it in the read order:
	// older entries when newest first, newer entries when Ascending.
	BeforeCreatedAt time.Time
	BeforeID        model.AuditEventID
	Limit           int
	Visibility      AuditVisibilityScope
}

// MaximumAuditListActions bounds the actions one audit listing names.
const MaximumAuditListActions = 32

// AuditVisibilityScope constrains audit history in persistence. InstitutionWide
// explicitly retains operator visibility; its zero value denies all rows.
// AcademicInstitutionWide permits only events whose scope resolves to an
// Academic Unit or Class anywhere in the Institution and still requires the
// closed application-selected AllowedActions catalog. AcademicUnitRootIDs name
// current authorized subtrees. A subtree projection requires roots and actions.
type AuditVisibilityScope struct {
	InstitutionWide         bool
	AcademicInstitutionWide bool
	AcademicUnitRootIDs     []model.AcademicUnitID
	AllowedActions          []string
}

// AuditSelfRelation narrows a User's own history to the events about their
// account (subject) or the events they performed on anything else (actor).
// The zero value selects both.
type AuditSelfRelation string

const (
	AuditSelfRelationSubject AuditSelfRelation = "subject"
	AuditSelfRelationActor   AuditSelfRelation = "actor"
)

// MaximumAuditSelfEventKeys bounds the closed catalogue keys one self-history
// read may match.
const MaximumAuditSelfEventKeys = 256

// AuditSelfListOptions selects one keyset page of a User's own terminal audit
// history: events whose subject is the User, and events the User performed
// whose subject is anyone or nothing else. Events names the exact catalogue
// keys to match and is required; attempt rows never match. Since is
// inclusive and Until exclusive; both are optional.
type AuditSelfListOptions struct {
	UserID   model.UserID
	Relation AuditSelfRelation
	Events   []model.AuditSelfEventKey
	Since    time.Time
	Until    time.Time
	// Ascending reads oldest first; the default is newest first.
	Ascending bool
	// BeforeCreatedAt and BeforeID are the previous page's last key, at
	// microsecond precision. The page continues past it in the read order:
	// older entries when newest first, newer entries when Ascending.
	BeforeCreatedAt time.Time
	BeforeID        model.AuditEventID
	Limit           int
}

// MaximumAuditSelfCount bounds CountSelf. A history at the bound reports the
// bound, and the caller presents it as a lower bound.
const MaximumAuditSelfCount = MaximumListCount

// AuditSelfEntry is the bounded projection of one self-history event. It
// carries no parameters, prior state, or result beyond the recorded
// operation name and, for a Role Binding change, the bound Role.
// ResourceRelated reports whether the resource is the User themself, the
// Institution, or an Academic Unit or Class the User has a membership or Role
// Binding at, whether current or ended.
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
	// RoleID is the Role a role_binding.manage event granted or ended, read
	// from its value or, for an end, its prior state; zero for any other
	// action.
	RoleID model.RoleID
}

// MaximumAuditNames bounds the identifiers of each kind one ListNames read
// names: an audit page of at most 201 events names an actor, a subject and a
// resource each.
const MaximumAuditNames = 3 * 201

// AuditNameOptions names the records one page of audit events refers to, so
// the page reads every name in one bounded call rather than one per event.
// Each name is read only inside its visibility, the current authority the
// reader would need to see that record: Users through UserVisibility as the
// User directory applies it (a scoped visibility never names a disabled
// User), Academic Units through AcademicUnitInstitutionWide or the
// AcademicUnitRootIDs subtrees, and Classes through ClassVisibility as a
// Class search applies it. A zero visibility names nothing. Roles carry no
// visibility: the caller asks only for Roles the reader is already entitled
// to, such as the Roles bound to the reader themself. Archived and missing
// records are never named.
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

// AuditNames are the current presentable names ListNames found. A record
// outside its visibility, archived, or missing is absent.
type AuditNames struct {
	Users         map[model.UserID]string
	AcademicUnits map[model.AcademicUnitID]string
	Classes       map[model.ClassID]string
	Roles         map[model.RoleID]AuditRoleName
}

// AuditRoleName names one Role: its stable Name, which identifies the
// built-in System Administrator Role whose presentation is localized, and its
// DisplayName.
type AuditRoleName struct {
	Name        string
	DisplayName string
}

// AuditStore owns authoritative security records. Events are append-oriented;
// Complete permits only the terminal transition from attempt to success/fail.
type AuditStore interface {
	Save(context.Context, *model.AuditEvent) (*model.AuditEvent, error)
	Get(context.Context, model.AuditEventID) (*model.AuditEvent, error)
	Complete(context.Context, *AuditCompletion) (*model.AuditEvent, error)
	List(context.Context, AuditListOptions) ([]*model.AuditEvent, error)
	// Count counts the events List would return across every page for the
	// same filters and visibility, ignoring the keyset and limit, and stops
	// at maximum (see MaximumListCount).
	Count(context.Context, AuditListOptions, int) (int, error)
	// ListSelf returns up to Limit entries of one User's own history, newest
	// first by (created_at, id). It is the union of the subject and actor
	// relations, each read through its own index.
	ListSelf(context.Context, AuditSelfListOptions) ([]*AuditSelfEntry, error)
	// CountSelf counts the entries ListSelf would return across every page
	// for the same filters, ignoring the order, keyset, and limit, and stops
	// at MaximumAuditSelfCount.
	CountSelf(context.Context, AuditSelfListOptions) (int, error)
	// ListNames reads, in one bounded call per page, the current names of
	// the records the page's events refer to, each inside its visibility.
	ListNames(context.Context, AuditNameOptions) (AuditNames, error)
	// FailAbandonedAttempts terminalizes up to Limit attempts created longer
	// than Age ago by PostgreSQL time, oldest first, as failures with
	// model.AuditErrorAttemptAbandoned, and returns how many it changed.
	FailAbandonedAttempts(context.Context, *AuditAttemptAbandonment) (int64, error)
}

// AuditAttemptAbandonment bounds one pass over attempts whose requester never
// recorded their outcome, such as a node lost between the attempt and its
// completion. A terminal row lets retention reason about it again.
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
